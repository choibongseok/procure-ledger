package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/org"
)

func kstDay(offset int) string {
	return time.Now().In(time.FixedZone("KST", 9*3600)).AddDate(0, 0, offset).Format("2006-01-02")
}

// 퇴사자 카드 직접 조회(명세 6절): org:history(admin·auditor)만 본다. 그 밖에는 존재를 숨기는 404.
// 기준일이 과거이고 그날은 재직이었던 사람은 퇴사자가 아니므로 누구나 본다.
func TestLeftPersonCardNeedsHistoryPermission(t *testing.T) {
	e := newOrgEnv(t)
	gone := e.userID("staff2@pl.test")
	e.must("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","leaveKind":"자진","reason":"퇴사 처리","confirm":true}`, gone, kstDay(-1)), 201)
	path := fmt.Sprintf("/api/org/people/%d", gone)
	for who, want := range map[string]int{"staff": 404, "finance": 404, "lead": 404, "head": 404, "admin": 200, "auditor": 200} {
		if code, body := e.do(who, "GET", path, ""); code != want {
			t.Errorf("%s 의 퇴사자 카드 조회 %d(기대 %d): %s", who, code, want, body)
		}
	}
	// 그날은 재직이었던 기준일(퇴사 전)에는 직원도 본다.
	if code, body := e.do("staff", "GET", path+"?asOf=2021-06-01", ""); code != 200 || strings.Contains(body, `"active":false`) {
		t.Errorf("과거 재직 기준일 %d: %s", code, body)
	}
	// 구성원 목록·검색에서도 직원에게는 보이지 않는다.
	_, body := e.do("staff", "GET", "/api/org/people?q=영업직원&includeLeft=1", "")
	if strings.Contains(body, "영업직원") {
		t.Errorf("직원 검색에 퇴사자가 나옴: %s", body)
	}
	_, body = e.do("auditor", "GET", "/api/org/people?q=영업직원&includeLeft=1", "")
	if !strings.Contains(body, "영업직원") || !strings.Contains(body, `"active":false`) {
		t.Errorf("감사 담당 검색에 퇴사자가 안 나옴: %s", body)
	}
}

// 날짜 형식 오류는 Go 레이아웃이 아니라 한국어 업무 문구로 나온다. 입사일 이전 소급은 입사 전임을 알린다.
func TestOrgDateAndBeforeHireMessages(t *testing.T) {
	e := newOrgEnv(t)
	for _, p := range []string{"/api/org/tree?asOf=abc", "/api/org/people?asOf=2026-13-40", "/api/org/events?from=abc"} {
		code, body := e.do("admin", "GET", p, "")
		if code != 400 || !strings.Contains(body, "날짜는 연-월-일(예 2026-03-01)로 입력하세요") || strings.Contains(body, "2006") {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
	staff := e.userID("staff@pl.test")
	var unit int64
	_ = e.d.DB.QueryRow(`SELECT MIN(id) FROM departments WHERE name = '영업팀'`).Scan(&unit)
	code, body := e.do("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"transfer","userId":%d,"toUnit":%d,"effectiveDate":"2015-01-01","reason":"소급","confirm":true}`, staff, unit))
	if code != 400 || !strings.Contains(body, "입사 전인 사람에게는 발령할 수 없습니다") || strings.Contains(body, "퇴사한 사람") {
		t.Errorf("입사 전 소급: %d %s", code, body)
	}
}

// 퇴사 등록: 효력일을 따로 보내면 마지막 근무일 다음 날과 같아야 하고, 마지막 근무일이 오늘보다 이전이면 사유가 필요하다.
func TestLeaveEffectiveDateAndRetroReason(t *testing.T) {
	e := newOrgEnv(t)
	u := e.userID("staff2@pl.test")
	post := func(body string) (int, string) { return e.do("admin", "POST", "/api/org/events", body) }
	last := kstDay(5)
	if code, body := post(fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","effectiveDate":"%s","leaveKind":"자진"}`, u, last, last)); code != 400 || !strings.Contains(body, "마지막 근무일 다음 날") {
		t.Errorf("어긋난 효력일: %d %s", code, body)
	}
	if code, body := post(fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","leaveKind":"자진"}`, u, kstDay(-1))); code != 400 || !strings.Contains(body, "사유") {
		t.Errorf("마지막 근무일이 어제인데 사유 없이 등록됨: %d %s", code, body)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE kind = 'leave'`) != 0 {
		t.Fatal("거부된 퇴사가 이벤트를 남김")
	}
	// 효력일이 정확하면 받아들이고(예약), 오늘이 마지막 근무일이면 소급이 아니라 사유 없이도 된다.
	if code, body := post(fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","effectiveDate":"%s","leaveKind":"자진"}`, u, kstDay(0), kstDay(1))); code != 201 {
		t.Errorf("오늘이 마지막 근무일: %d %s", code, body)
	}
}

// 발령 목록 API: 쪽 나누기·전체 건수·종류·기간 조건.
func TestOrgEventsPagination(t *testing.T) {
	e := newOrgEnv(t)
	var all struct {
		Events []struct {
			ID            int64  `json:"id"`
			Kind          string `json:"kind"`
			EffectiveDate string `json:"effectiveDate"`
		} `json:"events"`
		Total  int `json:"total"`
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	get := func(q string) {
		code, body := e.do("admin", "GET", "/api/org/events?"+q, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		all.Events = nil
		if err := json.Unmarshal([]byte(body), &all); err != nil {
			t.Fatal(err)
		}
	}
	get("limit=5")
	total := all.Total
	if total < 15 || len(all.Events) != 5 || all.Limit != 5 {
		t.Fatalf("첫 쪽: total=%d len=%d limit=%d", total, len(all.Events), all.Limit)
	}
	first := all.Events[0].ID
	get("limit=5&offset=5")
	if len(all.Events) != 5 || all.Events[0].ID == first || all.Total != total || all.Offset != 5 {
		t.Fatalf("둘째 쪽: %+v", all)
	}
	get("")
	if all.Limit != 50 {
		t.Fatalf("기본 쪽 크기 %d", all.Limit)
	}
	get("limit=999")
	if all.Limit != 200 {
		t.Fatalf("쪽 크기 상한 %d", all.Limit)
	}
	get("kind=hire&limit=200")
	if all.Total != 9 {
		t.Fatalf("입사 9건 기대, %d", all.Total)
	}
	for _, ev := range all.Events {
		if ev.Kind != "hire" {
			t.Fatalf("종류 조건 밖: %+v", ev)
		}
	}
	get("from=2030-01-01")
	if all.Total != 0 {
		t.Fatalf("미래 기간 %d건", all.Total)
	}
	get("from=2020-01-01&to=2020-12-31&limit=200")
	if all.Total != total {
		t.Fatalf("2020년 기간 %d(전체 %d): 백필은 모두 2020-02-01 효력", all.Total, total)
	}
	// 조직장 보임 문구에 조직 이름이 들어간다.
	_, body := e.do("admin", "GET", "/api/org/events?kind=head_set&limit=200", "")
	if !strings.Contains(body, "구매팀 조직장에 구매부서장 보임") {
		t.Errorf("조직장 보임 문구: %s", body)
	}
}

// 점검 API: 이벤트 로그와 구간 테이블이 일치하는지가 함께 온다. 구간을 변조하면 어긋난 건수가 나온다.
func TestOrgConsistencyReportsLogMatch(t *testing.T) {
	e := newOrgEnv(t)
	read := func() (bool, int) {
		var out struct {
			Log struct {
				OK         bool   `json:"ok"`
				Mismatches int    `json:"mismatches"`
				Broken     string `json:"broken"`
			} `json:"log"`
		}
		code, body := e.do("admin", "GET", "/api/org/consistency", "")
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("%d %s", code, body)
		}
		return out.Log.OK, out.Log.Mismatches
	}
	if ok, n := read(); !ok || n != 0 {
		t.Fatalf("정상 상태: ok=%v n=%d", ok, n)
	}
	rep, err := e.d.Org.(*org.Service).Consistency(context.Background())
	if err != nil || !rep.OK {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := e.d.DB.Exec(`INSERT INTO org_write_guard (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.DB.Exec(`UPDATE org_person_spans SET primary_unit_id = (SELECT unit_id FROM org_unit_spans WHERE unit_id <> org_person_spans.primary_unit_id LIMIT 1) WHERE (user_id, valid_from) = (SELECT user_id, valid_from FROM org_person_spans ORDER BY user_id, valid_from LIMIT 1)`); err != nil {
		t.Fatalf("구간 변조 불가: %v", err)
	}
	_, _ = e.d.DB.Exec(`DELETE FROM org_write_guard`)
	if ok, n := read(); ok || n == 0 {
		t.Fatalf("변조를 못 잡음: ok=%v n=%d", ok, n)
	}
}

// 개발용 시드의 모든 발령은 행위자 시스템의 감사 기록을 남긴다(spec B20 ⑦). 백필은 한 번에 한 건과 건수.
func TestDevSeedWritesAuditRows(t *testing.T) {
	e := newPlatformEnv(t, true)
	ctx := context.Background()
	svc := e.d.Org.(*org.Service)
	did, err := svc.SeedDev(ctx)
	if err != nil || !did {
		t.Fatalf("시드: %v %v", did, err)
	}
	var events, hrAudits, orgAudits, nonSystem int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM org_events WHERE kind <> 'cancel'`).Scan(&events)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'hr.%'`).Scan(&hrAudits)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'org.%'`).Scan(&orgAudits)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE (action LIKE 'hr.%' OR action LIKE 'org.%') AND (actor_id <> 0 OR actor_role <> 'system')`).Scan(&nonSystem)
	// 취소 아닌 이벤트마다 감사 한 건(정정의 새 이벤트는 hr.event_correct 한 건), 단독 취소는 hr.event_cancel 한 건, 마지막에 투영 갱신 한 건이다.
	var cancels, corrects int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.event_cancel'`).Scan(&cancels)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.event_correct'`).Scan(&corrects)
	if cancels != 1 || corrects != 1 {
		t.Fatalf("취소 %d건·정정 %d건 감사(각 1건이어야 함)", cancels, corrects)
	}
	if hrAudits+orgAudits != events+cancels+1 {
		t.Fatalf("이벤트 %d건에 감사 기록 %d건(hr %d, org %d)", events, hrAudits+orgAudits, hrAudits, orgAudits)
	}
	if nonSystem != 0 {
		t.Fatalf("행위자가 시스템이 아닌 시드 감사 %d건", nonSystem)
	}
	if res, err := e.d.AuditReader.Verify(ctx); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

// dev IdP 가 켜진 환경에서는 로그인 화면 목록에 없는 시드 계정(p001 등)도 이메일을 직접 넣으면 로그인된다.
// 퇴사한 시드 계정은 거부되고, dev IdP 가 꺼진 운영 구성에서는 이 경로 자체가 없다.
func TestDevIdPLogsInSeededPeopleByEmail(t *testing.T) {
	e := newPlatformEnv(t, true)
	svc := e.d.Org.(*org.Service)
	if did, err := svc.SeedDev(context.Background()); err != nil || !did {
		t.Fatalf("시드: %v %v", did, err)
	}
	_, list := e.call(http.DefaultClient, "GET", "/api/auth/config", "", nil)
	if strings.Contains(list, "p001@pl.test") {
		t.Errorf("로그인 목록에 시드 인물이 나옴: %s", list)
	}
	c := e.browser()
	e.login(c, "p001@pl.test")
	code, body := e.call(c, "GET", "/api/me", "", nil)
	if code != 200 || !strings.Contains(body, "p001") && !strings.Contains(body, `"role"`) {
		t.Fatalf("p001 로그인 뒤 /api/me: %d %s", code, body)
	}
	var email string
	_ = e.d.DB.QueryRow(`SELECT u.email FROM users u JOIN org_person_spans s ON s.user_id = u.id WHERE s.left_flag = 1 LIMIT 1`).Scan(&email)
	if email == "" {
		t.Skip("퇴사한 시드 계정이 없음")
	}
	c2 := e.browser()
	res, err := c2.Get(e.base + "/api/auth/login?login_hint=" + email)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if code, _ := e.call(c2, "GET", "/api/me", "", nil); code == 200 {
		t.Errorf("퇴사한 시드 계정 %s 이 로그인됨", email)
	}

	off := newPlatformEnv(t, false)
	if code, _ := off.call(http.DefaultClient, "GET", "/api/auth/login?login_hint=p001@pl.test", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("dev IdP 꺼짐인데 로그인 시작이 %d", code)
	}
}

// 조직 상세: 직무대행 목록과 그 조직의 변경 이력(unitId 조건)이 API 로 나온다(명세 8.2).
func TestOrgUnitDetailActingAndHistory(t *testing.T) {
	e := newOrgEnv(t)
	var unit int64
	_ = e.d.DB.QueryRow(`SELECT MIN(id) FROM departments WHERE name = '구매팀'`).Scan(&unit)
	d := e.must("admin", "GET", fmt.Sprintf("/api/org/units/%d", unit), "", 200)
	if _, ok := d["acting"].([]any); !ok {
		t.Fatalf("acting 목록이 없음: %v", d)
	}
	body := e.must("admin", "GET", fmt.Sprintf("/api/org/events?unitId=%d&limit=50", unit), "", 200)
	evs, _ := body["events"].([]any)
	if len(evs) < 2 {
		t.Fatalf("구매팀 변경 이력 %d건", len(evs))
	}
	for _, x := range evs {
		m := x.(map[string]any)
		if m["subject"] != "unit" || int64(num(m["subjectId"])) != unit {
			t.Fatalf("다른 대상의 이벤트: %v", m)
		}
	}
}

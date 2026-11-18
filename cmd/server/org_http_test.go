package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// orgEnv 는 dev 시드 사용자를 조직으로 옮긴 서버(서버 기동 때의 백필과 같다)와 역할별 브라우저를 만든다.
type orgEnv struct {
	*platformEnv
	clients map[string]*http.Client
	csrfs   map[string]string
}

func newOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	e := &orgEnv{platformEnv: newPlatformEnv(t, true), clients: map[string]*http.Client{}, csrfs: map[string]string{}}
	if _, err := e.d.DB.Exec(`UPDATE users SET created_at = '2020-02-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if did, err := e.d.Org.(*org.Service).Backfill(context.Background()); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	return e
}

func (e *orgEnv) as(who string) *http.Client {
	e.t.Helper()
	if c, ok := e.clients[who]; ok {
		return c
	}
	c := e.browser()
	e.login(c, who+"@pl.test")
	e.clients[who], e.csrfs[who] = c, e.csrf(c)
	return c
}

func (e *orgEnv) do(who, method, path, body string) (int, string) {
	c := e.as(who)
	return e.call(c, method, path, body, map[string]string{"X-CSRF-Token": e.csrfs[who]})
}

func (e *orgEnv) must(who, method, path, body string, want int) map[string]any {
	e.t.Helper()
	code, resp := e.do(who, method, path, body)
	if code != want {
		e.t.Fatalf("%s %s %s: HTTP %d, want %d: %s", who, method, path, code, want, resp)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(resp), &out)
	return out
}

func (e *orgEnv) userID(email string) int64 {
	var id int64
	if err := e.d.DB.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *orgEnv) count(q string, args ...any) int {
	var n int
	if err := e.d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

// AC-65·66·67·68a: 권한 선언, 쓰기는 admin 만, 이력은 admin·auditor 만, 연락처는 API 응답 기준으로 가려진다.
func TestOrgAPIPermissionsAndMasking(t *testing.T) {
	e := newOrgEnv(t)
	roles := map[string]string{"admin": "admin", "finance": "finance", "lead": "manager", "staff": "staff", "auditor": "auditor"}
	// 조회는 다섯 역할 모두 된다.
	for who := range roles {
		tree := e.must(who, "GET", "/api/org/tree", "", 200)
		if tree["root"] == nil {
			t.Fatalf("%s: 조직 트리가 비어 있음", who)
		}
	}
	root := e.must("staff", "GET", "/api/org/tree", "", 200)["root"].(map[string]any)
	rootID := num(root["id"])
	// 쓰기·미리보기·목록·정합성·직위 수정은 admin 만: 다른 역할은 403 이고 거부가 감사 로그에 남는다.
	writes := []struct{ method, path, body string }{
		{"POST", "/api/org/events", `{"kind":"unit_sort","unitId":1,"sort":1}`},
		{"POST", "/api/org/events/preview", `{"kind":"unit_sort","unitId":1,"sort":1}`},
		{"POST", "/api/org/events/1/cancel", `{"reason":"x"}`},
		{"POST", "/api/org/events/1/correct", `{"kind":"unit_sort","unitId":1,"sort":1}`},
		{"GET", "/api/org/events", ""}, {"GET", "/api/org/consistency", ""}, {"PUT", "/api/org/ranks", `{"ranks":[]}`},
	}
	denied := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND result = 'denied'`)
	for _, who := range []string{"finance", "lead", "staff", "auditor"} {
		for _, w := range writes {
			if code, _ := e.do(who, w.method, w.path, w.body); code != http.StatusForbidden {
				t.Errorf("%s %s %s: %d, want 403", who, w.method, w.path, code)
			}
		}
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND result = 'denied'`) < denied+4*len(writes) {
		t.Error("권한 거부가 감사 로그에 남지 않음")
	}
	// 사용자 수정으로는 부서를 바꿀 수 없다(AC-64).
	leadID := e.userID("lead@pl.test")
	code, body := e.do("admin", "PATCH", fmt.Sprintf("/api/admin/users/%d", leadID), `{"departmentId":2}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "인사 발령으로만") {
		t.Fatalf("부서 직접 수정: %d %s", code, body)
	}

	// 연락처가 있는 사람을 입사시킨다(관리자만 등록 가능).
	dept := e.must("admin", "GET", "/api/org/tree", "", 200)["root"].(map[string]any)["children"].([]any)[0].(map[string]any)
	deptID := num(dept["id"])
	hired := e.must("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"hire","name":"홍길동","email":"hong@pl.test","toUnit":%d,"rank":2,"role":"staff","phone":"010-1234-5678","extension":"7001","employeeNo":"K-001","effectiveDate":"2021-01-04","reason":"입사 등록","confirm":true}`, deptID), 201)
	hongID := num(hired["userId"])
	card := func(who string) map[string]any {
		return e.must(who, "GET", fmt.Sprintf("/api/org/people/%d", hongID), "", 200)
	}
	if c := card("admin"); c["phone"] != "010-1234-5678" || c["email"] != "hong@pl.test" {
		t.Errorf("관리자는 원문: %v", c)
	}
	if c := card("finance"); c["phone"] != "010-1234-5678" {
		t.Errorf("재무(마스크 해제 권한)는 원문: %v", c)
	}
	for _, who := range []string{"lead", "staff", "auditor"} {
		c := card(who)
		if c["phone"] == "010-1234-5678" || c["email"] == "hong@pl.test" || c["extension"] != "7001" {
			t.Errorf("%s 에게 연락처 원문 노출 또는 내선 누락: %v", who, c)
		}
	}
	// 응답 본문 어디에도 원문이 없다(목록·검색 포함).
	for _, who := range []string{"lead", "staff", "auditor"} {
		for _, path := range []string{"/api/org/people?q=홍길동", fmt.Sprintf("/api/org/units/%d/members", deptID)} {
			if _, resp := e.do(who, "GET", path, ""); strings.Contains(resp, "010-1234-5678") || strings.Contains(resp, "hong@pl.test") {
				t.Errorf("%s %s: 응답에 연락처 원문", who, path)
			}
		}
	}
	// 이력(사유 포함)은 admin·auditor 만 본다. 사람 카드 응답에는 사유가 없다(AC-68a).
	for _, who := range []string{"finance", "lead", "staff"} {
		if code, _ := e.do(who, "GET", fmt.Sprintf("/api/org/people/%d/history", hongID), ""); code != http.StatusForbidden {
			t.Errorf("%s 이력: %d, want 403", who, code)
		}
	}
	for _, who := range []string{"admin", "auditor"} {
		if code, _ := e.do(who, "GET", fmt.Sprintf("/api/org/people/%d/history", hongID), ""); code != http.StatusOK {
			t.Errorf("%s 이력: %d, want 200", who, code)
		}
	}
	// 퇴사자는 org:history 가 없는 역할에게 검색되지 않는다(AC-57). 어제까지 근무한 사람으로 만든다.
	yesterday := string(contract.DayOf(time.Now().Add(-24 * time.Hour)))
	e.must("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","leaveKind":"자진","reason":"퇴사 처리","confirm":true}`, hongID, yesterday), 201)
	if _, resp := e.do("staff", "GET", "/api/org/people?q=홍길동&includeLeft=1", ""); strings.Contains(resp, "홍길동") {
		t.Errorf("권한 없는 호출자에게 퇴사자: %s", resp)
	}
	if _, resp := e.do("auditor", "GET", "/api/org/people?q=홍길동&includeLeft=1", ""); !strings.Contains(resp, "홍길동") {
		t.Errorf("감사 담당(org:history)은 퇴사자를 본다: %s", resp)
	}
	_ = rootID
}

// 미리보기는 저장하지 않고 감사도 남기지 않는다(AC-38). 30일 넘는 소급은 409 확인 요청(AC-32).
func TestOrgAPIPreviewAndRetroConfirm(t *testing.T) {
	e := newOrgEnv(t)
	staffID := e.userID("staff@pl.test")
	root := e.must("admin", "GET", "/api/org/tree", "", 200)["root"].(map[string]any)
	dept := root["children"].([]any)[1].(map[string]any)
	events, audits := e.count(`SELECT COUNT(*) FROM org_events`), e.count(`SELECT COUNT(*) FROM audit_logs`)
	pv := e.must("admin", "POST", "/api/org/events/preview", fmt.Sprintf(`{"kind":"transfer","userId":%d,"toUnit":%d}`, staffID, num(dept["id"])), 200)
	if effects, _ := pv["effects"].([]any); len(effects) == 0 {
		t.Fatalf("미리보기 영향 없음: %v", pv)
	}
	if e.count(`SELECT COUNT(*) FROM org_events`) != events || e.count(`SELECT COUNT(*) FROM audit_logs`) != audits {
		t.Fatal("미리보기가 저장하거나 감사를 남김")
	}
	// 소급: 사유 없으면 400, 30일 넘으면 409 + needConfirm, 확인하면 201
	body := fmt.Sprintf(`{"kind":"rank_change","userId":%d,"rank":3,"effectiveDate":"2020-06-01","reason":"소급 승진"}`, staffID)
	resp := e.must("admin", "POST", "/api/org/events", body, 409)
	if resp["needConfirm"] != true || num(resp["days"]) < 1000 {
		t.Fatalf("확인 요청: %v", resp)
	}
	if code, msg := e.do("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"rank_change","userId":%d,"rank":3,"effectiveDate":"2020-06-01"}`, staffID)); code != 400 || !strings.Contains(msg, "사유") {
		t.Fatalf("사유 없는 소급: %d %s", code, msg)
	}
	ok := e.must("admin", "POST", "/api/org/events", strings.Replace(body, `"reason"`, `"confirm":true,"reason"`, 1), 201)
	evID := num(ok["eventIds"].([]any)[0])
	// 취소는 사유가 필요하고 감사 기록이 남는다. 취소된 이벤트는 목록에 "취소됨".
	if code, _ := e.do("admin", "POST", fmt.Sprintf("/api/org/events/%d/cancel", evID), `{"reason":""}`); code != 400 {
		t.Fatalf("사유 없는 취소: %d", code)
	}
	e.must("admin", "POST", fmt.Sprintf("/api/org/events/%d/cancel", evID), `{"reason":"착오","confirm":true}`, 200)
	list := e.must("admin", "GET", "/api/org/events?status=cancelled", "", 200)["events"].([]any)
	found := false
	for _, x := range list {
		found = found || num(x.(map[string]any)["id"]) == evID
	}
	if !found {
		t.Fatalf("취소된 이벤트가 목록에 없음: %v", list)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.event_cancel'`) != 1 {
		t.Fatal("취소 감사 기록")
	}
}

// 정합성 목록·직위 표·기준일 조회.
func TestOrgAPIRanksConsistencyAndAsOf(t *testing.T) {
	e := newOrgEnv(t)
	ranks := e.must("admin", "GET", "/api/org/ranks", "", 200)["ranks"].([]any)
	if len(ranks) < 9 {
		t.Fatalf("직위 시드: %d", len(ranks))
	}
	first := ranks[0].(map[string]any)
	if num(first["inUse"]) == 0 {
		t.Fatalf("백필된 사람은 첫 직위(사원)를 쓴다: %v", first)
	}
	// 사용 중인 직위는 중지할 수 없다.
	if code, msg := e.do("admin", "PUT", "/api/org/ranks", fmt.Sprintf(`{"ranks":[{"id":%d,"name":"사원","sort":10,"active":false}]}`, num(first["id"]))); code != 400 || !strings.Contains(msg, "사용 중인 직위") {
		t.Fatalf("사용 중 직위 중지: %d %s", code, msg)
	}
	e.must("admin", "PUT", "/api/org/ranks", `{"ranks":[{"id":0,"name":"수석","sort":100,"active":true}]}`, 200)
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'org.rank_set'`) != 1 {
		t.Fatal("직위 표 변경 감사")
	}
	// 기준일: 입사 전 날짜(2019)에는 조직 트리가 비어 있다.
	if tree := e.must("staff", "GET", "/api/org/tree?asOf=2019-01-01", "", 200); tree["root"] != nil {
		t.Fatalf("조직 설립 전 트리: %v", tree)
	}
	if code, _ := e.do("staff", "GET", "/api/org/tree?asOf=2019-02-30", ""); code != http.StatusBadRequest {
		t.Fatalf("기준일 형식: %d", code)
	}
	if _, resp := e.do("admin", "GET", "/api/org/consistency", ""); !strings.Contains(resp, "issues") {
		t.Fatalf("정합성: %s", resp)
	}
}

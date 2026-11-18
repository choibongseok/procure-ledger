package main

// B41 1단계: 개인정보 조회 기록을 실제 라우트·세션·권한 표로 태운다(AC-4101·4103·4104·4106·4108·4109).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestPrivacyRoutesDeclaredAndByRole(t *testing.T) {
	e := newOrgEnv(t)
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
		if strings.Contains(r.Pattern, "/api/admin/privacy") && r.Method != "GET" && r.Method != "POST" && r.Method != "PUT" {
			t.Errorf("조회 기록에 %s 라우트: %s", r.Method, r.Pattern)
		}
	}
	for k, p := range map[string]contract.Permission{
		"GET /api/admin/privacy/log": contract.PermPrivacyLogRead, "GET /api/admin/privacy/stats": contract.PermPrivacyLogRead,
		"POST /api/admin/privacy/verify": contract.PermPrivacyLogRead, "GET /api/admin/privacy/log/{id}/subjects": contract.PermPrivacyLogRead,
		"GET /api/admin/privacy/retention": contract.PermPrivacyLogManage, "PUT /api/admin/privacy/retention": contract.PermPrivacyLogManage,
	} {
		if got[k] != p {
			t.Errorf("%s 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	// 감사만 기록을 읽고(관리자도 못 읽음), 보관 기간은 관리자만 본다.
	for who, want := range map[string][2]int{"auditor": {200, 403}, "admin": {403, 200}, "finance": {403, 403}, "staff": {403, 403}} {
		if code, body := e.do(who, "GET", "/api/admin/privacy/log", ""); code != want[0] {
			t.Errorf("%s 조회 기록: %d(기대 %d) %s", who, code, want[0], body)
		}
		if code, body := e.do(who, "GET", "/api/admin/privacy/retention", ""); code != want[1] {
			t.Errorf("%s 보관 설정: %d(기대 %d) %s", who, code, want[1], body)
		}
	}
	if code, _ := e.call(e.browser(), "GET", "/api/admin/privacy/log", "", nil); code != 401 {
		t.Errorf("미인증: %d", code)
	}
}

// 원문 포함 내보내기는 감사와 같은 트랜잭션에서 조회 기록을 남기고, 정보주체는 내보낸 사람들이다. 기본 내보내기·거부된 요청은 기록이 없다.
func TestPrivacyRawExportIsRecorded(t *testing.T) {
	e := newOrgEnv(t)
	if f := e.file("admin", "/api/org/people/export?format=csv"); f.code != 200 {
		t.Fatalf("기본 내보내기 %d", f.code)
	}
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 0 {
		t.Fatal("마스킹된 내보내기가 기록을 남김")
	}
	if f := e.file("staff", "/api/org/people/export?format=csv&raw=1"); f.code != 403 || e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 0 {
		t.Fatalf("거부된 원문 포함이 기록을 남김: %d", f.code)
	}
	f := e.file("admin", "/api/org/people/export?format=csv&raw=1")
	if f.code != 200 || !strings.Contains(string(f.body), "staff@pl.test") {
		t.Fatalf("관리자의 원문 포함 %d", f.code)
	}
	var export, count int
	var channel, category, screen, dataset, fields string
	if err := e.d.DB.QueryRow(`SELECT is_export, subject_count, channel, category, screen, dataset, fields FROM privacy_access_log`).Scan(&export, &count, &channel, &category, &screen, &dataset, &fields); err != nil {
		t.Fatalf("조회 기록이 없음: %v", err)
	}
	if export != 1 || channel != "web" || category != "contact" || screen != "조직 구성원 내보내기" || !strings.Contains(fields, "email") {
		t.Errorf("조회 기록 내용: %d %s %s %s %s %s", export, channel, category, screen, dataset, fields)
	}
	// 한 응답 한 기록: 서비스 층(org.ListPeople)이 기록하고 내보내기 핸들러는 다시 기록하지 않는다.
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 1 {
		t.Errorf("원문 포함 내보내기 한 번이 기록 %d건을 남김(1건이어야 함)", e.count(`SELECT COUNT(*) FROM privacy_access_log`))
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(string(f.body), "\xEF\xBB\xBF")), "\r\n")
	rows := len(lines) - 1
	var adminID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID)
	// 정보주체 수 = 내보낸 행 수(열람자 본인은 제외라 하나 적거나 같다)
	subs := e.count(`SELECT COUNT(*) FROM privacy_access_subjects`)
	if count < rows-1 || count > rows || subs != count || e.count(`SELECT COUNT(*) FROM privacy_access_subjects WHERE subject_id = ?`, adminID) != 0 {
		t.Errorf("정보주체 수 %d/행 %d, 기록 건수 %d (본인은 빠져야 함)", subs, rows, count)
	}
	// 감사에도 export.run 이 남고, 조회 기록 값에 원문이 없다.
	if e.auditCount("export.run", "success") != 2 {
		t.Errorf("내보내기 감사 %d건", e.auditCount("export.run", "success"))
	}
}

// 조회 기록을 쓰지 못하면(표가 없는 상황을 흉내) 원문 파일이 나가지 않는다(AC-4104).
func TestPrivacyExportFailClosed(t *testing.T) {
	e := newOrgEnv(t)
	if _, err := e.d.DB.Exec(`ALTER TABLE privacy_access_log RENAME TO privacy_access_log_x`); err != nil {
		t.Fatal(err)
	}
	f := e.file("admin", "/api/org/people/export?format=csv&raw=1")
	if f.code != 500 || strings.Contains(string(f.body), "staff@pl.test") {
		t.Errorf("기록 실패인데 원문 파일이 나감: %d %q", f.code, f.body)
	}
	if _, err := e.d.DB.Exec(`ALTER TABLE privacy_access_log_x RENAME TO privacy_access_log`); err != nil {
		t.Fatal(err)
	}
	if f := e.file("admin", "/api/org/people/export?format=csv&raw=1"); f.code != 200 {
		t.Errorf("복구 뒤 원문 포함: %d", f.code)
	}
}

func TestPrivacyAuditorFlow(t *testing.T) {
	e := newOrgEnv(t)
	for i := 0; i < 3; i++ {
		e.file("admin", "/api/org/people/export?format=csv&raw=1")
	}
	code, body := e.do("auditor", "GET", "/api/admin/privacy/log?export=1&category=contact", "")
	var list struct {
		Items []struct {
			ID           int64
			ActorName    string
			Screen       string
			IsExport     bool
			SubjectCount int
		}
		RetentionDays int
	}
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Items) != 3 || list.RetentionDays != 730 || !list.Items[0].IsExport || list.Items[0].ActorName == "" {
		t.Fatalf("목록: %d %s", code, body)
	}
	code, body = e.do("auditor", "GET", "/api/admin/privacy/log/"+itoa(list.Items[0].ID)+"/subjects?limit=5", "")
	var subs struct {
		Items []struct{ Name, Unit string }
		Total int
	}
	if code != 200 || json.Unmarshal([]byte(body), &subs) != nil || len(subs.Items) != 5 || subs.Total < 5 || subs.Items[0].Name == "" {
		t.Fatalf("정보주체: %d %s", code, body)
	}
	if code, body := e.do("auditor", "GET", "/api/admin/privacy/stats?days=30", ""); code != 200 || !strings.Contains(body, `"total":3`) {
		t.Errorf("통계: %d %s", code, body)
	}
	code, body = e.do("auditor", "POST", "/api/admin/privacy/verify", "")
	if code != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"checked":3`) {
		t.Errorf("무결성 확인: %d %s", code, body)
	}
	// 읽기도 감사에 남는다(목록·정보주체·통계·무결성 확인).
	if n := e.auditCount("privacy.log_read", "success"); n != 4 {
		t.Errorf("privacy.log_read 감사 %d건", n)
	}
	// 관리자는 보관 기간만 바꾼다(감사 privacy.retention_set). 범위 밖은 400, 감사는 못 바꾼다.
	if code, _ := e.do("admin", "PUT", "/api/admin/privacy/retention", `{"days":100}`); code != 400 {
		t.Errorf("범위 밖 보관 기간: %d", code)
	}
	if code, _ := e.do("auditor", "PUT", "/api/admin/privacy/retention", `{"days":800}`); code != 403 {
		t.Errorf("감사의 보관 기간 변경: %d", code)
	}
	if code, body := e.do("admin", "PUT", "/api/admin/privacy/retention", `{"days":800}`); code != 200 || !strings.Contains(body, `"retentionDays":800`) {
		t.Errorf("보관 기간 변경: %d %s", code, body)
	}
	if e.auditCount("privacy.retention_set", "success") != 1 {
		t.Error("보관 기간 변경 감사가 없음")
	}
}

package main

// B40 2단계: 전보·직위 변경·연락처·재입사·충돌·경고·허용 출발지(AC-4008·4010·4012·4014·4016).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

func (s *scimEnv) today() string { return string(s.d.Org.(*org.Service).Today()) }

func (s *scimEnv) events(kind string, uid string) int {
	return s.count(`SELECT COUNT(*) FROM org_events WHERE subject_type='person' AND kind = ? AND subject_id = ?`, kind, uid)
}

// seedStaff 는 시드에서 퇴사 이력이 없는 재직 직원 이메일을 순서대로 하나씩 고른다(시드에는 퇴사자도 섞여 있다).
func (s *scimEnv) seedStaff(skip int) string {
	s.t.Helper()
	rows, err := s.d.DB.Query(`SELECT u.email FROM users u WHERE u.email LIKE 'p0%@pl.test' AND u.role = 'staff' AND u.active = 1
		AND NOT EXISTS (SELECT 1 FROM org_events e WHERE e.subject_type='person' AND e.subject_id = u.id AND e.kind = 'leave')
		AND NOT EXISTS (SELECT 1 FROM user_effective_permissions p WHERE p.user_id = u.id AND p.permission IN ('role:manage','ledger:write','payment:write'))
		AND NOT EXISTS (SELECT 1 FROM org_events e WHERE e.subject_type='person' AND e.subject_id = u.id AND e.kind = 'hire' AND e.effective_date >= '2024-07-01') ORDER BY u.id`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	for i := 0; rows.Next(); i++ {
		var e string
		_ = rows.Scan(&e)
		if i == skip {
			return e
		}
	}
	s.t.Fatalf("시드에 재직 직원이 %d명 미만", skip+1)
	return ""
}

const entDept = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department"

func TestSCIMTransferRankAndPhone(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	r := c.EntraSync("ext-mv", "mover@corp.test", "이동 사원", "T-PU1", "사원", true)
	if r.Status != 201 {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	// 부서(코드)와 직위를 한 번에: 한 요청이 전보와 직위 변경 두 발령을 만든다(효력일은 오늘).
	r2 := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("replace", entDept, "T-PU2"), scimtest.Op("replace", "title", "대리")))
	if r2.Status != 200 || r2.Body["title"] != "대리" {
		t.Fatalf("전보·직위: %d %s", r2.Status, r2.Raw)
	}
	if ent, _ := r2.Body[scimtest.EnterpriseSchema].(map[string]any); ent["department"] != "구매2팀" {
		t.Errorf("부서가 구매2팀이 아님: %v", r2.Body[scimtest.EnterpriseSchema])
	}
	if s.events("transfer", id) != 1 || s.events("rank_change", id) != 1 {
		t.Error("전보·직위 변경 발령이 1건씩이 아님")
	}
	for _, d := range s.eventDays(`kind IN ('transfer','rank_change') AND subject_id = ` + id) {
		if d != s.today() {
			t.Errorf("효력일 %s(오늘이 아님)", d)
		}
	}
	for _, a := range []string{"hr.transfer", "hr.rank_change"} {
		if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND channel='scim' AND target_id = ?`, a, id) != 1 {
			t.Errorf("%s 감사(채널 scim)가 1건이 아님", a)
		}
	}
	// 같은 값(코드·이름 어느 쪽이든)을 다시 보내면 변경 없음: 이벤트·기록이 늘지 않는다.
	logs := s.count(`SELECT COUNT(*) FROM scim_sync_log`)
	u := scimtest.User("mover@corp.test", "ext-mv", "이동 사원", "구매2팀", "대리")
	if r3 := c.Put("/Users/"+id, u); r3.Status != 200 {
		t.Fatalf("PUT: %d %s", r3.Status, r3.Raw)
	}
	u[scimtest.EnterpriseSchema] = map[string]any{"department": "T-PU2"}
	if r3 := c.Put("/Users/"+id, u); r3.Status != 200 {
		t.Fatalf("PUT(코드): %d %s", r3.Status, r3.Raw)
	}
	if s.events("transfer", id) != 1 || s.count(`SELECT COUNT(*) FROM scim_sync_log`) != logs {
		t.Error("같은 값 재전송이 발령·기록을 만듦")
	}
	// 이름으로 되돌리는 전보.
	if r4 := c.Put("/Users/"+id, scimtest.User("mover@corp.test", "ext-mv", "이동 사원", "구매1팀", "대리")); r4.Status != 200 || s.events("transfer", id) != 2 {
		t.Fatalf("이름으로 전보: %d %s", r4.Status, r4.Raw)
	}
	// 없는 부서·없는 직위는 400 이고 아무것도 바뀌지 않는다(같은 요청의 다른 변경도 함께 취소).
	before := s.count(`SELECT COUNT(*) FROM org_events`)
	for name, b := range map[string]map[string]any{
		"없는 부서": scimtest.Patch(scimtest.Op("replace", "title", "과장"), scimtest.Op("replace", entDept, "NOPE")),
		"없는 직위": scimtest.Patch(scimtest.Op("replace", "title", "우주대장")),
	} {
		if r5 := c.Patch("/Users/"+id, b); r5.Status != 400 || r5.ScimType() != "invalidValue" {
			t.Errorf("%s: %d %s", name, r5.Status, r5.Raw)
		}
	}
	if s.count(`SELECT COUNT(*) FROM org_events`) != before {
		t.Error("거부된 요청이 발령을 남김(전부 취소되어야 함)")
	}
	// 연락처(발령이 아님): org_people.phone 과 감사(user.update, 번호 원문 없음).
	phone := scimtest.Patch(scimtest.Op("replace", `phoneNumbers[type eq "work"].value`, "010-9999-0000"))
	if r6 := c.Patch("/Users/"+id, phone); r6.Status != 200 {
		t.Fatalf("연락처: %d %s", r6.Status, r6.Raw)
	}
	var got string
	_ = s.d.DB.QueryRow(`SELECT phone FROM org_people WHERE user_id = ?`, id).Scan(&got)
	if got != "010-9999-0000" {
		t.Errorf("연락처 %q", got)
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='user.update' AND channel='scim' AND target_id = ? AND diff LIKE '%phone%'`, id) != 1 ||
		s.count(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%9999%' OR detail LIKE '%9999%'`) != 0 {
		t.Error("연락처 감사가 없거나 번호 원문이 남음")
	}
	// 공백·괄호는 하이픈으로 맞춰 반영하고, 형식이 맞지 않는 값은 그 속성만 경고로 남기며 요청은 성공한다.
	if r6 := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("replace", `phoneNumbers[type eq "work"].value`, "(010) 1234 5678"))); r6.Status != 200 {
		t.Fatalf("공백 연락처: %d %s", r6.Status, r6.Raw)
	}
	_ = s.d.DB.QueryRow(`SELECT phone FROM org_people WHERE user_id = ?`, id).Scan(&got)
	if got != "010-1234-5678" {
		t.Errorf("정규화한 연락처 %q", got)
	}
	if r7 := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("replace", `phoneNumbers[type eq "work"].value`, "전화번호 아님!"))); r7.Status != 200 {
		t.Errorf("잘못된 연락처가 요청 전체를 실패시킴: %d %s", r7.Status, r7.Raw)
	}
	_ = s.d.DB.QueryRow(`SELECT phone FROM org_people WHERE user_id = ?`, id).Scan(&got)
	if got != "010-1234-5678" {
		t.Errorf("잘못된 연락처가 반영됨: %q", got)
	}
	if code, body := s.call(s.admin, "GET", "/api/admin/scim/log?limit=1", "", nil); code != 200 || !strings.Contains(body, "연락처를 바꾸지 못했습니다") {
		t.Errorf("연락처 경고가 기록에 없음: %s", body)
	}
	// 퇴사 상태인 사용자의 부서·직위 변경은 재입사 때 반영한다(경고만).
	dr := c.Post("/Users", scimtest.User(s.seedStaff(0), "ext-p020", "", "", ""))
	if dr.Status != 201 {
		t.Fatalf("채택: %d %s", dr.Status, dr.Raw)
	}
	if x := c.Patch("/Users/"+dr.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); x.Status != 200 {
		t.Fatalf("퇴사: %d %s", x.Status, x.Raw)
	}
	if x := c.Patch("/Users/"+dr.ID(), scimtest.Patch(scimtest.Op("replace", "title", "과장"))); x.Status != 200 || s.events("rank_change", dr.ID()) != 0 {
		t.Errorf("퇴사자 직위 변경: %d", x.Status)
	}
	if code, body := s.call(s.admin, "GET", "/api/admin/scim/log?limit=3", "", nil); code != 200 || !strings.Contains(body, "재입사 때 반영") {
		t.Errorf("퇴사자 경고가 기록에 없음: %s", body)
	}
	assertChainOK(t, s)
}

func TestSCIMEmailConflictsAndRehire(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	entra := s.client(s.token("entra", "users"))
	okta := s.client(s.token("okta", "users"))
	r := entra.Post("/Users", scimtest.User("conflict@corp.test", "ext-c", "충돌 사용자", "T-PU1", "사원"))
	if r.Status != 201 {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	// 대소문자만 다른 이메일은 같은 사용자(멱등), 다른 출처가 같은 이메일을 보내면 409.
	if x := entra.Post("/Users", scimtest.User("CONFLICT@Corp.test", "ext-c", "충돌 사용자", "T-PU1", "사원")); x.Status != 200 || x.ID() != r.ID() {
		t.Errorf("대소문자 차이: %d %s", x.Status, x.Raw)
	}
	if x := okta.Post("/Users", scimtest.User("conflict@corp.test", "", "충돌 사용자", "T-PU1", "사원")); x.Status != 409 || x.ScimType() != "uniqueness" {
		t.Errorf("다른 출처: %d %s", x.Status, x.Raw)
	}
	if x := okta.Get("/Users/" + r.ID()); x.Status != 404 {
		t.Errorf("다른 출처가 남의 사용자를 봄: %d", x.Status)
	}
	// 연결이 없는 퇴사자의 이메일로 새 externalId 가 오면 재입사 경로로 간다(새 사용자를 만들지 않는다).
	p30, p31, p32 := s.seedStaff(1), s.seedStaff(2), s.seedStaff(3)
	a := entra.Post("/Users", scimtest.User(p30, "ext-p030", "", "", ""))
	if a.Status != 201 {
		t.Fatalf("채택: %d %s", a.Status, a.Raw)
	}
	var empNo string
	_ = s.d.DB.QueryRow(`SELECT employee_no FROM org_people WHERE user_id = ?`, a.ID()).Scan(&empNo)
	if x := entra.Delete("/Users/" + a.ID()); x.Status != 204 {
		t.Fatalf("퇴사: %d", x.Status)
	}
	if _, err := s.d.DB.Exec(`DELETE FROM scim_links WHERE user_id = ?`, a.ID()); err != nil {
		t.Fatal(err)
	}
	users := s.count(`SELECT COUNT(*) FROM users`)
	// 사람이 따로 준 직접 부여(비위험 역할)는 재입사 뒤에도 남는다.
	if _, err := s.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, include_descendants, source, reason, created_at)
		SELECT id, 'user', ?, 0, 'manual', '시험', '2026-11-01T00:00:00Z' FROM roles WHERE key = 'manager'`, a.ID()); err != nil {
		t.Fatal(err)
	}
	b := entra.Post("/Users", scimtest.User(p30, "ext-p030-new", "", "T-PU1", "사원"))
	if b.Status != 201 || b.ID() != a.ID() || !b.Active() {
		t.Fatalf("재입사: %d %s", b.Status, b.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM users`) != users {
		t.Error("재입사가 새 사용자를 만듦")
	}
	var empNo2 string
	_ = s.d.DB.QueryRow(`SELECT employee_no FROM org_people WHERE user_id = ?`, a.ID()).Scan(&empNo2)
	if empNo2 != empNo {
		t.Errorf("재입사로 사번이 바뀜: %s → %s", empNo, empNo2)
	}
	if s.count(`SELECT COUNT(*) FROM role_grants WHERE target_type='user' AND target_id = ? AND source='manual'`, a.ID()) != 1 {
		t.Error("재입사 뒤 직접 부여가 사라짐")
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.hire' AND channel='scim' AND target_id = ?`, a.ID()) != 1 {
		t.Error("재입사 발령 감사가 1건이 아님")
	}
	// 직접 위험 권한이 있는 퇴사자는 보호 계정이라 재입사가 거부된다.
	c1 := entra.Post("/Users", scimtest.User(p31, "ext-p031", "", "", ""))
	if x := entra.Patch("/Users/"+c1.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); x.Status != 200 {
		t.Fatalf("퇴사: %d %s", x.Status, x.Raw)
	}
	if _, err := s.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, include_descendants, source, reason, created_at)
		SELECT id, 'user', ?, 0, 'manual', '시험', '2026-11-01T00:00:00Z' FROM roles WHERE key = 'admin'`, c1.ID()); err != nil {
		t.Fatal(err)
	}
	if x := entra.Patch("/Users/"+c1.ID(), scimtest.Patch(scimtest.Op("replace", "active", true))); x.Status != 400 || x.ScimType() != "mutability" {
		t.Errorf("위험 권한 보유 퇴사자의 재입사: %d %s", x.Status, x.Raw)
	}
	// 조직 단위로 위험 역할이 부여된 조직으로 재입사하면 위험 권한 게이트가 거부한다(SCIM 주체에는 role:manage 가 없다).
	d1 := entra.Post("/Users", scimtest.User(p32, "ext-p032", "", "", ""))
	if x := entra.Patch("/Users/"+d1.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); x.Status != 200 {
		t.Fatalf("퇴사: %d %s", x.Status, x.Raw)
	}
	var unitID int64
	_ = s.d.DB.QueryRow(`SELECT id FROM org_units WHERE code = 'T-IN'`).Scan(&unitID)
	if _, err := s.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, include_descendants, source, reason, created_at)
		SELECT id, 'unit', ?, 0, 'manual', '시험', '2026-11-01T00:00:00Z' FROM roles WHERE key = 'admin'`, unitID); err != nil {
		t.Fatal(err)
	}
	if err := access.Recompute(context.Background(), s.d.DB, contract.Day(s.today())); err != nil {
		t.Fatal(err)
	}
	before := s.events("hire", d1.ID())
	x := entra.Patch("/Users/"+d1.ID(), scimtest.Patch(scimtest.Op("replace", "", map[string]any{"active": true, scimtest.EnterpriseSchema: map[string]any{"department": "T-IN"}})))
	if x.Status != 400 || x.ScimType() != "mutability" {
		t.Errorf("위험 역할 조직으로 재입사: %d %s", x.Status, x.Raw)
	}
	if s.events("hire", d1.ID()) != before {
		t.Error("거부된 재입사가 발령을 남김")
	}
	if s.auditCount("scim.rejected", "denied") < 1 {
		t.Error("거부 감사가 없음")
	}
	assertChainOK(t, s)
}

func TestSCIMLeaveWarningsHeadAndApprovals(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	var headEmail string
	var headID int64
	if err := s.d.DB.QueryRow(`SELECT u.id, u.email FROM users u JOIN org_unit_spans sp ON sp.head_user_id = u.id AND sp.head_acting = 0 AND sp.valid_to IS NULL
		WHERE u.email LIKE 'p%@pl.test' ORDER BY u.id LIMIT 1`).Scan(&headID, &headEmail); err != nil {
		t.Skipf("조직장인 일반 사용자가 시드에 없음: %v", err)
	}
	a := c.Post("/Users", scimtest.User(headEmail, "ext-head", "", "", ""))
	if a.Status != 201 {
		t.Fatalf("채택: %d %s", a.Status, a.Raw)
	}
	// 이 사람이 결재 차례인 진행 중 결재 2건.
	var dept, requester int64
	_ = s.d.DB.QueryRow(`SELECT id, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&requester, &dept)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	if _, err := s.d.DB.Exec(`INSERT INTO vendors (id, name) VALUES (9001, 'SCIM 시험 거래처')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := s.d.DB.Exec(`INSERT INTO purchase_requests (id, no, requester_id, department_id, vendor_id, title, status, total_amount, round, created_at, updated_at)
			VALUES (?, ?, ?, ?, 9001, '복사용지', 'in_review', 1000, 1, ?, ?)`, 9000+i, fmt.Sprintf("PR-9%05d", i), requester, dept, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.d.DB.Exec(`INSERT INTO approval_steps (pr_id, round, seq, step_kind, status) VALUES (?, 1, 1, 'team_lead', 'pending')`, 9000+i); err != nil {
			t.Fatal(err)
		}
		if _, err := s.d.DB.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (?, 1, 1, ?, 'x')`, 9000+i, headID); err != nil {
			t.Fatal(err)
		}
	}
	if x := c.Patch("/Users/"+a.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); x.Status != 200 || x.Active() {
		t.Fatalf("퇴사: %d %s", x.Status, x.Raw)
	}
	code, body := s.call(s.admin, "GET", "/api/admin/scim/log?limit=1", "", nil)
	if code != 200 || !strings.Contains(body, "조직장 공석") || !strings.Contains(body, "진행 중 결재 2건") || !strings.Contains(body, "deactivate") {
		t.Errorf("퇴사 경고(조직장 공석·진행 중 결재 2건): %s", body)
	}
	assertChainOK(t, s)
}

func TestSCIMConcurrentPatch(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	r := c.Post("/Users", scimtest.User(s.seedStaff(4), "ext-p040", "", "", ""))
	if r.Status != 201 {
		t.Fatalf("채택: %d %s", r.Status, r.Raw)
	}
	// 같은 사용자에 대한 동시 퇴사 PATCH: 퇴사 발령은 한 번만(나머지는 변경 없음), 같은 externalId 의 동시 POST 도 사용자 하나.
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = c.Patch("/Users/"+r.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))).Status
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("동시 PATCH %d: %d", i, code)
		}
	}
	if s.events("leave", r.ID()) != 1 {
		t.Errorf("퇴사 발령 %d건(1건이어야 함)", s.events("leave", r.ID()))
	}
	posts := make([]int, 6)
	for i := range posts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			posts[i] = c.Post("/Users", scimtest.User("race@corp.test", "ext-race", "경쟁 사용자", "T-PU1", "사원")).Status
		}()
	}
	wg.Wait()
	created := 0
	for _, code := range posts {
		if code == 201 {
			created++
		} else if code != 200 {
			t.Errorf("동시 POST: %d", code)
		}
	}
	if created != 1 || s.count(`SELECT COUNT(*) FROM users WHERE lower(email)='race@corp.test'`) != 1 {
		t.Errorf("동시 POST 로 사용자가 하나가 아님(201 %d건)", created)
	}
	assertChainOK(t, s)
}

func TestSCIMAllowCIDR(t *testing.T) {
	s := newSCIMEnv(t, func(c *config.Config) { c.SCIMAllowCIDR = "203.0.113.0/24, 198.51.100.7" })
	s.enable()
	tok := s.token("entra", "users")
	// 시험 클라이언트는 127.0.0.1 로 붙으므로 허용 목록 밖이라 토큰이 맞아도 403.
	if r := s.client(tok).Get("/ServiceProviderConfig"); r.Status != 403 {
		t.Fatalf("허용 밖 출발지: %d %s", r.Status, r.Raw)
	}
	if s.auditCount("scim.rejected", "denied") != 1 {
		t.Error("출발지 거부 감사가 1건이 아님")
	}
	s2 := newSCIMEnv(t, func(c *config.Config) { c.SCIMAllowCIDR = "127.0.0.0/8" })
	s2.enable()
	if r := s2.client(s2.token("entra", "users")).Get("/ServiceProviderConfig"); r.Status != 200 {
		t.Errorf("허용 출발지: %d %s", r.Status, r.Raw)
	}
}

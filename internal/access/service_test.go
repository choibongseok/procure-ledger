package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

type env struct {
	db    *sql.DB
	svc   *Service
	clock *time.Time
	admin contract.Principal // role:manage 보유자 1
	boss  contract.Principal // role:manage 보유자 2
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := newDB(t)
	ctx := context.Background()
	if _, err := SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	e := &env{db: db, clock: &now}
	e.svc = New(db, contract.SQLTxManager{DB: db}, audit.New(db), func() time.Time { return *e.clock })
	mk := func(email, role string) contract.Principal {
		id := addUser(t, db, email, role)
		perms, _ := PermsOf(ctx, db, id)
		return contract.Principal{UserID: id, Name: email, Role: contract.Role(role), Perms: perms, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	}
	e.admin = mk("admin@x", "admin")
	e.boss = mk("boss@x", "admin")
	return e
}

func (e *env) staff(t *testing.T, email string) contract.Principal {
	id := addUser(t, e.db, email, "staff")
	perms, _ := PermsOf(context.Background(), e.db, id)
	return contract.Principal{UserID: id, Name: email, Role: contract.RoleStaff, Perms: perms, Channel: contract.ChannelWeb}
}

func (e *env) n(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) auditCount(t *testing.T, action string) int {
	return e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = ?`, action)
}

func (e *env) roleID(t *testing.T, key string) int64 {
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM roles WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) perms(t *testing.T, id int64) contract.PermSet {
	p, err := PermsOf(context.Background(), e.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ---- 역할 ----

func TestRoleCrudRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	if _, err := e.svc.ListRoles(ctx, s); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 목록: %v", err)
	}
	r, err := e.svc.CreateRole(ctx, e.admin, " 구매 전담 ", "구매만", []string{"purchase:create", "purchase:read", "purchase:read", "inventory:read"})
	if err != nil || r.Kind != "custom" || len(r.Permissions) != 3 || !strings.HasPrefix(r.Key, "r_") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := e.svc.CreateRole(ctx, e.admin, "구매 전담", "", nil); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("이름 중복: %v", err)
	}
	if _, err := e.svc.CreateRole(ctx, e.admin, "직원", "", nil); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("템플릿 이름과 중복: %v", err)
	}
	for _, bad := range []string{"", "  ", strings.Repeat("가", 101), "a<b"} {
		if _, err := e.svc.CreateRole(ctx, e.admin, bad, "", nil); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("이름 %q 허용됨: %v", bad, err)
		}
	}
	if _, err := e.svc.CreateRole(ctx, e.admin, "엉뚱", "", []string{"no:such"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 권한: %v", err)
	}
	// 템플릿은 고치거나 지울 수 없지만 복제할 수 있다.
	tpl := e.roleID(t, "finance")
	if _, err := e.svc.UpdateRole(ctx, e.admin, tpl, 1, "재무", "", nil, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("템플릿 수정: %v", err)
	}
	if err := e.svc.DeleteRole(ctx, e.admin, tpl); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("템플릿 삭제: %v", err)
	}
	cl, err := e.svc.CloneRole(ctx, e.admin, tpl, "회계 팀장")
	if err != nil || cl.Kind != "custom" || len(cl.Permissions) != len(contract.RolePermissions[contract.RoleFinance]) {
		t.Fatalf("복제: %+v %v", cl, err)
	}
	// 수정: 낙관적 잠금.
	up, err := e.svc.UpdateRole(ctx, e.admin, r.ID, r.Version, "구매 전담", "설명 바꿈", []string{"purchase:create"}, "")
	if err != nil || up.Role == nil || up.Role.Version != r.Version+1 || len(up.Role.Permissions) != 1 {
		t.Fatalf("%+v %v", up, err)
	}
	if _, err := e.svc.UpdateRole(ctx, e.admin, r.ID, r.Version, "구매 전담", "", nil, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("옛 version 으로 수정(409): %v", err)
	}
	// 부여가 있으면 삭제할 수 없고, 회수하면 삭제된다(이름은 재사용 가능).
	g, err := e.svc.AddGrant(ctx, e.admin, r.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	if err != nil || g.AffectedUsers != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	if err := e.svc.DeleteRole(ctx, e.admin, r.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("부여된 역할 삭제: %v", err)
	}
	if err := e.svc.RevokeGrant(ctx, e.admin, g.GrantID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteRole(ctx, e.admin, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateRole(ctx, e.admin, "구매 전담", "", nil); err != nil {
		t.Fatalf("삭제한 이름 재사용: %v", err)
	}
	for _, a := range []string{"role.create", "role.update", "role.grant", "role.revoke", "role.delete"} {
		if e.auditCount(t, a) == 0 {
			t.Errorf("감사 %s 없음", a)
		}
	}
	// 감사에 권한이 한국어 라벨용 코드로만 남고 비밀은 없다. 감사 체인은 온전하다.
	if res, err := audit.New(e.db).Verify(ctx); err != nil || !res.OK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

// 사용자 정의 역할의 권한이 그 사람의 실효 권한이 되고 Can 이 그대로 판정한다. 회수하면 즉시 사라진다(AC-1312).
func TestCustomRoleGrantsEffectivePermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	r, _ := e.svc.CreateRole(ctx, e.admin, "감사 보조", "", []string{"audit:read"})
	if e.perms(t, s.UserID).Has(contract.PermAuditRead) {
		t.Fatal("부여 전인데 권한이 있음")
	}
	g, err := e.svc.AddGrant(ctx, e.admin, r.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	if err != nil {
		t.Fatal(err)
	}
	p := e.perms(t, s.UserID)
	if !p.Has(contract.PermAuditRead) || !p.Has(contract.PermPurchaseCreate) { // 합집합: 직원 템플릿 + 사용자 정의
		t.Fatalf("합집합이 아님: %v", SortedStrings(p))
	}
	if _, err := e.svc.AddGrant(ctx, e.admin, r.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 부여 중복: %v", err)
	}
	// 역할의 권한을 줄이면 즉시 반영, 회수하면 사라짐.
	if _, err := e.svc.UpdateRole(ctx, e.admin, r.ID, 1, "감사 보조", "", []string{"eval:read"}, ""); err != nil {
		t.Fatal(err)
	}
	if p := e.perms(t, s.UserID); p.Has(contract.PermAuditRead) || !p.Has(contract.PermEvalRead) {
		t.Fatalf("역할 수정이 반영되지 않음: %v", SortedStrings(p))
	}
	if err := e.svc.RevokeGrant(ctx, e.admin, g.GrantID); err != nil {
		t.Fatal(err)
	}
	if e.perms(t, s.UserID).Has(contract.PermEvalRead) {
		t.Fatal("회수했는데 권한이 남음")
	}
	// 출처: 직접 부여 두 건이 사람이 읽는 형태로 보인다.
	_, _ = e.svc.AddGrant(ctx, e.admin, r.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	ua, err := e.svc.UserAccess(ctx, e.admin, s.UserID)
	if err != nil || len(ua.Roles) != 2 {
		t.Fatalf("%+v %v", ua, err)
	}
	for _, src := range ua.Sources {
		if src.Via != "사람 직접" {
			t.Fatalf("출처 %+v", src)
		}
	}
	if my, err := e.svc.MyAccess(ctx, s); err != nil || my.UserID != s.UserID {
		t.Fatalf("%+v %v", my, err)
	}
}

// ---- 조직·직위·조직장 부여(AC-1311) ----

func (e *env) org(t *testing.T) {
	t.Helper()
	ex := func(q string, args ...any) {
		t.Helper()
		if _, err := e.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ex(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`)
	for i, c := range []string{"CO", "DIV", "DEP", "TEAM1", "TEAM2"} {
		ex(`INSERT INTO org_units (id, code) VALUES (?,?)`, i+1, c)
	}
	span := func(unit int, name, kind string, parent int, head int, acting int) {
		ex(`INSERT INTO org_unit_spans (unit_id, valid_from, name, code, kind, parent_id, head_user_id, head_acting) VALUES (?, '2020-01-01', ?, ?, ?, ?, ?, ?)`, unit, name, name, kind, parent, head, acting)
	}
	// 1 회사 > 2 본부 > 3 부서 > 4 팀1, 5 팀2(공석)
	u := func(email string) int64 {
		var id int64
		if err := e.db.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	span(1, "회사", "company", 0, 0, 0)
	span(2, "본부", "division", 1, 0, 0)
	span(3, "부서", "department", 2, 0, 0)
	span(4, "팀1", "team", 3, int(u("lead@x")), 0)
	span(5, "팀2", "team", 3, int(u("act@x")), 1) // 직무대행자만 있는 팀
	person := func(email string, rank, unit int, left int) {
		ex(`INSERT INTO org_person_spans (user_id, valid_from, left_flag, rank_id, primary_unit_id) VALUES (?, '2020-01-01', ?, ?, ?)`, u(email), left, rank, unit)
	}
	person("lead@x", 3, 4, 0)                                                                                    // 과장, 팀1
	person("m1@x", 1, 4, 0)                                                                                      // 사원, 팀1
	person("m2@x", 1, 5, 0)                                                                                      // 사원, 팀2
	person("act@x", 2, 5, 0)                                                                                     // 대리, 팀2 직무대행
	person("gone@x", 1, 4, 1)                                                                                    // 퇴사자, 팀1
	ex(`INSERT INTO org_concurrent_spans (user_id, unit_id, valid_from) VALUES (?, 5, '2020-01-01')`, u("m1@x")) // m1 은 팀2 겸직
	ex(`DELETE FROM org_write_guard`)
}

func TestOrgBasedGrants(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ids := map[string]int64{}
	for _, em := range []string{"lead@x", "m1@x", "m2@x", "act@x", "gone@x", "outsider@x"} {
		ids[em] = addUser(t, e.db, em, "")
	}
	e.org(t)
	r, _ := e.svc.CreateRole(ctx, e.admin, "조직 시험 역할", "", []string{"report:read"})
	has := func(em string) bool { return e.perms(t, ids[em]).Has(contract.PermReportRead) }
	grant := func(in GrantInput) int64 {
		t.Helper()
		g, err := e.svc.AddGrant(ctx, e.admin, r.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		return g.GrantID
	}
	// 조직(하위 제외): 팀1 = 팀장 + m1(주소속) + 겸직 없음. 퇴사자는 빠진다.
	g := grant(GrantInput{TargetType: "unit", TargetID: 4})
	for em, want := range map[string]bool{"lead@x": true, "m1@x": true, "m2@x": false, "act@x": false, "gone@x": false, "outsider@x": false} {
		if has(em) != want {
			t.Errorf("팀1 부여: %s = %v(기대 %v)", em, has(em), want)
		}
	}
	_ = e.svc.RevokeGrant(ctx, e.admin, g)
	// 팀2: 주소속 m2·act 와 겸직 m1(겸직도 소속).
	g = grant(GrantInput{TargetType: "unit", TargetID: 5})
	for em, want := range map[string]bool{"lead@x": false, "m1@x": true, "m2@x": true, "act@x": true, "gone@x": false} {
		if has(em) != want {
			t.Errorf("팀2 부여(겸직 포함): %s = %v(기대 %v)", em, has(em), want)
		}
	}
	_ = e.svc.RevokeGrant(ctx, e.admin, g)
	// 부서 + 하위 포함: 팀1·팀2 구성원 전부(부서 직속은 없음). 하위 제외면 아무도 없다.
	g = grant(GrantInput{TargetType: "unit", TargetID: 3})
	if has("lead@x") || has("m2@x") {
		t.Fatal("하위 포함이 아닌데 하위 조직원이 받음")
	}
	_ = e.svc.RevokeGrant(ctx, e.admin, g)
	g = grant(GrantInput{TargetType: "unit", TargetID: 3, IncludeDescendants: true})
	for em, want := range map[string]bool{"lead@x": true, "m1@x": true, "m2@x": true, "act@x": true, "gone@x": false, "outsider@x": false} {
		if has(em) != want {
			t.Errorf("부서(하위 포함): %s = %v(기대 %v)", em, has(em), want)
		}
	}
	_ = e.svc.RevokeGrant(ctx, e.admin, g)
	// 직위: 사원.
	g = grant(GrantInput{TargetType: "rank", TargetID: 1})
	for em, want := range map[string]bool{"lead@x": false, "m1@x": true, "m2@x": true, "act@x": false, "gone@x": false} {
		if has(em) != want {
			t.Errorf("직위 사원: %s = %v(기대 %v)", em, has(em), want)
		}
	}
	_ = e.svc.RevokeGrant(ctx, e.admin, g)
	// 팀 조직장: 팀1 의 lead 만(팀2 는 직무대행자라 제외, D4).
	g = grant(GrantInput{TargetType: "unit_head", UnitKind: "team"})
	for em, want := range map[string]bool{"lead@x": true, "act@x": false, "m1@x": false} {
		if has(em) != want {
			t.Errorf("팀 조직장: %s = %v(기대 %v)", em, has(em), want)
		}
	}
	// 미리보기는 적용하지 않고 영향 인원을 보인다.
	pv, err := e.svc.PreviewGrant(ctx, e.admin, r.ID, GrantInput{TargetType: "rank", TargetID: 1})
	if err != nil || pv.AffectedUsers != 2 || pv.NewlyGained != 2 || len(pv.Sample) != 2 {
		t.Fatalf("미리보기: %+v %v", pv, err)
	}
	if has("m2@x") {
		t.Fatal("미리보기가 적용됨")
	}
	if n := e.n(t, `SELECT COUNT(*) FROM role_grants WHERE target_type = 'rank'`); n != 0 {
		t.Fatalf("미리보기가 부여를 남김: %d", n)
	}
	// 부여 대상 검증.
	for _, bad := range []GrantInput{{TargetType: "unit", TargetID: 999}, {TargetType: "rank", TargetID: 999}, {TargetType: "unit_head", UnitKind: "galaxy"}, {TargetType: "user", TargetID: 99999}, {TargetType: "foo"}, {TargetType: "rank", TargetID: 1, IncludeDescendants: true}} {
		if _, err := e.svc.AddGrant(ctx, e.admin, r.ID, bad); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%+v 허용됨: %v", bad, err)
		}
	}
	// 날짜가 바뀌어 직위·소속이 달라지면 재계산이 반영한다(예약 발령 효력일 모사): 사원 직위 부여를 받은 m1 을 퇴사 처리.
	_ = grant(GrantInput{TargetType: "rank", TargetID: 1})
	if !has("m1@x") {
		t.Fatal("사원 부여가 m1 에게 적용되어야 함")
	}
	if _, err := e.db.Exec(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE org_person_spans SET valid_to = '2026-11-12' WHERE user_id = ?`, ids["m1@x"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`DELETE FROM org_write_guard`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.Reconcile(ctx, systemActor, false); err != nil || n == 0 {
		t.Fatalf("일일 대조가 어긋남을 못 잡음: %d %v", n, err)
	}
	if e.auditCount(t, "role.recompute") != 1 {
		t.Fatal("재계산 감사")
	}
	if d, _ := CacheDiffers(ctx, e.db, e.svc.today()); d {
		t.Fatal("재계산 뒤에도 캐시가 다름")
	}
	if has("m1@x") {
		t.Fatal("퇴사 처리한 사람에게 조직도 기반 권한이 남음")
	}
}

// ---- 위험 권한 2인 확인(AC-1330) ----

func TestRiskyGrantNeedsSecondAdmin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	risky, _ := e.svc.CreateRole(ctx, e.admin, "감사 검증자", "", []string{"audit:verify", "audit:read"})
	// 사유 없이는 요청도 못 한다. 사유가 있으면 요청이 만들어지고 적용되지 않는다.
	if _, err := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 위험 부여: %v", err)
	}
	res, err := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID, Reason: "감사 담당 지정"})
	if err != nil || res.Request == nil || res.GrantID != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("승인 전에 적용됨")
	}
	if len(res.Request.RiskPerms) != 1 || res.Request.RiskPerms[0] != "audit:verify" {
		t.Fatalf("위험 권한 표시: %+v", res.Request)
	}
	// 본인 승인 금지, 권한 없는 사람은 승인 못 한다.
	if err := e.svc.Approve(ctx, e.admin, res.Request.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("본인 승인: %v", err)
	}
	if err := e.svc.Approve(ctx, s, res.Request.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 승인: %v", err)
	}
	if err := e.svc.Approve(ctx, e.boss, res.Request.ID); err != nil {
		t.Fatal(err)
	}
	if !e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("승인했는데 적용되지 않음")
	}
	if err := e.svc.Approve(ctx, e.boss, res.Request.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 처리된 요청 재승인: %v", err)
	}
	for _, a := range []string{"role.request", "role.approve", "role.grant"} {
		if e.auditCount(t, a) != 1 {
			t.Errorf("감사 %s 가 1건이 아님", a)
		}
	}
	// 반려와 만료.
	s2 := e.staff(t, "s2@x")
	r2, _ := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s2.UserID, Reason: "시험"})
	if err := e.svc.Reject(ctx, e.boss, r2.Request.ID, ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 반려: %v", err)
	}
	if err := e.svc.Reject(ctx, e.boss, r2.Request.ID, "필요 없음"); err != nil {
		t.Fatal(err)
	}
	if e.perms(t, s2.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("반려했는데 적용됨")
	}
	s3 := e.staff(t, "s3@x")
	r3, _ := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s3.UserID, Reason: "시험"})
	*e.clock = e.clock.Add(25 * time.Hour)
	if err := e.svc.Approve(ctx, e.boss, r3.Request.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "만료") {
		t.Fatalf("만료된 요청 승인: %v", err)
	}
	if e.perms(t, s3.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("만료된 요청이 적용됨")
	}
	list, _ := e.svc.ListRequests(ctx, e.admin)
	states := map[string]int{}
	for _, q := range list {
		states[q.Status]++
	}
	if states["approved"] != 1 || states["rejected"] != 1 || states["expired"] != 1 {
		t.Fatalf("요청 상태: %v", states)
	}
	// 위험하지 않은 부여는 즉시 적용된다. 위험 권한을 줄이는 변경도 즉시 적용된다.
	safe, _ := e.svc.CreateRole(ctx, e.admin, "조회 보조", "", []string{"report:read"})
	if g, err := e.svc.AddGrant(ctx, e.admin, safe.ID, GrantInput{TargetType: "user", TargetID: s3.UserID}); err != nil || g.Request != nil {
		t.Fatalf("안전한 부여: %+v %v", g, err)
	}
	cur, _ := loadRole(ctx, e.db, risky.ID)
	if ch, err := e.svc.UpdateRole(ctx, e.admin, risky.ID, cur.Version, "감사 검증자", "", []string{"audit:read"}, ""); err != nil || ch.Role == nil {
		t.Fatalf("위험 권한 줄이기: %+v %v", ch, err)
	}
	if e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("권한을 줄였는데 남음")
	}
}

// 이미 부여된 역할에 위험 권한을 더하는 수정도 2인 확인이 필요하다(부여가 없는 역할은 즉시).
func TestRiskyRoleUpdateNeedsSecondAdmin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	r, _ := e.svc.CreateRole(ctx, e.admin, "보조", "", []string{"report:read"})
	if ch, err := e.svc.UpdateRole(ctx, e.admin, r.ID, 1, "보조", "", []string{"report:read", "audit:verify"}, ""); err != nil || ch.Role == nil {
		t.Fatalf("부여가 없는 역할은 즉시: %+v %v", ch, err)
	}
	r2, _ := e.svc.CreateRole(ctx, e.admin, "보조2", "", []string{"report:read"})
	if _, err := e.svc.AddGrant(ctx, e.admin, r2.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); err != nil {
		t.Fatal(err)
	}
	ch, err := e.svc.UpdateRole(ctx, e.admin, r2.ID, 1, "보조2", "", []string{"report:read", "audit:verify"}, "감사 보조")
	if err != nil || ch.Request == nil || ch.Role != nil {
		t.Fatalf("%+v %v", ch, err)
	}
	if e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("승인 전 적용")
	}
	if err := e.svc.Approve(ctx, e.boss, ch.Request.ID); err != nil {
		t.Fatal(err)
	}
	if !e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("승인 뒤 적용되지 않음")
	}
	// 요청 뒤에 역할이 바뀌었으면 적용하지 않는다.
	r3, _ := e.svc.CreateRole(ctx, e.admin, "보조3", "", []string{"report:read"})
	_, _ = e.svc.AddGrant(ctx, e.admin, r3.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	ch3, _ := e.svc.UpdateRole(ctx, e.admin, r3.ID, 1, "보조3", "", []string{"report:read", "audit:verify"}, "사유")
	if _, err := e.svc.UpdateRole(ctx, e.admin, r3.ID, 1, "보조3 새이름", "", []string{"report:read"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Approve(ctx, e.boss, ch3.Request.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("그 사이 바뀐 역할에 오래된 요청 적용: %v", err)
	}
}

// 관리자가 한 명뿐이면 2인 확인 대신 사유를 받고 즉시 적용하며 감사에 단독 관리자로 남는다.
func TestSoloAdminAppliesWithWarning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// boss 의 role:manage 를 없애 admin 한 명만 남긴다.
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, e.boss.UserID); err != nil {
		t.Fatal(err)
	}
	s := e.staff(t, "s@x")
	risky, _ := e.svc.CreateRole(ctx, e.admin, "감사 검증자", "", []string{"audit:verify"})
	if _, err := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("단독이어도 사유는 필요: %v", err)
	}
	res, err := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID, Reason: "소규모 회사"})
	if err != nil || res.Request != nil || !res.Solo || res.GrantID == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if !e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("적용되지 않음")
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'role.grant' AND diff LIKE '%solo_admin%'`) != 1 {
		t.Fatal("단독 관리자 표시가 감사에 없음")
	}
}

// ---- 마지막 관리자 보호(AC-1331) ----

func TestLastHolderProtection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// admin·boss 둘 다 템플릿 직접 부여. 하나를 회수하는 건 되지만 마지막은 안 된다.
	grantOf := func(uid int64) int64 {
		var id int64
		if err := e.db.QueryRow(`SELECT id FROM role_grants WHERE target_type='user' AND target_id=?`, uid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := e.svc.RevokeGrant(ctx, e.admin, grantOf(e.boss.UserID)); err != nil {
		t.Fatalf("두 명 중 하나 회수: %v", err)
	}
	err := e.svc.RevokeGrant(ctx, e.admin, grantOf(e.admin.UserID))
	if !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "한 명도 남지") {
		t.Fatalf("마지막 보유자 회수: %v", err)
	}
	if !e.perms(t, e.admin.UserID).Has(contract.PermRoleManage) {
		t.Fatal("거부된 회수가 반영됨(롤백되어야 함)")
	}
	// 역할에서 role:manage 를 빼는 변경도 같다(사용자 정의 역할로 관리자 권한을 가진 경우).
	custom, _ := e.svc.CreateRole(ctx, e.admin, "관리 보조", "", []string{"role:manage", "auth:manage"})
	if _, err := e.svc.AddGrant(ctx, e.admin, custom.ID, GrantInput{TargetType: "user", TargetID: e.boss.UserID, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	// 이제 admin(템플릿)과 boss(사용자 정의) 두 명. admin 을 회수해도 boss 가 남아 있다. 그 뒤 사용자 정의 역할에서 권한을 빼면 거부.
	if err := e.svc.RevokeGrant(ctx, e.boss, grantOf(e.admin.UserID)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateRole(ctx, e.boss, custom.ID, 1, "관리 보조", "", []string{"auth:manage"}, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("마지막 role:manage 를 빼는 역할 수정: %v", err)
	}
	// 비활성화하면 보유자로 세지 않는다: Guard 로 감싼 비활성은 거부.
	tx, _ := e.db.BeginTx(ctx, nil)
	err = Guard(ctx, tx, e.svc.today(), func() error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET active = 0 WHERE id = ?`, e.boss.UserID)
		return err
	})
	_ = tx.Rollback()
	if !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("마지막 보유자 비활성: %v", err)
	}
}

// ---- 권한 분리 점검(AC-1341) ----

func TestSeparationFindings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fin := e.staff(t, "f@x")
	if _, err := e.db.Exec(`UPDATE users SET role = 'finance' WHERE id = ?`, fin.UserID); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Separation(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, f := range res {
		found[string(f.Rule.A)+"+"+string(f.Rule.B)] = f.Count
	}
	// 재무 템플릿은 전표 쓰기·지급·카드 관리/소명 확인 권한을 함께 가진다.
	if found["ledger:write+payment:write"] != 1 || found["purchase:create+approval:decide"] != 1 {
		t.Fatalf("재무 사용자가 충돌 쌍에 걸려야 함: %v", found)
	}
	if found["role:manage+audit:verify"] != 0 {
		t.Fatalf("해당 없는 쌍: %v", found)
	}
	if _, err := e.svc.Separation(ctx, e.staff(t, "z@x")); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 점검: %v", err)
	}
	for _, f := range res {
		if f.Rule.A == contract.PermLedgerWrite && (len(f.Users) != 1 || len(f.Users[0].RolesA) == 0 || f.Users[0].RolesA[0] != "재무") {
			t.Fatalf("출처 역할: %+v", f)
		}
	}
}

// 권한 목록 메타: 모든 권한이 있고 위험 권한 표시가 목록과 같다. 새 권한 상수는 위험 목록에 알려진 권한만 둔다.
func TestPermissionMeta(t *testing.T) {
	list := Permissions()
	if len(list) != len(contract.AllPermissions) {
		t.Fatalf("%d != %d", len(list), len(contract.AllPermissions))
	}
	risk := 0
	for _, p := range list {
		if p.Risk {
			risk++
		}
		if p.Group == "" {
			t.Fatalf("그룹 없음: %+v", p)
		}
	}
	if risk != len(contract.HighRiskPermissions) {
		t.Fatalf("위험 표시 %d != %d", risk, len(contract.HighRiskPermissions))
	}
	for _, p := range contract.HighRiskPermissions {
		if !knownPerm(string(p)) {
			t.Fatalf("위험 목록에 알 수 없는 권한: %s", p)
		}
	}
	for _, r := range SeparationRules {
		if !knownPerm(string(r.A)) || !knownPerm(string(r.B)) {
			t.Fatalf("충돌 규칙에 알 수 없는 권한: %+v", r)
		}
	}
}

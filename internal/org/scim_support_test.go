package org

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B40: 외부 계정 동기화(SCIM)가 쓰는 org 보조 함수와, 동기화 입사·퇴사 발령이 RegisterBatch(auditEach=true) 경로로 들어갈 때의 규칙.

// batch 는 동기화 호출처럼 한 트랜잭션에서 RegisterBatch(auditEach=true)로 명령을 등록한다.
func (e *env) batch(actor contract.Principal, cmds ...Command) ([]Outcome, error) {
	e.t.Helper()
	var outs []Outcome
	err := e.svc.txs.WithTx(e.ctx, func(tx contract.Tx) error {
		var err error
		outs, err = e.svc.RegisterBatch(e.ctx, tx, actor, cmds, true)
		return err
	})
	return outs, err
}

func (e *env) resolve(ref string) (int64, error) {
	e.t.Helper()
	var id int64
	err := e.svc.txs.WithTx(e.ctx, func(tx contract.Tx) error {
		var err error
		id, err = e.svc.ResolveUnitRef(e.ctx, tx, ref)
		return err
	})
	return id, err
}

// 조직 코드 우선, 이름은 유일한 정확 일치만, 폐지·없음·모호는 거부한다(자동 신설 없음).
func TestResolveUnitRef(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, t2 := e.baseOrg()
	if id, err := e.resolve("T1"); err != nil || id != t1 {
		t.Fatalf("코드: %d %v", id, err)
	}
	if id, err := e.resolve(" 구매2팀 "); err != nil || id != t2 {
		t.Fatalf("이름(공백 제거): %d %v", id, err)
	}
	if _, err := e.resolve("구매"); !errors.Is(err, ErrUnitNotFound) {
		t.Fatalf("부분 일치는 거부: %v", err)
	}
	if _, err := e.resolve(""); !errors.Is(err, ErrUnitNotFound) {
		t.Fatalf("빈 값: %v", err)
	}
	// 코드가 다른 조직의 이름과 같으면 코드가 이긴다.
	named := e.reg(Command{Kind: KindUnitCreate, Code: "N2", Name: "NAME2", UnitKind: contract.OrgTeam, ParentID: dep, Effective: "2020-01-01", Reason: "설립"}).UnitID
	code := e.reg(Command{Kind: KindUnitCreate, Code: "NAME2", Name: "코드이름팀", UnitKind: contract.OrgTeam, ParentID: dep, Effective: "2020-01-01", Reason: "설립"}).UnitID
	if id, err := e.resolve("NAME2"); err != nil || id != code {
		t.Fatalf("코드 우선: %d %v(기대 %d)", id, err, code)
	}
	// 같은 이름이 둘이면 모호하다(팀은 이름이 겹칠 수 있다).
	e.reg(Command{Kind: KindUnitCreate, Code: "T1B", Name: "구매1팀", UnitKind: contract.OrgTeam, ParentID: t2, Effective: "2020-01-01", Reason: "설립"})
	if _, err := e.resolve("구매1팀"); !errors.Is(err, ErrUnitAmbiguous) || !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("모호: %v", err)
	}
	if id, err := e.resolve("T1"); err != nil || id != t1 {
		t.Fatalf("모호해도 코드는 유일: %d %v", id, err)
	}
	// 폐지된 조직은 후보가 아니다: 코드 조직을 폐지하면 같은 값을 이름으로 가진 열린 조직으로 해석된다.
	e.reg(Command{Kind: KindUnitClose, UnitID: code, Effective: "2026-03-10"})
	if id, err := e.resolve("NAME2"); err != nil || id != named {
		t.Fatalf("폐지된 조직 대신 열린 이름 일치: %d %v(기대 %d)", id, err, named)
	}
	e.reg(Command{Kind: KindUnitClose, UnitID: named, Effective: "2026-03-10"})
	if _, err := e.resolve("NAME2"); !errors.Is(err, ErrUnitNotFound) {
		t.Fatalf("모두 폐지: %v", err)
	}
}

// 표시 이름만 바뀌고 감사가 남으며, 같은 값은 아무것도 쓰지 않고, 이메일·부서는 그대로다.
func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("김철수", "kim@pl.test", t1, contract.RoleStaff, "2024-01-02")
	actor := contract.Principal{UserID: e.admin.UserID, Name: "동기화", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	_, deptBefore, _ := userRow(e, u)
	if err := e.svc.UpdateProfile(e.ctx, actor, u, "  김철수 "); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'user.update' AND target_id = ? AND diff LIKE '%"name"%'`, fmt.Sprint(u)); n != 0 {
		t.Fatalf("같은 이름인데 감사가 남음: %d", n)
	}
	if err := e.svc.UpdateProfile(e.ctx, actor, u, "김철수B"); err != nil {
		t.Fatal(err)
	}
	var name, email, empNo string
	if err := e.db.QueryRow(`SELECT name, email FROM users WHERE id = ?`, u).Scan(&name, &email); err != nil {
		t.Fatal(err)
	}
	_ = e.db.QueryRow(`SELECT employee_no FROM org_people WHERE user_id = ?`, u).Scan(&empNo)
	if name != "김철수B" || email != "kim@pl.test" || empNo == "" {
		t.Fatalf("이름 %q 이메일 %q 사번 %q", name, email, empNo)
	}
	if _, dept, _ := userRow(e, u); dept != deptBefore {
		t.Fatal("부서가 바뀜")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'user.update' AND target_id = ? AND actor_id = ? AND diff LIKE '%김철수B%'`, fmt.Sprint(u), actor.UserID); n != 1 {
		t.Fatalf("감사 %d건", n)
	}
	if err := e.svc.UpdateProfile(e.ctx, actor, u, "  "); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 이름: %v", err)
	}
	if err := e.svc.UpdateProfile(e.ctx, actor, 99999, "없는 사람"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사용자: %v", err)
	}
}

// 동기화 입사·퇴사: 화면 발령과 같은 이력·감사·투영을 낸다. 퇴사는 마지막 근무일=어제·효력일=오늘이라 즉시 계정이 꺼지고 세션·토큰·위임이 정리된다.
func TestSyncHireAndLeaveUseAppointmentPath(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, _ := e.baseOrg()
	other := e.hire("동료", "other@pl.test", dep, contract.RoleManager, "2021-01-04")
	actor := contract.Principal{UserID: e.admin.UserID, Name: "동기화", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	// 어제 입사한 것으로 시작해(소급 확인 포함) 오늘 퇴사시킨다.
	outs, err := e.batch(actor, Command{Kind: KindHire, Name: "신입", Email: "new@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1,
		Effective: "2026-03-01", Reason: "SCIM 동기화(시험): 입사", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	u := outs[0].UserID
	if active, dept, _ := userRow(e, u); active != 1 || dept != dep {
		t.Fatalf("입사 투영: active=%d dept=%d", active, dept)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'hire'`, u) != 1 ||
		e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.hire' AND target_id = ? AND actor_id = ? AND result = 'success'`, fmt.Sprint(u), actor.UserID) != 1 {
		t.Fatal("입사 발령 이력·감사가 남아야 함")
	}
	now := e.now.UTC().Format(time.RFC3339)
	if _, err := e.db.Exec(`INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at) VALUES ('hs1', ?, 'c', ?, '2099-01-01T00:00:00Z', ?)`, u, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO personal_tokens (user_id, name, token_prefix, token_hash, created_at, expires_at) VALUES (?, 't', 'plp_cd', 'hh2', ?, '2099-01-01T00:00:00Z')`, u, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_delegations (delegator_id, delegate_id, start_at, end_at, created_at) VALUES (?, ?, '2026-03-01T00:00:00Z', '2099-01-01T00:00:00Z', ?)`, u, other, now); err != nil {
		t.Fatal(err)
	}
	yesterday := contract.Day("2026-03-09")
	if _, err := e.batch(actor, Command{Kind: KindLeave, UserID: u, LastDay: yesterday, LeaveKind: "기타", Reason: "SCIM 동기화(시험): 퇴사", Confirm: true}); err != nil {
		t.Fatalf("퇴사: %v", err)
	}
	if active, _, _ := userRow(e, u); active != 0 {
		t.Fatal("퇴사 즉시 계정이 꺼져야 함")
	}
	if e.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, u) != 0 ||
		e.count(`SELECT COUNT(*) FROM personal_tokens WHERE user_id = ? AND revoked_at IS NULL`, u) != 0 ||
		e.count(`SELECT COUNT(*) FROM approval_delegations WHERE delegator_id = ? AND revoked_at IS NULL`, u) != 0 {
		t.Fatal("세션·토큰·위임이 정리되어야 함")
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'leave' AND effective_date = '2026-03-10'`, u) != 1 ||
		e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.leave' AND target_id = ? AND actor_id = ? AND result = 'success'`, fmt.Sprint(u), actor.UserID) != 1 {
		t.Fatal("퇴사 발령(효력일 오늘)·감사가 남아야 함")
	}
	// 이미 퇴사한 사람에게 다시 퇴사를 등록하면 규칙 위반이다(호출처가 멱등 처리한다).
	if _, err := e.batch(actor, Command{Kind: KindLeave, UserID: u, LastDay: yesterday, LeaveKind: "기타", Reason: "다시", Confirm: true}); err == nil {
		t.Fatal("이미 퇴사한 사람의 퇴사가 등록됨")
	}
}

// 입사 당일 퇴사(마지막 근무일이 입사일보다 이름)는 거부되고 아무것도 남지 않는다(D8).
func TestSyncLeaveOnHireDayIsRejected(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	actor := contract.Principal{UserID: e.admin.UserID, Name: "동기화", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	outs, err := e.batch(actor, Command{Kind: KindHire, Name: "당일", Email: "today@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1, Reason: "SCIM 동기화(시험): 입사"})
	if err != nil {
		t.Fatal(err)
	}
	u := outs[0].UserID
	events := e.count(`SELECT COUNT(*) FROM org_events`)
	audits := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.leave'`)
	_, err = e.batch(actor, Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-09", LeaveKind: "기타", Reason: "SCIM 동기화(시험): 퇴사", Confirm: true})
	if err == nil || !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("입사 당일 퇴사가 거부되어야 함: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events`) != events || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.leave'`) != audits {
		t.Fatal("거부됐는데 발령·감사가 남음")
	}
	if active, _, _ := userRow(e, u); active != 1 {
		t.Fatal("거부됐는데 계정이 꺼짐")
	}
}

// 사용자 이름으로 등록하는 동기화 발령(actor.UserID≠0)은 위험 권한 획득 판정을 거친다: role:manage 가 없으면 위험 권한이 새로 생기는 입사를 막는다.
func TestSyncHireGainingRiskIsBlockedWithoutRoleManage(t *testing.T) {
	r := newRiskEnv(t)
	r.grant("unit", r.t2, "") // 구매2팀에 전표 입력 역할(위험 권한 ledger:write)
	hire := Command{Kind: KindHire, Name: "위험입사", Email: "risk@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: r.t2, Reason: "SCIM 동기화(시험): 입사"}
	before := r.count(`SELECT COUNT(*) FROM users`)
	_, err := r.batch(r.low, hire)
	var rg *RiskGainError
	if !errors.As(err, &rg) || !strings.Contains(err.Error(), "위험 권한이 새로 생기는 발령은 역할 관리 권한이 필요합니다") {
		t.Fatalf("거부되어야 함: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM users`) != before {
		t.Fatal("거부됐는데 사용자가 만들어짐")
	}
	// 위험이 없는 부서로의 입사는 role:manage 없이도 된다.
	hire.ToUnit, hire.Email = r.t1, "safe@pl.test"
	if _, err := r.batch(r.low, hire); err != nil {
		t.Fatalf("무위험 입사: %v", err)
	}
	// 위험 권한 역할로 입사시키는 것은 trustedRole 이 없으므로 요청 경로에서 항상 거부된다.
	hire.Role, hire.Email = contract.RoleAdmin, "adm@pl.test"
	if _, err := r.batch(r.low, hire); err == nil {
		t.Fatal("위험 권한이 든 역할로 입사됨")
	}
}

// 연락처: 휴대전화만 바뀌고(내선·이름·이메일 불변) 감사 diff 는 마스킹되며, 같은 값은 무동작, 빈 값은 지움, 형식 오류·없는 사용자·조직 미등록은 거부한다.
func TestUpdateContact(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.reg(Command{Kind: KindHire, Name: "김철수", Email: "kim@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1, Effective: "2024-01-02", Reason: "입사", Phone: "010-1111-2222", Extension: "3901"}).UserID
	actor := contract.Principal{UserID: e.admin.UserID, Name: "동기화", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	phoneAudits := func() int {
		return e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'user.update' AND target_id = ? AND diff LIKE '%"contact_phone"%'`, fmt.Sprint(u))
	}
	if err := e.svc.UpdateContact(e.ctx, actor, u, " 010-1111-2222 "); err != nil || phoneAudits() != 0 {
		t.Fatalf("같은 값은 무동작: %v 감사 %d", err, phoneAudits())
	}
	if err := e.svc.UpdateContact(e.ctx, actor, u, "010-3333-4444"); err != nil {
		t.Fatal(err)
	}
	var phone, ext, name, email string
	_ = e.db.QueryRow(`SELECT phone, extension FROM org_people WHERE user_id = ?`, u).Scan(&phone, &ext)
	_ = e.db.QueryRow(`SELECT name, email FROM users WHERE id = ?`, u).Scan(&name, &email)
	if phone != "010-3333-4444" || ext != "3901" || name != "김철수" || email != "kim@pl.test" {
		t.Fatalf("전화 %q 내선 %q 이름 %q 이메일 %q", phone, ext, name, email)
	}
	var diff string
	if err := e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action = 'user.update' AND target_id = ? AND diff LIKE '%"contact_phone"%'`, fmt.Sprint(u)).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, "3333") || strings.Contains(diff, "1111") || strings.Contains(diff, "4444-") || !strings.Contains(diff, "010-") {
		t.Fatalf("감사 diff 가 마스킹되지 않았거나 값이 없다: %s", diff)
	}
	if err := e.svc.UpdateContact(e.ctx, actor, u, ""); err != nil {
		t.Fatalf("빈 값은 지움: %v", err)
	}
	_ = e.db.QueryRow(`SELECT phone FROM org_people WHERE user_id = ?`, u).Scan(&phone)
	if phone != "" || phoneAudits() != 2 {
		t.Fatalf("지운 뒤 전화 %q 감사 %d", phone, phoneAudits())
	}
	if err := e.svc.UpdateContact(e.ctx, actor, u, "전화번호"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("형식 오류: %v", err)
	}
	if err := e.svc.UpdateContact(e.ctx, actor, u, "010\n1234"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("줄바꿈: %v", err)
	}
	if err := e.svc.UpdateContact(e.ctx, actor, 99999, "010-1234-5678"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사용자: %v", err)
	}
	// 조직에 등록되지 않은 기존 사용자(관리자 계정).
	if err := e.svc.UpdateContact(e.ctx, actor, e.admin.UserID, "010-1234-5678"); !errors.Is(err, contract.ErrNotInOrg) {
		t.Fatalf("조직 미등록: %v", err)
	}
}

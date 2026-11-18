package sod_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t                         *testing.T
	db                        *sql.DB
	txs                       contract.TxManager
	eng                       *sod.Engine
	clock                     *time.Time
	admin, auditor, staff, s2 int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	e := &env{t: t, db: db, clock: &now}
	e.txs = dbutil.NewTxManager(db, dbutil.DialectOf(db))
	eng, err := sod.New(sod.Options{DB: db, Txs: e.txs, Audit: audit.New(db), Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.eng = eng
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	user := func(email, name, role string) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, ?, (SELECT id FROM departments WHERE name = '운영')) RETURNING id`, email, name, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.admin, e.auditor = user("a@x.test", "관리자", "admin"), user("u@x.test", "감사", "auditor")
	e.staff, e.s2 = user("s@x.test", "직원", "staff"), user("s2@x.test", "직원 둘", "staff")
	if _, err := access.SyncTemplates(ctx, db); err != nil { // 템플릿 권한을 코드와 맞추고 users.role 호환 부여를 만든다(실효 권한 캐시)
		t.Fatal(err)
	}
	if _, err := access.New(db, e.txs, audit.New(db), func() time.Time { return now }).Reconcile(ctx, contract.Principal{Role: "system", Channel: contract.ChannelWeb}, true); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) p(id int64, role contract.Role) contract.Principal {
	return contract.Principal{UserID: id, Role: role, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}
func (e *env) ctx(id int64, role contract.Role) context.Context {
	return contract.WithPrincipal(context.Background(), e.p(id, role))
}
func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// eval 은 판정을 한 트랜잭션 안에서 부르고, 차단이면 롤백한 뒤 롤백 밖에 기록한다(서비스 쓰기 래퍼와 같은 순서).
func (e *env) eval(c sod.Check) (sod.Decision, error) {
	var d sod.Decision
	err := e.txs.WithTx(context.Background(), func(tx contract.Tx) error {
		var err error
		d, err = e.eng.Evaluate(context.Background(), tx, c)
		return err
	})
	sod.NoteBlocked(context.Background(), e.eng, err)
	return d, err
}

func (e *env) check(rule string, actor, other int64, payID int64) sod.Check {
	return sod.Check{Rule: rule, Actor: e.p(actor, contract.RoleStaff), Subject: sod.Ref{Type: "payment", ID: payID, No: "PAY-1"}, Other: other}
}

func TestDifferentPeopleOrNoOtherPass(t *testing.T) {
	e := newEnv(t)
	for _, c := range []sod.Check{e.check(sod.RulePayApproveSettle, e.staff, e.s2, 1), e.check(sod.RulePayApproveSettle, e.staff, 0, 1)} {
		d, err := e.eval(c)
		if err != nil || d.Violation {
			t.Fatalf("다른 사람이면 통과해야 한다: %+v %v", d, err)
		}
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations`) != 0 {
		t.Fatal("통과한 판정은 기록하지 않는다")
	}
}

func TestWarnWritesViolationAuditAndNotification(t *testing.T) {
	e := newEnv(t)
	d, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, 7))
	if err != nil || !d.Violation || d.Mode != sod.ModeWarn {
		t.Fatalf("경고는 통과하고 위반을 알린다: %+v %v", d, err)
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'pay.create_approve' AND result = 'allowed' AND subject_id = 7 AND actor_id = ?`, e.staff) != 1 {
		t.Fatal("위반 행이 없다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.violation' AND result = 'success'`) != 1 {
		t.Fatal("감사 sod.violation 이 없다")
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'sod_violation' AND user_id = ?`, e.auditor) != 1 || e.count(`SELECT COUNT(*) FROM notifications WHERE user_id = ?`, e.staff) != 0 {
		t.Fatal("sod:read 권한자에게만 알려야 한다")
	}
}

func TestWarnRollsBackWithCaller(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("후속 실패")
	err := e.txs.WithTx(context.Background(), func(tx contract.Tx) error {
		if _, err := e.eng.Evaluate(context.Background(), tx, e.check(sod.RulePayCreateApprove, e.staff, e.staff, 1)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || e.count(`SELECT COUNT(*) FROM sod_violations`) != 0 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.violation'`) != 0 {
		t.Fatal("경고 기록은 호출자 트랜잭션과 함께 사라져야 한다")
	}
}

func TestBlockRollsBackThenRecordsOutside(t *testing.T) {
	e := newEnv(t)
	var inner int
	err := e.txs.WithTx(context.Background(), func(tx contract.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO app_settings (key, value) VALUES ('zz.test','1')`); err != nil {
			return err
		}
		_, err := e.eng.Evaluate(context.Background(), tx, e.check(sod.RulePayApproveSettle, e.staff, e.staff, 3))
		return err
	})
	_ = inner
	if !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("차단은 ErrForbidden 계열이다: %v", err)
	}
	if _, ok := sod.IsBlocked(err); !ok {
		t.Fatal("IsBlocked 가 알아보지 못함")
	}
	if e.count(`SELECT COUNT(*) FROM app_settings WHERE key = 'zz.test'`) != 0 {
		t.Fatal("차단되면 같은 트랜잭션의 쓰기가 롤백되어야 한다")
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations`) != 0 {
		t.Fatal("롤백 전에는 위반 행이 없다")
	}
	sod.NoteBlocked(context.Background(), e.eng, err)
	if e.count(`SELECT COUNT(*) FROM sod_violations WHERE result = 'blocked' AND mode = 'block' AND rule_code = 'pay.approve_settle'`) != 1 ||
		e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.blocked' AND result = 'denied'`) != 1 ||
		e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'sod_violation' AND user_id = ?`, e.auditor) != 1 {
		t.Fatal("롤백 밖에 blocked 행·거부 감사·알림이 남아야 한다")
	}
}

func TestSetModeAndLockedRules(t *testing.T) {
	e := newEnv(t)
	admin := e.ctx(e.admin, contract.RoleAdmin)
	if err := e.eng.SetMode(admin, sod.RulePayCreateSettle, sod.ModeOff); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("잠긴 규칙은 모드를 바꿀 수 없다: %v", err)
	}
	if err := e.eng.SetMode(e.ctx(e.staff, contract.RoleStaff), sod.RulePayCreateApprove, sod.ModeBlock); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("sod:manage 없이 모드를 바꿀 수 없다: %v", err)
	}
	if err := e.eng.SetMode(admin, sod.RulePayCreateApprove, "bogus"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 모드: %v", err)
	}
	if err := e.eng.SetMode(admin, sod.RulePayCreateApprove, sod.ModeBlock); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, 1)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("경고→차단으로 바꾸면 막혀야 한다: %v", err)
	}
	if err := e.eng.SetMode(admin, sod.RulePayCreateApprove, sod.ModeOff); err != nil {
		t.Fatal(err)
	}
	before := e.count(`SELECT COUNT(*) FROM sod_violations`)
	if d, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, 2)); err != nil || d.Violation || e.count(`SELECT COUNT(*) FROM sod_violations`) != before {
		t.Fatalf("끄면 통과하고 기록하지 않는다: %+v %v", d, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.rule_mode'`) != 2 {
		t.Fatal("모드 변경 감사가 두 번 남아야 한다")
	}
	// 잠긴 규칙은 설정 행이 어떻게 되어 있든 코드의 모드(차단)다.
	if _, err := e.db.Exec(`INSERT INTO sod_rule_settings (rule_code, mode, updated_by, updated_at) VALUES ('pay.create_settle','off',?,'t')`, e.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval(e.check(sod.RulePayCreateSettle, e.staff, e.staff, 1)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("잠긴 규칙은 DB 설정으로 풀 수 없다: %v", err)
	}
}

func (e *env) requestException(rule string, payID int64) int64 {
	e.t.Helper()
	id, err := e.eng.RequestException(e.ctx(e.admin, contract.RoleAdmin), sod.ExceptionInput{Rule: rule, SubjectType: "payment", SubjectID: payID, SubjectNo: "PAY-1", UserID: e.staff, Reason: "소규모 팀이라 한 사람이 처리해야 합니다"})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestExceptionSingleUseAndExpiry(t *testing.T) {
	e := newEnv(t)
	aud := e.ctx(e.auditor, contract.RoleAuditor)
	id := e.requestException(sod.RulePayApproveSettle, 11)
	// 승인 전에는 통과하지 못한다.
	if _, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 11)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("승인 전: %v", err)
	}
	if err := e.eng.DecideException(aud, id, true, ""); err != nil {
		t.Fatal(err)
	}
	// 다른 거래에는 쓸 수 없다.
	if _, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 12)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("다른 거래: %v", err)
	}
	d, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 11))
	if err != nil || !d.Excepted {
		t.Fatalf("승인된 예외로 1회 통과해야 한다: %+v %v", d, err)
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations WHERE result = 'excepted' AND exception_id = ?`, id) != 1 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.exception_used'`) != 1 {
		t.Fatal("예외 통과 기록·감사가 없다")
	}
	if _, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 11)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("2회째는 차단: %v", err)
	}
	// 7일 만료.
	id2 := e.requestException(sod.RulePayApproveSettle, 13)
	if err := e.eng.DecideException(aud, id2, true, ""); err != nil {
		t.Fatal(err)
	}
	*e.clock = e.clock.AddDate(0, 0, 8)
	if _, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 13)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("만료된 예외: %v", err)
	}
	if n, err := e.eng.Expire(context.Background()); err != nil || n != 1 {
		t.Fatalf("만료 처리 %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM sod_exceptions WHERE id = ? AND status = 'expired'`, id2) != 1 {
		t.Fatal("상태가 만료가 아니다")
	}
}

func TestExceptionRules(t *testing.T) {
	e := newEnv(t)
	admin, aud := e.ctx(e.admin, contract.RoleAdmin), e.ctx(e.auditor, contract.RoleAuditor)
	in := sod.ExceptionInput{Rule: sod.RulePayCreateSettle, SubjectType: "payment", SubjectID: 1, UserID: e.staff, Reason: "소규모 팀이라 한 사람이 처리해야 합니다"}
	if _, err := e.eng.RequestException(admin, in); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("잠긴 규칙은 예외 불가: %v", err)
	}
	in.Rule = "hold.ledger_payment"
	if _, err := e.eng.RequestException(admin, in); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("hold 규칙은 예외 대상이 아니다: %v", err)
	}
	in.Rule = sod.RulePayApproveSettle
	in.Reason = "짧음"
	if _, err := e.eng.RequestException(admin, in); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 10자 미만: %v", err)
	}
	in.Reason = "소규모 팀이라 한 사람이 처리해야 합니다"
	in.UserID = e.admin
	if _, err := e.eng.RequestException(admin, in); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("요청자=대상자: %v", err)
	}
	if _, err := e.eng.RequestException(aud, sod.ExceptionInput{Rule: sod.RulePayApproveSettle, SubjectType: "payment", SubjectID: 1, UserID: e.staff, Reason: "소규모 팀이라 한 사람이 처리해야 합니다"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("요청은 sod:manage 만: %v", err)
	}
	id := e.requestException(sod.RulePayApproveSettle, 5)
	if _, err := e.eng.RequestException(admin, sod.ExceptionInput{Rule: sod.RulePayApproveSettle, SubjectType: "payment", SubjectID: 5, UserID: e.staff, Reason: "소규모 팀이라 한 사람이 처리해야 합니다"}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 거래 중복 요청: %v", err)
	}
	if err := e.eng.DecideException(admin, id, true, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("승인은 sod:exception_approve 만: %v", err)
	}
	// 요청자≠승인자≠대상자: 승인 권한이 있어도 요청자·대상자 본인은 안 된다.
	pre := e.p(e.auditor, contract.RoleAuditor)
	if _, err := e.db.Exec(`UPDATE sod_exceptions SET requested_by = ? WHERE id = ?`, pre.UserID, id); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.DecideException(aud, id, true, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("요청자 본인 승인: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE sod_exceptions SET requested_by = ?, user_id = ? WHERE id = ?`, e.admin, e.auditor, id); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.DecideException(aud, id, true, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("대상자 본인 승인: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE sod_exceptions SET user_id = ? WHERE id = ?`, e.staff, id); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.DecideException(aud, id, false, "근거 부족"); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.DecideException(aud, id, true, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 처리: %v", err)
	}
	if err := e.eng.RevokeException(aud, id); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("반려된 예외는 거둘 수 없다: %v", err)
	}
	id2 := e.requestException(sod.RulePayApproveSettle, 6)
	if err := e.eng.DecideException(aud, id2, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.RevokeException(aud, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 6)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("거둔 예외는 쓸 수 없다: %v", err)
	}
	for _, a := range []string{"sod.exception_request", "sod.exception_approve", "sod.exception_reject", "sod.exception_revoke"} {
		if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = ?`, a) == 0 {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
}

func TestViolationNotificationsGroupedAfterTen(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 14; i++ {
		if _, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, int64(100+i))); err != nil {
			t.Fatal(err)
		}
		*e.clock = e.clock.Add(time.Minute)
	}
	if n := e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'sod_violation' AND user_id = ?`, e.auditor); n != 11 {
		t.Fatalf("한 시간 10건까지 개별, 11번째에 묶음 한 번, 이후 생략 = 11건이어야 한다: %d", n)
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations`) != 14 {
		t.Fatal("알림은 묶어도 위반 기록은 모두 남아야 한다")
	}
	*e.clock = e.clock.Add(2 * time.Hour)
	if _, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, 200)); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'sod_violation' AND user_id = ?`, e.auditor); n != 12 {
		t.Fatalf("한 시간이 지나면 다시 개별 알림: %d", n)
	}
}

func TestViolationsListAndRulesView(t *testing.T) {
	e := newEnv(t)
	if _, err := e.eval(e.check(sod.RulePayCreateApprove, e.staff, e.staff, 1)); err != nil {
		t.Fatal(err)
	}
	_, _ = e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 2))
	aud := e.ctx(e.auditor, contract.RoleAuditor)
	vs, err := e.eng.Violations(aud, sod.ViolationFilter{})
	if err != nil || len(vs) != 2 || vs[0].Result != "blocked" || vs[0].ActorName != "직원" {
		t.Fatalf("위반 목록 %+v %v", vs, err)
	}
	if vs, _ := e.eng.Violations(aud, sod.ViolationFilter{Result: "allowed"}); len(vs) != 1 {
		t.Fatalf("결과 필터 %+v", vs)
	}
	if _, err := e.eng.Violations(e.ctx(e.staff, contract.RoleStaff), sod.ViolationFilter{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("sod:read 없이 조회: %v", err)
	}
	rules, err := e.eng.Rules(aud)
	if err != nil || len(rules) != len(sod.Rules) {
		t.Fatalf("규칙 표 %v", err)
	}
	for _, r := range rules {
		if r.Code == sod.RulePayApproveSettle && (r.Violations != 1 || r.Blocked != 1 || r.Mode != sod.ModeBlock) {
			t.Fatalf("규칙 건수 %+v", r)
		}
	}
}

// 동시에 같은 예외로 통과하려는 두 요청 중 한 쪽만 통과한다(예외는 1회용: 소진은 상태 조건부 갱신 한 번).
func TestExceptionSingleUseUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	id := e.requestException(sod.RulePayApproveSettle, 31)
	if err := e.eng.DecideException(e.ctx(e.auditor, contract.RoleAuditor), id, true, ""); err != nil {
		t.Fatal(err)
	}
	const n = 6
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := e.eval(e.check(sod.RulePayApproveSettle, e.staff, e.staff, 31))
			results <- err
		}()
	}
	passed := 0
	for i := 0; i < n; i++ {
		if err := <-results; err == nil {
			passed++
		} else if !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("예상 밖 오류: %v", err)
		}
	}
	if passed != 1 {
		t.Fatalf("예외로 통과한 요청이 %d 건이다(1건이어야 한다)", passed)
	}
	if e.count(`SELECT COUNT(*) FROM sod_violations WHERE result = 'excepted'`) != 1 {
		t.Fatal("예외 통과 기록이 1건이어야 한다")
	}
}

package auth

import (
	"context"
	"database/sql"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/access"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

func identityEnv(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil { // 기본 역할 템플릿 권한(B33)
		t.Fatal(err)
	}
	if err := SeedDevUsers(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 시드 사용자는 예전 oidc_sub 를 갖지만 신원 표에는 없다(마이그레이션 뒤에 만들어졌으므로): 첫 로그인이 이메일로 연결한다.
	svc, err := New(Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: audit.New(db), Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, db
}

func resolve(t *testing.T, s *Service, id Identity) (contract.Principal, error) {
	t.Helper()
	var p contract.Principal
	err := s.txs.WithTx(context.Background(), func(tx contract.Tx) error {
		var err error
		p, err = s.resolveAccount(context.Background(), tx, id)
		return err
	})
	return p, err
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 등록된 사용자의 이메일로 한 번 연결되고, 이후엔 주체로 찾는다. 자동 가입은 없다(AC-1060).
func TestResolveAccountLinksOnceAndNeverCreates(t *testing.T) {
	s, db := identityEnv(t)
	users := count(t, db, `SELECT COUNT(*) FROM users`)
	p, err := resolve(t, s, Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-1", Email: "  Admin@PL.test "})
	if err != nil || p.Role != contract.RoleAdmin {
		t.Fatalf("첫 연결: %v %+v", err, p)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM user_identities WHERE provider='google' AND subject='g-1' AND user_id=?`, p.UserID); n != 1 {
		t.Fatalf("신원 연결 %d건", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.identity_link' AND target_type='credential'`); n != 1 {
		t.Errorf("연결 감사 %d건", n)
	}
	// 이메일이 바뀌어도(없어도) 주체로 인식한다
	if p2, err := resolve(t, s, Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-1"}); err != nil || p2.UserID != p.UserID {
		t.Fatalf("주체로 재로그인: %v", err)
	}
	// 등록되지 않은 이메일은 거부되고 사용자가 늘지 않는다
	_, err = resolve(t, s, Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-2", Email: "stranger@pl.test"})
	if !errors.Is(err, contract.ErrForbidden) || denialReason(err) != ReasonUnknownAccount {
		t.Fatalf("미등록: %v", err)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM users`); got != users {
		t.Fatalf("자동 가입 발생: 사용자 %d → %d", users, got)
	}
	// 신뢰할 수 있는 이메일이 없으면 첫 연결을 하지 않는다
	if _, err := resolve(t, s, Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-3"}); denialReason(err) != ReasonNoEmail {
		t.Fatalf("이메일 없음: %v", err)
	}
}

// 같은 방식에 다른 주체가 이미 묶인 이메일은 거부된다(AC-1062). 다른 방식은 따로 묶인다.
func TestResolveAccountRejectsRebindingToOtherSubject(t *testing.T) {
	s, _ := identityEnv(t)
	if _, err := resolve(t, s, Identity{Provider: MethodMicrosoft, Issuer: "tid-1", Subject: "oid-1", Email: "admin@pl.test"}); err != nil {
		t.Fatal(err)
	}
	_, err := resolve(t, s, Identity{Provider: MethodMicrosoft, Issuer: "tid-1", Subject: "oid-ATTACKER", Email: "admin@pl.test"})
	if !errors.Is(err, contract.ErrForbidden) || denialReason(err) != ReasonIdentityBound {
		t.Fatalf("다른 주체 재연결: %v", err)
	}
	if _, err := resolve(t, s, Identity{Provider: MethodSAML, Issuer: "idp", Subject: "admin@pl.test", Email: "admin@pl.test"}); err != nil {
		t.Fatalf("다른 방식은 따로 연결: %v", err)
	}
}

// 비활성·퇴사·입사 전 계정은 어떤 방식으로도 거부되고 신원이 묶이지도 않는다(B20, AC-1061).
func TestResolveAccountDeniesInactiveAndNotEmployed(t *testing.T) {
	s, db := identityEnv(t)
	if _, err := db.Exec(`UPDATE users SET active = 0 WHERE email = 'staff@pl.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(t, s, Identity{Provider: MethodSAML, Issuer: "idp", Subject: "n1", Email: "staff@pl.test"}); denialReason(err) != ReasonInactive {
		t.Fatalf("비활성: %v", err)
	}
	// 퇴사: 재직 구간이 오늘을 덮지 않는다
	var uid int64
	_ = db.QueryRow(`SELECT id FROM users WHERE email = 'lead@pl.test'`).Scan(&uid)
	dbtest.FKOff(t, db)
	_, _ = db.Exec(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`) // 구간 표 보호 트리거 우회(시험 전용)
	if _, err := db.Exec(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (?, '2020-01-01', '2026-10-01', 0, 1, 1), (?, '2026-10-01', NULL, 1, 1, 1)`, uid, uid); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{MethodGoogle, MethodMicrosoft, MethodSAML, MethodOIDC, MethodDev} {
		if _, err := resolve(t, s, Identity{Provider: m, Issuer: "x", Subject: "left-" + m, Email: "lead@pl.test"}); !errors.Is(err, contract.ErrForbidden) || denialReason(err) != ReasonNotEmployed {
			t.Fatalf("%s 퇴사자: %v", m, err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM user_identities WHERE user_id IN (?, (SELECT id FROM users WHERE email='staff@pl.test'))`, uid); n != 0 {
		t.Fatalf("거부된 계정에 신원이 묶임: %d", n)
	}
	// 이미 묶여 있던 퇴사자도 막힌다
	_, _ = db.Exec(`INSERT INTO user_identities (user_id, provider, issuer, subject, linked_at) VALUES (?, 'google', 'x', 'bound-left', '2026-01-01T00:00:00Z')`, uid)
	if _, err := resolve(t, s, Identity{Provider: MethodGoogle, Issuer: "x", Subject: "bound-left"}); denialReason(err) != ReasonNotEmployed {
		t.Fatalf("묶인 퇴사자: %v", err)
	}
}

// 로그인 거부와 성공 감사에 방식이 남고, 세션에 방식이 기록된다(AC-1082).
func TestLoginWithRecordsMethodAndSession(t *testing.T) {
	s, db := identityEnv(t)
	raw, _, err := s.loginWith(context.Background(), Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-9", Email: "finance@pl.test"})
	if err != nil {
		t.Fatal(err)
	}
	var method string
	var setup sql.NullString
	if err := db.QueryRow(`SELECT auth_method, setup FROM sessions WHERE id_hash = ?`, sha256Hex(raw)).Scan(&method, &setup); err != nil || method != "google" || setup.Valid {
		t.Fatalf("세션 방식 %q setup=%v err=%v", method, setup, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='success' AND diff LIKE '%"login_method"%google%'`); n != 1 {
		t.Errorf("성공 감사에 방식이 없음(%d)", n)
	}
	if _, _, err := s.loginWith(context.Background(), Identity{Provider: MethodGoogle, Issuer: "pl.test", Subject: "g-x", Email: "nobody@pl.test"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("미등록 로그인: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='denied' AND diff LIKE '%unknown_account%' AND diff NOT LIKE '%nobody%'`); n != 1 {
		t.Errorf("거부 감사(사유 코드, 이메일 미기록) %d", n)
	}
}

// 제한 세션은 설정 경로(PermSetup)와 공개 경로 외 모든 권한을 거부하고 ErrSetupRequired 를 돌려준다(AC-1003).
func TestRestrictedSessionOnlyAllowsSetupRoutes(t *testing.T) {
	restricted := contract.Principal{UserID: 7, Role: contract.RoleAdmin, Setup: contract.SetupPasswordChange}
	ctx := contract.WithPrincipal(context.Background(), restricted)
	for _, perm := range append([]contract.Permission{contract.PermAuthenticated}, contract.AllPermissions...) {
		if _, err := contract.Authorize(ctx, perm); !errors.Is(err, contract.ErrSetupRequired) {
			t.Fatalf("%s: 제한 세션이 통과함(err=%v)", perm, err)
		}
	}
	for _, perm := range []contract.Permission{contract.PermSetup, contract.PermPublic} {
		if _, err := contract.Authorize(ctx, perm); err != nil {
			t.Fatalf("%s: 제한 세션이 막힘: %v", perm, err)
		}
	}
	normal := restricted
	normal.Setup = ""
	if _, err := contract.Authorize(contract.WithPrincipal(context.Background(), normal), contract.PermAuthenticated); err != nil {
		t.Fatal(err)
	}
}

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// AC-1072·1073: 이메일 로그인 끄기 보호 조건, 비상 로그인(비상 관리자만·TOTP·감사), 연결 해제·방식 끄기 보호, 서버 복구 명령.
func TestEmailLoginOffGuardAndEmergencyLogin(t *testing.T) {
	e := newProvEnv(t)
	box, _ := newSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	e.s.box = box
	ctx := context.Background()
	off := false
	adminID := e.uid(t, "admin@pl.test")
	e.setPw(t, "admin@pl.test", goodPw, false)
	e.setPw(t, "staff@pl.test", goodPw, false)

	expect := func(what string, err error, sub string) {
		t.Helper()
		if !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), sub) {
			t.Fatalf("%s: %v (기대 %q)", what, err, sub)
		}
	}
	_, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmailLoginEnabled: &off})
	expect("외부 방식 없음", err, "외부 로그인 방식")

	e.enableGoogle(t) // 시험 성공 기록이 있는 켜진 구글
	_, err = e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmailLoginEnabled: &off})
	expect("연결된 관리자 없음", err, "연결된 활성 관리자")

	e.db.Exec(`INSERT INTO user_identities (user_id, provider, issuer, subject, linked_at) VALUES (?, 'google', 'accounts.google.com', 'g-admin', '2026-11-12T00:00:00Z')`, adminID)
	_, err = e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmailLoginEnabled: &off})
	expect("비상 관리자 없음", err, "비상 관리자")

	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmergencyAdmins: &[]int64{e.uid(t, "staff@pl.test")}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("관리자가 아닌 사용자를 비상 관리자로 지정: %v", err)
	}
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmergencyAdmins: &[]int64{adminID, adminID}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("중복: %v", err)
	}
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmergencyAdmins: &[]int64{adminID}, EmailLoginEnabled: &off}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "2단계") {
		t.Fatalf("TOTP 미등록 비상 관리자: %v", err)
	}
	// 비상 관리자로 지정하면 다음 로그인에서 2단계 등록이 강제되고, 등록을 마치면 끌 수 있다.
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmergencyAdmins: &[]int64{adminID}}); err != nil {
		t.Fatal(err)
	}
	lr, _ := e.pwEnv.login("admin@pl.test", goodPw)
	if lr.Setup != contract.SetupTOTPEnroll {
		t.Fatalf("비상 관리자는 TOTP 필수: %+v", lr)
	}
	secret, _ := e.enroll(t, "admin@pl.test", contract.RoleAdmin, lr.RawSession)
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{EmailLoginEnabled: &off}); err != nil {
		t.Fatalf("조건을 모두 갖췄는데 거부됨: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.policy_update' AND diff LIKE '%email_login%'`) != 1 {
		t.Fatal("정책 변경 감사")
	}

	// 일반 이메일 로그인은 꺼졌고, 정책 변경으로 깨지는 방식 끄기·연결 해제는 거부된다.
	if _, err := e.pwEnv.login("staff@pl.test", goodPw); !errors.Is(err, ErrEmailLoginOff) {
		t.Fatalf("이메일 로그인이 꺼졌는데: %v", err)
	}
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &off}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("마지막 외부 방식 끄기: %v", err)
	}
	if err := e.s.UnlinkIdentity(ctx, e.admin, adminID, MethodGoogle); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("마지막 연결 해제: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='google'`) != 1 {
		t.Fatal("거부된 해제가 반영됨(롤백되어야 함)")
	}

	// 비상 로그인: 비상 관리자만, 비밀번호+TOTP, 감사.
	*e.clock = e.clock.Add(time.Minute)
	if _, err := e.s.LoginPassword(ctx, PasswordLogin{Email: "staff@pl.test", Password: goodPw, Emergency: true}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("일반 사용자의 비상 로그인: %v", err)
	}
	tk, err := e.s.LoginPassword(ctx, PasswordLogin{Email: "admin@pl.test", Password: goodPw, Emergency: true})
	if err != nil || tk.Ticket == "" || tk.RawSession != "" {
		t.Fatalf("비상 로그인은 티켓이어야 함: %+v %v", tk, err)
	}
	done, err := e.s.LoginTOTP(ctx, tk.Ticket, e.codeNow(secret, 0))
	if err != nil || done.RawSession == "" {
		t.Fatalf("%+v %v", done, err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.emergency_login'`) != 1 || e.s.RecentEmergencyLogin(ctx) == "" {
		t.Fatal("비상 로그인 감사·최근 사용 알림")
	}

	// 서버 복구 명령: 잠금과 꺼진 이메일 로그인을 되돌린다.
	for i := 0; i < 5; i++ {
		e.db.Exec(`UPDATE user_credentials SET failed_count = 4`)
		_, _ = e.s.LoginPassword(ctx, PasswordLogin{Email: "admin@pl.test", Password: "wrong-Pass1!x", Emergency: true})
	}
	lines, err := e.s.CLIUnlock(ctx, "ADMIN@pl.test")
	if err != nil || len(lines) < 2 {
		t.Fatalf("%v %v", lines, err)
	}
	if p, _ := e.s.Policy(ctx); !p.EmailLoginEnabled {
		t.Fatal("이메일 로그인이 다시 켜지지 않음")
	}
	if _, err := e.s.CLIUnlock(ctx, "ghost@pl.test"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 계정: %v", err)
	}
}

// 정책 변경 검증과 비밀번호 정책 경계.
func TestUpdatePolicyValidation(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	bad := DefaultPasswordPolicy()
	bad.MinLength = 4
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{Password: &bad}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("최소 길이 4: %v", err)
	}
	ok := DefaultPasswordPolicy()
	ok.MinLength, ok.ExpireDays = 12, 90
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{Password: &ok}); err != nil {
		t.Fatal(err)
	}
	if err := ok.Check("Abcdefgh1!x", "", ""); err == nil { // 11자
		t.Fatal("바뀐 최소 길이가 적용되지 않음")
	}
	perms := []string{"role:manage", "ghost:perm"}
	if _, err := e.s.UpdatePolicy(ctx, e.admin, PolicyInput{TOTPRequiredPerms: &perms}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 권한: %v", err)
	}
	staff := contract.Principal{UserID: e.uid(t, "staff@pl.test"), Role: contract.RoleStaff}
	if _, err := e.s.UpdatePolicy(ctx, staff, PolicyInput{Password: &ok}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없음: %v", err)
	}
	e.setPw(t, "staff@pl.test", goodPw, false)
	var exp string
	_ = e.db.QueryRow(`SELECT COALESCE(expires_at,'') FROM user_credentials WHERE user_id=?`, staff.UserID).Scan(&exp)
}

// 검증자 지적 1: 비활성·비밀번호 없는 계정은 없는 이메일과 같이 다룬다(잠금 행·423 이 없다).
func TestUnusableAccountsNeverLock(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	e.db.Exec(`UPDATE users SET active = 0 WHERE email = 'staff@pl.test'`)
	for i := 0; i < 12; i++ {
		for _, em := range []string{"staff@pl.test", "manager@pl.test", "nobody@pl.test"} {
			if _, err := e.login(em, "wrong-Pass1!"); !errors.Is(err, ErrBadCredentials) {
				t.Fatalf("%s %d회째: %v", em, i, err)
			}
		}
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials WHERE locked_until IS NOT NULL OR failed_count > 0`) != 0 {
		t.Fatal("비활성 계정에 잠금 흔적이 생김")
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials`) != 1 {
		t.Fatal("자격 행이 늘어남")
	}
	// 정답이어도 같은 오류, 감사에는 거부 사유가 남는다.
	if _, err := e.login("staff@pl.test", goodPw); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("비활성 계정 정답: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='denied' AND diff LIKE '%inactive%'`) != 1 {
		t.Fatal("거부 감사")
	}
}

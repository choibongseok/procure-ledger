package auth

import (
	"context"
	"database/sql"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// AC-1030: RFC 6238 부록 B 의 SHA-1 시험 벡터(비밀키 "12345678901234567890", 8자리).
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		unix int64
		want string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		if got := hotp(secret, uint64(c.unix/30), 8); got != c.want {
			t.Errorf("T=%d: %s, 기대 %s", c.unix, got, c.want)
		}
	}
	// RFC 4226 부록 D 의 HOTP 벡터(6자리).
	for i, want := range []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"} {
		if got := hotp(secret, uint64(i), 6); got != want {
			t.Errorf("HOTP %d: %s, 기대 %s", i, got, want)
		}
	}
}

// ±1 구간만 허용하고 같은 구간(이전 구간 포함)의 코드는 한 번만 받는다.
func TestMatchTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	cur := totpStep(now)
	code := func(step int64) string { return hotp(secret, uint64(step), 6) }
	if _, ok := matchTOTP(secret, code(cur), now, 0); !ok {
		t.Fatal("현재 구간")
	}
	for _, d := range []int64{-1, 1} {
		if _, ok := matchTOTP(secret, code(cur+d), now, 0); !ok {
			t.Fatalf("±1 구간(%d)이 거부됨", d)
		}
	}
	for _, d := range []int64{-2, 2, 10} {
		if _, ok := matchTOTP(secret, code(cur+d), now, 0); ok {
			t.Fatalf("범위 밖 구간(%d)이 허용됨", d)
		}
	}
	step, _ := matchTOTP(secret, code(cur), now, 0)
	if _, ok := matchTOTP(secret, code(cur), now, step); ok {
		t.Fatal("같은 구간 코드 재사용이 허용됨")
	}
	if _, ok := matchTOTP(secret, code(cur-1), now, step); ok {
		t.Fatal("이미 쓴 구간보다 이전 구간 코드가 허용됨")
	}
	if _, ok := matchTOTP(secret, "000000", now, 0); ok && code(cur) != "000000" && code(cur-1) != "000000" && code(cur+1) != "000000" {
		t.Fatal("틀린 코드가 허용됨")
	}
}

func TestSecretBoxBindsToUser(t *testing.T) {
	b, err := newSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b.seal([]byte("topsecret-key-bytes!"), "7")
	if strings.Contains(enc, "topsecret") {
		t.Fatal("평문이 암호문에 보임")
	}
	if got, err := b.open(enc, "7"); err != nil || string(got) != "topsecret-key-bytes!" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := b.open(enc, "8"); err == nil {
		t.Fatal("다른 사용자 번호로 풀렸음(AAD 묶음 실패)")
	}
	if _, err := newSecretBox(make([]byte, 16)); err == nil {
		t.Fatal("32바이트가 아닌 키 허용")
	}
	if k, err := DecodeSecretKey(""); k != nil || err != nil {
		t.Fatal("빈 키는 기능 꺼짐(nil)이어야 함")
	}
	if _, err := DecodeSecretKey("c2hvcnQ="); err == nil {
		t.Fatal("짧은 키 허용")
	}
}

func totpEnv(t *testing.T) *pwEnv {
	t.Helper()
	e := newPwEnv(t, false)
	box, err := newSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	e.s.box = box
	return e
}

func (e *pwEnv) principal(t *testing.T, email string, role contract.Role) contract.Principal {
	return contract.Principal{UserID: e.uid(t, email), Role: role, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

// enroll 은 등록·확인까지 마치고 비밀키와 복구 코드를 돌려준다.
func (e *pwEnv) enroll(t *testing.T, email string, role contract.Role, sess string) (secret []byte, recovery []string) {
	t.Helper()
	ctx := context.Background()
	p := e.principal(t, email, role)
	b32, uri, err := e.s.EnrollTOTP(ctx, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/ProcureLedger:") || !strings.Contains(uri, "secret="+b32) {
		t.Fatalf("otpauth 링크: %s", uri)
	}
	secret, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	if err != nil {
		t.Fatal(err)
	}
	// 확인 전에는 활성이 아니다(AC-1031).
	if e.s.totpConfirmed(ctx, e.db, p.UserID) {
		t.Fatal("확인 전에 활성")
	}
	if _, err := e.s.ConfirmTOTP(ctx, p, sess, "000000"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("틀린 코드로 활성화: %v", err)
	}
	recovery, err = e.s.ConfirmTOTP(ctx, p, sess, hotp(secret, uint64(totpStep(e.s.now())), 6))
	if err != nil {
		t.Fatal(err)
	}
	return secret, recovery
}

func (e *pwEnv) codeNow(secret []byte, plusSteps int64) string {
	return hotp(secret, uint64(totpStep(e.s.now())+plusSteps), 6)
}

// AC-1030~1032: 비밀키 암호화 저장, 복구 코드 8개(해시), 티켓→세션, 재생 방지, 복구 코드 1회용.
func TestTOTPEnrollAndLoginFlow(t *testing.T) {
	e := totpEnv(t)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, err := e.login("staff@pl.test", goodPw)
	if err != nil || lr.Ticket != "" {
		t.Fatalf("등록 전에는 바로 세션: %+v %v", lr, err)
	}
	secret, recovery := e.enroll(t, "staff@pl.test", contract.RoleStaff, lr.RawSession)
	if len(recovery) != 8 {
		t.Fatalf("복구 코드 %d개", len(recovery))
	}
	var enc string
	_ = e.db.QueryRow(`SELECT secret_enc FROM user_totp WHERE user_id=?`, e.uid(t, "staff@pl.test")).Scan(&enc)
	if enc == "" || strings.Contains(enc, base32.StdEncoding.EncodeToString(secret)) {
		t.Fatalf("비밀키가 평문이거나 없음: %q", enc)
	}
	for _, rc := range recovery {
		if e.n(t, `SELECT COUNT(*) FROM totp_recovery_codes WHERE code_hash = ?`, rc) != 0 {
			t.Fatal("복구 코드 원문이 저장됨")
		}
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`, recovery[0], recovery[0]) != 0 {
		t.Fatal("복구 코드가 감사에 남음")
	}

	// 이제 비밀번호만으로는 세션이 생기지 않는다.
	lr2, err := e.login("staff@pl.test", goodPw)
	if err != nil || lr2.Ticket == "" || lr2.RawSession != "" {
		t.Fatalf("티켓이어야 함: %+v %v", lr2, err)
	}
	var tn int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM login_tickets WHERE ticket_hash = ?`, lr2.Ticket).Scan(&tn)
	if tn != 0 {
		t.Fatal("티켓 원문이 저장됨")
	}
	*e.clock = e.clock.Add(31 * time.Second) // 가입 때 쓴 구간과 다른 구간의 코드
	if _, err := e.s.LoginTOTP(ctx, lr2.Ticket, "000000"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("틀린 코드: %v", err)
	}
	code := e.codeNow(secret, 0)
	done, err := e.s.LoginTOTP(ctx, lr2.Ticket, code)
	if err != nil || done.RawSession == "" {
		t.Fatalf("로그인 실패: %+v %v", done, err)
	}
	// 같은 코드·같은 티켓의 재사용은 거부(티켓은 소비됨), 새 티켓에 같은 코드도 거부(재생 방지).
	if _, err := e.s.LoginTOTP(ctx, lr2.Ticket, code); !errors.Is(err, ErrBadTicket) {
		t.Fatalf("소비된 티켓 재사용: %v", err)
	}
	lr3, _ := e.login("staff@pl.test", goodPw)
	if _, err := e.s.LoginTOTP(ctx, lr3.Ticket, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("같은 구간 코드 재생이 통과함: %v", err)
	}
	// 복구 코드는 1회용이다.
	if _, err := e.s.LoginTOTP(ctx, lr3.Ticket, recovery[0]); err != nil {
		t.Fatalf("복구 코드 로그인: %v", err)
	}
	lr4, _ := e.login("staff@pl.test", goodPw)
	if _, err := e.s.LoginTOTP(ctx, lr4.Ticket, strings.ToUpper(recovery[0])); !errors.Is(err, ErrBadCode) {
		t.Fatalf("쓴 복구 코드 재사용: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND diff LIKE '%recovery%'`) != 1 {
		t.Fatal("복구 코드 로그인 감사 없음")
	}
}

// AC-1032: 티켓은 5회 시도로 막히고 실패는 계정 잠금 횟수에 합산되며, 비밀번호만 반복해서 맞혀도 횟수가 지워지지 않는다.
func TestTOTPTicketFailuresLockAccount(t *testing.T) {
	e := totpEnv(t)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, _ := e.login("staff@pl.test", goodPw)
	_, _ = e.enroll(t, "staff@pl.test", contract.RoleStaff, lr.RawSession)
	*e.clock = e.clock.Add(time.Minute)
	for round := 0; round < 2; round++ {
		tk, err := e.login("staff@pl.test", goodPw)
		if err != nil {
			if round == 1 {
				var lk LockedError
				if !errors.As(err, &lk) {
					t.Fatalf("잠겨야 함: %v", err)
				}
				return
			}
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ { // 티켓당 3번씩 두 번 = 6번 → 5번째에서 잠금
			_, _ = e.s.LoginTOTP(ctx, tk.Ticket, "000000")
		}
	}
	_, err := e.login("staff@pl.test", goodPw)
	var lk LockedError
	if !errors.As(err, &lk) {
		t.Fatalf("비밀번호 로그인과 티켓 실패가 합산돼 잠겨야 함: %v", err)
	}
}

func TestTOTPTicketExpiresAndAttempts(t *testing.T) {
	e := totpEnv(t)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, _ := e.login("staff@pl.test", goodPw)
	secret, _ := e.enroll(t, "staff@pl.test", contract.RoleStaff, lr.RawSession)
	*e.clock = e.clock.Add(time.Minute)
	tk, _ := e.login("staff@pl.test", goodPw)
	*e.clock = e.clock.Add(6 * time.Minute)
	if _, err := e.s.LoginTOTP(ctx, tk.Ticket, e.codeNow(secret, 0)); !errors.Is(err, ErrBadTicket) {
		t.Fatalf("5분이 지난 티켓: %v", err)
	}
	// 티켓 시도 5회 한도(잠금 한도보다 앞서 티켓이 막히는 경우)
	e.db.Exec(`UPDATE user_credentials SET failed_count = 0, locked_until = NULL`)
	tk2, _ := e.login("staff@pl.test", goodPw)
	e.db.Exec(`UPDATE login_tickets SET attempts = 5`)
	if _, err := e.s.LoginTOTP(ctx, tk2.Ticket, e.codeNow(secret, 0)); !errors.Is(err, ErrBadTicket) {
		t.Fatalf("시도 횟수를 넘은 티켓: %v", err)
	}
}

// AC-1031: 해제·재등록은 비밀번호+코드가 필요하다. 필수 역할은 해제할 수 없다.
func TestTOTPDisableAndReenroll(t *testing.T) {
	e := totpEnv(t)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, _ := e.login("staff@pl.test", goodPw)
	secret, _ := e.enroll(t, "staff@pl.test", contract.RoleStaff, lr.RawSession)
	p := e.principal(t, "staff@pl.test", contract.RoleStaff)
	*e.clock = e.clock.Add(time.Minute)
	if _, _, err := e.s.EnrollTOTP(ctx, p, "", ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("코드 없이 재등록: %v", err)
	}
	if err := e.s.DisableTOTP(ctx, p, "wrong-Pass1!", e.codeNow(secret, 0)); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("비밀번호 틀림: %v", err)
	}
	if err := e.s.DisableTOTP(ctx, p, goodPw, "000000"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("코드 틀림: %v", err)
	}
	if err := e.s.DisableTOTP(ctx, p, goodPw, e.codeNow(secret, 0)); err != nil {
		t.Fatal(err)
	}
	if e.s.totpConfirmed(ctx, e.db, p.UserID) || e.n(t, `SELECT COUNT(*) FROM totp_recovery_codes`) != 0 {
		t.Fatal("해제 뒤에도 남음")
	}
	// 필수 역할(admin 기본)은 해제 불가.
	e.setPw(t, "admin@pl.test", goodPw, false)
	if err := e.s.DisableTOTP(ctx, e.admin, goodPw, "123456"); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "필수") {
		t.Fatalf("필수 역할이 해제됨: %v", err)
	}
}

// AC-1033: 필수 역할(admin)은 미등록이면 제한 세션(totp_enroll)이고, 등록하면 풀린다. 관리자 초기화 후 다음 로그인에서 다시 강제된다.
func TestTOTPRequiredRoleRestrictedSession(t *testing.T) {
	e := totpEnv(t)
	ctx := context.Background()
	e.setPw(t, "admin@pl.test", goodPw, false)
	lr, err := e.login("admin@pl.test", goodPw)
	if err != nil || lr.Setup != contract.SetupTOTPEnroll {
		t.Fatalf("제한 세션이어야 함: %+v %v", lr, err)
	}
	adminP := contract.Principal{UserID: e.admin.UserID, Role: contract.RoleAdmin, Setup: lr.Setup, Auth: contract.AuthSession}
	if adminP.Can(contract.PermUserManage) {
		t.Fatal("제한 세션이 관리 권한을 가짐")
	}
	b32, _, err := e.s.EnrollTOTP(ctx, adminP, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	if _, err := e.s.ConfirmTOTP(ctx, adminP, lr.RawSession, e.codeNow(secret, 0)); err != nil {
		t.Fatal(err)
	}
	var setup sql.NullString
	_ = e.db.QueryRow(`SELECT setup FROM sessions WHERE id_hash=?`, sha256Hex(lr.RawSession)).Scan(&setup)
	if setup.Valid {
		t.Fatalf("등록 뒤에도 제한 세션: %s", setup.String)
	}
	// 관리자 초기화(다른 관리자): 세션 폐기, 다음 로그인은 다시 등록 강제.
	other := contract.Principal{UserID: e.uid(t, "staff@pl.test"), Role: contract.RoleAdmin, Auth: contract.AuthSession} // 다른 관리자 흉내
	if err := e.s.ResetTOTP(ctx, other, e.admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.sessionPrincipal(ctx, lr.RawSession); err == nil {
		t.Fatal("초기화 뒤 세션이 살아 있음")
	}
	lr2, err := e.login("admin@pl.test", goodPw)
	if err != nil || lr2.Setup != contract.SetupTOTPEnroll {
		t.Fatalf("재등록 강제: %+v %v", lr2, err)
	}
	if err := e.s.ResetTOTP(ctx, contract.Principal{UserID: 99, Role: contract.RoleStaff}, e.admin.UserID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없이 초기화: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.totp_reset'`) != 1 {
		t.Fatal("초기화 감사 없음")
	}
}

// 키가 없으면 2단계 인증 기능이 꺼지고, 필수 역할이어도 요구하지 않는다.
func TestTOTPOffWithoutKey(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "admin@pl.test", goodPw, false)
	lr, err := e.login("admin@pl.test", goodPw)
	if err != nil || lr.Setup != "" || lr.Ticket != "" {
		t.Fatalf("%+v %v", lr, err)
	}
	if _, _, err := e.s.EnrollTOTP(context.Background(), e.admin, "", ""); !errors.Is(err, ErrTOTPOff) {
		t.Fatalf("키 없이 등록: %v", err)
	}
}

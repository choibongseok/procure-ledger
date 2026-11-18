package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/auth/authtest"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

const msTenant = "11111111-2222-3333-4444-555555555555"

type provEnv struct {
	*pwEnv
	google, ms *authtest.MockIdP
}

// newProvEnv 는 구글·MS 환경변수가 설정되고 발급자가 모의 IdP 를 가리키는 서비스를 만든다. 방식은 아직 꺼져 있다.
func newProvEnv(t *testing.T) *provEnv {
	t.Helper()
	e := newPwEnv(t, false)
	pe := &provEnv{pwEnv: e}
	pe.google = authtest.NewMockIdP(t, "", "google-client")
	pe.ms = authtest.NewMockIdP(t, "/"+msTenant+"/v2.0", "ms-client")
	e.s.google = OAuthEnv{ClientID: "google-client", ClientSecret: "google-secret"}
	e.s.microsoft = OAuthEnv{ClientID: "ms-client", ClientSecret: "ms-secret"}
	e.s.issuerOverride[MethodGoogle] = pe.google.Issuer
	e.s.issuerOverride[MethodMicrosoft] = pe.ms.Issuer
	return pe
}

// enable 은 시험을 건너뛰고 방식을 켠다(시험 흐름은 따로 검증한다).
func (e *provEnv) enable(t *testing.T, provider, cfgJSON string) {
	t.Helper()
	if _, err := e.db.Exec(`UPDATE auth_providers SET enabled = 1, config_json = ?, tested_at = ? WHERE provider = ?`, cfgJSON, e.clock.Format(timeLayout), provider); err != nil {
		t.Fatal(err)
	}
}

func (e *provEnv) enableGoogle(t *testing.T) {
	e.enable(t, MethodGoogle, `{"allowedDomains":["pl.test"]}`)
}
func (e *provEnv) enableMS(t *testing.T) {
	e.enable(t, MethodMicrosoft, `{"tenantId":"`+msTenant+`"}`)
}

// login 은 인증 시작 → 모의 IdP 응답 → 콜백 완료를 한 번 돈다.
func (e *provEnv) login(t *testing.T, idp *authtest.MockIdP, provider string, claims map[string]any) (string, error) {
	t.Helper()
	u, err := e.s.BeginProviderLogin(context.Background(), provider, "/", "", purposeLogin, 0)
	if err != nil {
		return "", err
	}
	state, code := idp.Issue(u, claims)
	raw, _, _, err := e.s.CompleteLogin(context.Background(), state, code)
	return raw, err
}

func (e *provEnv) lastDenyReason(t *testing.T) string {
	t.Helper()
	var diff string
	if err := e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action='session.login' AND result='denied' ORDER BY seq DESC LIMIT 1`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	return diff
}

// AC-1040: 구글 정상 로그인(hd·email_verified·도메인 일치) → 세션, 신원은 (google, accounts.google.com, sub)로 묶인다.
func TestGoogleLoginSuccessAndBinding(t *testing.T) {
	e := newProvEnv(t)
	e.enableGoogle(t)
	raw, err := e.login(t, e.google, MethodGoogle, map[string]any{"sub": "g-1", "email": "Staff@pl.test", "email_verified": true, "hd": "pl.test"})
	if err != nil || raw == "" {
		t.Fatalf("로그인: %v", err)
	}
	var m string
	_ = e.db.QueryRow(`SELECT auth_method FROM sessions WHERE id_hash=?`, sha256Hex(raw)).Scan(&m)
	if m != MethodGoogle {
		t.Fatalf("auth_method=%q", m)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='google' AND issuer='accounts.google.com' AND subject='g-1' AND user_id=?`, e.uid(t, "staff@pl.test")) != 1 {
		t.Fatal("신원이 묶이지 않음")
	}
	// 이후에는 이메일이 달라져도 sub 로 인식한다(E2).
	raw2, err := e.login(t, e.google, MethodGoogle, map[string]any{"sub": "g-1", "email": "other@pl.test", "email_verified": true, "hd": "pl.test"})
	if err != nil || raw2 == "" {
		t.Fatalf("재로그인: %v", err)
	}
	p, _, err := e.s.sessionPrincipal(context.Background(), raw2)
	if err != nil || p.UserID != e.uid(t, "staff@pl.test") {
		t.Fatalf("같은 사용자여야 함: %+v %v", p, err)
	}
}

// AC-1040: 거부 사례 전부(개인 계정 hd 없음·hd 불일치·미검증 이메일·이메일 도메인 불일치·미등록·서명·aud·exp·nonce·alg=none).
func TestGoogleLoginRejections(t *testing.T) {
	e := newProvEnv(t)
	e.enableGoogle(t)
	good := func() map[string]any {
		return map[string]any{"sub": "g-9", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"}
	}
	with := func(k string, v any) map[string]any { c := good(); c[k] = v; return c }
	claimCases := []struct {
		name   string
		claims map[string]any
		reason string
	}{
		{"hd 없음(개인 구글 계정)", with("hd", nil), ReasonDomainMismatch},
		{"hd 불일치", with("hd", "evil.test"), ReasonDomainMismatch},
		{"이메일 미검증", with("email_verified", false), ReasonEmailUnverified},
		{"이메일 도메인이 hd 와 다름", with("email", "staff@evil.test"), ReasonDomainMismatch},
		{"hd 대소문자만 다른 것은 허용되지 않는 도메인", with("hd", "pl.test.evil.com"), ReasonDomainMismatch},
		{"미등록 이메일", with("email", "ghost@pl.test"), ReasonUnknownAccount},
	}
	for _, c := range claimCases {
		_, err := e.login(t, e.google, MethodGoogle, c.claims)
		if !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("%s: 거부되어야 함: %v", c.name, err)
		}
		if got := e.lastDenyReason(t); !strings.Contains(got, c.reason) {
			t.Fatalf("%s: 감사 사유 %q 에 %q 기대", c.name, got, c.reason)
		}
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='google'`) != 0 {
		t.Fatal("거부된 로그인에 신원이 묶임")
	}
	// 토큰 자체가 틀린 경우: 서명 키 불일치·aud·exp·nonce·alg=none. 모두 ID 토큰 검증 단계에서 거부.
	bad := []struct {
		name  string
		claim map[string]any
		key   bool
		alg   string
	}{
		{"서명 키 불일치", nil, true, "RS256"},
		{"aud 불일치", map[string]any{"aud": "other-client"}, false, "RS256"},
		{"만료", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}, false, "RS256"},
		{"nonce 불일치", map[string]any{"nonce": "attacker"}, false, "RS256"},
		{"alg=none", nil, false, "none"},
		{"발급자 불일치", map[string]any{"iss": "https://evil.example"}, false, "RS256"},
	}
	for _, c := range bad {
		u, err := e.s.BeginProviderLogin(context.Background(), MethodGoogle, "/", "", purposeLogin, 0)
		if err != nil {
			t.Fatal(err)
		}
		claims := good()
		for k, v := range c.claim {
			claims[k] = v
		}
		var key = e.google.Key
		if c.key {
			key = e.google.Other
		}
		state, code := e.google.IssueWith(u, claims, key, c.alg)
		if raw, _, _, err := e.s.CompleteLogin(context.Background(), state, code); err == nil || raw != "" {
			t.Fatalf("%s: 거부되어야 함", c.name)
		} else if !errors.Is(err, ErrLoginFailed) {
			t.Fatalf("%s: 오류 종류 %v", c.name, err)
		}
	}
	if e.n(t, `SELECT COUNT(*) FROM sessions WHERE auth_method='google'`) != 0 {
		t.Fatal("거부된 로그인에 세션이 생김")
	}
}

// AC-1042: state 는 1회 소비되고(재생 방지), 알 수 없는 state·만료된 요청은 거부된다. PKCE verifier 가 틀리면 코드 교환이 실패한다.
func TestOIDCStateSingleUseAndPKCE(t *testing.T) {
	e := newProvEnv(t)
	e.enableGoogle(t)
	ctx := context.Background()
	u, _ := e.s.BeginProviderLogin(ctx, MethodGoogle, "/x", "", purposeLogin, 0)
	state, code := e.google.Issue(u, map[string]any{"sub": "g-2", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"})
	if _, _, to, err := e.s.CompleteLogin(ctx, state, code); err != nil || to != "/x" {
		t.Fatalf("첫 사용: %v %q", err, to)
	}
	if _, _, _, err := e.s.CompleteLogin(ctx, state, code); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("state 재사용: %v", err)
	}
	if _, _, _, err := e.s.CompleteLogin(ctx, "unknown-state", "c"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("알 수 없는 state: %v", err)
	}
	// 만료
	u2, _ := e.s.BeginProviderLogin(ctx, MethodGoogle, "/", "", purposeLogin, 0)
	st2, c2 := e.google.Issue(u2, map[string]any{"sub": "g-3", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"})
	*e.clock = e.clock.Add(11 * time.Minute)
	if _, _, _, err := e.s.CompleteLogin(ctx, st2, c2); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("만료된 요청: %v", err)
	}
	// verifier 가 저장된 것과 다르면 IdP 가 코드 교환을 거부한다.
	u3, _ := e.s.BeginProviderLogin(ctx, MethodGoogle, "/", "", purposeLogin, 0)
	st3, c3 := e.google.Issue(u3, map[string]any{"sub": "g-4", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"})
	e.db.Exec(`UPDATE oidc_auth_requests SET code_verifier = 'tampered-verifier-tampered-verifier-tampered-0123' WHERE state = ?`, st3)
	if _, _, _, err := e.s.CompleteLogin(ctx, st3, c3); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("PKCE 불일치: %v", err)
	}
	// 구글 인증 URL 에 hd 힌트와 PKCE 가 실린다(검증은 서버가 한다).
	if !strings.Contains(u3, "hd=pl.test") || !strings.Contains(u3, "code_challenge_method=S256") {
		t.Fatalf("인증 URL: %s", u3)
	}
}

// AC-1070: 꺼진 방식은 로그인을 시작할 수 없다(404). 설정이 덜 된 방식도 마찬가지.
func TestDisabledProviderCannotLogin(t *testing.T) {
	e := newProvEnv(t)
	if _, err := e.s.BeginProviderLogin(context.Background(), MethodGoogle, "/", "", purposeLogin, 0); !errors.Is(err, ErrLoginNotConfigured) {
		t.Fatalf("꺼진 구글: %v", err)
	}
	e.enableGoogle(t)
	e.s.google = OAuthEnv{} // 환경변수가 사라지면 켜져 있어도 쓸 수 없다
	if _, err := e.s.BeginProviderLogin(context.Background(), MethodGoogle, "/", "", purposeLogin, 0); !errors.Is(err, ErrLoginNotConfigured) {
		t.Fatalf("환경변수 없음: %v", err)
	}
	if lp := e.s.loginProviders(context.Background()); len(lp) != 0 {
		t.Fatalf("로그인 화면에 보임: %+v", lp)
	}
}

// AC-1041: MS — 첫 연결은 이메일, 이후엔 (tid, oid)로만. 이메일을 바꾼 신원이 다른 사람 계정에 들어가지 못한다.
func TestMicrosoftLoginTenantAndOID(t *testing.T) {
	e := newProvEnv(t)
	e.enableMS(t)
	staff := e.uid(t, "staff@pl.test")
	c := func(oid, email string) map[string]any {
		return map[string]any{"sub": "pairwise-" + oid, "oid": oid, "tid": msTenant, "email": email}
	}
	raw, err := e.login(t, e.ms, MethodMicrosoft, c("oid-A", "staff@pl.test"))
	if err != nil || raw == "" {
		t.Fatalf("첫 로그인: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='microsoft' AND issuer=? AND subject='oid-a' AND user_id=?`, msTenant, staff) != 1 {
		t.Fatal("(tid, oid)로 묶이지 않음")
	}
	// 같은 oid 가 이메일을 다른 사람(lead)의 것으로 바꿔 와도 원래 사용자다.
	raw2, err := e.login(t, e.ms, MethodMicrosoft, c("oid-A", "lead@pl.test"))
	if err != nil {
		t.Fatal(err)
	}
	if p, _, _ := e.s.sessionPrincipal(context.Background(), raw2); p.UserID != staff {
		t.Fatalf("이메일이 바뀐 신원이 다른 사용자로 들어감: %d", p.UserID)
	}
	// 다른 oid 가 staff 의 이메일로 오면(이미 다른 주체가 묶인 이메일) 거부.
	if _, err := e.login(t, e.ms, MethodMicrosoft, c("oid-B", "staff@pl.test")); !errors.Is(err, contract.ErrForbidden) || !strings.Contains(e.lastDenyReason(t), ReasonIdentityBound) {
		t.Fatalf("다른 oid 의 재연결: %v", err)
	}
	// 다른 테넌트: tid 불일치(iss 도 다르면 서명 검증 전에 거부되지만 여기서는 tid 만 다르게)
	other := c("oid-C", "lead@pl.test")
	other["tid"] = "99999999-2222-3333-4444-555555555555"
	if _, err := e.login(t, e.ms, MethodMicrosoft, other); !errors.Is(err, contract.ErrForbidden) || !strings.Contains(e.lastDenyReason(t), ReasonTenantMismatch) {
		t.Fatalf("tid 불일치: %v", err)
	}
	// iss 불일치(다른 테넌트의 발급자)는 토큰 검증에서 거부.
	bad := c("oid-D", "lead@pl.test")
	bad["iss"] = "https://login.microsoftonline.com/99999999-2222-3333-4444-555555555555/v2.0"
	if _, err := e.login(t, e.ms, MethodMicrosoft, bad); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("iss 불일치: %v", err)
	}
	// 이메일 없음 + 미연결 → 첫 연결 불가
	noMail := c("oid-E", "")
	delete(noMail, "email")
	if _, err := e.login(t, e.ms, MethodMicrosoft, noMail); !errors.Is(err, contract.ErrForbidden) || !strings.Contains(e.lastDenyReason(t), ReasonNoEmail) {
		t.Fatalf("이메일 없음: %v", err)
	}
	// 이메일은 없고 preferred_username 만 있어도 첫 연결에 쓴다.
	pu := c("oid-F", "")
	delete(pu, "email")
	pu["preferred_username"] = "finance@pl.test"
	if raw, err := e.login(t, e.ms, MethodMicrosoft, pu); err != nil || raw == "" {
		t.Fatalf("preferred_username: %v", err)
	}
	// MS 설정 검증: common·조직 이름은 테넌트가 될 수 없다.
	if _, err := e.s.UpdateProvider(context.Background(), e.admin, MethodMicrosoft, ProviderInput{TenantID: ptr("common")}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("common 테넌트 허용: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// 구글·MS·범용 OIDC 가 같은 코드 경로를 쓴다: 범용 oidc 도 같은 함수(identityFromClaims 기본 분기)와 state·nonce·PKCE 검증을 거친다.
func TestGenericOIDCUsesSamePath(t *testing.T) {
	c := idClaims{Subject: "s", Email: "a@b.c", EmailVerified: false}
	id, _, err := identityFromClaims(MethodOIDC, providerConfig{}, c)
	if err != nil || id.Email != "" || id.Provider != MethodOIDC {
		t.Fatalf("검증되지 않은 이메일은 연결에 쓰지 않는다: %+v %v", id, err)
	}
	c.EmailVerified = true
	if id, _, _ := identityFromClaims(MethodDev, providerConfig{}, c); id.Email != "a@b.c" || id.Provider != MethodDev {
		t.Fatalf("%+v", id)
	}
}

// 설정 화면의 규칙: 시험 성공 전에는 켤 수 없고, 켜진 방식의 설정은 바꿀 수 없고, 필수 설정·환경변수가 있어야 한다. 비밀은 응답에 없다.
func TestProviderUpdateRules(t *testing.T) {
	e := newProvEnv(t)
	ctx := context.Background()
	on := true
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &on}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("도메인 없이 켬: %v", err)
	}
	v, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{AllowedDomains: &[]string{" PL.test ", "pl.test", "corp.example"}})
	if err != nil || strings.Join(v.AllowedDomains, ",") != "pl.test,corp.example" {
		t.Fatalf("도메인 정리: %+v %v", v, err)
	}
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{AllowedDomains: &[]string{"not a domain"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("잘못된 도메인: %v", err)
	}
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &on}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "시험") {
		t.Fatalf("시험 없이 켬: %v", err)
	}
	// 연결 시험: 시험한 관리자 본인 계정(admin)이 구글로 로그인해야 성공한다.
	u, err := e.s.StartProviderTest(ctx, e.admin, MethodGoogle)
	if err != nil {
		t.Fatal(err)
	}
	// 다른 사용자(staff)의 계정으로 시험하면 실패하고 연결도 남지 않는다.
	state, code := e.google.Issue(u, map[string]any{"sub": "g-t1", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"})
	raw, _, to, err := e.s.CompleteLogin(ctx, state, code)
	if err != nil || raw != "" || !strings.HasPrefix(to, "/admin/auth") {
		t.Fatalf("시험은 세션 없이 끝나야 함: %q %q %v", raw, to, err)
	}
	if r, _ := e.s.LastTestResult(ctx, e.admin, MethodGoogle); r == nil || r.OK {
		t.Fatalf("다른 계정 시험이 성공으로 기록됨: %+v", r)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='google'`) != 0 {
		t.Fatal("실패한 시험이 신원을 묶음")
	}
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &on}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("실패한 시험 뒤 켬: %v", err)
	}
	// 본인 계정으로 성공.
	u, _ = e.s.StartProviderTest(ctx, e.admin, MethodGoogle)
	state, code = e.google.Issue(u, map[string]any{"sub": "g-admin", "email": "admin@pl.test", "email_verified": true, "hd": "pl.test"})
	if raw, _, _, err := e.s.CompleteLogin(ctx, state, code); err != nil || raw != "" {
		t.Fatalf("시험: %q %v", raw, err)
	}
	r, _ := e.s.LastTestResult(ctx, e.admin, MethodGoogle)
	if r == nil || !r.OK || r.Summary["도메인"] != "pl.test" || strings.Contains(r.Summary["이메일"], "admin@") {
		t.Fatalf("시험 결과(이메일은 가려야 함): %+v", r)
	}
	if e.n(t, `SELECT COUNT(*) FROM sessions WHERE auth_method='google'`) != 0 {
		t.Fatal("시험이 세션을 만듦")
	}
	if v, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &on}); err != nil || !v.Enabled {
		t.Fatalf("시험 성공 뒤 켜기: %+v %v", v, err)
	}
	if lp := e.s.loginProviders(ctx); len(lp) != 1 || lp[0].ID != MethodGoogle || lp[0].Label != "Google 로 로그인" {
		t.Fatalf("로그인 화면 방식: %+v", lp)
	}
	// 켜져 있는 동안은 설정을 바꿀 수 없다. 끄면 바꿀 수 있고 시험 기록이 지워진다.
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{AllowedDomains: &[]string{"x.test"}}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("켜진 상태의 설정 변경: %v", err)
	}
	off := false
	if _, err := e.s.UpdateProvider(ctx, e.admin, MethodGoogle, ProviderInput{Enabled: &off, AllowedDomains: &[]string{"x.test"}}); err != nil {
		t.Fatal(err)
	}
	var tested sql.NullString
	_ = e.db.QueryRow(`SELECT tested_at FROM auth_providers WHERE provider='google'`).Scan(&tested)
	if tested.Valid {
		t.Fatal("설정이 바뀌었는데 시험 기록이 남음")
	}
	// 권한: auth:manage 없이는 모두 거부.
	staff := contract.Principal{UserID: e.uid(t, "staff@pl.test"), Role: contract.RoleStaff}
	if _, err := e.s.UpdateProvider(ctx, staff, MethodGoogle, ProviderInput{Enabled: &off}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 변경: %v", err)
	}
	if _, err := e.s.ListProviders(ctx, staff); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 목록: %v", err)
	}
	// 감사: 변경·시험이 남고 비밀은 없다.
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.provider_update'`) < 3 || e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.provider_test'`) != 2 {
		t.Fatal("감사 행 수")
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%google-secret%' OR detail LIKE '%google-secret%'`) != 0 {
		t.Fatal("클라이언트 비밀이 감사에 남음")
	}
	views, _ := e.s.ListProviders(ctx, e.admin)
	for _, v := range views {
		if v.Provider == MethodGoogle && !v.EnvReady {
			t.Fatal("환경변수 상태")
		}
	}
}

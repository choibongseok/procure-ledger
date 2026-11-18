package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const (
	devIdPPrefix    = "/dev-idp"
	devClientID     = "procure-ledger-dev"
	devClientSecret = "procure-ledger-dev-secret" // 로컬 검증 전용 상수. dev IdP 가 꺼진 운영 구성에서는 쓰이지 않는다.
	devCodeTTL      = 2 * time.Minute
	devTokenTTL     = 10 * time.Minute
)

type devCode struct {
	account   DevAccount
	redirect  string
	challenge string
	nonce     string
	expires   time.Time
}

// DevIdP 는 로컬 검증용 내장 OIDC 공급자다. 표준 흐름 그대로(디스커버리, authorize, token, JWKS,
// PKCE S256 필수, 1회용 코드, RS256 서명 ID 토큰)라서 앱은 외부 IdP 와 같은 코드 경로로 붙는다.
// 비밀번호가 없으므로 PL_DEV_IDP=1 로 명시적으로 켠 로컬·테스트 환경에서만 쓴다.
type DevIdP struct {
	issuer   string
	redirect string
	now      func() time.Time
	key      *rsa.PrivateKey
	kid      string

	// lookup 은 login_hint 가 시드 계정 9개에 없을 때 users 테이블에서 계정을 찾는다(개발용 조직 시드의 사람들로 로그인해 보는 용도).
	// 로그인 화면 목록에는 넣지 않고 이메일을 직접 입력했을 때만 쓰인다. dev IdP 자체가 켜진 로컬 환경에만 존재한다.
	lookup func(ctx context.Context, email string) (DevAccount, bool)

	mu    sync.Mutex
	codes map[string]devCode
}

// NewDevIdP 는 새 서명 키를 만들어 dev IdP 를 준비한다.
func NewDevIdP(issuer, redirectURL string, now func() time.Time) (*DevIdP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	kid, err := randomString(8)
	if err != nil {
		return nil, err
	}
	return &DevIdP{issuer: issuer, redirect: redirectURL, now: now, key: key, kid: kid, codes: map[string]devCode{}}, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// register 는 dev IdP 라우트를 등록한다. IdP 엔드포인트는 인증 전 경로라 Public 으로 선언한다.
func (d *DevIdP) register(r contract.Registrar) {
	r.HandleFunc("GET", devIdPPrefix+"/.well-known/openid-configuration", contract.PermPublic, d.discovery)
	r.HandleFunc("GET", devIdPPrefix+"/jwks", contract.PermPublic, d.jwks)
	r.HandleFunc("GET", devIdPPrefix+"/authorize", contract.PermPublic, d.authorize)
	r.HandleFunc("POST", devIdPPrefix+"/token", contract.PermPublic, d.token)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *DevIdP) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                d.issuer,
		"authorization_endpoint":                d.issuer + "/authorize",
		"token_endpoint":                        d.issuer + "/token",
		"jwks_uri":                              d.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
	})
}

func (d *DevIdP) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := d.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": d.kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

var pickerTmpl = template.Must(template.New("picker").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><title>개발용 로그인</title></head>
<body><h1>개발용 로그인</h1>
<p>로컬 검증 전용 가짜 IdP 입니다. 계정을 고르세요.</p>
<ul>{{range .}}<li><a href="{{.URL}}">{{.Name}} ({{.Email}}) - {{.Role}}</a></li>{{end}}</ul>
</body></html>`))

func (d *DevIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != devClientID || q.Get("redirect_uri") != d.redirect {
		http.Error(w, "등록되지 않은 클라이언트 또는 리다이렉트 주소입니다", http.StatusBadRequest)
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "response_type=code 와 PKCE(S256)가 필요합니다", http.StatusBadRequest)
		return
	}
	hint := q.Get("login_hint")
	for _, a := range DevAccounts {
		if a.Email == hint {
			d.issueCode(w, r, a)
			return
		}
	}
	if hint != "" && d.lookup != nil {
		if a, ok := d.lookup(r.Context(), hint); ok {
			d.issueCode(w, r, a)
			return
		}
	}
	type item struct {
		DevAccount
		URL string
	}
	items := make([]item, 0, len(DevAccounts))
	for _, a := range DevAccounts {
		nq := url.Values{}
		for k, v := range q {
			nq[k] = v
		}
		nq.Set("login_hint", a.Email)
		items = append(items, item{a, devIdPPrefix + "/authorize?" + nq.Encode()})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pickerTmpl.Execute(w, items)
}

func (d *DevIdP) issueCode(w http.ResponseWriter, r *http.Request, a DevAccount) {
	q := r.URL.Query()
	code, err := randomString(24)
	if err != nil {
		http.Error(w, "서버 오류", http.StatusInternalServerError)
		return
	}
	d.mu.Lock()
	now := d.now()
	for k, v := range d.codes { // 만료된 코드 정리
		if now.After(v.expires) {
			delete(d.codes, k)
		}
	}
	d.codes[code] = devCode{account: a, redirect: d.redirect, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), expires: now.Add(devCodeTTL)}
	d.mu.Unlock()
	u, _ := url.Parse(d.redirect)
	rq := u.Query()
	rq.Set("code", code)
	if st := q.Get("state"); st != "" {
		rq.Set("state", st)
	}
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (d *DevIdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != devClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(devClientSecret)) != 1 {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	d.mu.Lock()
	c, found := d.codes[r.PostForm.Get("code")]
	delete(d.codes, r.PostForm.Get("code")) // 1회용: 검증 실패여도 소비한다
	d.mu.Unlock()
	now := d.now()
	if !found || now.After(c.expires) || r.PostForm.Get("redirect_uri") != c.redirect {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(c.challenge)) != 1 {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	idToken, err := d.sign(map[string]any{
		"iss": d.issuer, "sub": c.account.Subject(), "aud": devClientID,
		"iat": now.Unix(), "exp": now.Add(devTokenTTL).Unix(), "nonce": c.nonce,
		"email": c.account.Email, "email_verified": true, "name": c.account.Name,
	})
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	access, _ := randomString(24)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(devTokenTTL.Seconds()), "id_token": idToken,
	})
}

func (d *DevIdP) sign(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": d.kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, d.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

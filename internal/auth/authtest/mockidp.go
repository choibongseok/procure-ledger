// Package authtest 는 로그인 시험 전용 도구다(모의 OIDC IdP, 모의 SMTP 서버). 서버 바이너리는 이 패키지를 가져오지 않는다.
package authtest

// 시험 전용 모의 OIDC IdP. 실제 구글·MS 와 연결하지 않고 같은 코드 경로(Authorization Code + PKCE, 디스커버리, JWKS)를 검증한다.
// 시험이 클레임·서명 키·알고리즘을 지정해 정상형과 오류형(서명 키 불일치·aud 불일치·만료·nonce 불일치·alg=none·hd 누락·다른 tid)을 만든다.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

type MockIdP struct {
	t        testing.TB
	srv      *httptest.Server
	Issuer   string
	ClientID string
	Key      *rsa.PrivateKey
	Other    *rsa.PrivateKey // 서명 키 불일치 시험용
	mu       sync.Mutex
	codes    map[string]mockCode
}

type mockCode struct {
	claims    map[string]any
	challenge string
	nonce     string
	signWith  *rsa.PrivateKey
	alg       string
}

// NewMockIdP 는 issuerPath("" 또는 "/<tid>/v2.0")를 발급자 경로로 갖는 모의 IdP 를 띄운다.
func NewMockIdP(t testing.TB, issuerPath, clientID string) *MockIdP {
	t.Helper()
	k1, _ := rsa.GenerateKey(rand.Reader, 2048)
	k2, _ := rsa.GenerateKey(rand.Reader, 2048)
	m := &MockIdP{t: t, ClientID: clientID, Key: k1, Other: k2, codes: map[string]mockCode{}}
	mux := http.NewServeMux()
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	m.Issuer = m.srv.URL + issuerPath
	mux.HandleFunc(issuerPath+"/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": m.Issuer, "authorization_endpoint": m.Issuer + "/authorize", "token_endpoint": m.Issuer + "/token",
			"jwks_uri": m.Issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc(issuerPath+"/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": b64u(k1.N.Bytes()), "e": b64u(big.NewInt(int64(k1.E)).Bytes()),
		}}})
	})
	mux.HandleFunc(issuerPath+"/token", m.token)
	return m
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// issue 는 BeginProviderLogin 이 만든 인증 URL 에 대한 응답 코드를 만든다. claims 는 기본 클레임(iss·aud·sub·exp·nonce)에 덮어쓴다(값 nil 이면 삭제).
func (m *MockIdP) Issue(authURL string, claims map[string]any) (state, code string) {
	return m.IssueWith(authURL, claims, nil, "RS256")
}

func (m *MockIdP) IssueWith(authURL string, claims map[string]any, signWith *rsa.PrivateKey, alg string) (state, code string) {
	m.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		m.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		m.t.Fatalf("PKCE S256·state·nonce 가 인증 URL 에 있어야 함: %s", authURL)
	}
	base := map[string]any{"iss": m.Issuer, "aud": m.ClientID, "sub": "sub-1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": q.Get("nonce")}
	for k, v := range claims {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	code = "code-" + q.Get("state")
	m.mu.Lock()
	m.codes[code] = mockCode{claims: base, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), signWith: signWith, alg: alg}
	m.mu.Unlock()
	return q.Get("state"), code
}

func (m *MockIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	m.mu.Lock()
	c, ok := m.codes[r.Form.Get("code")]
	delete(m.codes, r.Form.Get("code")) // 코드는 1회용
	m.mu.Unlock()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !ok || r.Form.Get("grant_type") != "authorization_code" || b64u(sum[:]) != c.challenge {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	key := m.Key
	if c.signWith != nil {
		key = c.signWith
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": m.sign(c.claims, key, c.alg)})
}

func (m *MockIdP) sign(claims map[string]any, key *rsa.PrivateKey, alg string) string {
	hb, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": "k1"})
	cb, _ := json.Marshal(claims)
	in := b64u(hb) + "." + b64u(cb)
	if alg == "none" {
		return in + "."
	}
	h := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		m.t.Fatal(err)
	}
	return in + "." + b64u(sig)
}

func StateOf(authURL string) string {
	u, _ := url.Parse(authURL)
	return u.Query().Get("state")
}

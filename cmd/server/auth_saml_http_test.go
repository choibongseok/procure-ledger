package main

// SAML 라우트 시험(실제 서버 경로). 서명 검증·변형 응답의 상세 시험은 internal/auth/saml_test.go 의 부정 시험 세트가 한다.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/config"
)

func writeTestSPKeys(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-sp"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "sp.crt"), filepath.Join(dir, "sp.key")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	return
}

func TestSAMLRoutes(t *testing.T) {
	cert, key := writeTestSPKeys(t)
	e := newPlatformEnvWith(t, true, func(c *config.Config) { cheapArgon(c); c.Auth.SAMLCertPath, c.Auth.SAMLKeyPath = cert, key })
	c := e.browser()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	// SP 메타데이터는 공개이고 entityID·ACS·NameIDFormat·인증서가 있다. 개인 키는 없다.
	code, body := e.call(c, "GET", "/api/auth/saml/metadata", "", nil)
	if code != 200 || !strings.Contains(body, e.base+"/api/auth/saml/acs") || !strings.Contains(body, "emailAddress") || !strings.Contains(body, "X509Certificate") || strings.Contains(body, "PRIVATE") {
		t.Fatalf("메타데이터: %d %.300s", code, body)
	}
	// 꺼진 방식의 로그인은 404, 로그인 화면 설정에도 없다.
	if code, _ := e.call(c, "GET", "/api/auth/saml/login", "", nil); code != 404 {
		t.Fatalf("꺼진 SAML 로그인: %d", code)
	}
	if _, body := e.call(c, "GET", "/api/auth/config", "", nil); strings.Contains(body, `"id":"saml"`) {
		t.Fatalf("꺼진 방식이 로그인 화면에 나옴: %s", body)
	}
	// ACS: RelayState·요청 없이 온 응답(IdP 시작·교차 사이트 로그인 CSRF)은 세션 없이 오류 화면으로 간다. CSRF 토큰 없이도 라우트에 닿는다(공개 ACS).
	form := url.Values{"SAMLResponse": {"AAAA"}}
	req, _ := http.NewRequest("POST", e.base+"/api/auth/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://idp.example.test") // 교차 사이트 POST
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/login?error=state") || len(res.Cookies()) != 0 {
		t.Fatalf("요청 없는 ACS 응답: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if e.auditCount("session.login", "success") != 0 {
		t.Fatal("로그인이 만들어짐")
	}

	// 설정 화면 API: 메타데이터를 넣기 전에는 켤 수 없고 SP 값이 응답에 있으며 비밀은 없다.
	adm := e.browser()
	e.login(adm, "admin@pl.test")
	h := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(adm)})
	code, body = e.call(adm, "GET", "/api/admin/auth/providers", "", nil)
	if code != 200 || !strings.Contains(body, `"acsUrl":"`+e.base+`/api/auth/saml/acs"`) || strings.Contains(body, "PRIVATE") {
		t.Fatalf("SAML 카드: %d %.400s", code, body)
	}
	if code, _ := e.call(adm, "PUT", "/api/admin/auth/providers/saml", `{"enabled":true}`, h); code != 409 {
		t.Fatalf("메타데이터 없이 켬: %d", code)
	}
	if code, b := e.call(adm, "PUT", "/api/admin/auth/providers/saml", `{"idpMetadata":"<!DOCTYPE x><a/>"}`, h); code != 400 {
		t.Fatalf("DTD 메타데이터: %d %s", code, b)
	}
}

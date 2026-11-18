package auth

// SAML 시험. 시험용 IdP·SP 키는 이 시험이 실행할 때마다 메모리에서 새로 만든다(저장소에 키 파일이 없다 = 실제 비밀도, 시험 전용 키 파일도 없음).
// 정상 응답과 변형(서명 없음·변조·서명 래핑·Assertion 복수·SHA-1·미등록 인증서·다른 발급자/Audience/Recipient/Destination/InResponseTo·만료/미래·재전송·IdP 시작)을
// 만들어 모두 거부되는지 본다. 시간은 시험 시계를 주입해 고정한다.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const (
	testIdPEntity = "https://idp.example.test/meta"
	testIdPSSO    = "https://idp.example.test/sso"
)

type testCert struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	der  []byte
}

func newTestCert(t *testing.T, cn string, now time.Time) testCert {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(400 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCert{k, c, der}
}

func idpMetadataXML(entity, sso string, certs ...testCert) string {
	var kd strings.Builder
	for _, c := range certs {
		kd.WriteString(`<KeyDescriptor use="signing"><ds:KeyInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:X509Data><ds:X509Certificate>` +
			base64.StdEncoding.EncodeToString(c.der) + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo></KeyDescriptor>`)
	}
	return `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="` + entity + `"><IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` +
		kd.String() + `<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="` + sso + `"/></IDPSSODescriptor></EntityDescriptor>`
}

type samlEnv struct {
	*pwEnv
	idp testCert
}

func newSAMLEnv(t *testing.T) *samlEnv {
	t.Helper()
	e := newPwEnv(t, false)
	se := &samlEnv{pwEnv: e, idp: newTestCert(t, "test-idp", *e.clock)}
	sp := newTestCert(t, "test-sp", *e.clock)
	e.s.samlKeys = &samlSPKeys{cert: sp.cert, key: sp.key}
	se.enableWith(t, idpMetadataXML(testIdPEntity, testIdPSSO, se.idp))
	return se
}

func (e *samlEnv) enableWith(t *testing.T, metadata string) {
	t.Helper()
	cj := `{"idpMetadata":` + jsonString(metadata) + `}`
	if _, err := e.db.Exec(`UPDATE auth_providers SET enabled = 1, config_json = ?, tested_at = ? WHERE provider = 'saml'`, cj, e.clock.Format(timeLayout)); err != nil {
		t.Fatal(err)
	}
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// samlOpts 는 응답 변형 지정이다. 영값은 정상.
type samlOpts struct {
	NameID       string
	Issuer       string
	Audience     string
	Recipient    string
	Destination  string
	InResponseTo string
	NotBefore    time.Time
	NotOnOrAfter time.Time
	AssertionID  string
	Signer       *testCert // nil 이면 IdP 키
	SigMethod    string
	Unsigned     bool
	SignResponse bool // Assertion 대신 Response 에 서명
	Tamper       func(resp *etree.Element, assertion *etree.Element)
	PreSign      func(assertion *etree.Element) // 서명 전에 Assertion 을 고친다(IdP 가 서명한 값이 비정상인 경우)
}

func (e *samlEnv) request(t *testing.T) (requestID, relay string) {
	t.Helper()
	u, err := e.s.beginSAML(context.Background(), "/after", purposeLogin, 0)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	if !strings.HasPrefix(u, testIdPSSO) || pu.Query().Get("SAMLRequest") == "" || pu.Query().Get("Signature") == "" {
		t.Fatalf("AuthnRequest 리다이렉트(서명 포함): %s", u)
	}
	relay = pu.Query().Get("RelayState")
	if err := e.db.QueryRow(`SELECT request_id FROM saml_requests WHERE relay_hash = ?`, sha256Hex(relay)).Scan(&requestID); err != nil {
		t.Fatalf("요청이 저장되지 않음: %v", err)
	}
	return
}

func (e *samlEnv) build(t *testing.T, requestID string, o samlOpts) string {
	t.Helper()
	now := *e.clock
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	acs := e.s.samlACSURL()
	nb, na := o.NotBefore, o.NotOnOrAfter
	if nb.IsZero() {
		nb = now.Add(-time.Minute)
	}
	if na.IsZero() {
		na = now.Add(5 * time.Minute)
	}
	id := def(o.AssertionID, fmt.Sprintf("_a%d", time.Now().UnixNano()))
	ts := func(x time.Time) string { return x.UTC().Format("2006-01-02T15:04:05Z") }
	aXML := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="` + id + `" Version="2.0" IssueInstant="` + ts(now) + `">` +
		`<saml:Issuer>` + def(o.Issuer, testIdPEntity) + `</saml:Issuer>` +
		`<saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">` + def(o.NameID, "staff@pl.test") + `</saml:NameID>` +
		`<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData NotOnOrAfter="` + ts(na) + `" Recipient="` + def(o.Recipient, acs) +
		`" InResponseTo="` + def(o.InResponseTo, requestID) + `"/></saml:SubjectConfirmation></saml:Subject>` +
		`<saml:Conditions NotBefore="` + ts(nb) + `" NotOnOrAfter="` + ts(na) + `"><saml:AudienceRestriction><saml:Audience>` + def(o.Audience, e.s.samlEntityID()) +
		`</saml:Audience></saml:AudienceRestriction></saml:Conditions>` +
		`<saml:AuthnStatement AuthnInstant="` + ts(now) + `"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement>` +
		`</saml:Assertion>`
	adoc := etree.NewDocument()
	if err := adoc.ReadFromString(aXML); err != nil {
		t.Fatal(err)
	}
	assertion := adoc.Root()
	if o.PreSign != nil {
		o.PreSign(assertion)
	}
	signer := o.Signer
	if signer == nil {
		signer = &e.idp
	}
	signCtx := func() *dsig.SigningContext {
		ctx := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{signer.der}, PrivateKey: signer.key, Leaf: signer.cert}))
		ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
		m := o.SigMethod
		if m == "" {
			m = dsig.RSASHA256SignatureMethod
		}
		if err := ctx.SetSignatureMethod(m); err != nil {
			t.Fatal(err)
		}
		return ctx
	}
	if !o.Unsigned && !o.SignResponse {
		signed, err := signCtx().SignEnveloped(assertion)
		if err != nil {
			t.Fatal(err)
		}
		assertion = signed
	}
	rXML := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_r` + fmt.Sprint(time.Now().UnixNano()) +
		`" Version="2.0" IssueInstant="` + ts(now) + `" Destination="` + def(o.Destination, acs) + `" InResponseTo="` + def(o.InResponseTo, requestID) + `">` +
		`<saml:Issuer>` + def(o.Issuer, testIdPEntity) + `</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status></samlp:Response>`
	rdoc := etree.NewDocument()
	if err := rdoc.ReadFromString(rXML); err != nil {
		t.Fatal(err)
	}
	resp := rdoc.Root()
	resp.AddChild(assertion)
	if o.Tamper != nil {
		o.Tamper(resp, assertion)
	}
	if o.SignResponse {
		signed, err := signCtx().SignEnveloped(resp)
		if err != nil {
			t.Fatal(err)
		}
		rdoc.SetRoot(signed)
	}
	out, err := rdoc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func (e *samlEnv) post(t *testing.T, b64, relay string) (oidcResult, error) {
	t.Helper()
	form := url.Values{"SAMLResponse": {b64}}
	if relay != "" {
		form.Set("RelayState", relay)
	}
	req := httptest.NewRequest("POST", e.s.samlACSURL(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.s.completeSAML(context.Background(), req)
}

func (e *samlEnv) denyReason(t *testing.T) string {
	t.Helper()
	var d string
	if err := e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action='session.login' AND result='denied' ORDER BY seq DESC LIMIT 1`).Scan(&d); err != nil {
		return ""
	}
	return d
}

// AC-1050: SP 메타데이터에 entityID·ACS·NameIDFormat·SP 인증서가 있다.
func TestSAMLSPMetadata(t *testing.T) {
	e := newSAMLEnv(t)
	b, err := e.s.SAMLMetadata()
	if err != nil {
		t.Fatal(err)
	}
	x := string(b)
	for _, want := range []string{e.s.samlEntityID(), e.s.samlACSURL(), "emailAddress", base64.StdEncoding.EncodeToString(e.s.samlKeys.cert.Raw)} {
		if !strings.Contains(x, want) {
			t.Errorf("SP 메타데이터에 %q 없음", want)
		}
	}
	if strings.Contains(x, "PRIVATE") {
		t.Fatal("메타데이터에 키가 있음")
	}
}

// AC-1050: IdP 메타데이터 등록 검사.
func TestSAMLIdPMetadataValidation(t *testing.T) {
	e := newSAMLEnv(t)
	now := *e.clock
	two := newTestCert(t, "second", now)
	expired := newTestCert(t, "old", now.Add(-800*24*time.Hour))
	cases := []struct{ name, xml, want string }{
		{"정상", idpMetadataXML(testIdPEntity, testIdPSSO, e.idp), ""},
		{"교체 중 인증서 2개", idpMetadataXML(testIdPEntity, testIdPSSO, e.idp, two), ""},
		{"빈 값", "", "비어"},
		{"entityID 없음", idpMetadataXML("", testIdPSSO, e.idp), "entityID"},
		{"SSO 가 http", idpMetadataXML(testIdPEntity, "http://idp.example.test/sso", e.idp), "https"},
		{"인증서 없음", idpMetadataXML(testIdPEntity, testIdPSSO), "1~2개"},
		{"인증서 3개", idpMetadataXML(testIdPEntity, testIdPSSO, e.idp, two, newTestCert(t, "third", now)), "1~2개"},
		{"만료된 인증서", idpMetadataXML(testIdPEntity, testIdPSSO, expired), "만료"},
		{"DTD", `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a SYSTEM "file:///etc/passwd">]>` + idpMetadataXML(testIdPEntity, testIdPSSO, e.idp), "DTD"},
		{"256KiB 초과", idpMetadataXML(testIdPEntity, testIdPSSO, e.idp) + strings.Repeat(" ", samlMetadataMax), "256KiB"},
		{"XML 아님", "not xml", "XML"},
	}
	for _, c := range cases {
		_, _, err := parseIdPMetadata(c.xml, now)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v (기대 %q)", c.name, err, c.want)
		}
	}
}

// AC-1051: 정상 서명 응답은 로그인되고 신원은 (IdP entityID, NameID)로 묶인다.
func TestSAMLValidLogin(t *testing.T) {
	e := newSAMLEnv(t)
	rid, relay := e.request(t)
	res, err := e.post(t, e.build(t, rid, samlOpts{NameID: " Staff@pl.test "}), relay)
	if err != nil || res.Denied != nil {
		t.Fatalf("정상 응답 거부: %v %v", err, res.Denied)
	}
	raw, _, err := e.s.loginWith(context.Background(), res.Identity)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := e.s.sessionPrincipal(context.Background(), raw)
	if err != nil || p.UserID != e.uid(t, "staff@pl.test") {
		t.Fatalf("%+v %v", p, err)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='saml' AND issuer=? AND subject='staff@pl.test'`, testIdPEntity) != 1 {
		t.Fatal("신원이 묶이지 않음")
	}
	if res.Redirect != "/after" {
		t.Fatalf("이동 경로 %q", res.Redirect)
	}
	// Response 에만 서명한 응답도 받는다(IdP 마다 서명 위치가 다르다).
	rid, relay = e.request(t)
	if _, err := e.post(t, e.build(t, rid, samlOpts{SignResponse: true}), relay); err != nil {
		t.Fatalf("Response 서명: %v", err)
	}
}

// AC-1051·1052: 모든 변형은 거부된다(fail-closed). 거부는 세션도 신원도 만들지 않는다.
func TestSAMLNegativeSuite(t *testing.T) {
	e := newSAMLEnv(t)
	other := newTestCert(t, "attacker", *e.clock)
	now := *e.clock
	evilAssertion := func() *etree.Element {
		d := etree.NewDocument()
		_ = d.ReadFromString(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_evil" Version="2.0" IssueInstant="` + now.UTC().Format("2006-01-02T15:04:05Z") +
			`"><saml:Issuer>` + testIdPEntity + `</saml:Issuer><saml:Subject><saml:NameID>admin@pl.test</saml:NameID></saml:Subject></saml:Assertion>`)
		return d.Root()
	}
	cases := []struct {
		name string
		o    samlOpts
	}{
		{"서명 없음", samlOpts{Unsigned: true}},
		{"서명 후 값 변조", samlOpts{Tamper: func(r, a *etree.Element) { a.FindElement(".//NameID").SetText("admin@pl.test") }}},
		{"Assertion 복제(서명된 것 앞에 위조 Assertion)", samlOpts{Tamper: func(r, a *etree.Element) { r.InsertChildAt(a.Index(), evilAssertion()) }}},
		{"서명 래핑: 서명된 Assertion 을 위조 Assertion 의 Advice 안으로", samlOpts{Tamper: func(r, a *etree.Element) {
			ev := evilAssertion()
			adv := etree.NewElement("saml:Advice")
			r.RemoveChild(a)
			adv.AddChild(a)
			ev.AddChild(adv)
			r.AddChild(ev)
		}}},
		{"서명 래핑: 서명된 Assertion 을 Extensions 로 옮기고 위조를 본문에", samlOpts{Tamper: func(r, a *etree.Element) {
			ext := etree.NewElement("samlp:Extensions")
			r.RemoveChild(a)
			ext.AddChild(a)
			r.InsertChildAt(2, ext)
			r.AddChild(evilAssertion())
		}}},
		{"SHA-1 서명", samlOpts{SigMethod: dsig.RSASHA1SignatureMethod}},
		{"미등록 인증서로 서명", samlOpts{Signer: &other}},
		{"다른 발급자", samlOpts{Issuer: "https://evil.example/idp"}},
		{"다른 Audience", samlOpts{Audience: "https://other-sp.example/meta"}},
		{"다른 Recipient", samlOpts{Recipient: "https://evil.example/acs"}},
		{"다른 Destination", samlOpts{Destination: "https://evil.example/acs"}},
		{"InResponseTo 불일치", samlOpts{InResponseTo: "_not-ours"}},
		{"만료(오차 2분 밖)", samlOpts{NotBefore: now.Add(-20 * time.Minute), NotOnOrAfter: now.Add(-5 * time.Minute)}},
		{"미래(오차 2분 밖)", samlOpts{NotBefore: now.Add(10 * time.Minute), NotOnOrAfter: now.Add(20 * time.Minute)}},
		{"NameID 비어 있음", samlOpts{NameID: "   "}},
		// GO-2026-6909 모양: 서명은 정상이지만 Subject 가 비정상이다(라이브러리 nil 패닉·이름공간을 구분하지 않는 NameID 해석).
		{"Subject 없음", samlOpts{PreSign: func(a *etree.Element) { a.RemoveChild(a.FindElement("./Subject")) }}},
		{"빈 Subject", samlOpts{PreSign: func(a *etree.Element) {
			s := a.FindElement("./Subject")
			for _, c := range s.ChildElements() {
				s.RemoveChild(c)
			}
		}}},
		{"빈 NameID 요소", samlOpts{PreSign: func(a *etree.Element) { a.FindElement(".//NameID").SetText("") }}},
		{"다른 이름공간의 NameID 만 있음", samlOpts{PreSign: func(a *etree.Element) {
			n := a.FindElement(".//NameID")
			n.Space, n.Tag = "x", "NameID"
			n.CreateAttr("xmlns:x", "urn:evil:ns")
			n.SetText("admin@pl.test")
		}}},
		{"SAML 2.0 NameID 앞에 다른 이름공간의 NameID 를 끼움", samlOpts{PreSign: func(a *etree.Element) {
			s := a.FindElement("./Subject")
			fake := etree.NewElement("x:NameID")
			fake.CreateAttr("xmlns:x", "urn:evil:ns")
			fake.SetText("admin@pl.test")
			s.InsertChildAt(0, fake)
		}}},
		{"NameID 두 개", samlOpts{PreSign: func(a *etree.Element) {
			s := a.FindElement("./Subject")
			dup := etree.NewElement("saml:NameID")
			dup.SetText("admin@pl.test")
			s.InsertChildAt(0, dup)
		}}},
		{"NameID 안에 자식 요소", samlOpts{PreSign: func(a *etree.Element) { a.FindElement(".//NameID").AddChild(etree.NewElement("saml:Evil")) }}},
	}
	for _, c := range cases {
		rid, relay := e.request(t)
		res, err := e.post(t, e.build(t, rid, c.o), relay)
		if err == nil && res.Denied == nil {
			t.Errorf("%s: 거부되어야 함", c.name)
		}
	}
	if e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='saml'`) != 0 || e.n(t, `SELECT COUNT(*) FROM sessions WHERE auth_method='saml'`) != 0 {
		t.Fatal("거부된 응답이 신원·세션을 만듦")
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='denied' AND diff LIKE '%saml%'`) < len(cases)-2 {
		t.Fatal("거부 감사가 남지 않음")
	}
}

// 오차 ±2분 경계: 만료 직후 1분은 허용, 3분은 거부.
func TestSAMLClockSkewBoundary(t *testing.T) {
	e := newSAMLEnv(t)
	now := *e.clock
	ok := func(na time.Time) bool {
		rid, relay := e.request(t)
		res, err := e.post(t, e.build(t, rid, samlOpts{NotBefore: now.Add(-10 * time.Minute), NotOnOrAfter: na}), relay)
		return err == nil && res.Denied == nil
	}
	if !ok(now.Add(-time.Minute)) {
		t.Error("만료 1분 뒤(오차 안)는 허용되어야 함")
	}
	if ok(now.Add(-3 * time.Minute)) {
		t.Error("만료 3분 뒤(오차 밖)는 거부되어야 함")
	}
}

// AC-1052: 같은 Assertion ID 재전송은 거부, IdP 시작(우리가 시작하지 않은) 응답은 거부.
func TestSAMLReplayAndIdPInitiated(t *testing.T) {
	e := newSAMLEnv(t)
	rid, relay := e.request(t)
	resp := e.build(t, rid, samlOpts{AssertionID: "_same-assertion"})
	if res, err := e.post(t, resp, relay); err != nil || res.Denied != nil {
		t.Fatalf("첫 전송: %v %v", err, res.Denied)
	}
	rid2, relay2 := e.request(t)
	res, err := e.post(t, e.build(t, rid2, samlOpts{AssertionID: "_same-assertion"}), relay2)
	if err == nil && res.Denied == nil {
		t.Fatal("같은 Assertion ID 재전송이 통과함")
	}
	if !strings.Contains(e.denyReason(t), ReasonReplay) {
		t.Fatalf("재생 사유: %s", e.denyReason(t))
	}
	// RelayState 없음 = IdP 시작(unsolicited)
	rid3, _ := e.request(t)
	if _, err := e.post(t, e.build(t, rid3, samlOpts{}), ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("IdP 시작 응답: %v", err)
	}
	// 알 수 없는 RelayState
	if _, err := e.post(t, e.build(t, rid3, samlOpts{}), "forged-relay"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("위조 RelayState: %v", err)
	}
}

// AC-1053: 요청 ID 는 1회 소비되고 5분 뒤 무효다.
func TestSAMLRequestSingleUseAndExpiry(t *testing.T) {
	e := newSAMLEnv(t)
	rid, relay := e.request(t)
	if _, err := e.post(t, e.build(t, rid, samlOpts{}), relay); err != nil {
		t.Fatal(err)
	}
	if _, err := e.post(t, e.build(t, rid, samlOpts{}), relay); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("요청 재사용: %v", err)
	}
	rid, relay = e.request(t)
	b := e.build(t, rid, samlOpts{NotOnOrAfter: e.clock.Add(30 * time.Minute)})
	*e.clock = e.clock.Add(6 * time.Minute)
	if _, err := e.post(t, b, relay); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("5분 지난 요청: %v", err)
	}
}

// 정책: 미등록 이메일·퇴사(비활성)는 거부되고 계정이 생기지 않는다(AC-1060·1061).
func TestSAMLUnknownAndInactiveAccount(t *testing.T) {
	e := newSAMLEnv(t)
	before := e.n(t, `SELECT COUNT(*) FROM users`)
	rid, relay := e.request(t)
	res, err := e.post(t, e.build(t, rid, samlOpts{NameID: "ghost@pl.test"}), relay)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.loginWith(context.Background(), res.Identity); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("미등록: %v", err)
	}
	e.db.Exec(`UPDATE users SET active = 0 WHERE email = 'staff@pl.test'`)
	rid, relay = e.request(t)
	res, _ = e.post(t, e.build(t, rid, samlOpts{}), relay)
	if _, _, err := e.s.loginWith(context.Background(), res.Identity); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("비활성: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM users`) != before || e.n(t, `SELECT COUNT(*) FROM user_identities WHERE provider='saml'`) != 0 {
		t.Fatal("계정·신원이 생김")
	}
}

// 꺼진 방식은 로그인을 시작할 수 없고, 인증서가 만료되면 SAML 로그인이 실패한다(비상 로그인으로 복구).
func TestSAMLDisabledAndExpiredCert(t *testing.T) {
	e := newSAMLEnv(t)
	e.db.Exec(`UPDATE auth_providers SET enabled = 0 WHERE provider='saml'`)
	if _, err := e.s.beginSAML(context.Background(), "/", purposeLogin, 0); !errors.Is(err, ErrLoginNotConfigured) {
		t.Fatalf("꺼진 SAML: %v", err)
	}
	e.db.Exec(`UPDATE auth_providers SET enabled = 1 WHERE provider='saml'`)
	*e.clock = e.clock.Add(500 * 24 * time.Hour)
	if _, err := e.s.beginSAML(context.Background(), "/", purposeLogin, 0); !errors.Is(err, ErrLoginNotConfigured) {
		t.Fatalf("만료된 IdP 인증서: %v", err)
	}
}

// SP 키 파일 권한이 넓으면 SAML 은 꺼진 채 시작한다.
func TestSAMLKeyFilePermissions(t *testing.T) {
	now := time.Now()
	c := newTestCert(t, "sp", now)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "sp.crt"), filepath.Join(dir, "sp.key")
	writePEMs(t, certPath, keyPath, c)
	if k := loadSAMLKeys(SAMLEnv{CertPath: certPath, KeyPath: keyPath}); k == nil || k.cert == nil {
		t.Fatalf("정상 키가 로드되지 않음: %+v", k)
	}
	_ = os.Chmod(keyPath, 0o644)
	if k := loadSAMLKeys(SAMLEnv{CertPath: certPath, KeyPath: keyPath}); k == nil || k.cert != nil || !strings.Contains(k.err, "0600") {
		t.Fatalf("넓은 권한이 허용됨: %+v", k)
	}
	if loadSAMLKeys(SAMLEnv{}) != nil {
		t.Fatal("경로가 없으면 nil")
	}
}

var _ = http.StatusOK

func writePEMs(t *testing.T, certPath, keyPath string, c testCert) {
	t.Helper()
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
	kp := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(c.key)})
	if err := os.WriteFile(certPath, cp, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, kp, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 라이브러리 패닉은 거부(오류)로 바뀌고 서버가 죽지 않는다.
func TestRunGuardedTurnsPanicIntoError(t *testing.T) {
	err := runGuarded(func() error {
		var a *struct{ x *int }
		_ = *a.x // nil 포인터 패닉
		return nil
	})
	if !errors.Is(err, errSAMLPanic) {
		t.Fatalf("패닉이 오류로 바뀌지 않음: %v", err)
	}
	if err := runGuarded(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if samlReasonOf := ReasonSAMLPanic; samlReasonOf == "" {
		t.Fatal("사유 코드")
	}
}

// 정상 응답의 NameID 는 응답 XML 에서 직접 읽은 값과 같고, 이름공간이 다른 Assertion 은 거부된다.
func TestReadNameIDDirect(t *testing.T) {
	e := newSAMLEnv(t)
	rid, _ := e.request(t)
	b64 := e.build(t, rid, samlOpts{NameID: " Staff@pl.test ", AssertionID: "_direct"})
	if v, err := readNameID(b64, "_direct"); err != nil || v != "Staff@pl.test" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := readNameID(b64, "_other"); err == nil {
		t.Fatal("다른 ID 의 Assertion 이 통과함")
	}
	raw, _ := base64.StdEncoding.DecodeString(b64)
	evil := strings.ReplaceAll(string(raw), "urn:oasis:names:tc:SAML:2.0:assertion", "urn:evil:assertion")
	if _, err := readNameID(base64.StdEncoding.EncodeToString([]byte(evil)), "_direct"); err == nil {
		t.Fatal("SAML 2.0 이름공간이 아닌 Assertion 이 통과함")
	}
}

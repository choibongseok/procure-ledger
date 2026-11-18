package auth

// SAML 2.0 SP(명세 9절). XML 서명 검증은 직접 구현하지 않고 검증된 라이브러리(crewjam/saml 0.6.3, goxmldsig)에 맡기되,
// 그 위에 우리 쪽 검사를 한 번 더 건다(발급자·Destination·Audience·Recipient·InResponseTo·시각·NameID·Assertion 개수·재생 방지).
// SP 시작 흐름만 지원한다(IdP 시작 응답 거부). 메타데이터는 붙여넣기만 받고 URL 조회는 하지 않는다. SLO 는 하지 않는다.

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const (
	samlMetadataMax   = 256 << 10 // IdP 메타데이터 크기 상한
	samlResponseMax   = 1 << 20   // ACS 로 받는 SAMLResponse(base64 해독 뒤) 상한
	samlSkew          = 2 * time.Minute
	samlRequestTTL    = 5 * time.Minute
	samlAssertionKeep = time.Hour
)

func init() {
	saml.MaxClockSkew = samlSkew // 명세 9.4-5: 시계 오차 ±2분
}

// 추가 거부 사유 코드.
const (
	ReasonSignatureInvalid = "signature_invalid"
	ReasonReplay           = "replay"
	ReasonSAMLInvalid      = "saml_invalid"
	ReasonSAMLChecks       = "saml_check_failed"
)

// SAMLEnv 는 SP 인증서·키 파일 경로다(환경변수). 키 파일은 저장소 밖에 두고 권한 0600 이어야 한다.
type SAMLEnv struct{ CertPath, KeyPath string }

// samlSPKeys 는 읽어 둔 SP 인증서·키다.
type samlSPKeys struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey // 서명·복호화. RSA 키만 쓴다
	err  string          // 로드하지 못한 이유(비밀 아님: 경로·권한 문제)
}

func loadSAMLKeys(e SAMLEnv) *samlSPKeys {
	if e.CertPath == "" || e.KeyPath == "" {
		return nil
	}
	fail := func(msg string, err error) *samlSPKeys {
		log.Printf("auth: SAML SP 인증서·키를 쓸 수 없어 SAML 이 꺼진 채 시작합니다: %s: %v", msg, err)
		return &samlSPKeys{err: msg}
	}
	st, err := os.Stat(e.KeyPath)
	if err != nil {
		return fail("키 파일을 읽을 수 없습니다", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fail("키 파일 권한이 너무 넓습니다(0600 이어야 함)", fmt.Errorf("mode %v", st.Mode().Perm()))
	}
	pair, err := tls.LoadX509KeyPair(e.CertPath, e.KeyPath)
	if err != nil {
		return fail("인증서·키 파일을 읽을 수 없습니다", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fail("인증서 형식이 올바르지 않습니다", err)
	}
	rk, ok := pair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return fail("SP 키는 RSA 여야 합니다", errors.New("not rsa"))
	}
	return &samlSPKeys{cert: cert, key: rk}
}

// samlEnvReady 는 SP 인증서·키가 정상으로 읽혔는지다.
func (s *Service) samlEnvReady() bool { return s.samlKeys != nil && s.samlKeys.cert != nil }

func (s *Service) samlEnvProblem() string {
	switch {
	case s.samlKeys == nil:
		return "환경변수 PL_SAML_SP_CERT_PATH·PL_SAML_SP_KEY_PATH 가 설정되지 않았습니다"
	case s.samlKeys.err != "":
		return s.samlKeys.err
	}
	return ""
}

func (s *Service) samlEntityID() string { return s.base + "/api/auth/saml/metadata" }
func (s *Service) samlACSURL() string   { return s.base + "/api/auth/saml/acs" }

// ---- IdP 메타데이터 ----

// idpInfo 는 검증을 마친 IdP 메타데이터 요약이다(공개 정보).
type idpInfo struct {
	EntityID     string
	SSOURL       string
	Certs        []*x509.Certificate
	Fingerprints []string
	NotAfter     time.Time
}

func certFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// parseIdPMetadata 는 붙여넣은 메타데이터를 검사한다: 크기·DTD·외부 엔티티 금지, entityID, https SSO(HTTP-Redirect), 서명 인증서 1~2개(만료 아님).
func parseIdPMetadata(raw string, now time.Time) (*saml.EntityDescriptor, idpInfo, error) {
	bad := func(msg string) error { return fmt.Errorf("%w: IdP 메타데이터: %s", contract.ErrValidation, msg) }
	if len(raw) > samlMetadataMax {
		return nil, idpInfo{}, bad("크기가 256KiB 를 넘습니다")
	}
	data := []byte(strings.TrimSpace(raw))
	if len(data) == 0 {
		return nil, idpInfo{}, bad("비어 있습니다")
	}
	if len(data) > samlMetadataMax {
		return nil, idpInfo{}, bad("크기가 256KiB 를 넘습니다")
	}
	low := bytes.ToLower(data)
	if bytes.Contains(low, []byte("<!doctype")) || bytes.Contains(low, []byte("<!entity")) {
		return nil, idpInfo{}, bad("DTD·외부 엔티티는 허용되지 않습니다")
	}
	ed, err := samlsp.ParseMetadata(data)
	if err != nil {
		return nil, idpInfo{}, bad("XML 을 읽을 수 없습니다")
	}
	if strings.TrimSpace(ed.EntityID) == "" {
		return nil, idpInfo{}, bad("entityID 가 없습니다")
	}
	if len(ed.IDPSSODescriptors) == 0 {
		return nil, idpInfo{}, bad("IdP(IDPSSODescriptor) 정보가 없습니다")
	}
	d := ed.IDPSSODescriptors[0]
	info := idpInfo{EntityID: strings.TrimSpace(ed.EntityID)}
	for _, ep := range d.SingleSignOnServices {
		if ep.Binding != saml.HTTPRedirectBinding {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(ep.Location))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, idpInfo{}, bad("SSO 주소는 https 여야 합니다")
		}
		info.SSOURL = u.String()
		break
	}
	if info.SSOURL == "" {
		return nil, idpInfo{}, bad("HTTP-Redirect 바인딩의 SSO 주소(https)가 없습니다")
	}
	seen := map[string]bool{}
	for _, kd := range d.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}
		for _, x := range kd.KeyInfo.X509Data.X509Certificates {
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(x.Data), ""))
			if err != nil {
				return nil, idpInfo{}, bad("서명 인증서를 읽을 수 없습니다")
			}
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, idpInfo{}, bad("서명 인증서를 읽을 수 없습니다")
			}
			if !now.Before(c.NotAfter) || now.Before(c.NotBefore) {
				return nil, idpInfo{}, bad("서명 인증서가 만료되었거나 아직 유효하지 않습니다")
			}
			if fp := certFingerprint(c); !seen[fp] {
				seen[fp] = true
				info.Certs = append(info.Certs, c)
				info.Fingerprints = append(info.Fingerprints, fp)
				if info.NotAfter.IsZero() || c.NotAfter.Before(info.NotAfter) {
					info.NotAfter = c.NotAfter
				}
			}
		}
	}
	if n := len(info.Certs); n < 1 || n > 2 {
		return nil, idpInfo{}, bad("서명 인증서는 1~2개여야 합니다(교체 중 중복 허용)")
	}
	return ed, info, nil
}

func (s *Service) applyIdPMetadata(st *providerState, raw string) ([]audit.DiffItem, error) {
	var oldID, oldFP string
	if st.Config.IdPMetadata != "" {
		if _, oi, err := parseIdPMetadataLoose(st.Config.IdPMetadata); err == nil {
			oldID, oldFP = oi.EntityID, strings.Join(oi.Fingerprints, ",")
		}
	}
	if strings.TrimSpace(raw) == "" { // 지우기
		st.Config.IdPMetadata = ""
		return []audit.DiffItem{{Key: "idp_entity_id", Old: oldID}}, nil
	}
	_, info, err := parseIdPMetadata(raw, s.now())
	if err != nil {
		return nil, err
	}
	st.Config.IdPMetadata = strings.TrimSpace(raw)
	return []audit.DiffItem{
		{Key: "idp_entity_id", Old: oldID, New: info.EntityID},
		{Key: "idp_cert_fingerprint", Old: shortFPs(oldFP), New: shortFPs(strings.Join(info.Fingerprints, ","))},
	}, nil
}

func shortFPs(joined string) string {
	if joined == "" {
		return ""
	}
	parts := strings.Split(joined, ",")
	for i, p := range parts {
		if len(p) > 16 {
			parts[i] = p[:16]
		}
	}
	return strings.Join(parts, ",")
}

// parseIdPMetadataLoose 는 저장된 메타데이터를 만료 검사 없이 읽는다(화면에 만료를 보이기 위해).
func parseIdPMetadataLoose(raw string) (*saml.EntityDescriptor, idpInfo, error) {
	return parseIdPMetadata(raw, time.Time{}.Add(24*time.Hour))
}

func (s *Service) fillSAMLView(v *ProviderView, st providerState) {
	v.EntityID, v.ACSURL = s.samlEntityID(), s.samlACSURL()
	v.EnvProblem = s.samlEnvProblem()
	if st.Config.IdPMetadata != "" {
		if _, info, err := parseIdPMetadataLoose(st.Config.IdPMetadata); err == nil {
			v.IdPEntityID, v.IdPCertFingerprints = info.EntityID, info.Fingerprints
			na := info.NotAfter
			v.IdPCertNotAfter = &na
		}
	}
}

// ---- SP 구성 ----

// samlSP 는 저장된 설정으로 ServiceProvider 를 만든다. 로그인 목적이면 켜진 방식만 허용한다.
func (s *Service) samlSP(ctx context.Context, purpose string) (*saml.ServiceProvider, idpInfo, error) {
	if !s.samlEnvReady() {
		return nil, idpInfo{}, ErrLoginNotConfigured
	}
	st, err := s.loadProvider(ctx, s.db, MethodSAML)
	if err != nil {
		return nil, idpInfo{}, err
	}
	if purpose != purposeTest && !st.Enabled {
		return nil, idpInfo{}, ErrLoginNotConfigured
	}
	if configProblem(MethodSAML, st.Config) != "" {
		return nil, idpInfo{}, ErrLoginNotConfigured
	}
	ed, info, err := parseIdPMetadata(st.Config.IdPMetadata, s.now())
	if err != nil {
		return nil, idpInfo{}, ErrLoginNotConfigured // 인증서가 만료되면 SAML 로그인이 실패한다(비상 로그인으로 복구)
	}
	meta, _ := url.Parse(s.samlEntityID())
	acs, _ := url.Parse(s.samlACSURL())
	sp := &saml.ServiceProvider{
		EntityID: s.samlEntityID(), Key: s.samlKeys.key, Certificate: s.samlKeys.cert,
		MetadataURL: *meta, AcsURL: *acs, IDPMetadata: ed,
		AuthnNameIDFormat:                saml.EmailAddressNameIDFormat,
		AllowIDPInitiated:                false, // IdP 시작 응답은 거부(D10)
		SignatureMethod:                  dsig.RSASHA256SignatureMethod,
		RejectSHA1Signatures:             true,
		RejectUnknownConditions:          true,
		RequireSignedResponseForCBC:      true,
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		ReplayCache:                      &dbReplayCache{s: s},
		Clock:                            dsig.NewFakeClockAt(s.now()),
		MetadataValidDuration:            24 * time.Hour,
	}
	return sp, info, nil
}

// dbReplayCache 는 Assertion ID 재생 방지(saml_assertions_seen)다. 라이브러리가 ParseResponse 안에서 부르므로 트랜잭션 밖에서만 쓴다.
type dbReplayCache struct{ s *Service }

func (c *dbReplayCache) Consume(id string, expiresAt time.Time) error {
	now := c.s.now().UTC()
	if max := now.Add(samlAssertionKeep); expiresAt.After(max) {
		expiresAt = max
	}
	if expiresAt.Before(now) {
		expiresAt = now.Add(time.Minute)
	}
	ctx := context.Background()
	return c.s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM saml_assertions_seen WHERE expires_at < ?`, now.Format(timeLayout)); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO saml_assertions_seen (assertion_id, expires_at) VALUES (?,?) ON CONFLICT(assertion_id) DO NOTHING`, id, expiresAt.Format(timeLayout))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return saml.ErrAssertionReplayed
		}
		return nil
	})
}

// ---- 로그인 시작 ----

// beginSAML 은 SP 시작 로그인(AuthnRequest, HTTP-Redirect)을 만들고 요청 ID·RelayState 를 저장한 뒤 IdP 로 보낼 주소를 돌려준다.
func (s *Service) beginSAML(ctx context.Context, redirectTo, purpose string, testUser int64) (string, error) {
	sp, info, err := s.samlSP(ctx, purpose)
	if err != nil {
		return "", err
	}
	req, err := sp.MakeAuthenticationRequest(info.SSOURL, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", err
	}
	relay, err := randomString(24)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	var tu any
	if testUser != 0 {
		tu = testUser
	}
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM saml_requests WHERE expires_at < ?`, now.Format(timeLayout)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO saml_requests (request_id, relay_hash, redirect_to, purpose, test_user_id, created_at, expires_at) VALUES (?,?,?,?,?,?,?)`,
			req.ID, sha256Hex(relay), safeRedirect(redirectTo), purpose, tu, now.Format(timeLayout), now.Add(samlRequestTTL).Format(timeLayout))
		return err
	})
	if err != nil {
		return "", err
	}
	u, err := req.Redirect(relay, sp)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// SAMLMetadata 는 SP 메타데이터 XML 이다(공개: entityID·ACS·NameIDFormat·SP 인증서).
func (s *Service) SAMLMetadata() ([]byte, error) {
	if !s.samlEnvReady() {
		return nil, ErrLoginNotConfigured
	}
	meta, _ := url.Parse(s.samlEntityID())
	acs, _ := url.Parse(s.samlACSURL())
	sp := &saml.ServiceProvider{
		EntityID: s.samlEntityID(), Key: s.samlKeys.key, Certificate: s.samlKeys.cert, MetadataURL: *meta, AcsURL: *acs,
		AuthnNameIDFormat: saml.EmailAddressNameIDFormat, AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		MetadataValidDuration: 24 * time.Hour, Clock: dsig.NewFakeClockAt(s.now()),
	}
	return xmlBytes(sp.Metadata())
}

// ---- 응답 처리 ----

type samlRequestRow struct {
	RequestID, Redirect, Purpose string
	TestUser                     int64
}

// consumeSAMLRequest 는 RelayState 로 우리가 시작한 요청을 찾아 1회 소비한다(로그인 CSRF 방어: 우리가 시작하지 않은 응답은 거부).
func (s *Service) consumeSAMLRequest(ctx context.Context, relay string) (samlRequestRow, error) {
	var row samlRequestRow
	if relay == "" {
		return row, ErrInvalidState
	}
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var exp string
		var tu sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT request_id, redirect_to, purpose, test_user_id, expires_at FROM saml_requests WHERE relay_hash = ?`, sha256Hex(relay)).
			Scan(&row.RequestID, &row.Redirect, &row.Purpose, &tu, &exp)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidState
		}
		if err != nil {
			return err
		}
		row.TestUser = tu.Int64
		if _, err := tx.ExecContext(ctx, `DELETE FROM saml_requests WHERE request_id = ?`, row.RequestID); err != nil { // 성공·실패와 무관하게 1회용
			return err
		}
		if !s.now().UTC().Before(parseTime(exp)) {
			return ErrInvalidState
		}
		return nil
	})
	return row, err
}

// preCheckSAML 은 라이브러리에 넘기기 전에 우리 쪽에서 응답 XML 의 구조를 검사한다: 크기, DTD·엔티티 금지,
// Response 의 Destination 이 우리 ACS 일 것, Assertion(암호화 포함)이 정확히 한 개일 것(서명 래핑 변형 차단).
func (s *Service) preCheckSAML(b64 string) (denial, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 || len(raw) > samlResponseMax {
		return denial{ReasonSAMLInvalid}, false
	}
	low := bytes.ToLower(raw)
	if bytes.Contains(low, []byte("<!doctype")) || bytes.Contains(low, []byte("<!entity")) {
		return denial{ReasonSAMLInvalid}, false
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil || doc.Root() == nil {
		return denial{ReasonSAMLInvalid}, false
	}
	root := doc.Root()
	if root.Tag != "Response" {
		return denial{ReasonSAMLInvalid}, false
	}
	if root.SelectAttrValue("Destination", "") != s.samlACSURL() {
		return denial{ReasonSAMLChecks}, false
	}
	n := 0
	for _, e := range doc.FindElements("//Assertion") {
		_ = e
		n++
	}
	n += len(doc.FindElements("//EncryptedAssertion"))
	if n != 1 {
		return denial{ReasonSAMLChecks}, false
	}
	return denial{}, true
}

func samlReason(err error) string {
	switch {
	case errors.Is(err, saml.ErrAssertionReplayed):
		return ReasonReplay
	case errors.Is(err, saml.ErrInvalidSignature), errors.Is(err, saml.ErrUntrustedCertificate), errors.Is(err, saml.ErrCertificateNotValidNow):
		return ReasonSignatureInvalid
	}
	var ire *saml.InvalidResponseError
	if errors.As(err, &ire) && ire.PrivateErr != nil {
		return samlReason(ire.PrivateErr)
	}
	return ReasonSAMLInvalid
}

// completeSAML 은 ACS 로 온 응답을 검증해 신원을 돌려준다(세션은 아직 없다).
func (s *Service) completeSAML(ctx context.Context, r *http.Request) (oidcResult, error) {
	if err := r.ParseForm(); err != nil {
		return oidcResult{}, ErrInvalidState
	}
	row, err := s.consumeSAMLRequest(ctx, r.PostFormValue("RelayState"))
	if err != nil {
		return oidcResult{}, err
	}
	res := oidcResult{Purpose: row.Purpose, TestUser: row.TestUser, Redirect: safeRedirect(row.Redirect)}
	res.Identity.Provider = MethodSAML
	sp, info, err := s.samlSP(ctx, row.Purpose)
	if err != nil {
		return oidcResult{}, err
	}
	deny := func(reason string, detail error) (oidcResult, error) {
		if detail != nil {
			log.Printf("auth: SAML 응답 거부(%s): %v", reason, detail)
		}
		d := denial{reason}
		if row.Purpose == purposeTest {
			res.Denied = d
			return res, nil
		}
		s.auditLoginDenied(ctx, MethodSAML, "idp:"+truncate(info.EntityID, 60), d)
		return oidcResult{}, contract.ErrForbidden
	}
	if d, ok := s.preCheckSAML(r.PostFormValue("SAMLResponse")); !ok {
		return deny(d.reason, nil)
	}
	var a *saml.Assertion
	err = runGuarded(func() error {
		var perr error
		a, perr = sp.ParseResponse(r, []string{row.RequestID})
		return perr
	})
	if err != nil {
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) && ire.PrivateErr != nil {
			err = ire.PrivateErr // 라이브러리는 공개 오류를 일부러 뭉뚱그리므로 서버 로그에는 원인을 남긴다(응답 원문·비밀은 아님)
		}
		return deny(samlReason(err), err)
	}
	// 우리 쪽 추가 검사(라이브러리 검사와 겹쳐도 한 번 더 건다). 라이브러리 버그(GO-2026-6909)에 대비해 패닉도 거부로 바꾼다.
	var nameID string
	err = runGuarded(func() error {
		if err := s.checkAssertion(a, info, row.RequestID); err != nil {
			return err
		}
		// NameID 는 라이브러리가 해석한 값을 그대로 믿지 않고, 응답 XML 의 Assertion 요소에서 SAML 2.0 이름공간까지 맞춰 직접 읽어 대조한다.
		direct, derr := readNameID(r.PostFormValue("SAMLResponse"), a.ID)
		if derr != nil {
			return derr
		}
		if direct != strings.TrimSpace(a.Subject.NameID.Value) {
			return errors.New("NameID 가 라이브러리 해석과 응답 XML 에서 다릅니다")
		}
		nameID = normEmail(direct)
		return nil
	})
	if err != nil {
		reason := ReasonSAMLChecks
		if errors.Is(err, errSAMLPanic) {
			reason = ReasonSAMLPanic
		}
		return deny(reason, err)
	}
	res.Identity = Identity{Provider: MethodSAML, Issuer: info.EntityID, Subject: nameID, Email: nameID}
	res.Summary = map[string]string{"이메일": maskEmail(nameID), "IdP": info.EntityID}
	return res, nil
}

// 알려진 취약점(GO-2026-6909, crewjam/saml v0.6.3, 수정 버전 없음): validateAssertion 의 nil 포인터 패닉과 이름공간을 구분하지 않는
// Subject.NameID 해석. 보완: ParseResponse 호출과 우리 쪽 검사를 recover 로 감싸 패닉을 거부로 바꾸고, NameID 를 응답 XML 에서 직접
// 읽어 SAML 2.0 이름공간까지 대조한다. 수정 버전이 나오면 go.mod 의 crewjam/saml 을 올리고 이 보완은 그대로 둔다(심층 방어).
var errSAMLPanic = errors.New("SAML 처리 중 패닉(라이브러리 오류)을 거부로 바꿨습니다")

// ReasonSAMLPanic 은 라이브러리 패닉을 거부로 바꾼 사유 코드다.
const ReasonSAMLPanic = "saml_parser_panic"

const samlAssertionNS = "urn:oasis:names:tc:SAML:2.0:assertion"

// runGuarded 는 fn 의 패닉을 errSAMLPanic 으로 바꾼다.
func runGuarded(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errSAMLPanic, r)
		}
	}()
	return fn()
}

// readNameID 는 응답 XML 에서 ID 가 assertionID 인 SAML 2.0 Assertion 하나를 찾아 그 Subject 의 NameID 값을 읽는다.
// Assertion 은 정확히 하나, Subject 는 하나, SAML 2.0 이름공간의 NameID 는 정확히 하나여야 하고, 다른 이름공간에서 이름만 NameID 인
// 요소가 Subject 안에 끼어 있으면 거부한다.
func readNameID(b64, assertionID string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", errors.New("SAMLResponse 를 해독할 수 없습니다")
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil || doc.Root() == nil {
		return "", errors.New("응답 XML 을 읽을 수 없습니다")
	}
	var assertions []*etree.Element
	for _, e := range doc.FindElements("//Assertion") {
		if e.NamespaceURI() != samlAssertionNS {
			return "", errors.New("SAML 2.0 이름공간이 아닌 Assertion 이 있습니다")
		}
		assertions = append(assertions, e)
	}
	if len(assertions) != 1 || assertions[0].SelectAttrValue("ID", "") != assertionID {
		return "", errors.New("응답 XML 의 Assertion 이 검증된 Assertion 과 맞지 않습니다")
	}
	var subjects []*etree.Element
	for _, c := range assertions[0].ChildElements() {
		if c.Tag == "Subject" {
			if c.NamespaceURI() != samlAssertionNS {
				return "", errors.New("Subject 의 이름공간이 SAML 2.0 이 아닙니다")
			}
			subjects = append(subjects, c)
		}
	}
	if len(subjects) != 1 {
		return "", errors.New("Subject 가 정확히 하나가 아닙니다")
	}
	var ids []*etree.Element
	for _, c := range subjects[0].ChildElements() {
		if c.Tag != "NameID" {
			continue
		}
		if c.NamespaceURI() != samlAssertionNS {
			return "", errors.New("SAML 2.0 이름공간이 아닌 NameID 가 끼어 있습니다")
		}
		ids = append(ids, c)
	}
	if len(ids) != 1 || len(ids[0].ChildElements()) != 0 {
		return "", errors.New("NameID 가 정확히 하나의 글자 값이 아닙니다")
	}
	v := strings.TrimSpace(ids[0].Text())
	if v == "" {
		return "", errors.New("NameID 가 비어 있습니다")
	}
	return v, nil
}

func (s *Service) checkAssertion(a *saml.Assertion, info idpInfo, requestID string) error {
	now := s.now().UTC()
	if a == nil || a.Subject == nil || a.Subject.NameID == nil || strings.TrimSpace(a.Subject.NameID.Value) == "" {
		return errors.New("NameID 가 없습니다")
	}
	switch a.Subject.NameID.Format {
	case "", string(saml.EmailAddressNameIDFormat), string(saml.UnspecifiedNameIDFormat):
	default:
		return fmt.Errorf("NameID 형식이 emailAddress 가 아닙니다: %s", a.Subject.NameID.Format)
	}
	if a.Issuer.Value != info.EntityID {
		return errors.New("발급자가 등록한 IdP 가 아닙니다")
	}
	if a.Conditions == nil {
		return errors.New("Conditions 가 없습니다")
	}
	if !a.Conditions.NotBefore.IsZero() && a.Conditions.NotBefore.After(now.Add(samlSkew)) {
		return errors.New("아직 유효하지 않은 Assertion")
	}
	if a.Conditions.NotOnOrAfter.IsZero() || !now.Add(-samlSkew).Before(a.Conditions.NotOnOrAfter) {
		return errors.New("만료된 Assertion")
	}
	if len(a.Conditions.AudienceRestrictions) == 0 {
		return errors.New("AudienceRestriction 이 없습니다")
	}
	for _, ar := range a.Conditions.AudienceRestrictions {
		if ar.Audience.Value != s.samlEntityID() {
			return errors.New("Audience 가 우리 entityID 가 아닙니다")
		}
	}
	okConfirm := false
	for _, sc := range a.Subject.SubjectConfirmations {
		d := sc.SubjectConfirmationData
		if sc.Method != "urn:oasis:names:tc:SAML:2.0:cm:bearer" || d == nil {
			continue
		}
		if d.Recipient == s.samlACSURL() && d.InResponseTo == requestID && !d.NotOnOrAfter.IsZero() && now.Add(-samlSkew).Before(d.NotOnOrAfter) {
			okConfirm = true
		}
	}
	if !okConfirm {
		return errors.New("bearer SubjectConfirmation(Recipient·InResponseTo·시각)이 맞지 않습니다")
	}
	return nil
}

func xmlBytes(ed *saml.EntityDescriptor) ([]byte, error) {
	b, err := xml.MarshalIndent(ed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}

package taxaspadmin_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/keyrotate"
	"github.com/choibongseok/procure-ledger/internal/taxasp"
	"github.com/choibongseok/procure-ledger/internal/taxasp/mock"
	"github.com/choibongseok/procure-ledger/internal/taxaspadmin"
	"github.com/choibongseok/procure-ledger/migrations"
)

const (
	secKey  = "KEY-SECRET-123456"
	secPass = "PW-SECRET-987654"
	secPath = "private-path-777"
)

// fake 는 Configure 로 받은 설정을 기록하고 ListReceived 에서 지정한 오류를 돌려주는 어댑터다.
type fake struct {
	got *taxasp.Config
	err error
}

func (f *fake) Name() string                      { return "fake" }
func (f *fake) Capabilities() taxasp.Capabilities { return taxasp.Capabilities{ListReceived: true} }
func (f *fake) Configure(_ context.Context, c taxasp.Config) error {
	f.got = &c
	return nil
}
func (f *fake) Issue(context.Context, taxasp.IssueRequest) (taxasp.IssueResult, error) {
	return taxasp.IssueResult{}, nil
}
func (f *fake) Status(context.Context, string) (taxasp.StatusResult, error) {
	return taxasp.StatusResult{}, nil
}
func (f *fake) ListReceived(context.Context, taxasp.ListQuery) (taxasp.ReceivedPage, error) {
	return taxasp.ReceivedPage{}, f.err
}

type env struct {
	t     *testing.T
	db    *sql.DB
	svc   *taxaspadmin.Service
	f     *fake
	clock time.Time
	admin contract.Principal
	key   []byte
}

func newEnv(t *testing.T, production bool) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, f: &fake{}, clock: time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)}
	var uid int64
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('a@x.test','관리자','admin',(SELECT id FROM departments WHERE name='운영')) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	e.admin = contract.Principal{UserID: uid, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	if _, err := db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (?, 'taxasp:manage', 1, 1)`, uid); err != nil {
		t.Fatal(err)
	}
	e.key = bytes.Repeat([]byte{7}, 32)
	e.svc = e.build(e.key, production)
	return e
}

func (e *env) build(key []byte, production bool) *taxaspadmin.Service {
	c, err := acctsecret.New(key)
	if err != nil {
		e.t.Fatal(err)
	}
	reg := taxasp.NewRegistry()
	reg.Register("fake", func() taxasp.Provider { return e.f })
	reg.Register("mock", mock.Factory(mock.NewStore()))
	return taxaspadmin.New(taxaspadmin.Options{DB: e.db, Txs: dbutil.NewTxManager(e.db, dbutil.DialectOf(e.db)), Audit: audit.New(e.db), Sealer: c, Registry: reg,
		AllowLocal: false, Production: production, Now: func() time.Time { return e.clock }})
}

func (e *env) ctx() context.Context { return contract.WithPrincipal(context.Background(), e.admin) }

func (e *env) count(q string, a ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, a...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func certPair(t *testing.T, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "테스트 법인 인증서"}, NotBefore: notAfter.AddDate(-1, 0, 0), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}

func (e *env) saveFake(notAfter time.Time) taxaspadmin.View {
	e.t.Helper()
	cp, kp := certPair(e.t, notAfter)
	v, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "fake", Endpoint: "https://api.asp.example.com/" + secPath, APIKey: secKey, CertPEM: cp, KeyPEM: kp, Password: secPass})
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// AC-4307: 비밀은 봉인으로만 저장되고 화면 응답·감사·알림 어디에도 평문이 없다. 만료일은 평문 날짜 칸이다.
func TestSecretsAreSealedOnlyAndNeverEchoed(t *testing.T) {
	e := newEnv(t, true)
	v := e.saveFake(time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC))
	secrets := []string{secKey, secPass, secPath, "BEGIN CERTIFICATE", "BEGIN EC PRIVATE KEY", "api.asp.example.com/"}
	js, _ := json.Marshal(v)
	if v.Host != "api.asp.example.com" || v.CertNotAt != "2027-03-01" || !v.HasKey || !v.HasCert || v.Active {
		t.Fatalf("요약: %s", js)
	}
	for _, s := range secrets {
		if bytes.Contains(js, []byte(s)) {
			t.Fatalf("응답에 평문 %q", s)
		}
	}
	var dump string
	for _, q := range []string{`SELECT provider || endpoint_hint || secret_sealed || cert_subject_hint FROM tax_asp_settings`,
		`SELECT COALESCE(group_concat(COALESCE(diff,'') || COALESCE(detail,''), '|'),'') FROM audit_logs`} {
		if dbtest.PG() && strings.Contains(q, "group_concat") {
			q = `SELECT COALESCE(string_agg(COALESCE(diff::text,'') || COALESCE(detail,''), '|'),'') FROM audit_logs`
		}
		var part string
		if err := e.db.QueryRow(q).Scan(&part); err != nil {
			t.Fatal(err)
		}
		dump += part
	}
	for _, s := range secrets {
		if strings.Contains(dump, s) {
			t.Fatalf("저장소·감사에 평문 %q", s)
		}
	}
	if !strings.Contains(dump, "enc:v1:") {
		t.Fatal("봉인 접두가 없음")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'taxasp.save'`) != 1 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'taxasp.host_set'`) != 1 {
		t.Fatal("저장·호스트 고정 감사가 한 건씩이어야 한다")
	}
	// 같은 호스트로 다시 저장하면 호스트 고정 감사는 늘지 않는다. 비밀 칸을 비우면 저장된 값을 유지한다.
	if _, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "fake"}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'taxasp.host_set'`) != 1 {
		t.Fatal("호스트가 같으면 고정 감사가 없어야 한다")
	}
	if _, err := e.svc.SetActive(e.ctx(), true); err != nil {
		t.Fatal(err)
	}
	cfg, ok, err := e.svc.Active(context.Background())
	if err != nil || !ok || cfg.APIKey != secKey || cfg.Password != secPass || !strings.Contains(cfg.CertPEM, "CERTIFICATE") {
		t.Fatalf("복호화한 설정: ok=%v err=%v", ok, err)
	}
	// 호스트 고정이 어긋나면(설정 값을 다른 호스트로 바꾼 사고) 연결하지 않는다.
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('taxasp.allow_host','evil.example.com') ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.svc.Active(context.Background()); ok || err == nil {
		t.Fatalf("고정 호스트와 다르면 거부: ok=%v err=%v", ok, err)
	}
}

func TestSaveValidation(t *testing.T) {
	e := newEnv(t, true)
	ctx := e.ctx()
	cp, kp := certPair(t, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	_, otherKey := certPair(t, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	for name, in := range map[string]taxaspadmin.SaveInput{
		"모르는 어댑터":     {Provider: "nope", Endpoint: "https://a.example.com", APIKey: "k"},
		"http 주소":     {Provider: "fake", Endpoint: "http://a.example.com", APIKey: "k"},
		"IP 주소":       {Provider: "fake", Endpoint: "https://10.0.0.5/x", APIKey: "k"},
		"키 없음":        {Provider: "fake", Endpoint: "https://a.example.com"},
		"주소 없음":       {Provider: "fake", APIKey: "k"},
		"인증서 형식":      {Provider: "fake", Endpoint: "https://a.example.com", APIKey: "k", CertPEM: "garbage"},
		"인증서·개인키 불일치": {Provider: "fake", Endpoint: "https://a.example.com", APIKey: "k", CertPEM: cp, KeyPEM: otherKey},
		"개인키만":        {Provider: "fake", Endpoint: "https://a.example.com", APIKey: "k", KeyPEM: kp},
	} {
		if _, err := e.svc.Save(ctx, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), "garbage") {
			t.Errorf("%s: 오류에 입력값이 있음", name)
		}
	}
	if e.count(`SELECT COUNT(*) FROM tax_asp_settings`) != 0 {
		t.Fatal("거부된 저장이 행을 남김")
	}
	if _, err := e.svc.SetActive(ctx, true); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("설정 없이 켜기: %v", err)
	}
}

// AC-4314: 대행사 설정은 taxasp:manage 만. 다른 역할은 거부된다.
func TestOnlyManagerMayTouchSettings(t *testing.T) {
	e := newEnv(t, true)
	fin := contract.Principal{UserID: 99, Role: contract.RoleFinance, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	ctx := contract.WithPrincipal(context.Background(), fin)
	if _, err := e.svc.Get(ctx); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무가 조회: %v", err)
	}
	if _, err := e.svc.Save(ctx, taxaspadmin.SaveInput{Provider: "mock"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무가 저장: %v", err)
	}
	if _, err := e.svc.Test(ctx); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무가 시험: %v", err)
	}
	if _, err := e.svc.Get(context.Background()); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("비로그인: %v", err)
	}
}

func TestExpiredCertificateRefusesIssuing(t *testing.T) {
	e := newEnv(t, true)
	e.saveFake(time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC))
	if _, err := e.svc.SetActive(e.ctx(), true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Ready(context.Background()); err != nil {
		t.Fatalf("유효한 인증서: %v", err)
	}
	e.clock = time.Date(2026, 12, 2, 1, 0, 0, 0, time.UTC) // KST 12-02
	err := e.svc.Ready(context.Background())
	if !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "만료") {
		t.Fatalf("만료 거부: %v", err)
	}
	if _, err := e.svc.SetActive(e.ctx(), true); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("만료된 인증서로 켜기: %v", err)
	}
	v, _ := e.svc.Get(e.ctx())
	if v.CertState != "expired" {
		t.Fatalf("상태: %s", v.CertState)
	}
}

func TestConnectionTestSentenceAndAudit(t *testing.T) {
	e := newEnv(t, true)
	e.saveFake(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	res, err := e.svc.Test(e.ctx())
	if err != nil || !res.OK || e.f.got == nil || e.f.got.APIKey != secKey {
		t.Fatalf("성공 시험: %+v %v", res, err)
	}
	e.f.err = taxasp.Err("auth", 401)
	res, err = e.svc.Test(e.ctx())
	if err != nil || res.OK || !strings.Contains(res.Message, "인증") || strings.Contains(res.Message, "auth") {
		t.Fatalf("실패 시험: %+v %v", res, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'taxasp.test'`) != 2 {
		t.Fatal("시험마다 감사가 남아야 한다")
	}
}

func TestMockWarningInProduction(t *testing.T) {
	prod := newEnv(t, true)
	if _, err := prod.svc.Save(prod.ctx(), taxaspadmin.SaveInput{Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := prod.svc.Get(prod.ctx()); !v.MockWarning {
		t.Fatal("운영에서 모의를 고르면 경고가 있어야 한다")
	}
	dev := newEnv(t, false)
	if _, err := dev.svc.Save(dev.ctx(), taxaspadmin.SaveInput{Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := dev.svc.Get(dev.ctx()); v.MockWarning {
		t.Fatal("개발 모드에서는 경고가 없다")
	}
}

// 인증서 만료 알림은 30일·7일·당일 문턱마다 한 번씩, 본문에 날짜·값 없이 간다(D12).
func TestExpiryNoticesAtThresholds(t *testing.T) {
	e := newEnv(t, true)
	e.saveFake(time.Date(2026, 12, 3, 12, 0, 0, 0, time.UTC)) // KST 12-03
	if _, err := e.svc.SetActive(e.ctx(), true); err != nil {
		t.Fatal(err)
	}
	n := func() int { return e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'tax_cert_expiring'`) }
	ctx := context.Background()
	step := func(day time.Time, want int) {
		t.Helper()
		e.clock = day
		for i := 0; i < 2; i++ { // 같은 문턱에서 다시 돌려도 늘지 않는다
			if err := e.svc.CheckExpiry(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if n() != want {
			t.Fatalf("%s: 알림 %d건, 기대 %d", day.Format("01-02"), n(), want)
		}
	}
	step(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), 0)  // 63일 전
	step(time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC), 1) // 20일 전
	step(time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC), 1) // 13일 전: 같은 문턱
	step(time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC), 2) // 5일 전
	step(time.Date(2026, 12, 3, 1, 0, 0, 0, time.UTC), 3)  // 당일
	var body string
	if err := e.db.QueryRow(`SELECT body FROM notifications WHERE kind = 'tax_cert_expiring' ORDER BY id LIMIT 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "2026") || strings.Contains(body, "api.asp") {
		t.Fatalf("본문: %s", body)
	}
	// 새 인증서로 교체하면 문턱 기록이 새 인증서 기준으로 다시 시작한다.
	e.clock = time.Date(2026, 12, 3, 2, 0, 0, 0, time.UTC)
	cp, kp := certPair(t, time.Date(2027, 12, 3, 0, 0, 0, 0, time.UTC))
	if _, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "fake", CertPEM: cp, KeyPEM: kp}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CheckExpiry(ctx); err != nil || n() != 3 {
		t.Fatalf("교체 뒤: %d %v", n(), err)
	}
}

// 키 교체 뒤에도 대행사 설정을 열어 시험 호출이 된다(조용히 멈추지 않는다, 명세 7절 3항).
func TestRotateAccountKeepsSettingsUsable(t *testing.T) {
	e := newEnv(t, false)
	e.saveFake(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	if _, err := e.svc.SetActive(e.ctx(), true); err != nil {
		t.Fatal(err)
	}
	newKey := bytes.Repeat([]byte{9}, 32)
	if _, err := e.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil && !dbtest.PG() {
		t.Fatal(err)
	}
	res, err := keyrotate.RotateAccount(context.Background(), e.db, e.key, newKey)
	if err != nil || res.Resealed["tax_asp_settings.secret_sealed"] != 1 {
		t.Fatalf("교체: %+v %v", res, err)
	}
	// 옛 키로는 이제 못 풀고 새 키 서비스는 시험 호출이 된다.
	if _, ok, err := e.svc.Active(context.Background()); ok || err == nil {
		t.Fatalf("옛 키로 풀림: ok=%v err=%v", ok, err)
	}
	fresh := e.build(newKey, false)
	e.f.got = nil
	res2, err := fresh.Test(e.ctx())
	if err != nil || !res2.OK || e.f.got == nil || e.f.got.APIKey != secKey {
		t.Fatalf("새 키로 시험 연결: %+v %v", res2, err)
	}
	if _, ok, err := fresh.Active(context.Background()); err != nil || !ok {
		t.Fatalf("새 키 Active: %v %v", ok, err)
	}
}

// 모의 대행사는 비밀이 없어 봉인 없이 저장되고(키 없는 개발 환경), 시험 호출이 된다.
func TestMockNeedsNoSealKey(t *testing.T) {
	e := newEnv(t, false)
	noKey := taxaspadmin.New(taxaspadmin.Options{DB: e.db, Txs: dbutil.NewTxManager(e.db, dbutil.DialectOf(e.db)), Audit: audit.New(e.db), Registry: func() *taxasp.Registry {
		r := taxasp.NewRegistry()
		r.Register("mock", mock.Factory(mock.NewStore()))
		return r
	}(), Now: func() time.Time { return e.clock }})
	if _, err := noKey.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := noKey.SetActive(e.ctx(), true); err != nil {
		t.Fatal(err)
	}
	if res, err := noKey.Test(e.ctx()); err != nil || !res.OK {
		t.Fatalf("모의 시험: %+v %v", res, err)
	}
	if _, err := noKey.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "mock", Endpoint: "https://x.example.com", APIKey: "k"}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("비밀이 있는데 키 없음: %v", err)
	}
}

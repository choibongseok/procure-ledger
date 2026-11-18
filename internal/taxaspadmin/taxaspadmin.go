// Package taxaspadmin 은 전자세금계산서 대행사 설정 저장소·관리 기능이다(B43 1단계 platform, 7절).
//
// 불변식
//   - 엔드포인트 URL·API 키·인증서·개인키·비밀번호는 JSON 한 덩어리로 봉인(용도 tax-asp)해 tax_asp_settings.secret_sealed 에만 둔다.
//     화면·응답·감사·로그·전송 기록·변경 피드 어디에도 평문이 없다. 저장 뒤에는 다시 볼 수 없다(교체만).
//   - 인증서 만료일은 평문 날짜 칸이다. 알림·점검표·발행 전 검사는 풀지 않고 읽는다.
//   - 호스트는 등록 때 하나로 고정한다(taxasp.allow_host). 연결 직전 IP 검사는 어댑터가 netsafe 로 한다.
//   - 이 패키지의 쓰기는 모두 같은 트랜잭션에서 감사를 남긴다. 기록이 실패하면 쓰기도 실패한다.
package taxaspadmin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/taxasp"
)

// SealPurpose 는 봉인 용도다(keyrotate.AccountColumns 의 용도와 같아야 한다).
const SealPurpose = "tax-asp"

// Sealer 는 계좌 키 봉인기(acctsecret.Cipher)다.
type Sealer interface {
	SealFor(purpose, plain string) (string, error)
	OpenFor(purpose, stored string) (string, error)
}

// Options 는 Service 구성이다.
type Options struct {
	DB       *sql.DB
	Txs      contract.TxManager
	Audit    audit.Recorder
	Sealer   Sealer
	Registry *taxasp.Registry
	// AllowLocal 은 루프백 주소(와 http)의 엔드포인트를 허용한다(PL_NOTIFY_INSECURE_LOCAL=1, 로컬 시험 전용).
	AllowLocal bool
	// Production 이면(PL_DEV_IDP 꺼짐) 모의 대행사를 켠 상태에 경고를 보인다.
	Production bool
	// Rehearsal 이면 설정 변경·시험 연결을 거부한다(복구본이 운영 대행사에 나가지 않게, 명세 8.6).
	Rehearsal bool
	// Deliver 는 만료 알림의 메일·웹훅 발송 건을 만든다(notify.Service.Enqueue). 비면 앱 안 알림만 만든다.
	Deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
	// Holders 는 그 법인에서 권한이 효력을 갖는 사용자(알림 수신자)를 고른다(entity.PermissionHolders 를 bootstrap 이 꽂는다. org 를 import 하지 않으려는 주입이다).
	// 비면 권한을 가진 활성 사용자 전원이다(단일 법인 호환).
	Holders func(ctx context.Context, tx contract.Tx, perm contract.Permission, entityID int64, day contract.Day) ([]int64, error)
	Now     func() time.Time
}

// Service 는 대행사 설정을 읽고 쓴다. taxasp.SettingsSource 도 구현한다.
type Service struct {
	o Options
}

var _ taxasp.SettingsSource = (*Service)(nil)

// New 는 Service 를 만든다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

var kst = time.FixedZone("KST", 9*3600)

func (s *Service) now() time.Time { return s.o.Now() }

// ---- 오류 ----

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

func stateErr(msg string) error { return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg) }

// ---- 봉인 덩어리 ----

type blob struct {
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	CertPEM  string `json:"cert_pem,omitempty"`
	KeyPEM   string `json:"key_pem,omitempty"`
	Password string `json:"password,omitempty"`
}

type row struct {
	Provider    string
	Hint        string
	Sealed      string
	CertNotAt   string
	CertSubject string
	Active      bool
	UpdatedAt   string
}

// entityOf 는 요청이 일하는 법인이다(B44). 현재 법인이 정해져 있지 않거나 "전체" 모드면 기본 법인이다(대행사 설정은 법인 하나를 골라서만 다룬다).
func entityOf(ctx context.Context) int64 {
	if a, ok := contract.ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return a.ID
	}
	return contract.DefaultEntityID
}

// settingKey 는 법인별 app_settings 키다. 기본 법인은 이전 키 그대로, 다른 법인은 ".E<번호>" 를 붙인다.
func settingKey(base string, entityID int64) string {
	if entityID == contract.DefaultEntityID {
		return base
	}
	return fmt.Sprintf("%s.E%d", base, entityID)
}

func (s *Service) load(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (row, bool, error) {
	var r row
	var active int
	err := q.QueryRowContext(ctx, `SELECT provider, endpoint_hint, secret_sealed, cert_not_after, cert_subject_hint, active, updated_at FROM tax_asp_settings WHERE entity_id = ?`, entityOf(ctx)).
		Scan(&r.Provider, &r.Hint, &r.Sealed, &r.CertNotAt, &r.CertSubject, &active, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, err
	}
	r.Active = active == 1
	return r, true, nil
}

func (s *Service) open(sealed string) (blob, error) {
	var b blob
	if s.o.Sealer == nil {
		return b, stateErr("대행사 정보를 풀 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없습니다.")
	}
	plain, err := s.o.Sealer.OpenFor(SealPurpose, sealed)
	if err != nil {
		return b, stateErr("저장된 대행사 정보를 풀 수 없습니다. 키 교체 뒤라면 대행사 설정을 다시 저장하세요.")
	}
	if err := json.Unmarshal([]byte(plain), &b); err != nil {
		return blob{}, stateErr("저장된 대행사 정보가 올바르지 않습니다. 대행사 설정을 다시 저장하세요.")
	}
	return b, nil
}

func (s *Service) mockMode(ctx context.Context) string {
	var v string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'taxasp.mock_mode'`).Scan(&v); err != nil {
		return "none"
	}
	if v = strings.TrimSpace(v); v == "" {
		return "none"
	}
	return v
}

func (s *Service) allowHost(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingKey("taxasp.allow_host", entityOf(ctx))).Scan(&v); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// config 는 저장된 행에서 복호화한 설정을 만든다. 호스트 고정이 어긋나면 거부한다.
func (s *Service) config(ctx context.Context, r row) (taxasp.Config, error) {
	if r.Provider == "mock" && r.Sealed == "" {
		return taxasp.Config{Provider: "mock", MockMode: s.mockMode(ctx)}, nil
	}
	b, err := s.open(r.Sealed)
	if err != nil {
		return taxasp.Config{}, err
	}
	cfg := taxasp.Config{Provider: r.Provider, Endpoint: b.Endpoint, APIKey: b.APIKey, CertPEM: b.CertPEM, KeyPEM: b.KeyPEM, Password: b.Password}
	if r.Provider == "mock" {
		cfg.MockMode = s.mockMode(ctx)
		return cfg, nil
	}
	if pin := s.allowHost(ctx, s.o.DB); pin != "" {
		if h := hostOf(b.Endpoint); !strings.EqualFold(h, pin) {
			return taxasp.Config{}, stateErr("대행사 호스트가 고정된 값과 다릅니다. 대행사 설정을 다시 저장하세요.")
		}
	}
	return cfg, nil
}

// Active 는 taxasp.SettingsSource 다: 켜진 설정을 복호화해 돌려준다. 설정이 없거나 꺼져 있으면 ok=false.
func (s *Service) Active(ctx context.Context) (taxasp.Config, bool, error) {
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil || !ok || !r.Active {
		return taxasp.Config{}, false, err
	}
	cfg, err := s.config(ctx, r)
	if err != nil {
		return taxasp.Config{}, false, err
	}
	return cfg, true, nil
}

// Ready 는 서비스의 발행 전 검사(ASPReady)다: 어댑터 등록 확인에 더해 인증서 만료를 평문 날짜로 본다(E2, 풀지 않는다).
func (s *Service) Ready(ctx context.Context) error {
	if err := taxasp.Ready(s, s.o.Registry)(ctx); err != nil {
		return err
	}
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil {
		return err
	}
	if ok && expired(r.CertNotAt, s.now()) {
		return stateErr(domain.TaxCodeLabels["cert_expired"] + " 관리자에게 대행사 인증서 교체를 요청하세요.")
	}
	return nil
}

func hostOf(raw string) string {
	h := strings.TrimSpace(raw)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// expired 는 만료일(YYYY-MM-DD, KST 기준 그날 끝까지 유효)이 지났는지 본다. 빈 값은 만료 없음이다.
func expired(notAfter string, now time.Time) bool {
	d, ok := parseDay(notAfter)
	return ok && now.In(kst).Format("2006-01-02") > d.Format("2006-01-02")
}

func parseDay(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", v, kst)
	return t, err == nil
}

// daysLeft 는 오늘부터 만료일까지 남은 날수다(당일 0, 지나면 음수). 만료일이 없으면 ok=false.
func daysLeft(notAfter string, now time.Time) (int, bool) {
	d, ok := parseDay(notAfter)
	if !ok {
		return 0, false
	}
	today, _ := time.ParseInLocation("2006-01-02", now.In(kst).Format("2006-01-02"), kst)
	return int(d.Sub(today).Hours() / 24), true
}

// ---- 화면용 ----

// View 는 화면에 내는 설정 요약이다. 키·인증서·주소 원문은 없다.
type View struct {
	Configured  bool     `json:"configured"`
	Provider    string   `json:"provider"`
	Providers   []string `json:"providers"`
	Active      bool     `json:"active"`
	Host        string   `json:"host"` // 호스트만(경로·쿼리 없음)
	HasKey      bool     `json:"hasKey"`
	HasCert     bool     `json:"hasCert"`
	CertNotAt   string   `json:"certNotAfter"`
	CertSubject string   `json:"certSubject"`
	CertState   string   `json:"certState"` // none | ok | expiring | expired
	DaysLeft    *int     `json:"daysLeft,omitempty"`
	MockWarning bool     `json:"mockWarning"`
	Rehearsal   bool     `json:"rehearsal"`
	UpdatedAt   string   `json:"updatedAt"`
}

// authorize 는 대행사 설정 관리 권한을 그 요청의 법인에서 본다(다른 법인의 인증서를 건드릴 수 없다).
func (s *Service) authorize(ctx context.Context) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, contract.PermTaxASPManage)
	if err != nil {
		return p, err
	}
	if a, ok := contract.ActiveEntityFrom(ctx); ok && a.All {
		return p, contract.ErrEntityRequired
	}
	if !p.CanIn(contract.PermTaxASPManage, entityOf(ctx)) {
		return p, contract.ErrForbidden
	}
	return p, nil
}

// Get 는 설정 요약을 돌려준다(taxasp:manage).
func (s *Service) Get(ctx context.Context) (View, error) {
	if _, err := s.authorize(ctx); err != nil {
		return View{}, err
	}
	v := View{Providers: s.o.Registry.Names(), CertState: "none", Rehearsal: s.o.Rehearsal}
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil {
		return View{}, err
	}
	if !ok {
		return v, nil
	}
	v.Configured, v.Provider, v.Active, v.Host = true, r.Provider, r.Active, r.Hint
	v.CertNotAt, v.CertSubject, v.UpdatedAt = r.CertNotAt, r.CertSubject, r.UpdatedAt
	v.HasCert = r.CertNotAt != ""
	if b, err := s.open(r.Sealed); err == nil && r.Sealed != "" {
		v.HasKey = b.APIKey != ""
	}
	if n, ok := daysLeft(r.CertNotAt, s.now()); ok {
		v.DaysLeft = &n
		switch {
		case n < 0:
			v.CertState = "expired"
		case n <= 30:
			v.CertState = "expiring"
		default:
			v.CertState = "ok"
		}
	}
	v.MockWarning = r.Provider == "mock" && s.o.Production
	return v, nil
}

// ---- 저장 ----

// SaveInput 은 저장 입력이다. 비밀 칸을 비우면 저장된 값을 유지한다(교체만 가능, 다시 볼 수는 없다).
type SaveInput struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey"`
	CertPEM  string `json:"certPem"`
	KeyPEM   string `json:"keyPem"`
	Password string `json:"password"`
}

const (
	maxKey  = 1024
	maxPEM  = 32 * 1024
	maxPass = 256
)

func validProvider(p string) bool {
	if p == "" || len(p) > 40 {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *Service) urlOptions() netsafe.URLOptions {
	return netsafe.URLOptions{AllowLocal: s.o.AllowLocal}
}

// parseCert 는 인증서 PEM 에서 만료일(KST 날짜)과 주체 끝 부분을 뽑고, 개인키가 함께 오면 짝인지 검사한다. 오류 문장에 값이 없다.
func parseCert(certPEM, keyPEM string) (notAfter, subject string, err error) {
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil || blk.Type != "CERTIFICATE" {
		return "", "", valErr("인증서 형식(PEM)이 올바르지 않습니다.")
	}
	c, perr := x509.ParseCertificate(blk.Bytes)
	if perr != nil {
		return "", "", valErr("인증서를 읽을 수 없습니다.")
	}
	if keyPEM != "" && !strings.Contains(keyPEM, "ENCRYPTED") {
		if _, kerr := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); kerr != nil {
			return "", "", valErr("인증서와 개인키가 서로 맞지 않습니다.")
		}
	}
	name := c.Subject.CommonName
	if name == "" {
		name = c.Subject.String()
	}
	if r := []rune(name); len(r) > 12 {
		name = "…" + string(r[len(r)-12:])
	}
	return c.NotAfter.In(kst).Format("2006-01-02"), name, nil
}

func (s *Service) record(ctx context.Context, tx contract.Tx, p contract.Principal, action string, diff ...audit.DiffItem) error {
	return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetTaxASP, TargetID: fmt.Sprint(entityOf(ctx)), Result: audit.ResultSuccess, Diff: diff, EntityID: entityOf(ctx)})
}

func (s *Service) rehearsalGuard() error {
	if s.o.Rehearsal {
		return stateErr(domain.TaxCodeLabels["rehearsal"])
	}
	return nil
}

// Save 는 대행사 설정을 저장한다. 감사에는 대행사 이름·호스트 변경·인증서 만료일뿐이고 키·인증서·주소 원문은 없다.
func (s *Service) Save(ctx context.Context, in SaveInput) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	if err := s.rehearsalGuard(); err != nil {
		return View{}, err
	}
	prov := strings.ToLower(strings.TrimSpace(in.Provider))
	if !validProvider(prov) {
		return View{}, valErr("대행사 이름을 입력하세요(영문 소문자·숫자·-·_, 40자 이하).")
	}
	if !s.o.Registry.Has(prov) {
		return View{}, valErr("이 서버에 연결된 대행사 어댑터가 아닙니다. 선택 목록에서 고르세요.")
	}
	if len(in.APIKey) > maxKey || len(in.CertPEM) > maxPEM || len(in.KeyPEM) > maxPEM || len(in.Password) > maxPass {
		return View{}, valErr("입력한 값이 너무 깁니다.")
	}
	prev, had, err := s.load(ctx, s.o.DB)
	if err != nil {
		return View{}, err
	}
	var b blob
	if had {
		if old, oerr := s.open(prev.Sealed); oerr == nil { // 풀 수 없으면(키 교체 사고 등) 새로 입력한 값만으로 저장한다
			b = old
		}
	}
	urlChanged := false
	if e := strings.TrimSpace(in.Endpoint); e != "" {
		// 모의 제공자도 같은 주소 규칙이다: 내부 주소(https://127.0.0.1/… 등)는 저장할 수 없다.
		if _, uerr := taxasp.ValidateEndpoint(e, s.o.AllowLocal); uerr != nil {
			return View{}, valErr("%s", strings.TrimPrefix(uerr.Error(), taxasp.ErrEndpoint.Error()+": "))
		}
		urlChanged = e != b.Endpoint
		b.Endpoint = e
	}
	if k := strings.TrimSpace(in.APIKey); k != "" {
		b.APIKey = k
	}
	if pw := in.Password; pw != "" {
		b.Password = pw
	}
	certNotAfter, certSubject := prev.CertNotAt, prev.CertSubject
	if c := strings.TrimSpace(in.CertPEM); c != "" {
		b.CertPEM = c
	}
	if k := strings.TrimSpace(in.KeyPEM); k != "" {
		b.KeyPEM = k
	}
	if strings.TrimSpace(in.CertPEM) != "" || strings.TrimSpace(in.KeyPEM) != "" {
		if b.CertPEM == "" {
			return View{}, valErr("개인키만 따로 저장할 수 없습니다. 인증서(PEM)도 함께 입력하세요.")
		}
		na, sub, perr := parseCert(b.CertPEM, b.KeyPEM)
		if perr != nil {
			return View{}, perr
		}
		certNotAfter, certSubject = na, sub
	}
	if prov != "mock" {
		if b.Endpoint == "" {
			return View{}, valErr("대행사 엔드포인트 주소를 입력하세요.")
		}
		if b.APIKey == "" {
			return View{}, valErr("대행사 API 키를 입력하세요.")
		}
	}
	// 모의 대행사는 비밀이 없다: 봉인할 값이 하나도 없으면 빈 값으로 두어 키(PL_ACCOUNT_SECRET_KEY) 없는 개발 환경에서도 쓸 수 있다.
	sealed := ""
	if b != (blob{}) {
		if s.o.Sealer == nil {
			return View{}, stateErr("대행사 정보를 안전하게 저장할 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없어 저장할 수 없습니다.")
		}
		plain, merr := json.Marshal(b)
		if merr != nil {
			return View{}, merr
		}
		if sealed, err = s.o.Sealer.SealFor(SealPurpose, string(plain)); err != nil {
			return View{}, stateErr("대행사 정보를 봉인하지 못했습니다.")
		}
	}
	host := hostOf(b.Endpoint)
	hint := host
	now := s.now().UTC().Format(time.RFC3339)
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO tax_asp_settings (entity_id, provider, endpoint_hint, secret_sealed, cert_not_after, cert_subject_hint, active, updated_by, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)
			ON CONFLICT(entity_id) DO UPDATE SET provider = excluded.provider, endpoint_hint = excluded.endpoint_hint, secret_sealed = excluded.secret_sealed,
			cert_not_after = excluded.cert_not_after, cert_subject_hint = excluded.cert_subject_hint, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			entityOf(ctx), prov, hint, sealed, certNotAfter, certSubject, nullID(p.UserID), now); e != nil {
			return e
		}
		diff := []audit.DiffItem{{Key: "provider", New: prov}, {Key: "url_changed", New: yesNo(urlChanged || !had)}}
		if certNotAfter != "" {
			diff = append(diff, audit.DiffItem{Key: "cert_not_after", New: certNotAfter})
		}
		if e := s.record(ctx, tx, p, "taxasp.save", diff...); e != nil {
			return e
		}
		if prov == "mock" || host == "" {
			return nil
		}
		return s.pinHost(ctx, tx, p, host, now)
	})
	if err != nil {
		return View{}, err
	}
	return s.Get(ctx)
}

// pinHost 는 호스트 고정 설정(taxasp.allow_host)을 엔드포인트 호스트로 맞춘다. 바뀔 때만 감사 taxasp.host_set 을 남긴다.
func (s *Service) pinHost(ctx context.Context, tx contract.Tx, p contract.Principal, host, now string) error {
	if cur := s.allowHost(ctx, tx); cur == host {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, settingKey("taxasp.allow_host", entityOf(ctx)), host, nullID(p.UserID), now); err != nil {
		return err
	}
	return s.record(ctx, tx, p, "taxasp.host_set", audit.DiffItem{Key: "asp_host", New: host})
}

// SetActive 는 사용을 켜거나 끈다. 켜기는 어댑터가 등록돼 있고 인증서가 만료되지 않았을 때만 된다.
func (s *Service) SetActive(ctx context.Context, on bool) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	if err := s.rehearsalGuard(); err != nil {
		return View{}, err
	}
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil {
		return View{}, err
	}
	if !ok {
		return View{}, stateErr("대행사 설정을 먼저 저장하세요.")
	}
	if on {
		if !s.o.Registry.Has(r.Provider) {
			return View{}, stateErr("설정된 대행사 어댑터를 이 서버에서 쓸 수 없습니다.")
		}
		if expired(r.CertNotAt, s.now()) {
			return View{}, stateErr(domain.TaxCodeLabels["cert_expired"] + " 인증서를 교체한 뒤 켜세요.")
		}
		if _, cerr := s.config(ctx, r); cerr != nil {
			return View{}, cerr
		}
		// 이전에 저장된 주소도 켜기 전에 다시 검사한다(규칙이 생기기 전에 저장된 내부 주소를 막는다).
		if b, oerr := s.open(r.Sealed); oerr == nil && b.Endpoint != "" {
			if _, uerr := taxasp.ValidateEndpoint(b.Endpoint, s.o.AllowLocal); uerr != nil {
				return View{}, stateErr("저장된 대행사 주소를 쓸 수 없습니다. 주소를 다시 저장하세요.")
			}
		}
	}
	now := s.now().UTC().Format(time.RFC3339)
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE tax_asp_settings SET active = ?, updated_by = ?, updated_at = ? WHERE entity_id = ?`, boolInt(on), nullID(p.UserID), now, entityOf(ctx)); e != nil {
			return e
		}
		return s.record(ctx, tx, p, "taxasp.activate", audit.DiffItem{Key: "active", New: yesNo(on)})
	})
	if err != nil {
		return View{}, err
	}
	return s.Get(ctx)
}

// TestResult 는 시험 연결의 결과다. 문장은 한국어 업무 문장이고 대행사 응답 본문이 없다.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Test 는 저장된 설정으로 어댑터를 만들어(Configure) 가벼운 호출 하나를 해 본다. 꺼져 있어도 시험할 수 있다.
func (s *Service) Test(ctx context.Context) (TestResult, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return TestResult{}, err
	}
	if err := s.rehearsalGuard(); err != nil {
		return TestResult{}, err
	}
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil {
		return TestResult{}, err
	}
	if !ok {
		return TestResult{}, stateErr("대행사 설정을 먼저 저장하세요.")
	}
	cfg, err := s.config(ctx, r)
	if err != nil {
		return TestResult{}, err
	}
	code := "ok"
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prov, rerr := s.o.Registry.Resolve(cctx, taxasp.Static{Cfg: cfg, On: true})
	if rerr == nil && prov.Capabilities().ListReceived {
		today := s.now().In(kst).Format("2006-01-02")
		_, rerr = prov.ListReceived(cctx, taxasp.ListQuery{From: today, To: today, Limit: 1})
	}
	if rerr != nil {
		if errors.Is(rerr, taxasp.ErrNotConfigured) {
			code = "not_configured"
		} else {
			code = taxasp.AsError(rerr).Code
		}
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, "taxasp.test", audit.DiffItem{Key: "result_code", New: code})
	})
	if err != nil {
		return TestResult{}, err
	}
	msg := domain.TaxCodeLabels[code]
	if code == "ok" {
		msg = "시험 연결에 성공했습니다."
	} else if msg == "" {
		msg = "시험 연결에 실패했습니다."
	}
	return TestResult{OK: code == "ok", Message: msg}, nil
}

// ---- 만료 알림 ----

// 만료 알림 문턱(남은 날수): 30일·7일·당일(명세 7절 2항, D12).
func noticeBucket(days int) int {
	switch {
	case days <= 0:
		return 0
	case days <= 7:
		return 7
	case days <= 30:
		return 30
	}
	return -1
}

// CheckExpiry 는 켜져 있는 설정의 인증서 만료가 30일·7일·당일 문턱에 들면 taxasp:manage 보유자에게 알린다. 같은 인증서의 같은 문턱은 한 번만 알린다.
// 만료일은 평문 칸이라 비밀을 풀지 않는다. 본문에는 날수와 앱 링크뿐이다.
func (s *Service) CheckExpiry(ctx context.Context) error {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT entity_id FROM tax_asp_settings WHERE active = 1 AND cert_not_after <> '' ORDER BY entity_id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.checkExpiryFor(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: id}), id); err != nil {
			return err
		}
	}
	return nil
}

// checkExpiryFor 는 한 법인의 설정을 점검한다(알림 받는 사람은 그 법인에서 taxasp:manage 가 효력을 갖는 사람).
func (s *Service) checkExpiryFor(ctx context.Context, entityID int64) error {
	r, ok, err := s.load(ctx, s.o.DB)
	if err != nil || !ok || !r.Active || r.CertNotAt == "" {
		return err
	}
	days, ok := daysLeft(r.CertNotAt, s.now())
	if !ok {
		return nil
	}
	bucket := noticeBucket(days)
	if bucket < 0 {
		return nil
	}
	mark := fmt.Sprintf("%s|%d", r.CertNotAt, bucket)
	key := settingKey("taxasp.cert_notice", entityID)
	now := s.now()
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var last string
		if e := tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&last); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if last == mark {
			return nil
		}
		title, body := "대행사 인증서 만료가 가깝습니다", fmt.Sprintf("전자세금계산서 대행사 인증서가 %d일 뒤 만료됩니다. 만료되면 세금계산서를 발행할 수 없으니 인증서를 교체하세요.", days)
		if days <= 0 {
			title, body = "대행사 인증서가 만료되었습니다", "전자세금계산서 대행사 인증서가 만료되어 세금계산서를 발행할 수 없습니다. 인증서를 교체하세요."
		}
		if e := s.notifyManagers(ctx, tx, entityID, title, body, now); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, mark, now.UTC().Format(time.RFC3339))
		return e
	})
}

func (s *Service) notifyManagers(ctx context.Context, tx contract.Tx, entityID int64, title, body string, now time.Time) error {
	var ids []int64
	var err error
	if s.o.Holders != nil {
		ids, err = s.o.Holders(ctx, tx, contract.PermTaxASPManage, entityID, contract.DayOf(now))
	} else {
		ids, err = s.allHolders(ctx, tx)
	}
	if err != nil {
		return err
	}
	for _, uid := range ids {
		nid, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: uid, Kind: "tax_cert_expiring", Title: title, Body: body, Link: "/admin/tax-asp", RefType: "tax_asp", RefID: entityID, Now: now})
		if err != nil {
			return err
		}
		if s.o.Deliver != nil {
			_ = s.o.Deliver(ctx, tx, nid, uid, "tax_cert_expiring", now) // 발송 건 생성 실패가 앱 안 알림을 막지 않는다(Base.Deliver 계약)
		}
	}
	return nil
}

// allHolders 는 Holders 가 꽂히지 않았을 때(단일 법인 시험)의 수신자: taxasp:manage 가 효력인 활성 사용자 전원.
func (s *Service) allHolders(ctx context.Context, tx contract.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ? ORDER BY e.user_id`, string(contract.PermTaxASPManage))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Run 은 한 시간마다 만료 알림을 점검한다. ctx 가 끝나면 돌아온다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		_ = s.CheckExpiry(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

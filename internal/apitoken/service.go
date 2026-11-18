// Package apitoken 는 연동 API 토큰(pli_)의 발급·폐기·인증·속도 제한·감사다(B42 1단계, 6·13절).
//
// 불변식
//   - 토큰 원문은 발급 응답에서 한 번만 나가고 해시(SHA-256)만 저장한다. 목록·감사·로그 어디에도 원문이 없다.
//   - API 주체의 Perms 는 범위 표(apiv1.PermsFor)가 정한다: 발급자의 권한을 물려받지 않고 위험 권한은 어떤 범위로도 붙지 않는다.
//   - 발급·폐기·설정 변경은 로그인 세션만(개인 토큰·SCIM·API 토큰으로는 거부), 발급 시점에 발급자가 선택한 범위의 권한을 모두 가져야 한다(상한).
//   - 인증 실패·거부는 IP 별로 제한하고 감사는 10분당 1행으로 줄인다. 읽기 호출은 토큰·자원·분 단위 요약 1행으로만 남긴다(D15).
package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 토큰 형식·기간.
const (
	Prefix     = "pli_"
	DefaultTTL = 180 * 24 * time.Hour
	MinTTL     = 24 * time.Hour
	MaxTTL     = 365 * 24 * time.Hour
	expiryWarn = 14 * 24 * time.Hour
	maxName    = 80
	maxCIDRs   = 20
)

// 기본 한도.
const (
	DefaultMaxActive     = 5
	DefaultMaxConcurrent = 32
	DefaultRatePerMin    = 600
	MinRatePerMin        = 10
	MaxRatePerMin        = 6000
	DefaultWritePerMin   = 30
	MinWritePerMin       = 1
	MaxWritePerMin       = 600
	authFailPerIP        = 20
	feedPerSecond        = 5
)

const (
	keyEnabled = "api.enabled"
	keyRate    = "api.rate_per_min"
	keyWrite   = "api.write_per_min" // 토큰당 분당 쓰기 호출 한도(쓰기 API 가 읽는다: internal/apiwrite)
)

// Options 는 Service 구성이다.
type Options struct {
	DB            *sql.DB
	Txs           contract.TxManager
	Audit         audit.Recorder
	Now           func() time.Time
	MaxActive     int    // 0 이면 기본(5)
	MaxConcurrent int    // 0 이면 기본(32)
	RatePerMin    int    // 화면 설정이 없을 때의 기본 분당 읽기 한도(0 이면 600)
	AllowCIDR     string // 전역 허용 출발지(쉼표 구분 CIDR·IP), 비면 제한 없음
	Disabled      bool   // 복구 리허설 모드: 모든 API 호출을 거부한다
}

// Service 는 토큰 관리와 인증을 맡는다. apiv1.Authenticator 를 구현한다.
type Service struct {
	db            *sql.DB
	txs           contract.TxManager
	audit         audit.Recorder
	now           func() time.Time
	maxActive     int
	maxConcurrent int
	ratePerMin    int
	globalNets    []*net.IPNet
	disabled      bool

	sem chan struct{}

	mu       sync.Mutex
	ipFails  map[string][]time.Time
	throttle map[string]time.Time
	feedHits map[int64][]time.Time
	agg      map[aggKey]*aggVal
}

var _ apiv1.Authenticator = (*Service)(nil)

// New 는 Service 를 만든다. 전역 허용 출발지 형식이 틀리면 오류다(기동을 거부한다).
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil {
		return nil, errors.New("apitoken: DB·Txs·Audit 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxActive <= 0 {
		o.MaxActive = DefaultMaxActive
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = DefaultMaxConcurrent
	}
	if o.RatePerMin <= 0 {
		o.RatePerMin = DefaultRatePerMin
	}
	nets, err := parseCIDRs(o.AllowCIDR)
	if err != nil {
		return nil, fmt.Errorf("PL_API_ALLOW_CIDR: %w", err)
	}
	return &Service{db: o.DB, txs: o.Txs, audit: o.Audit, now: o.Now, maxActive: o.MaxActive, maxConcurrent: o.MaxConcurrent, ratePerMin: o.RatePerMin,
		globalNets: nets, disabled: o.Disabled, sem: make(chan struct{}, o.MaxConcurrent),
		ipFails: map[string][]time.Time{}, throttle: map[string]time.Time{}, feedHits: map[int64][]time.Time{}, agg: map[aggKey]*aggVal{}}, nil
}

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

type stateError string

func (e stateError) Error() string      { return string(e) }
func (stateError) Is(target error) bool { return target == contract.ErrInvalidState }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func parseCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("올바른 IP 가 아닙니다: %q", part)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			part += "/" + strconv.Itoa(bits)
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("올바른 CIDR 이 아닙니다: %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func inNets(nets []*net.IPNet, ip string) bool {
	if len(nets) == 0 {
		return true
	}
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

const tsFmt = time.RFC3339

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsFmt, s)
	return t
}

// ---- 설정 ----

// Settings 는 연동 관리 화면의 설정 카드다.
type Settings struct {
	Enabled       bool   `json:"enabled"`
	RatePerMin    int    `json:"ratePerMin"`
	WritePerMin   int    `json:"writePerMin"`
	DefaultRate   int    `json:"defaultRate"`
	MaxActive     int    `json:"maxActiveTokens"`
	MaxConcurrent int    `json:"maxConcurrent"`
	GlobalCIDR    bool   `json:"globalCidr"` // 서버 전체 허용 출발지가 설정되어 있는가(값은 보이지 않는다)
	Rehearsal     bool   `json:"rehearsal"`  // 복구 리허설 모드라 API 가 꺼져 있다
	KeepDays      int    `json:"changesKeepDays"`
	DocsPath      string `json:"openapiPath"`
}

func (s *Service) readSetting(ctx context.Context, key string) (string, bool) {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

func (s *Service) enabled(ctx context.Context) bool {
	v, ok := s.readSetting(ctx, keyEnabled)
	return ok && v == "1"
}

func (s *Service) rate(ctx context.Context) int {
	if v, ok := s.readSetting(ctx, keyRate); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= MinRatePerMin && n <= MaxRatePerMin {
			return n
		}
	}
	return s.ratePerMin
}

func (s *Service) authorizeWrite(ctx context.Context, p contract.Principal) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAPIManage); err != nil {
		return err
	}
	if p.Auth == contract.AuthToken || p.Auth == contract.AuthSCIM || p.Auth == contract.AuthAPI {
		return fmt.Errorf("%w: 토큰으로는 연동 토큰을 만들거나 바꿀 수 없습니다(로그인 세션 필요)", contract.ErrForbidden)
	}
	return nil
}

func (s *Service) webActor(p contract.Principal) contract.Principal {
	p.Channel = contract.ChannelWeb
	return p
}

// GetSettings 는 설정을 읽는다(api:manage).
func (s *Service) GetSettings(ctx context.Context, p contract.Principal) (Settings, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAPIManage); err != nil {
		return Settings{}, err
	}
	return Settings{Enabled: s.enabled(ctx), RatePerMin: s.rate(ctx), WritePerMin: s.writeRate(ctx), DefaultRate: s.ratePerMin, MaxActive: s.maxActive, MaxConcurrent: s.maxConcurrent,
		GlobalCIDR: len(s.globalNets) > 0, Rehearsal: s.disabled, KeepDays: s.keepDays(ctx), DocsPath: "/api/v1/openapi.json"}, nil
}

func (s *Service) writeSetting(ctx context.Context, tx contract.Tx, key, value string, by int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, key, value, by, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

// SetEnabled 는 API 를 켜거나 끈다(api:manage, 세션 전용). 감사 api.enabled_set.
func (s *Service) SetEnabled(ctx context.Context, p contract.Principal, on bool) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	old := s.enabled(ctx)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		v := "0"
		if on {
			v = "1"
		}
		if err := s.writeSetting(ctx, tx, keyEnabled, v, p.UserID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: s.webActor(p), Action: "api.enabled_set", TargetType: audit.TargetAPIToken, TargetID: "settings", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "api_enabled", Old: onOff(old), New: onOff(on)}}})
	})
}

func onOff(b bool) string {
	if b {
		return "켬"
	}
	return "끔"
}

func (s *Service) writeRate(ctx context.Context) int {
	if v, ok := s.readSetting(ctx, keyWrite); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= MinWritePerMin && n <= MaxWritePerMin {
			return n
		}
	}
	return DefaultWritePerMin
}

// SetWriteRate 는 토큰당 분당 쓰기 호출 한도를 바꾼다(api:manage, 세션 전용). 읽기 한도와 따로 센다(가져오기·전표 초안 같은 쓰기 호출만). 감사 api.rate_set.
func (s *Service) SetWriteRate(ctx context.Context, p contract.Principal, perMin int) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	if perMin < MinWritePerMin || perMin > MaxWritePerMin {
		return verr("분당 쓰기 한도는 %d~%d 사이여야 합니다", MinWritePerMin, MaxWritePerMin)
	}
	old := s.writeRate(ctx)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.writeSetting(ctx, tx, keyWrite, strconv.Itoa(perMin), p.UserID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: s.webActor(p), Action: "api.rate_set", TargetType: audit.TargetAPIToken, TargetID: "settings", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "api_write_rate", Old: strconv.Itoa(old), New: strconv.Itoa(perMin)}}})
	})
}

// SetRate 는 토큰당 분당 읽기 한도를 바꾼다(api:manage, 세션 전용). 감사 api.rate_set.
func (s *Service) SetRate(ctx context.Context, p contract.Principal, perMin int) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	if perMin < MinRatePerMin || perMin > MaxRatePerMin {
		return verr("분당 한도는 %d~%d 사이여야 합니다", MinRatePerMin, MaxRatePerMin)
	}
	old := s.rate(ctx)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.writeSetting(ctx, tx, keyRate, strconv.Itoa(perMin), p.UserID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: s.webActor(p), Action: "api.rate_set", TargetType: audit.TargetAPIToken, TargetID: "settings", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "api_rate", Old: strconv.Itoa(old), New: strconv.Itoa(perMin)}}})
	})
}

// ---- 토큰 ----

// IssueInput 은 발급 입력이다.
type IssueInput struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Days      int      `json:"days"`      // 0 이면 180
	AllowCIDR string   `json:"allowCidr"` // 쉼표 구분, 비면 전역 설정만
	// EntityID 는 토큰이 묶일 법인이다(B44). 0 이고 Group 이 아니면 발급자의 현재 법인(없으면 기본 법인). Group 이면 모든 법인 읽기(발급자에게 그룹 범위가 있어야 한다).
	EntityID int64 `json:"entityId,omitempty"`
	Group    bool  `json:"group,omitempty"`
}

// TokenInfo 는 화면에 보이는 토큰 한 건이다(원문 없음).
type TokenInfo struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"`
	Scopes        []string   `json:"scopes"`
	CreatedBy     int64      `json:"createdBy"`
	CreatedByName string     `json:"createdByName"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
	AllowCIDR     string     `json:"allowCidr"`
	Status        string     `json:"status"` // active | expired | revoked
	Calls24h      int64      `json:"calls24h"`
	Denied24h     int64      `json:"denied24h"`
	Warnings      []string   `json:"warnings"` // expiring | issuer_left | issuer_reduced
	EntityID      int64      `json:"entityId"` // 0 = 그룹 전체
	EntityCode    string     `json:"entityCode"`
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func normScopes(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if _, ok := apiv1.ScopeByName(n); !ok {
			return nil, verr("알 수 없는 범위입니다: %s", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, verr("범위를 하나 이상 고르세요")
	}
	sort.Strings(out)
	return out, nil
}

// Issue 는 토큰을 발급한다. 원문(raw)은 이 반환값으로만 나가고 저장되지 않는다. 같은 트랜잭션에서 유효 토큰 상한을 확인한다.
func (s *Service) Issue(ctx context.Context, p contract.Principal, in IssueInput) (string, TokenInfo, error) {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return "", TokenInfo{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return "", TokenInfo{}, verr("토큰 이름은 1~%d자여야 합니다", maxName)
	}
	scopes, err := normScopes(in.Scopes)
	if err != nil {
		return "", TokenInfo{}, err
	}
	// 토큰이 묶일 법인(B44, 명세 12절): 발급자가 가진 범위보다 넓은 토큰은 만들 수 없다.
	entityID := in.EntityID
	if in.Group {
		entityID = 0
	} else if entityID <= 0 {
		if entityID = contract.EntityFrom(ctx); entityID <= 0 {
			entityID = contract.DefaultEntityID
		}
	}
	// 발급자 권한 상한(명세 6.4): 자기가 못 보는 데이터를 연동으로 우회해 볼 수 없다.
	for _, n := range scopes {
		def, _ := apiv1.ScopeByName(n)
		for _, perm := range issuerNeeds(def) {
			if !p.Can(perm) || (in.Group && !p.EntitiesFor(perm).All) || (!in.Group && !p.CanIn(perm, entityID)) {
				return "", TokenInfo{}, fmt.Errorf("%w: 발급하는 분이 \"%s\" 범위에 필요한 권한을 가지고 있지 않아 발급할 수 없습니다", contract.ErrForbidden, def.Title)
			}
		}
	}
	ttl := DefaultTTL
	if in.Days != 0 {
		ttl = time.Duration(in.Days) * 24 * time.Hour
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return "", TokenInfo{}, verr("유효 기간은 1일~365일이어야 합니다")
	}
	cidr := strings.TrimSpace(in.AllowCIDR)
	if cidr != "" {
		nets, err := parseCIDRs(cidr)
		if err != nil {
			return "", TokenInfo{}, verr("허용 출발지 형식이 올바르지 않습니다: %v", err)
		}
		if len(nets) > maxCIDRs {
			return "", TokenInfo{}, verr("허용 출발지는 %d개까지입니다", maxCIDRs)
		}
		parts := make([]string, 0, len(nets))
		for _, n := range nets {
			parts = append(parts, n.String())
		}
		cidr = strings.Join(parts, ",")
	}
	body, err := randomString(32)
	if err != nil {
		return "", TokenInfo{}, err
	}
	raw := Prefix + body
	now := s.now().UTC().Truncate(time.Second)
	scopesJSON, _ := json.Marshal(scopes)
	info := TokenInfo{Name: name, Prefix: raw[:len(Prefix)+4], Scopes: scopes, CreatedBy: p.UserID, CreatedByName: p.Name, CreatedAt: now, ExpiresAt: now.Add(ttl), AllowCIDR: cidr, Status: "active", Warnings: []string{}}
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT expires_at FROM api_tokens WHERE revoked_at IS NULL`)
		if err != nil {
			return err
		}
		active := 0
		for rows.Next() {
			var exp string
			if err := rows.Scan(&exp); err != nil {
				rows.Close()
				return err
			}
			if now.Before(parseTS(exp)) {
				active++
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if active >= s.maxActive {
			return stateError(fmt.Sprintf("유효한 API 토큰은 %d개까지 만들 수 있습니다. 쓰지 않는 토큰을 폐기한 뒤 다시 발급하세요(교체할 때는 새 토큰을 연동에 적용한 다음 옛 토큰을 폐기합니다)", s.maxActive))
		}
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at, allow_cidr, entity_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			name, info.Prefix, sha256Hex(raw), string(scopesJSON), p.UserID, now.Format(tsFmt), info.ExpiresAt.Format(tsFmt), cidr, entityID)
		if err != nil {
			return err
		}
		info.ID = id
		info.EntityID = entityID
		info.EntityCode = s.entityCode(ctx, tx, entityID)
		diff := []audit.DiffItem{{Key: "api_token_name", New: name}, {Key: "api_scopes", New: strings.Join(scopes, ", ")}, {Key: "api_expires_at", New: info.ExpiresAt.Format(tsFmt)}}
		if cidr != "" {
			diff = append(diff, audit.DiffItem{Key: "api_allow_cidr", New: cidr})
		}
		diff = append(diff, audit.DiffItem{Key: "api_entity", New: info.EntityCode})
		return s.audit.Record(ctx, tx, audit.Entry{Actor: s.webActor(p), Action: "api.token_issue", TargetType: audit.TargetAPIToken, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess, Diff: diff, EntityID: entityAuditID(entityID)})
	})
	if err != nil {
		return "", TokenInfo{}, err
	}
	return raw, info, nil
}

// List 는 토큰 목록이다(최신순, 폐기된 것 포함). 최근 24시간 호출·거부 수와 경고(만료 임박·발급자 퇴사·발급자 권한 축소)를 함께 준다.
func (s *Service) List(ctx context.Context, p contract.Principal) ([]TokenInfo, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAPIManage); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.name, t.token_prefix, t.scopes, t.created_by, COALESCE(u.name,''), COALESCE(u.active,0), t.created_at, t.expires_at, t.revoked_at, t.last_used_at, t.allow_cidr, t.entity_id
		FROM api_tokens t LEFT JOIN users u ON u.id = t.created_by ORDER BY t.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	type raw struct {
		info   TokenInfo
		active bool
	}
	var list []raw
	now := s.now().UTC()
	for rows.Next() {
		var t TokenInfo
		var scopes, created, expires string
		var revoked, used sql.NullString
		var active int
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &t.CreatedBy, &t.CreatedByName, &active, &created, &expires, &revoked, &used, &t.AllowCIDR, &t.EntityID); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopes), &t.Scopes)
		t.CreatedAt, t.ExpiresAt = parseTS(created), parseTS(expires)
		if revoked.Valid {
			v := parseTS(revoked.String)
			t.RevokedAt = &v
		}
		if used.Valid {
			v := parseTS(used.String)
			t.LastUsedAt = &v
		}
		t.Status = "active"
		if t.RevokedAt != nil {
			t.Status = "revoked"
		} else if !now.Before(t.ExpiresAt) {
			t.Status = "expired"
		}
		t.Warnings = []string{}
		list = append(list, raw{t, active == 1})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	minNow := now.Unix() / 60
	out := make([]TokenInfo, 0, len(list))
	permCache := map[int64]contract.PermSet{}
	for _, r := range list {
		t := r.info
		t.EntityCode = s.entityCode(ctx, s.db, t.EntityID)
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(calls),0), COALESCE(SUM(denied),0) FROM api_rate_windows WHERE token_id = ? AND minute >= ?`, t.ID, minNow-1440).Scan(&t.Calls24h, &t.Denied24h)
		if t.Status == "active" {
			if t.ExpiresAt.Sub(now) <= expiryWarn {
				t.Warnings = append(t.Warnings, "expiring")
			}
			if !r.active {
				t.Warnings = append(t.Warnings, "issuer_left")
			} else {
				ps, ok := permCache[t.CreatedBy]
				if !ok {
					ps, err = access.PermsOf(ctx, s.db, t.CreatedBy)
					if err != nil {
						return nil, err
					}
					permCache[t.CreatedBy] = ps
				}
			scopeLoop:
				for _, n := range t.Scopes {
					if def, ok := apiv1.ScopeByName(n); ok {
						for _, perm := range def.Perms {
							if !ps.Has(perm) {
								t.Warnings = append(t.Warnings, "issuer_reduced")
								break scopeLoop
							}
						}
					}
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// Revoke 는 토큰을 즉시 폐기한다(api:manage, 세션 전용). 처리 중인 요청은 끝까지 간다. 감사 api.token_revoke.
func (s *Service) Revoke(ctx context.Context, p contract.Principal, id int64) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var name, scopes string
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT name, scopes, revoked_at FROM api_tokens WHERE id = ?`, id).Scan(&name, &scopes, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if revoked.Valid {
			return stateError("이미 폐기된 토큰입니다")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ?`, s.now().UTC().Format(tsFmt), id); err != nil {
			return err
		}
		var sc []string
		_ = json.Unmarshal([]byte(scopes), &sc)
		return s.audit.Record(ctx, tx, audit.Entry{Actor: s.webActor(p), Action: "api.token_revoke", TargetType: audit.TargetAPIToken, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "api_token_name", New: name}, {Key: "api_scopes", New: strings.Join(sc, ", ")}}})
	})
}

// ScopeInfo 는 발급 화면의 범위 선택지다.
type ScopeInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Held    bool   `json:"held"`    // 발급하는 사람이 이 범위에 필요한 권한을 모두 가졌는가(내부 권한은 따지지 않는다)
	Write   bool   `json:"write"`   // 쓰기 범위(가져오기·전표 초안): 읽기 범위보다 위험하다
	Note    string `json:"note"`    // 이 범위를 고르면 알아야 할 점(쓰기 범위의 안내)
	Warning string `json:"warning"` // 발급하는 사람에게 해당하는 경고(전표 초안 + 승인 권한 겸임 등). 없으면 ""
}

const (
	scopeJournalDraft = apiv1.ScopeJournalsDraft
	noteImport        = "가져오기(미리보기 후 반영)로 마스터 데이터를 만들고 바꿉니다. 화면 가져오기와 같은 규칙(중복 거부 등)을 받고 반영은 감사에 남지만, 바뀐 데이터를 되돌리려면 화면에서 직접 고쳐야 합니다. 꼭 필요한 연동에만 고르세요."
	noteDraft         = "전표 초안만 만들 수 있고 승인 요청·승인은 못 합니다. 초안의 작성자는 이 토큰을 발급한 분으로 기록됩니다. 승인은 재무가 화면에서 검토해 전자결재로 합니다."
)

// scopeNote 는 쓰기 범위의 안내 문구다(읽기 범위는 "").
func scopeNote(name string) string {
	switch {
	case name == scopeJournalDraft:
		return noteDraft
	case strings.HasSuffix(name, ":write"):
		return noteImport
	}
	return ""
}

// draftWarning 은 전표 초안 범위를 발급하는 사람에게 해당하는 경고다(B41 jv.create_approve): 초안의 작성자는 발급자로 기록되므로 발급자가 전표를 승인하는 사람이면
// 같은 사람이 작성과 승인을 겸하게 된다. 전표 승인에 관여하는 권한(ledger:write·approval:finance_stage)을 가졌으면 규칙 모드에 따라 안내한다(꺼져 있으면 경고 없음).
func (s *Service) draftWarning(ctx context.Context, p contract.Principal) string {
	if !p.Can(contract.PermLedgerWrite) && !p.Can(contract.PermFinanceStage) {
		return ""
	}
	r, ok := sod.RuleByCode(sod.RuleJVCreateApprove)
	if !ok {
		return ""
	}
	switch sod.ModeOf(ctx, s.db, r) {
	case sod.ModeBlock:
		return "발급하는 분이 전표를 승인하는 권한도 가지고 있습니다. 이 토큰이 만든 전표 초안은 작성자가 발급하는 분이라 그분이 승인할 수 없습니다(권한 분리 규칙 차단). 승인할 다른 재무 담당이 있는지 확인하세요."
	case sod.ModeWarn:
		return "발급하는 분이 전표를 승인하는 권한도 가지고 있습니다. 이 토큰이 만든 전표 초안은 작성자가 발급하는 분이라, 그분이 승인하면 권한 분리 위반으로 기록되고 감사인에게 알려집니다. 승인은 다른 담당이 하도록 하거나 전표 승인 권한이 없는 관리자가 발급하세요."
	}
	return ""
}

// IssueWarnings 는 선택한 범위와 발급자에 대한 경고 문장들이다(발급은 막지 않는다). 발급 응답과 화면이 쓴다.
func (s *Service) IssueWarnings(ctx context.Context, p contract.Principal, scopes []string) []string {
	out := []string{}
	for _, n := range scopes {
		if n == scopeJournalDraft {
			if w := s.draftWarning(ctx, p); w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

// Scopes 는 범위 목록과 발급자의 보유 여부·안내·경고를 준다(api:manage).
func (s *Service) Scopes(ctx context.Context, p contract.Principal) ([]ScopeInfo, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAPIManage); err != nil {
		return nil, err
	}
	out := make([]ScopeInfo, 0, len(apiv1.Scopes))
	for _, d := range apiv1.Scopes {
		held := true
		for _, perm := range issuerNeeds(d) {
			if !p.Can(perm) {
				held = false
			}
		}
		info := ScopeInfo{Name: d.Name, Title: d.Title, Held: held, Write: d.Write, Note: scopeNote(d.Name)}
		if d.Name == scopeJournalDraft {
			info.Warning = s.draftWarning(ctx, p)
		}
		out = append(out, info)
	}
	return out, nil
}

// issuerNeeds 는 범위를 발급하는 사람이 가져야 하는 권한이다: 범위의 Perms 중 내부 권한이 아닌 것(내부 권한은 누구도 갖지 않고 범위가 주는 것이다) + 범위가 정한 IssuerRequires(사람 권한).
func issuerNeeds(d apiv1.ScopeDef) []contract.Permission {
	var out []contract.Permission
	for _, perm := range d.Perms {
		if !isInternalPerm(perm) {
			out = append(out, perm)
		}
	}
	return append(out, d.IssuerRequires...)
}

func isInternalPerm(perm contract.Permission) bool {
	for _, x := range contract.InternalPermissions {
		if x == perm {
			return true
		}
	}
	return false
}

// entityCode 는 토큰이 묶인 법인의 코드다(0 이면 "그룹 전체").
func (s *Service) entityCode(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, entityID int64) string {
	if entityID == 0 {
		return "그룹 전체"
	}
	var code string
	if err := q.QueryRowContext(ctx, `SELECT code FROM entities WHERE id = ?`, entityID).Scan(&code); err != nil {
		return ""
	}
	return code
}

// entityAuditID 는 토큰 발급 감사의 법인이다(그룹 토큰은 법인과 무관한 사건이라 0 → 감사가 요청의 현재 법인을 쓰지 않도록 NoEntity).
func entityAuditID(entityID int64) int64 {
	if entityID == 0 {
		return audit.NoEntity
	}
	return entityID
}

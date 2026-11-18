// Package scim 은 IdP(Entra ID·Okta 등)가 SCIM 2.0 으로 보내는 사용자 변경을 기존 인사 발령 경로로 옮기는 입구다(B40).
//
// 원칙: SCIM 은 새로운 쓰기 경로가 아니다. 입사·퇴사는 org.Service.RegisterBatch 로 들어가 화면 발령과 같은 검증·이력·투영·감사를 거친다.
// 요청을 발령으로 옮길 때의 행위자(Principal)는 토큰 발급자의 사용자 번호를 달되 권한은 org:manage·scim:provision 뿐이라
// 어떤 요청도 위험 권한을 새로 만들 수 없다(newActor, 소스 검사 시험이 UserID 0 을 막는다).
package scim

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const (
	// DefaultTokenTTL·MinTokenTTL·MaxTokenTTL 은 연동 토큰 유효 기간이다(명세 6.2).
	DefaultTokenTTL = 180 * 24 * time.Hour
	MinTokenTTL     = 24 * time.Hour
	MaxTokenTTL     = 365 * 24 * time.Hour

	tokenPrefix = "pls_"
	timeLayout  = time.RFC3339Nano

	maxBody       = 1 << 20 // 요청 본문 1MiB
	maxCount      = 200
	defaultCount  = 100
	maxFilterLen  = 1024
	authFailPerIP = 20
)

// 출처(IdP 종류)와 범위.
var (
	Sources = []string{"entra", "okta", "google", "other"}
	Scopes  = []string{"users", "users_groups"}
)

// Options 는 Service 구성이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Org   *org.Service
	// Access 는 그룹→역할 연동의 부여·회수(SCIMGrant·SCIMRevoke)에 쓴다. nil 이면 역할 모드를 저장할 수 없다.
	Access *access.Service
	Now    func() time.Time // 시험이 시계를 주입한다. 비면 time.Now
	// BaseURL 은 서버의 접속 주소(PL_BASE_URL)다. 화면의 연결 주소·Location 헤더에 쓴다.
	BaseURL string
	// MaxActiveTokens 는 같은 출처의 유효 토큰 상한이다(0 이면 상한 없음).
	MaxActiveTokens int
	// RatePerMin 은 토큰당 분당 호출 한도다(0 이면 끔).
	RatePerMin int
	// LogKeepDays 는 동기화 기록 보존 일수다(0 이하면 정리하지 않는다).
	LogKeepDays int
	// AllowCIDR 은 SCIM 요청을 받을 출발지(쉼표로 구분한 CIDR 또는 IP)다. 비면 제한 없음.
	AllowCIDR string
	// Disabled 는 복구 리허설 모드처럼 SCIM 을 받으면 안 되는 환경이다(모든 요청 403).
	Disabled bool
}

// Service 는 SCIM 서버와 연동 관리다.
type Service struct {
	db       *sql.DB
	txs      contract.TxManager
	audit    audit.Recorder
	org      *org.Service
	access   *access.Service
	now      func() time.Time
	baseURL  string
	maxToken int
	rate     int
	keepDays int
	disabled bool
	allow    []*net.IPNet

	mu       sync.Mutex
	hits     map[int64][]time.Time // 토큰별 최근 1분 호출 시각
	ipFails  map[string][]time.Time
	lastAud  map[string]time.Time // 감사 남발 방지: 키별 마지막 기록 시각
	lastTick time.Time
}

// New 는 Service 를 만든다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil || o.Org == nil {
		return nil, errors.New("scim: DB·Txs·Audit·Org 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	allow, err := parseAllow(o.AllowCIDR)
	if err != nil {
		return nil, err
	}
	return &Service{allow: allow, db: o.DB, txs: o.Txs, audit: o.Audit, org: o.Org, access: o.Access, now: o.Now, baseURL: strings.TrimRight(o.BaseURL, "/"),
		maxToken: o.MaxActiveTokens, rate: o.RatePerMin, keepDays: o.LogKeepDays, disabled: o.Disabled,
		hits: map[int64][]time.Time{}, ipFails: map[string][]time.Time{}, lastAud: map[string]time.Time{}}, nil
}

// BaseEndpoint 는 IdP 에 입력할 SCIM 기본 주소다(화면 표시용).
func (s *Service) BaseEndpoint() string { return s.baseURL + "/scim/v2" }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) stamp() string { return s.now().UTC().Format(timeLayout) }

func parseTime(v string) time.Time {
	t, err := time.Parse(timeLayout, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	return &t
}

// newActor 는 SCIM 요청을 발령으로 옮길 때 쓰는 행위자다(명세 5.2). UserID 는 토큰 발급자라서 0 이 아니고(0 이면 org 의 위험 권한 게이트가 꺼진다),
// Perms 는 org:manage 와 scim:provision 만이라 role:manage 가 없다. 발급자가 관리자여도 그 권한을 물려받지 않는다.
func newActor(t tokenRow) contract.Principal {
	return contract.Principal{
		UserID: t.CreatedBy, Name: "SCIM(" + t.Name + ")", Role: "scim",
		Channel: contract.ChannelSCIM, Auth: contract.AuthSCIM, TokenID: t.ID,
		Perms: contract.NewPermSet(contract.PermOrgManage, contract.PermSCIMProvision),
	}
}

// record 는 감사 한 건을 남긴다(tx 안, 실패하면 호출자가 롤백한다).
func (s *Service) record(ctx context.Context, tx contract.Tx, actor contract.Principal, action string, tt audit.TargetType, id string, res audit.Result, diff []audit.DiffItem, detail string) error {
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: tt, TargetID: id, Result: res, Diff: diff, Detail: detail})
}

// auditThrottled 는 같은 키의 감사를 간격 안에서는 한 번만 남긴다(거부·인증 실패가 감사 체인을 불리지 않게). 실패는 서버 로그로만 남긴다.
func (s *Service) auditThrottled(ctx context.Context, key string, every time.Duration, actor contract.Principal, action string, tt audit.TargetType, id string, res audit.Result, diff []audit.DiffItem, detail string) {
	now := s.now()
	s.mu.Lock()
	if last, ok := s.lastAud[key]; ok && now.Sub(last) < every {
		s.mu.Unlock()
		return
	}
	s.lastAud[key] = now
	if len(s.lastAud) > 4096 {
		s.lastAud = map[string]time.Time{key: now}
	}
	s.mu.Unlock()
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.txs.WithTx(ctx2, func(tx contract.Tx) error {
		return s.record(ctx2, tx, actor, action, tt, id, res, diff, detail)
	}); err != nil {
		logf("scim: 감사 기록 실패(%s): %v", action, err)
	}
}

// Run 은 보존 기간이 지난 동기화 기록을 한 시간마다 정리한다(서버만 돈다). 감사 로그에는 영향이 없다.
func (s *Service) Run(ctx context.Context) {
	if s.keepDays <= 0 {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.purge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) purge(ctx context.Context) {
	cut := s.now().UTC().AddDate(0, 0, -s.keepDays).Format(timeLayout)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM scim_sync_log WHERE at < ?`, cut); err != nil {
		logf("scim: 동기화 기록 정리 실패: %v", err)
	}
}

// Register 는 SCIM 엔드포인트와 관리 API 를 선언과 함께 등록한다.
func (s *Service) Register(r contract.Registrar) {
	s.registerSCIM(r)
	s.registerAdmin(r)
}

// Wrap 은 /scim/v2 경로에 SCIM 토큰 인증을 거는 미들웨어다. 다른 경로는 건드리지 않는다(호출하는 쪽이 경로를 나눠 부른다).
func (s *Service) Wrap(next http.Handler) http.Handler { return s.authMiddleware(next) }

// IsSCIMPath 는 SCIM 엔드포인트 경로인지다. cmd/server 의 조립이 인증 미들웨어를 나눌 때 쓴다.
func IsSCIMPath(p string) bool { return p == "/scim/v2" || strings.HasPrefix(p, "/scim/v2/") }

// parseAllow 는 PL_SCIM_ALLOW_CIDR 을 해석한다. 비어 있으면 제한 없음이고, 형식이 틀리면 기동을 거부한다.
func parseAllow(v string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("PL_SCIM_ALLOW_CIDR 의 %q 을(를) 해석할 수 없습니다(CIDR 또는 IP)", part)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("PL_SCIM_ALLOW_CIDR 의 %q 을(를) 해석할 수 없습니다(CIDR 또는 IP)", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Service) ipAllowed(ip string) bool {
	if len(s.allow) == 0 {
		return true
	}
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range s.allow {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

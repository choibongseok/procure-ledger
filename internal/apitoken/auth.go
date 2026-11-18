package apitoken

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// observation 은 한 요청의 인증 결과를 인증기(AuthenticateAPI)와 관찰 미들웨어(Wrap)가 주고받는 칸이다.
type obsKey struct{}

type observation struct {
	mu       sync.Mutex
	tokenID  int64
	issuer   int64
	name     string
	reported bool // 인증기가 이미 거부 감사를 남겼다(관찰 미들웨어가 중복으로 남기지 않는다)
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// ErrDisabled 는 API 가 꺼져 있다는 뜻이다(403). 토큰을 확인한 뒤에만 알려 토큰 존재 여부를 노출하지 않는다.
var ErrDisabled = errors.New("연동 API 가 꺼져 있습니다")

func (e disabledError) Is(target error) bool { return target == contract.ErrForbidden }

type disabledError struct{}

func (disabledError) Error() string { return ErrDisabled.Error() }

type tokenRow struct {
	ID        int64
	Name      string
	Scopes    []string
	CreatedBy int64
	AllowNets string
	EntityID  int64 // 0 = 그룹 전체
}

func tokenPrincipal(t tokenRow, perms contract.PermSet) contract.Principal {
	p := contract.Principal{UserID: t.CreatedBy, Name: "API(" + t.Name + ")", Role: "api", Channel: contract.ChannelAPI, Auth: contract.AuthAPI, TokenID: t.ID, Perms: perms}
	if perms != nil {
		// 토큰의 모든 권한은 토큰이 묶인 법인에서만 효력이다(0 이면 그룹 전체). 계산된 EntityPerms 라 서비스의 법인 판정이 그대로 걸린다.
		set := contract.NewEntitySet(t.EntityID)
		if t.EntityID == 0 {
			set = contract.EntitySet{All: true}
		} else {
			p.OwnEntities = []int64{t.EntityID}
		}
		p.EntityPerms = map[contract.Permission]contract.EntitySet{}
		for perm := range perms {
			p.EntityPerms[perm] = set
		}
	}
	return p
}

func anonPrincipal() contract.Principal {
	return contract.Principal{Role: "api", Name: "API", Channel: contract.ChannelAPI, Auth: contract.AuthAPI}
}

// rec 은 감사 한 건을 짧은 별도 트랜잭션에 남긴다. 요청 취소와 무관하게 남기고 실패는 로그만 남긴다(거부 응답은 그대로 간다).
func (s *Service) rec(ctx context.Context, actor contract.Principal, action, id string, result audit.Result, diff []audit.DiffItem, detail string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: audit.TargetAPIToken, TargetID: id, Result: result, Diff: diff, Detail: detail})
	})
	if err != nil {
		log.Printf("apitoken: 감사 기록 실패(%s): %v", action, err)
	}
}

// recThrottled 는 같은 key 의 감사를 ttl 에 한 번만 남긴다(인증 실패·거부 폭주가 감사 체인을 채우지 않게).
func (s *Service) recThrottled(ctx context.Context, key string, ttl time.Duration, actor contract.Principal, action, id string, diff []audit.DiffItem, detail string) {
	now := s.now()
	s.mu.Lock()
	if last, ok := s.throttle[key]; ok && now.Sub(last) < ttl {
		s.mu.Unlock()
		return
	}
	if len(s.throttle) > 4096 {
		s.throttle = map[string]time.Time{}
	}
	s.throttle[key] = now
	s.mu.Unlock()
	s.rec(ctx, actor, action, id, audit.ResultDenied, diff, detail)
}

func (s *Service) ipBlocked(ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.ipFails[ip]
	cut := now.Add(-time.Minute)
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	s.ipFails[ip] = h[i:]
	return len(h[i:]) >= authFailPerIP
}

func (s *Service) ipFail(ip string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ipFails) > 4096 {
		s.ipFails = map[string][]time.Time{}
	}
	s.ipFails[ip] = append(s.ipFails[ip], now)
}

func (s *Service) lookup(ctx context.Context, raw string, now time.Time) (tokenRow, bool, error) {
	var t tokenRow
	var scopes, expires string
	var revoked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, scopes, created_by, expires_at, revoked_at, allow_cidr, entity_id FROM api_tokens WHERE token_hash = ?`, sha256Hex(raw)).
		Scan(&t.ID, &t.Name, &scopes, &t.CreatedBy, &expires, &revoked, &t.AllowNets, &t.EntityID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	if revoked.Valid || !now.Before(parseTS(expires)) {
		return t, false, nil
	}
	if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
		return t, false, err
	}
	return t, true, nil
}

// AuthenticateAPI 는 /api/v1 요청의 Bearer 토큰(pli_)을 확인한다(apiv1.Authenticator). 쿠키·세션·개인 토큰은 인정하지 않는다.
// 실패는 401(토큰 없음·모름·폐기·만료), 403(꺼짐·허용 출발지 밖·리허설), 429(인증 실패 폭주·분당 한도)다.
func (s *Service) AuthenticateAPI(ctx context.Context, r *http.Request) (apiv1.Caller, error) {
	obs, _ := ctx.Value(obsKey{}).(*observation)
	mark := func() {
		if obs != nil {
			obs.mu.Lock()
			obs.reported = true
			obs.mu.Unlock()
		}
	}
	if s.disabled { // 복구 리허설 모드: 복구본이 운영 연동 시스템과 이어지는 사고를 막는다
		return apiv1.Caller{}, disabledError{}
	}
	now := s.now().UTC()
	ip := clientIP(r)
	if !inNets(s.globalNets, ip) { // 전역 허용 출발지 밖: 토큰을 확인하기 전에 거부한다
		mark()
		s.recThrottled(ctx, "ip|"+ip, 10*time.Minute, anonPrincipal(), "api.denied", "route", []audit.DiffItem{{Key: "api_reason", New: "ip_not_allowed"}}, "허용되지 않은 출발지")
		return apiv1.Caller{}, contract.ErrForbidden
	}
	if s.ipBlocked(ip, now) {
		mark()
		return apiv1.Caller{}, apiv1.ErrRateLimited{RetryAfterSeconds: 60}
	}
	fail := func(reason string) (apiv1.Caller, error) {
		mark()
		s.ipFail(ip, now)
		s.recThrottled(ctx, "auth|"+ip, 10*time.Minute, anonPrincipal(), "api.auth_failed", "route", []audit.DiffItem{{Key: "api_reason", New: reason}}, "")
		return apiv1.Caller{}, contract.ErrUnauthenticated
	}
	raw, present := bearer(r)
	if !present || !strings.HasPrefix(raw, Prefix) {
		return fail("invalid_token")
	}
	tok, ok, err := s.lookup(ctx, raw, now)
	if err != nil {
		log.Printf("apitoken: 토큰 확인 실패: %v", err)
		return apiv1.Caller{}, err
	}
	if !ok {
		return fail("invalid_token")
	}
	if obs != nil {
		obs.mu.Lock()
		obs.tokenID, obs.issuer, obs.name = tok.ID, tok.CreatedBy, tok.Name
		obs.mu.Unlock()
	}
	actor := tokenPrincipal(tok, nil)
	if tok.AllowNets != "" {
		nets, _ := parseCIDRs(tok.AllowNets)
		if !inNets(nets, ip) {
			mark()
			s.recThrottled(ctx, "tip|"+strconv.FormatInt(tok.ID, 10), 10*time.Minute, actor, "api.denied", strconv.FormatInt(tok.ID, 10), []audit.DiffItem{{Key: "api_reason", New: "ip_not_allowed"}}, "토큰 허용 출발지 밖")
			return apiv1.Caller{}, contract.ErrForbidden
		}
	}
	if !s.enabled(ctx) { // 켜짐 여부는 토큰 확인 뒤에 판단한다(존재 노출 방지)
		mark()
		s.recThrottled(ctx, "off|"+strconv.FormatInt(tok.ID, 10), 10*time.Minute, actor, "api.denied", strconv.FormatInt(tok.ID, 10), []audit.DiffItem{{Key: "api_reason", New: "api_disabled"}}, "API 꺼짐")
		return apiv1.Caller{}, disabledError{}
	}
	if wait := s.feedWait(tok.ID, r.URL.Path, now); wait > 0 { // 변경 피드는 폴링 폭주를 막으려고 토큰당 초당 5회
		mark()
		return apiv1.Caller{}, apiv1.ErrRateLimited{RetryAfterSeconds: 1}
	}
	if limited, wait, first := s.countCall(ctx, tok.ID, now); limited {
		mark()
		if first {
			s.rec(ctx, actor, "api.rate_limited", strconv.FormatInt(tok.ID, 10), audit.ResultDenied, []audit.DiffItem{{Key: "api_reason", New: "rate_limited"}}, "분당 호출 한도 초과")
		}
		return apiv1.Caller{}, apiv1.ErrRateLimited{RetryAfterSeconds: wait}
	}
	perms, err := apiv1.PermsFor(tok.Scopes)
	if err != nil {
		return apiv1.Caller{}, err
	}
	return apiv1.Caller{Principal: tokenPrincipal(tok, perms), Scopes: tok.Scopes, TokenID: tok.ID, EntityID: tok.EntityID}, nil
}

// feedWait 는 변경 피드 경로의 초당 한도다. 넘으면 기다릴 시간(>0)을 돌려준다.
func (s *Service) feedWait(id int64, path string, now time.Time) time.Duration {
	if !strings.HasPrefix(path, "/api/v1/changes") {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := now.Add(-time.Second)
	h := s.feedHits[id]
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	h = h[i:]
	if len(h) >= feedPerSecond {
		s.feedHits[id] = h
		return time.Second
	}
	if len(s.feedHits) > 4096 {
		s.feedHits = map[int64][]time.Time{}
	}
	s.feedHits[id] = append(h, now)
	return 0
}

// countCall 은 분 창에 호출을 한 번 더한다(api_rate_windows: 다중 프로세스·재시작에도 맞다). 한도를 넘으면 거부 수를 올리고 다음 분까지 기다릴 초를 돌려준다.
// first 는 이 창의 첫 거부다(그때만 감사 api.rate_limited 를 바로 남긴다).
func (s *Service) countCall(ctx context.Context, id int64, now time.Time) (limited bool, wait int, first bool) {
	minute := now.Unix() / 60
	limit := s.rate(ctx)
	var calls int
	if err := s.db.QueryRowContext(ctx, `INSERT INTO api_rate_windows (token_id, minute, calls, denied) VALUES (?,?,1,0)
		ON CONFLICT (token_id, minute) DO UPDATE SET calls = api_rate_windows.calls + 1 RETURNING calls`, id, minute).Scan(&calls); err != nil {
		log.Printf("apitoken: 호출 수 기록 실패: %v", err)
		return false, 0, false // 계수 실패가 연동을 막지는 않는다(감사·로그에 남음)
	}
	// 마지막 사용 시각은 분 단위로만 갱신한다(호출마다 쓰지 않는다).
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now.Format(tsFmt), id, now.Add(-time.Minute).Format(tsFmt))
	if calls <= limit {
		return false, 0, false
	}
	var denied int
	if err := s.db.QueryRowContext(ctx, `UPDATE api_rate_windows SET denied = denied + 1 WHERE token_id = ? AND minute = ? RETURNING denied`, id, minute).Scan(&denied); err != nil {
		denied = 2
	}
	wait = int((minute+1)*60 - now.Unix())
	if wait < 1 {
		wait = 1
	}
	return true, wait, denied == 1
}

// ---- 관찰·동시 실행 제한 ----

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// resourceOf 는 /api/v1 경로의 첫 마디(자원 이름)다. 값(쿼리·번호)은 담지 않는다.
func resourceOf(path string) string {
	p := strings.TrimPrefix(path, "/api/v1/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	if p == "" || len(p) > 40 {
		return "-"
	}
	return p
}

type aggKey struct {
	token    int64
	resource string
	minute   int64
}

type aggVal struct {
	calls  int
	issuer int64
	name   string
}

// Wrap 은 /api/v1 핸들러 바깥에서 서버 전체 동시 요청 상한(PL_API_MAX_CONCURRENT)을 걸고, 인증을 통과한 호출을 토큰·자원·분 단위로 세며,
// 범위 밖(403)으로 끝난 호출은 거부 감사(api.denied, 10분당 1행)로 남긴다. /api/v1 경로가 아니면 건드리지 않는다.
func (s *Service) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !apiv1.IsAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		default:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"동시에 처리할 수 있는 요청 수를 넘었습니다. 잠시 뒤 다시 시도하세요."}}`))
			return
		}
		obs := &observation{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), obsKey{}, obs)))
		obs.mu.Lock()
		tid, issuer, name, reported := obs.tokenID, obs.issuer, obs.name, obs.reported
		obs.mu.Unlock()
		if tid == 0 || reported {
			return
		}
		res := resourceOf(r.URL.Path)
		minute := s.now().Unix() / 60
		s.mu.Lock()
		k := aggKey{tid, res, minute}
		v := s.agg[k]
		if v == nil {
			v = &aggVal{issuer: issuer, name: name}
			s.agg[k] = v
		}
		v.calls++
		s.mu.Unlock()
		if sw.status == http.StatusForbidden {
			actor := tokenPrincipal(tokenRow{ID: tid, Name: name, CreatedBy: issuer}, nil)
			s.recThrottled(r.Context(), "den|"+strconv.FormatInt(tid, 10)+"|"+res, 10*time.Minute, actor, "api.denied", strconv.FormatInt(tid, 10),
				[]audit.DiffItem{{Key: "api_reason", New: "forbidden_scope"}, {Key: "api_resource", New: res}}, "토큰 범위 밖 자원")
		}
	})
}

// Flush 는 끝난 분(또는 all 이면 전부)의 읽기 호출 요약을 감사 api.read 로 남긴다: 토큰·자원·분 단위 1행(D15). 쿼리 값·응답 값은 없다.
func (s *Service) Flush(ctx context.Context, all bool) {
	cur := s.now().Unix() / 60
	s.mu.Lock()
	var keys []aggKey
	for k := range s.agg {
		if all || k.minute < cur {
			keys = append(keys, k)
		}
	}
	s.mu.Unlock()
	for _, k := range keys {
		s.mu.Lock()
		v := s.agg[k]
		s.mu.Unlock()
		if v == nil {
			continue
		}
		actor := tokenPrincipal(tokenRow{ID: k.token, Name: v.name, CreatedBy: v.issuer}, nil)
		err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
			return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "api.read", TargetType: audit.TargetAPIToken, TargetID: strconv.FormatInt(k.token, 10), Result: audit.ResultSuccess,
				Diff: []audit.DiffItem{{Key: "api_resource", New: k.resource}, {Key: "api_calls", New: strconv.Itoa(v.calls)}, {Key: "api_minute", New: time.Unix(k.minute*60, 0).UTC().Format("2006-01-02 15:04")}}})
		})
		if err != nil {
			log.Printf("apitoken: 읽기 요약 감사 실패(%d·%s): %v", k.token, k.resource, err)
			continue // 다음 주기에 다시 시도(메모리에 남겨 둔다)
		}
		s.mu.Lock()
		delete(s.agg, k)
		s.mu.Unlock()
	}
}

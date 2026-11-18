package apiv1

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// ---- 인증 자리(platform 이 구현을 붙인다) ----

// Caller 는 인증을 마친 API 호출자다. Principal 은 토큰의 고정 주체(발급자 번호·ChannelAPI·AuthAPI·범위가 정한 Perms)이고 Scopes 는 토큰의 범위 이름들이다.
type Caller struct {
	Principal contract.Principal
	Scopes    []string
	TokenID   int64
	// EntityID 는 토큰이 묶인 법인이다(0 = 그룹 전체: 읽기만 모든 법인, B44). 토큰이 가진 모든 읽기·쓰기가 이 법인으로 고정된다.
	EntityID int64
}

// Authenticator 는 /api/v1 요청의 Bearer 토큰(pli_)을 확인한다. 구현(토큰 조회·만료·폐기·허용 CIDR·속도 제한·감사)은 platform 몫이다.
// 실패는 contract.ErrUnauthenticated(401)·contract.ErrForbidden(403, 꺼짐·IP·범위 밖)·ErrRateLimited(429)로 돌려준다. 시험은 가짜 구현으로 주체를 주입한다.
type Authenticator interface {
	AuthenticateAPI(ctx context.Context, r *http.Request) (Caller, error)
}

// ErrRateLimited 는 호출 한도를 넘었다는 뜻이다(429). RetryAfter 가 있으면 Retry-After 헤더로 낸다.
type ErrRateLimited struct{ RetryAfterSeconds int }

func (e ErrRateLimited) Error() string { return "호출 한도를 넘었습니다" }

// IsAPIPath 는 /api/v1 경로인지다. cmd/server 의 조립이 SCIM 처럼 경로를 나눠 인증 미들웨어를 고를 때 쓴다.
func IsAPIPath(p string) bool { return p == "/api/v1" || strings.HasPrefix(p, "/api/v1/") }

const openAPIPath = "/api/v1/openapi.json"

type callerKey struct{}

// Options 는 Service 구성이다.
type Options struct {
	Read domain.APIReadService
	DB   contract.Tx // 변경 피드 조회
	Auth Authenticator
	// Write 는 쓰기 라우트의 서비스 쪽이다(B42 3단계). 비어 있으면 쓰기 라우트는 503(unavailable)이다.
	Write Writer
}

// Service 는 /api/v1 라우트와 인증 미들웨어다.
type Service struct {
	read  domain.APIReadService
	db    contract.Tx
	auth  Authenticator
	write Writer
}

// New 는 Service 를 만든다.
func New(o Options) *Service { return &Service{read: o.Read, db: o.DB, auth: o.Auth, write: o.Write} }

// Wrap 은 /api/v1 경로에 API 토큰 인증을 거는 미들웨어다. 다른 경로는 건드리지 않는다(호출하는 쪽이 경로를 나눠 부른다).
// OpenAPI 문서(스키마만, 값 없음)는 인증 없이 나간다. 인증을 통과하면 context 에 고정 주체(contract.WithPrincipal)와 Caller 를 싣는다.
func (s *Service) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAPIPath(r.URL.Path) || r.URL.Path == openAPIPath {
			next.ServeHTTP(w, r)
			return
		}
		rid := requestID(w)
		if s.auth == nil {
			writeError(w, rid, http.StatusServiceUnavailable, "unavailable", "연동 API 인증이 설정되지 않았습니다.")
			return
		}
		c, err := s.auth.AuthenticateAPI(r.Context(), r)
		if err != nil {
			var rl ErrRateLimited
			switch {
			case errors.As(err, &rl):
				if rl.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds))
				}
				writeError(w, rid, http.StatusTooManyRequests, "rate_limited", "호출 한도를 넘었습니다. 잠시 뒤 다시 시도하세요.")
			case errors.Is(err, contract.ErrUnauthenticated):
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, rid, http.StatusUnauthorized, "unauthenticated", "API 토큰이 올바르지 않거나 만료되었습니다.")
			case errors.Is(err, contract.ErrForbidden):
				writeError(w, rid, http.StatusForbidden, "forbidden_scope", "이 토큰으로는 호출할 수 없습니다.")
			default:
				writeError(w, rid, http.StatusInternalServerError, "unavailable", "서버 오류가 발생했습니다.")
			}
			return
		}
		ctx := contract.WithPrincipal(r.Context(), c.Principal)
		if c.EntityID > 0 {
			ctx = contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: c.EntityID})
		} else if c.EntityID == 0 && c.Principal.EntityPerms != nil {
			ctx = contract.WithActiveEntity(ctx, contract.ActiveEntity{All: true})
		}
		ctx = context.WithValue(ctx, callerKey{}, c)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func callerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// ---- 공통 응답 ----

func requestID(w http.ResponseWriter) string {
	if id := w.Header().Get("X-Request-Id"); id != "" {
		return id
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	w.Header().Set("X-Request-Id", id)
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, rid string, status int, code, msg string) {
	writeJSON(w, status, map[string]ErrorBody{"error": {Code: code, Message: msg, RequestID: rid}})
}

// fail 은 서비스 오류를 고정 오류 코드로 옮긴다. 알 수 없는 오류의 상세는 숨긴다.
func fail(w http.ResponseWriter, rid string, err error) {
	switch {
	case errors.Is(err, contract.ErrForbidden):
		writeError(w, rid, http.StatusForbidden, "forbidden_scope", "이 토큰의 범위로는 읽을 수 없습니다.")
	case errors.Is(err, contract.ErrNotFound):
		writeError(w, rid, http.StatusNotFound, "not_found", "대상을 찾을 수 없습니다.")
	case errors.Is(err, changes.ErrCursorExpired):
		writeError(w, rid, http.StatusGone, "cursor_expired", "커서가 만료되었습니다. 전체를 다시 읽어 주세요.")
	case errors.Is(err, changes.ErrBadCursor), errors.Is(err, contract.ErrValidation):
		writeError(w, rid, http.StatusBadRequest, "invalid_argument", "요청 값이 올바르지 않습니다.")
	default:
		writeError(w, rid, http.StatusInternalServerError, "unavailable", "서버 오류가 발생했습니다.")
	}
}

// page 는 목록 응답이다.
type page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

const pageCursorPrefix = "p1."

func pageCursor(id int64) string { return pageCursorPrefix + strconv.FormatInt(id, 10) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// listParams 는 목록의 공통 쿼리다: after(번호) 또는 cursor(p1.<번호>), limit(1~200, 기본 100).
func listParams(r *http.Request) (after int64, limit int, err error) {
	q := r.URL.Query()
	limit = 100
	if v := q.Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 200 {
			return 0, 0, contract.ErrValidation
		}
		limit = n
	}
	// id: 웹훅 본문의 api_url(?id=번호)이 가리키는 한 건 조회. 번호 오름차순 목록의 "그 번호 바로 앞부터 한 건"이므로 없으면 빈 목록이다.
	if v := q.Get("id"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 1 {
			return 0, 0, contract.ErrValidation
		}
		return n - 1, 1, nil
	}
	if v := q.Get("cursor"); v != "" {
		if !strings.HasPrefix(v, pageCursorPrefix) {
			return 0, 0, contract.ErrValidation
		}
		n, perr := strconv.ParseInt(strings.TrimPrefix(v, pageCursorPrefix), 10, 64)
		if perr != nil || n < 0 {
			return 0, 0, contract.ErrValidation
		}
		return n, limit, nil
	}
	if v := q.Get("after"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			return 0, 0, contract.ErrValidation
		}
		after = n
	}
	return after, limit, nil
}

// scoped 는 라우트가 선언한 범위가 호출자 토큰에 있는지 확인한 뒤 핸들러를 부른다.
func scoped(scope string, h func(w http.ResponseWriter, r *http.Request, rid string, c Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := requestID(w)
		c, ok := callerFrom(r.Context())
		if !ok {
			writeError(w, rid, http.StatusUnauthorized, "unauthenticated", "인증이 필요합니다.")
			return
		}
		if !HasScope(c.Scopes, scope) {
			writeError(w, rid, http.StatusForbidden, "forbidden_scope", "이 토큰에는 필요한 범위가 없습니다.")
			return
		}
		h(w, r, rid, c)
	}
}

func listHandler[S any, T any](scope string, fetch func(ctx context.Context, after int64, limit int) ([]S, bool, error), conv func(S) T, idOf func(S) int64) http.HandlerFunc {
	return scoped(scope, func(w http.ResponseWriter, r *http.Request, rid string, _ Caller) {
		after, limit, err := listParams(r)
		if err != nil {
			fail(w, rid, err)
			return
		}
		rows, more, err := fetch(r.Context(), after, limit)
		if err != nil {
			fail(w, rid, err)
			return
		}
		out := page[T]{Data: make([]T, 0, len(rows)), HasMore: more}
		for _, row := range rows {
			out.Data = append(out.Data, conv(row))
		}
		if more && len(rows) > 0 {
			c := pageCursor(idOf(rows[len(rows)-1]))
			out.NextCursor = &c
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// ---- 변경 피드 ----

func (s *Service) changesHandler() http.HandlerFunc {
	return scoped(ScopeChangesRead, func(w http.ResponseWriter, r *http.Request, rid string, c Caller) {
		q := r.URL.Query()
		after, err := changes.DecodeCursor(q.Get("cursor"))
		if err != nil {
			fail(w, rid, err)
			return
		}
		limit := 200
		if v := q.Get("limit"); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n < 1 || n > 500 {
				fail(w, rid, contract.ErrValidation)
				return
			}
			limit = n
		}
		// 토큰 범위가 읽기 범위를 가진 자원의 줄만 보인다. 다른 자원의 존재도 드러내지 않는다.
		allowed := ResourcesFor(c.Scopes)
		want := allowed
		if v := q.Get("resource"); v != "" {
			want = nil
			for _, res := range strings.Split(v, ",") {
				res = strings.TrimSpace(res)
				if !contains(allowed, res) {
					writeError(w, rid, http.StatusForbidden, "forbidden_scope", "이 토큰에는 그 자원의 읽기 범위가 없습니다.")
					return
				}
				want = append(want, res)
			}
		}
		type changesResp struct {
			Data       []Change `json:"data"`
			NextCursor string   `json:"next_cursor"`
			HasMore    bool     `json:"has_more"`
		}
		if len(want) == 0 { // 읽기 범위가 하나도 없으면 줄이 없다(커서는 그대로)
			writeJSON(w, http.StatusOK, changesResp{Data: []Change{}, NextCursor: q.Get("cursor")})
			return
		}
		rows, more, head, err := changes.AfterIn(r.Context(), s.db, after, limit, want, feedScope(r.Context()))
		if err != nil {
			fail(w, rid, err)
			return
		}
		out := changesResp{Data: make([]Change, 0, len(rows)), HasMore: more, NextCursor: q.Get("cursor")}
		// 읽은 범위(허용된 자원)의 마지막 줄까지만 커서가 간다: 필터로 걸러진 줄은 어차피 이 토큰에 보이지 않는다.
		last := after
		for _, e := range rows {
			out.Data = append(out.Data, toChange(e))
			last = e.Seq
		}
		switch {
		case more && len(rows) > 0:
			out.NextCursor = changes.EncodeCursor(last)
		case !more && head > after:
			out.NextCursor = changes.EncodeCursor(head) // 읽기 시작 때의 head 까지 모두 훑었다(걸러진 줄 포함)
		case len(rows) > 0:
			out.NextCursor = changes.EncodeCursor(last)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func (s *Service) headHandler() http.HandlerFunc {
	return scoped(ScopeChangesRead, func(w http.ResponseWriter, r *http.Request, rid string, _ Caller) {
		head, err := changes.Head(r.Context(), s.db)
		if err != nil {
			fail(w, rid, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]ChangeHead{"data": {Cursor: changes.EncodeCursor(head)}})
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// feedScope 는 변경 피드를 읽는 법인 범위다: 토큰이 한 법인에 묶여 있으면 그 법인, 그룹 토큰(전체)이면 모두.
func feedScope(ctx context.Context) contract.EntityScope {
	if c, ok := callerFrom(ctx); ok && c.EntityID > 0 {
		return contract.EntityScope{IDs: []int64{c.EntityID}}
	}
	return contract.EntityScope{All: true}
}

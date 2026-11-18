package scim

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

type ctxKey struct{}

func withToken(ctx context.Context, t tokenRow) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

func tokenFrom(ctx context.Context) (tokenRow, bool) {
	t, ok := ctx.Value(ctxKey{}).(tokenRow)
	return t, ok
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

// allowToken 는 토큰별 분당 호출 한도를 센다(메모리, 재시작하면 초기화). 한도를 넘으면 false 와 기다릴 초를 돌려준다.
func (s *Service) allowToken(id int64, now time.Time) (bool, int) {
	if s.rate <= 0 {
		return true, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := now.Add(-time.Minute)
	h := s.hits[id]
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	h = h[i:]
	if len(h) >= s.rate {
		s.hits[id] = h
		wait := int(h[0].Add(time.Minute).Sub(now).Seconds()) + 1
		if wait < 1 {
			wait = 1
		}
		return false, wait
	}
	s.hits[id] = append(h, now)
	return true, 0
}

// ipBlocked 는 인증 실패가 한도(IP 당 분당 20회)를 넘었는지다.
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

// authMiddleware 는 /scim/v2 요청의 인증·설정·범위·속도 제한을 확인한다. 쿠키·세션·개인 토큰은 인정하지 않는다(Authorization 헤더의 pls_ 토큰만).
func (s *Service) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		sysActor := contract.Principal{Role: "scim", Channel: contract.ChannelSCIM}
		if s.disabled { // 복구 리허설 모드: 복구본이 운영 IdP 와 이어지는 사고를 막는다
			writeAPIError(w, &apiError{Status: 403, Detail: "이 환경에서는 계정 동기화를 받지 않습니다."})
			return
		}
		now := s.now().UTC()
		ip := clientIP(r)
		if !s.ipAllowed(ip) { // 허용 출발지 밖(PL_SCIM_ALLOW_CIDR): 토큰을 확인하기 전에 거부한다
			s.auditThrottled(r.Context(), "ip|"+ip, 10*time.Minute, sysActor, "scim.rejected", audit.TargetRoute, "/scim/v2", audit.ResultDenied,
				[]audit.DiffItem{{Key: "reason", New: "ip_not_allowed"}}, "허용되지 않은 출발지")
			writeAPIError(w, &apiError{Status: 403, Detail: "허용되지 않은 출발지 주소입니다."})
			return
		}
		if s.ipBlocked(ip, now) {
			w.Header().Set("Retry-After", "60")
			writeAPIError(w, &apiError{Status: 429, Detail: "인증 실패가 너무 많습니다. 잠시 후 다시 시도하세요."})
			return
		}
		raw, present := bearer(r)
		var tok tokenRow
		ok := false
		if present {
			var err error
			tok, ok, err = s.authenticate(r.Context(), raw)
			if err != nil {
				logf("scim: 토큰 확인 실패: %v", err)
				writeAPIError(w, &apiError{Status: 500, Detail: "서버 오류가 발생했습니다."})
				return
			}
		}
		if !ok {
			s.ipFail(ip, now)
			s.auditThrottled(r.Context(), "auth|"+ip, 10*time.Minute, sysActor, "scim.auth_failed", audit.TargetRoute, "/scim/v2", audit.ResultDenied,
				[]audit.DiffItem{{Key: "reason", New: "invalid_token"}}, "")
			writeAPIError(w, &apiError{Status: 401, Detail: "인증이 필요합니다. 유효한 SCIM 토큰을 Bearer 로 보내세요."})
			return
		}
		if okRate, wait := s.allowToken(tok.ID, now); !okRate {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			s.auditThrottled(r.Context(), "rate|"+strconv.FormatInt(tok.ID, 10), 10*time.Minute, newActor(tok), "scim.rejected", audit.TargetRoute, "/scim/v2", audit.ResultDenied,
				[]audit.DiffItem{{Key: "reason", New: reasonRate}, {Key: "token", New: tok.Name}}, "분당 호출 한도 초과")
			writeAPIError(w, &apiError{Status: 429, Detail: "분당 호출 한도를 넘었습니다. 잠시 후 다시 시도하세요."})
			return
		}
		st, err := s.loadSettings(r.Context(), s.db)
		if err != nil {
			logf("scim: 설정 읽기 실패: %v", err)
			writeAPIError(w, &apiError{Status: 500, Detail: "서버 오류가 발생했습니다."})
			return
		}
		if !st.Enabled {
			writeAPIError(w, &apiError{Status: 403, Detail: "계정 동기화가 꺼져 있습니다. 관리자 화면에서 켜 주세요."})
			return
		}
		if (r.URL.Path == "/scim/v2/Groups" || strings.HasPrefix(r.URL.Path, "/scim/v2/Groups/")) && tok.Scope != "users_groups" {
			writeAPIError(w, &apiError{Status: 403, Detail: "이 토큰은 그룹 범위가 없습니다."})
			return
		}
		ctx := contract.WithPrincipal(withToken(r.Context(), tok), newActor(tok))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

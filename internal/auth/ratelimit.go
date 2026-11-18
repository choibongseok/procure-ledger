package auth

import (
	"sync"
	"time"
)

const (
	ipLimitPerMin   = 20
	rateAuditWindow = 10 * time.Minute
)

// ipLimiter 는 IP(와 범위)별 분당 요청 수 제한이다. 메모리에만 두며(재시작하면 초기화) 계정 잠금과는 별개다.
type ipLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	lastAudit map[string]time.Time
	calls     int
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{hits: map[string][]time.Time{}, lastAudit: map[string]time.Time{}}
}

// allow 는 요청을 받아도 되는지 돌려준다. audit 는 이 거부가 감사 행을 남길 차례인지(범위별 10분당 1행)다.
func (l *ipLimiter) allow(scope, ip string, now time.Time) (ok, audit bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := scope + "|" + ip
	cut := now.Add(-time.Minute)
	h := l.hits[key]
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	h = h[i:]
	l.calls++
	if l.calls%256 == 0 { // 오래된 키 정리
		for k, v := range l.hits {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(l.hits, k)
			}
		}
	}
	if len(h) >= ipLimitPerMin {
		l.hits[key] = h
		if last, seen := l.lastAudit[scope]; !seen || now.Sub(last) >= rateAuditWindow {
			l.lastAudit[scope] = now
			return false, true
		}
		return false, false
	}
	l.hits[key] = append(h, now)
	return true, false
}

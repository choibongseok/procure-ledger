package export

import (
	"errors"
	"sync"
	"time"
)

// 내보내기 속도 제한: 사용자당 분당 5회, 동시 1건(명세 3.5). 서버가 다시 시작되면 카운터는 초기화된다(메모리 카운터).
const (
	PerUserPerMinute = 5
	window           = time.Minute
)

// ErrTooOften 은 속도 제한·동시 실행 제한에 걸렸다는 뜻이다.
var ErrTooOften = errors.New("내보내기를 너무 자주 하셨습니다. 1분 뒤 다시 시도하세요.")

// Limiter 는 사용자별 내보내기 횟수와 동시 실행을 센다.
type Limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	perMin int
	users  map[int64]*userState
}

type userState struct {
	times    []time.Time
	inflight bool
}

// NewLimiter 는 기본 한도(분당 5회)의 제한기를 만든다. now 가 nil 이면 time.Now.
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, perMin: PerUserPerMinute, users: map[int64]*userState{}}
}

// Acquire 는 한 번의 내보내기 시도를 시작한다. 성공하면 끝났을 때 부를 release 를 돌려준다.
// 실패한 시도도 횟수에 센다(release 후에도 최근 1분 안의 시도로 남는다). 한도·동시 실행에 걸린 시도는 세지 않는다(계속 막히지 않게).
func (l *Limiter) Acquire(userID int64) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	st := l.users[userID]
	if st == nil {
		st = &userState{}
		l.users[userID] = st
	}
	cut := now.Add(-window)
	kept := st.times[:0]
	for _, t := range st.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	st.times = kept
	if st.inflight || len(st.times) >= l.perMin {
		return nil, ErrTooOften
	}
	st.times = append(st.times, now)
	st.inflight = true
	return func() {
		l.mu.Lock()
		st.inflight = false
		l.mu.Unlock()
	}, nil
}

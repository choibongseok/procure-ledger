package llmgw

import (
	"sync"
	"time"
)

// 회로 차단(B45 명세 6절, D7·D8): 공급자별 프로세스 메모리 상태다.
//
//	closed → open     최근 20회 중 10회 이상이고 재시도 가능 실패가 50% 이상이거나, 연속 5회 실패
//	open              기본 60초 동안 그 공급자를 시도하지 않고 바로 대체로 간다
//	open → half_open  시간이 지나면 한 호출만 시험으로 보낸다
//	half_open         성공하면 closed, 실패하면 다시 open
//
// 재시도 불가 오류(auth·bad_request)는 회로를 열지도 닫지도 않는다. auth 는 "설정 확인 필요" 표시만 켠다.
const (
	breakerWindow      = 20
	breakerMinCalls    = 10
	breakerConsecutive = 5
	breakerOpenFor     = 60 * time.Second
)

// 회로 상태 문자열.
const (
	CircuitClosed   = "closed"
	CircuitOpen     = "open"
	CircuitHalfOpen = "half_open"
)

type breaker struct {
	window     []bool // true = 실패(최근 breakerWindow 개)
	consec     int
	state      string
	openUntil  time.Time
	probing    bool
	needsCheck bool
}

// BreakerSet 은 공급자 코드별 회로다. 게이트웨이와 관리 서비스가 하나를 공유한다.
type BreakerSet struct {
	mu  sync.Mutex
	m   map[string]*breaker
	now func() time.Time
	// OnChange 는 closed→open·half_open→closed 전이 때 락 밖에서 불린다(event: "open" | "close").
	OnChange func(code, event string)
}

// NewBreakerSet 은 시계를 주입한 회로 모음을 만든다(nil 이면 time.Now).
func NewBreakerSet(now func() time.Time) *BreakerSet {
	if now == nil {
		now = time.Now
	}
	return &BreakerSet{m: map[string]*breaker{}, now: now}
}

func (s *BreakerSet) get(code string) *breaker {
	b := s.m[code]
	if b == nil {
		b = &breaker{state: CircuitClosed}
		s.m[code] = b
	}
	return b
}

// Allow 는 지금 그 공급자를 시도해도 되는지다. open 이면 false, 시간이 지났으면 반열림으로 바꾸고 시험 한 번만 true 다.
func (s *BreakerSet) Allow(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(code)
	switch b.state {
	case CircuitOpen:
		if s.now().Before(b.openUntil) {
			return false
		}
		b.state, b.probing = CircuitHalfOpen, true
		return true
	case CircuitHalfOpen:
		if b.probing {
			return false // 시험 호출이 진행 중이다
		}
		b.probing = true
		return true
	}
	return true
}

func (b *breaker) push(fail bool) {
	b.window = append(b.window, fail)
	if len(b.window) > breakerWindow {
		b.window = b.window[1:]
	}
}

// Success 는 성공을 기록한다. 반열림이면 닫는다.
func (s *BreakerSet) Success(code string) {
	var event string
	s.mu.Lock()
	b := s.get(code)
	b.needsCheck = false
	if b.state == CircuitHalfOpen {
		b.state, b.probing, b.window, b.consec = CircuitClosed, false, nil, 0
		event = "close"
	} else {
		b.push(false)
		b.consec = 0
	}
	cb := s.OnChange
	s.mu.Unlock()
	if event != "" && cb != nil {
		cb(code, event)
	}
}

// Failure 는 재시도 가능한 실패를 기록한다. 임계에 닿으면 연다. 반열림의 실패는 다시 연다.
func (s *BreakerSet) Failure(code string) {
	var event string
	s.mu.Lock()
	b := s.get(code)
	switch b.state {
	case CircuitHalfOpen:
		b.state, b.probing, b.openUntil = CircuitOpen, false, s.now().Add(breakerOpenFor)
	case CircuitClosed:
		b.push(true)
		b.consec++
		fails := 0
		for _, f := range b.window {
			if f {
				fails++
			}
		}
		if b.consec >= breakerConsecutive || (len(b.window) >= breakerMinCalls && fails*2 >= len(b.window)) {
			b.state, b.openUntil = CircuitOpen, s.now().Add(breakerOpenFor)
			event = "open"
		}
	}
	cb := s.OnChange
	s.mu.Unlock()
	if event != "" && cb != nil {
		cb(code, event)
	}
}

// Release 는 시험 호출이 성공도 재시도 가능 실패도 아닌 결과로 끝났을 때 반열림의 시험 표시만 푼다.
func (s *BreakerSet) Release(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.get(code); b.state == CircuitHalfOpen {
		b.probing = false
	}
}

// AuthFailed 는 인증 실패를 표시한다(화면의 "설정 확인 필요"). 다음 성공이 지운다.
func (s *BreakerSet) AuthFailed(code string) {
	s.mu.Lock()
	s.get(code).needsCheck = true
	s.mu.Unlock()
}

// State 는 공급자의 회로 상태와 설정 확인 필요 여부다. 열림 시간이 지났어도 다음 호출 전까지는 open 으로 보인다.
func (s *BreakerSet) State(code string) (state string, needsCheck bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.m[code]
	if b == nil {
		return CircuitClosed, false
	}
	return b.state, b.needsCheck
}

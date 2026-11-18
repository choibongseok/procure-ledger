// Package setup 은 설치 마법사(B38, 4절)다.
//
// 빈 DB 의 첫 기동에서만 열리고(setup_state='pending'), 완료하면 영구히 잠긴다. 열린 동안 모든 요청은 서버 로그에만 출력되는
// 일회용 설치 코드(또는 PL_SETUP_CODE)를 요구한다: 서버 포트가 공개돼 있어도 서버를 운영하는 사람만 첫 관리자를 정할 수 있다.
package setup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	codeLen       = 24 // 24자 × 5비트 = 120비트
	codeAlphabet  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	maxFails      = 5
	lockoutFor    = 15 * time.Minute
	codeValidFor  = 24 * time.Hour
	minEnvCodeLen = 16
)

// 코드 확인 결과 오류. 응답에는 코드 힌트를 싣지 않는다.
var (
	ErrBadCode = errors.New("설치 코드가 올바르지 않습니다")
	ErrExpired = errors.New("설치 코드의 유효 시간이 지났습니다. 서버를 다시 시작해 새 코드를 확인하세요")
)

// LockedError 는 틀린 코드가 연속 5회라 모든 코드 시도를 거부하는 중이라는 오류다(올바른 코드도 거부한다).
type LockedError struct{ Until time.Time }

func (e LockedError) Error() string {
	return "설치 코드 입력이 잠겼습니다. 잠시 뒤에 다시 시도하세요"
}

// Guard 는 설치 코드의 해시와 시도 제한을 메모리에만 가진다(DB 에 두지 않는다. 재기동하면 새 코드).
type Guard struct {
	mu          sync.Mutex
	hash        [32]byte
	expires     time.Time
	fails       int
	lockedUntil time.Time
	revoked     bool
	now         func() time.Time
}

// NewGuard 는 설치 코드를 정한다. envCode 가 비어 있지 않으면 그 값(16자 이상)을 쓰고 generated 는 빈 문자열이다.
// 비어 있으면 CSPRNG 120비트 코드를 만들어 4자씩 끊어 돌려준다(호출자가 로그에 한 번 출력한다).
func NewGuard(envCode string, now func() time.Time) (g *Guard, generated string, err error) {
	if now == nil {
		now = time.Now
	}
	g = &Guard{now: now, expires: now().Add(codeValidFor)}
	if envCode = strings.TrimSpace(envCode); envCode != "" {
		if len(envCode) < minEnvCodeLen {
			return nil, "", errors.New("PL_SETUP_CODE 는 16자 이상이어야 합니다")
		}
		g.hash = sha256.Sum256([]byte(envCode))
		return g, "", nil
	}
	raw := make([]byte, codeLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	plain := make([]byte, codeLen)
	for i, b := range raw {
		plain[i] = codeAlphabet[int(b)&31] // 알파벳이 정확히 32자라 편향이 없다
	}
	g.hash = sha256.Sum256(plain)
	var parts []string
	for i := 0; i < codeLen; i += 4 {
		parts = append(parts, string(plain[i:i+4]))
	}
	return g, strings.Join(parts, "-"), nil
}

// normalize 는 화면에서 끊어 읽은 코드(공백·하이픈, 소문자)를 정규형으로 만든다. 환경변수 코드는 대소문자를 구분하므로 두 형태를 모두 비교한다.
func normalize(c string) string {
	c = strings.TrimSpace(c)
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
}

// Check 는 후보 코드를 확인한다. 잠금 중이면 올바른 코드도 LockedError 다. newlyLocked 는 이 시도로 잠금이 시작됐는지다(감사 1행용).
func (g *Guard) Check(candidate string) (newlyLocked bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.revoked {
		return false, ErrBadCode
	}
	if now.Before(g.lockedUntil) {
		return false, LockedError{g.lockedUntil}
	}
	if now.After(g.expires) {
		return false, ErrExpired
	}
	ok := false
	for _, c := range []string{strings.TrimSpace(candidate), normalize(candidate)} {
		h := sha256.Sum256([]byte(c))
		if subtle.ConstantTimeCompare(h[:], g.hash[:]) == 1 {
			ok = true
		}
	}
	if ok {
		g.fails = 0
		return false, nil
	}
	g.fails++
	if g.fails >= maxFails {
		g.fails = 0
		g.lockedUntil = now.Add(lockoutFor)
		return true, ErrBadCode
	}
	return false, ErrBadCode
}

// Revoke 는 코드를 폐기한다(설치 완료 직후). 이후 어떤 코드도 통과하지 못한다.
func (g *Guard) Revoke() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked = true
	g.hash = [32]byte{}
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ArgonParams 는 argon2id 비용이다. 기본은 메모리 64 MiB·반복 3·병렬 2(명세 4.2). 저장 형식이 PHC 문자열이라
// 값을 올려도 기존 해시가 그대로 검증되고, 로그인 성공 때 낮은 비용이면 새 값으로 다시 해시한다.
type ArgonParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultArgon 은 기본 비용이다.
var DefaultArgon = ArgonParams{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2}

const (
	argonSaltLen = 16
	argonKeyLen  = 32
	hashSlots    = 4 // 동시 해시 연산 수 상한(메모리 소진 방지)
)

// ErrBusy 는 동시 해시 연산 한도를 넘어 기다리다 포기한 경우다(503).
var ErrBusy = errors.New("서버가 바빠 잠시 뒤 다시 시도해 주세요")

// hasher 는 argon2id 해시·검증을 동시 연산 수를 제한해서 수행한다.
type hasher struct {
	params ArgonParams
	slots  chan struct{}
	// verifies 는 비용을 치른 검증 호출 수다(시험이 "모든 로그인 경로가 같은 비용의 검증을 정확히 한 번 한다"를 결정적으로 세는 데 쓴다).
	verifies atomic.Int64
}

func newHasher(p ArgonParams) *hasher {
	if p.MemoryKiB == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		p = DefaultArgon
	}
	return &hasher{params: p, slots: make(chan struct{}, hashSlots)}
}

func (h *hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrBusy
	}
}

func (h *hasher) release() { <-h.slots }

// Hash 는 PHC 문자열($argon2id$v=19$m=...,t=...,p=...$솔트$해시)을 만든다.
func (h *hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// parsePHC 는 PHC 문자열을 풀어 쓴다.
func parsePHC(enc string) (p ArgonParams, salt, key []byte, err error) {
	parts := strings.Split(enc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("지원하지 않는 해시 형식")
	}
	var ver int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return p, nil, nil, errors.New("해시 버전")
	}
	var m, t uint32
	var par uint8
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil {
		return p, nil, nil, errors.New("해시 파라미터")
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, err
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, errors.New("해시 값")
	}
	return ArgonParams{MemoryKiB: m, Iterations: t, Parallelism: par}, salt, key, nil
}

// Verify 는 상수 시간으로 비교한다. needsRehash 는 저장된 비용이 현재 설정보다 낮을 때 참이다.
func (h *hasher) Verify(ctx context.Context, password, enc string) (ok, needsRehash bool, err error) {
	p, salt, key, perr := parsePHC(enc)
	if perr != nil {
		return false, false, perr
	}
	// 저장된 파라미터를 그대로 믿으면 변조된 행이 메모리를 소진시킬 수 있으므로 상한을 둔다.
	if p.MemoryKiB > 1<<20 || p.Iterations > 20 || p.Parallelism > 16 {
		return false, false, errors.New("해시 파라미터가 허용 범위를 넘습니다")
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	h.verifies.Add(1)
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(key)))
	ok = subtle.ConstantTimeCompare(got, key) == 1
	needsRehash = p.MemoryKiB < h.params.MemoryKiB || p.Iterations < h.params.Iterations || p.Parallelism < h.params.Parallelism
	return ok, needsRehash, nil
}

// ---- 정책 ----

// PasswordPolicy 는 비밀번호 정책이다(명세 4.3). 기본값은 10자·3종·재사용 3·만료 끔.
type PasswordPolicy struct {
	MinLength      int  `json:"minLength"`      // 최소 길이(문자 수)
	MaxLength      int  `json:"maxLength"`      // 최대 길이
	RequireClasses int  `json:"requireClasses"` // 문자 종류(대·소문자·숫자·기호) 최소 개수, 0 이면 검사 안 함
	HistoryCount   int  `json:"historyCount"`   // 최근 N 개와 같을 수 없음
	ExpireDays     int  `json:"expireDays"`     // 0 이면 만료 없음
	BanCommon      bool `json:"banCommon"`      // 흔한 비밀번호 목록과 같은 값 금지
}

// DefaultPasswordPolicy 는 정책 기본값이다.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: 10, MaxLength: 128, RequireClasses: 3, HistoryCount: 3, ExpireDays: 0, BanCommon: true}
}

// PolicyError 는 정책 위반이다. 문구에 무엇이 모자란지 구체적으로 쓴다(contract.ErrValidation 이다).
type PolicyError struct{ Msg string }

func (e PolicyError) Error() string { return e.Msg }
func (e PolicyError) Unwrap() error { return contract.ErrValidation }

// Check 는 비밀번호가 정책의 문자 규칙을 만족하는지 본다(재사용 검사는 해시가 필요해 서비스가 따로 한다).
// email·name 은 계정 정보와 같은 값을 막는 데 쓴다.
func (pp PasswordPolicy) Check(password, email, name string) error {
	n := utf8.RuneCountInString(password)
	if n < pp.MinLength {
		return PolicyError{fmt.Sprintf("비밀번호는 %d자 이상이어야 합니다. 지금 %d자입니다", pp.MinLength, n)}
	}
	if pp.MaxLength > 0 && n > pp.MaxLength {
		return PolicyError{fmt.Sprintf("비밀번호는 %d자를 넘을 수 없습니다. 지금 %d자입니다", pp.MaxLength, n)}
	}
	if pp.RequireClasses > 0 {
		if have := charClasses(password); have < pp.RequireClasses {
			return PolicyError{fmt.Sprintf("영문 대문자·소문자·숫자·기호 중 %d종류 이상을 써야 합니다. 지금 %d종류입니다", pp.RequireClasses, have)}
		}
	}
	low := strings.ToLower(password)
	if local, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@"); ok && local != "" && low == local {
		return PolicyError{"이메일 앞부분과 같은 비밀번호는 쓸 수 없습니다"}
	}
	if nm := strings.ToLower(strings.TrimSpace(name)); nm != "" && low == nm {
		return PolicyError{"이름과 같은 비밀번호는 쓸 수 없습니다"}
	}
	if pp.BanCommon && commonPasswords[low] {
		return PolicyError{"너무 흔한 비밀번호입니다. 다른 비밀번호를 쓰세요"}
	}
	return nil
}

func charClasses(s string) int {
	var lower, upper, digit, other bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	return n
}

// generateTempPassword 는 정책을 만족하는 14자 무작위 비밀번호를 만든다(헷갈리는 문자 l·1·O·0 제외).
func generateTempPassword() (string, error) {
	const (
		lower  = "abcdefghijkmnpqrstuvwxyz"
		upper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		digits = "23456789"
		syms   = "!@#$%^&*-_=+"
		all    = lower + upper + digits + syms
	)
	pick := func(set string) (byte, error) {
		var b [1]byte
		for {
			if _, err := rand.Read(b[:]); err != nil {
				return 0, err
			}
			if int(b[0]) < 256-256%len(set) { // 편향 없는 선택
				return set[int(b[0])%len(set)], nil
			}
		}
	}
	out := make([]byte, 0, 14)
	for _, set := range []string{lower, upper, digits, syms} {
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < 14 {
		c, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// 필수 문자가 앞 네 자리에 몰리지 않게 섞는다.
	for i := len(out) - 1; i > 0; i-- {
		var b [1]byte
		for {
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			if int(b[0]) < 256-256%(i+1) {
				break
			}
		}
		j := int(b[0]) % (i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

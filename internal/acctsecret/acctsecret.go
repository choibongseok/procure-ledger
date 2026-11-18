// Package acctsecret 은 계좌번호 저장 암호화(B32 D1 후속)다. AES-256-GCM 으로 봉하고 "enc:v1:" 머리말을 붙여 평문과 구분한다.
//
// 키는 환경변수 PL_ACCOUNT_SECRET_KEY(base64 32바이트)이고 DB 에 없다. 인증용 PL_AUTH_SECRET_KEY 와 따로 둔 이유: 인증 키를 교체·폐기해도
// 계좌번호 복호화가 깨지지 않고, 한 키가 새도 다른 쪽 비밀이 함께 풀리지 않게(용도별 키 분리). 키를 바꾸면 기존 계좌번호를 풀 수 없다(키 회전은 후속).
package acctsecret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Prefix 는 봉한 값의 머리말이다. 이 머리말이 없는 값은 이관 전 평문으로 본다.
const Prefix = "enc:v1:"

var aad = []byte("procure-ledger:account-no:v1")

// Cipher 는 계좌번호 봉인기다.
type Cipher struct{ aead cipher.AEAD }

// DecodeKey 는 base64 로 인코딩한 32바이트 키를 푼다. 비어 있으면 nil(암호화 꺼짐: 계좌 저장을 거부한다).
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		k, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, errors.New("PL_ACCOUNT_SECRET_KEY 는 base64 형식이어야 합니다(생성: openssl rand -base64 32)")
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("PL_ACCOUNT_SECRET_KEY 는 32바이트여야 합니다(지금 %d바이트)", len(k))
	}
	return k, nil
}

// New 는 32바이트 키로 봉인기를 만든다.
func New(key []byte) (*Cipher, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: g}, nil
}

// IsSealed 는 값이 봉한 형태인지다.
func IsSealed(s string) bool { return strings.HasPrefix(s, Prefix) }

// Seal 은 평문을 봉한다. 빈 값은 그대로 둔다. 이미 봉한 값은 다시 봉하지 않는다.
func (c *Cipher) Seal(plain string) (string, error) { return c.seal(plain, aad) }

// SealFor 는 같은 키로 다른 용도(purpose)의 비밀값을 봉한다. 용도마다 AAD 가 달라서, 한 용도로 봉한 값은 다른 용도(계좌번호 칸 포함)로 풀 수 없다.
// 예: 알림 웹훅 주소는 SealFor("notify-target", ...) 이다(B36 D5). 형식은 계좌번호와 같은 "enc:v1:" 머리말이다.
func (c *Cipher) SealFor(purpose, plain string) (string, error) {
	return c.seal(plain, purposeAAD(purpose))
}

// OpenFor 는 SealFor 로 봉한 값을 푼다. 용도가 다르거나 봉하지 않은 값이면 오류다(평문 통과 없음).
func (c *Cipher) OpenFor(purpose, stored string) (string, error) {
	if !IsSealed(stored) {
		return "", errors.New("봉하지 않은 값입니다")
	}
	return c.open(stored, purposeAAD(purpose))
}

func purposeAAD(purpose string) []byte { return []byte("procure-ledger:" + purpose + ":v1") }

func (c *Cipher) seal(plain string, aad []byte) (string, error) {
	if plain == "" || IsSealed(plain) {
		return plain, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := c.aead.Seal(nonce, nonce, []byte(plain), aad)
	return Prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open 은 봉한 값을 푼다. 봉하지 않은 값(이관 전 평문)은 그대로 돌려준다. 변조되었거나 키가 다르면 오류다.
func (c *Cipher) Open(stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	return c.open(stored, aad)
}

func (c *Cipher) open(stored string, aad []byte) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil || len(raw) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("계좌번호 암호문 형식이 올바르지 않습니다")
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, raw[:n], raw[n:], aad)
	if err != nil {
		return "", errors.New("계좌번호를 풀 수 없습니다(키가 다르거나 값이 바뀌었습니다)")
	}
	return string(plain), nil
}

package acctsecret_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
)

func box(t *testing.T, fill byte) *acctsecret.Cipher {
	t.Helper()
	c, err := acctsecret.New(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSealOpenRoundTripAndRandomNonce(t *testing.T) {
	c := box(t, 1)
	a, _ := c.Seal("110-123-456789")
	b, _ := c.Seal("110-123-456789")
	if a == b || !acctsecret.IsSealed(a) || strings.Contains(a, "456789") || strings.Contains(a, "110-123") {
		t.Fatalf("봉인 결과가 평문을 드러내거나 매번 같다: %q %q", a, b)
	}
	for _, s := range []string{a, b} {
		if got, err := c.Open(s); err != nil || got != "110-123-456789" {
			t.Fatalf("복호화: %q %v", got, err)
		}
	}
	if again, _ := c.Seal(a); again != a {
		t.Fatal("이미 봉한 값을 다시 봉함")
	}
	if e, _ := c.Seal(""); e != "" {
		t.Fatal("빈 값은 그대로여야 함")
	}
	if p, err := c.Open("123-456"); err != nil || p != "123-456" {
		t.Fatalf("이관 전 평문은 그대로 돌려줘야 함: %q %v", p, err)
	}
}

func TestOpenRejectsWrongKeyAndTamper(t *testing.T) {
	a, _ := box(t, 1).Seal("110-123-456789")
	if _, err := box(t, 2).Open(a); err == nil {
		t.Fatal("다른 키로 풀림")
	}
	if _, err := box(t, 1).Open(a[:len(a)-3] + "AAA"); err == nil {
		t.Fatal("변조된 값이 풀림")
	}
	if _, err := box(t, 1).Open(acctsecret.Prefix + "!!"); err == nil {
		t.Fatal("깨진 값이 풀림")
	}
}

func TestDecodeKey(t *testing.T) {
	if k, err := acctsecret.DecodeKey(" "); k != nil || err != nil {
		t.Fatalf("빈 키: %v %v", k, err)
	}
	if _, err := acctsecret.DecodeKey("***"); err == nil {
		t.Fatal("base64 가 아님")
	}
	if _, err := acctsecret.DecodeKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil || !strings.Contains(err.Error(), "32바이트") {
		t.Fatalf("길이: %v", err)
	}
	if k, err := acctsecret.DecodeKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))); err != nil || len(k) != 32 {
		t.Fatalf("정상 키: %v %v", k, err)
	}
}

package rbac

import "testing"

// DetectPII 는 MaskText 와 같은 패턴·순서로 센다: 전화는 계좌로 이중 집계되지 않고, 사업자번호·날짜는 세지 않는다.
func TestDetectPII(t *testing.T) {
	cases := []struct {
		text string
		want PIICount
	}{
		{"연락처 kim@example.com, 010-1234-5678", PIICount{Email: 1, Phone: 1}},
		{"계좌 110-123-456789 와 1002345678901", PIICount{Account: 2}},
		{"사업자번호 123-45-67890 은 세지 않는다. 날짜 2026-11-14 도 아니다.", PIICount{}},
		{"유선 02-123-4567", PIICount{Phone: 1}},
		{"", PIICount{}},
	}
	for _, c := range cases {
		if got := DetectPII(c.text); got != c.want {
			t.Errorf("%q: %+v (기대 %+v)", c.text, got, c.want)
		}
	}
}

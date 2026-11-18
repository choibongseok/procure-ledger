// Package rbac 는 마스킹 헬퍼와 권한 거부 기록 가드다.
// 역할 → 권한 표 자체는 contract.RolePermissions 가 유일한 정본이다.
package rbac

import (
	"regexp"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Masker 는 contract.Masker 구현이다.
type Masker struct{}

var _ contract.Masker = Masker{}

var (
	reDigit = regexp.MustCompile(`\d`)
	// 이메일 → 사업자번호 → 전화 → 계좌 순으로 치환한다(앞 패턴이 먼저 가려져 뒤 패턴과 겹치지 않는다).
	reEmail   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reBizNo   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{5}\b`)
	rePhone   = regexp.MustCompile(`\b(?:01\d|0\d{1,2})-\d{3,4}-\d{4}\b`)
	reAccount = regexp.MustCompile(`\b\d{2,6}(?:-\d{2,8}){1,3}\b|\b\d{10,14}\b`)
)

const maskedText = "[MASKED]"

// MaskField 는 원문을 보일 수 없으면 가린 값을 돌려준다. 원문은 mask:bypass 가 있고 MCP 채널이 아닐 때만이다(B41 D6: MCP 는 어떤 필드도 원문 금지).
// 빈 값은 그대로다.
func (Masker) MaskField(p contract.Principal, kind contract.FieldKind, value string) string {
	if value == "" || contract.RawFieldAllowed(p) {
		return value
	}
	switch kind {
	case contract.FieldBusinessNo:
		// 123-45-67890 → 123-**-*****
		return keepHeadDigits(value, 3)
	case contract.FieldAccountNo:
		// 마지막 4자리만 남긴다.
		return keepTailDigits(value, 4)
	case contract.FieldPhone:
		// 010-1234-5678 → 010-****-5678
		return maskPhone(value)
	case contract.FieldEmail:
		return maskEmail(value)
	default:
		return strings.Repeat("*", len([]rune(value)))
	}
}

// MaskText 는 자유 텍스트 안의 사업자번호·계좌·연락처 패턴을 무조건 가린다(LLM 전송 전).
func (Masker) MaskText(text string) string {
	text = reEmail.ReplaceAllString(text, maskedText)
	text = reBizNo.ReplaceAllString(text, maskedText)
	text = rePhone.ReplaceAllString(text, maskedText)
	// 계좌는 숫자가 10자리 이상인 것만 가린다(2026-11-10 같은 날짜를 건드리지 않는다).
	return reAccount.ReplaceAllStringFunc(text, func(m string) string {
		if len(reDigit.FindAllString(m, -1)) < 10 {
			return m
		}
		return maskedText
	})
}

func keepHeadDigits(s string, n int) string {
	seen := 0
	return reDigit.ReplaceAllStringFunc(s, func(d string) string {
		seen++
		if seen <= n {
			return d
		}
		return "*"
	})
}

func keepTailDigits(s string, n int) string {
	total := len(reDigit.FindAllString(s, -1))
	seen := 0
	return reDigit.ReplaceAllStringFunc(s, func(d string) string {
		seen++
		if seen > total-n {
			return d
		}
		return "*"
	})
}

func maskPhone(s string) string {
	parts := strings.Split(s, "-")
	if len(parts) == 3 {
		return parts[0] + "-" + strings.Repeat("*", len(parts[1])) + "-" + parts[2]
	}
	return keepTailDigits(s, 4)
}

func maskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return strings.Repeat("*", len([]rune(s)))
	}
	local := []rune(s[:at])
	return string(local[:1]) + strings.Repeat("*", len(local)-1) + s[at:]
}

// PIICount 는 자유 텍스트 안의 개인정보 패턴 개수다(이메일·휴대/유선 전화·계좌). 사업자번호는 개인정보 열람 기록 범주가 아니라 세지 않는다.
type PIICount struct{ Email, Phone, Account int }

// Any 는 하나라도 있는지다.
func (c PIICount) Any() bool { return c.Email+c.Phone+c.Account > 0 }

// DetectPII 는 MaskText 와 같은 패턴·순서로 개인정보 패턴을 센다(앞 패턴이 먼저 빠져 뒤 패턴과 겹치지 않는다). 값은 돌려주지 않는다.
func DetectPII(text string) PIICount {
	var c PIICount
	c.Email = len(reEmail.FindAllString(text, -1))
	text = reEmail.ReplaceAllString(text, " ")
	text = reBizNo.ReplaceAllString(text, " ")
	c.Phone = len(rePhone.FindAllString(text, -1))
	text = rePhone.ReplaceAllString(text, " ")
	for _, m := range reAccount.FindAllString(text, -1) {
		if len(reDigit.FindAllString(m, -1)) >= 10 {
			c.Account++
		}
	}
	return c
}

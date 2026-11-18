package mcp

import (
	"context"
	"errors"
	"log"
	"regexp"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// MCP 결과의 원문 감시(B41 D6, AC-4120). 도구 결과는 reply 가 나가기 직전 MaskText 를 거치고, MCP 채널은 서비스 마스킹(MaskField)도
// 어떤 필드도 원문을 내지 않으므로 나가는 결과에 원문 패턴이 있을 수 없다. 이 감시는 그 방어가 뚫린 경우(reply 를 거치지 않은 도구,
// 마스킹 규칙 회귀)를 잡는 마지막 줄이다: 원문 패턴이 보이면 호출을 거부하고(쓰기 도구는 이미 반영됐으므로 결과 글만 가린다) 이상 상태로 기록한다.

var (
	reExposeEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reExposeBiz   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{5}\b`)
	reExposePhone = regexp.MustCompile(`\b(?:01\d|0\d{1,2})-\d{3,4}-\d{4}\b`)
)

// ErrExposure 는 결과에 가려지지 않은 개인정보가 있어 돌려주지 않았다는 뜻이다.
var ErrExposure = errors.New("결과에 가려지지 않은 개인정보가 있어 돌려주지 않았습니다")

// exposedFields 는 결과 글에 원문 패턴이 남은 필드 이름이다. 마스킹이 이미 끝난 글은 MaskText 를 다시 거쳐도 변하지 않는다.
func exposedFields(m contract.Masker, r *sdk.CallToolResult) []string {
	if m == nil || r == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, c := range r.Content {
		tc, ok := c.(*sdk.TextContent)
		if !ok || m.MaskText(tc.Text) == tc.Text {
			continue
		}
		switch {
		case reExposeEmail.MatchString(tc.Text):
			add(privacy.FieldEmail)
		case reExposePhone.MatchString(tc.Text):
			add(privacy.FieldPhone)
		case reExposeBiz.MatchString(tc.Text):
			add(privacy.FieldBusinessNo)
		default:
			add(privacy.FieldAccountNo)
		}
	}
	return out
}

// scrub 는 결과 글을 MaskText 로 가린다(이미 반영된 쓰기 도구의 결과용).
func scrub(m contract.Masker, r *sdk.CallToolResult) {
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			tc.Text = m.MaskText(tc.Text)
		}
	}
}

// noteExposure 는 이상 상태를 기록한다. 기록 실패는 호출 거부를 바꾸지 않는다(원문은 어차피 나가지 않는다).
func noteExposure(ctx context.Context, n privacy.Noter, p contract.Principal, tool string, fields []string) {
	if n == nil {
		return
	}
	cat := privacy.CatAccount
	for _, f := range fields {
		if f == privacy.FieldEmail || f == privacy.FieldPhone {
			cat = privacy.CatContact
		}
	}
	// 정보주체는 알 수 없다: 기록기는 Anomaly 행을 정보주체 없이도 남겨야 한다.
	if err := n.Note(ctx, p, privacy.Access{Category: cat, Screen: "MCP " + tool, Fields: fields, Anomaly: true}); err != nil {
		log.Printf("mcp: 이상 상태 기록 실패 도구=%s: %v", tool, err)
	}
}

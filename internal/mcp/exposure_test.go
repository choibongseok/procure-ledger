package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// B41 D6·AC-4120: 나가는 MCP 결과에 원문 패턴이 있으면 호출을 거부하고 이상 상태로 기록한다. 마스킹이 끝난 결과는 건드리지 않는다.

type anomalyRec struct {
	got  []privacy.Access
	fail error
}

func (r *anomalyRec) Note(_ context.Context, _ contract.Principal, a privacy.Access) error {
	if r.fail != nil {
		return r.fail
	}
	if !privacy.ValidScreen(a.Screen) {
		return fmt.Errorf("허용되지 않은 화면 이름: %q", a.Screen)
	}
	r.got = append(r.got, a)
	return nil
}

func (r *anomalyRec) NoteTx(ctx context.Context, _ contract.Tx, p contract.Principal, a privacy.Access) error {
	return r.Note(ctx, p, a)
}

// testNoter 는 runGuard 가 Deps.Privacy 로 꽂는 시험용 기록기다(nil 이면 기록 없이 거부만).
var testNoter privacy.Noter

func textResult(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

// runGuard 는 guard 미들웨어에 가짜 도구 결과를 흘려 보낸다.
func runGuard(t *testing.T, tool string, produce *sdk.CallToolResult) (sdk.Result, *fake.Bundle) {
	t.Helper()
	b := fake.NewBundle()
	d := b.Deps()
	d.Masker = rbac.Masker{} // 실제 마스커(이메일 패턴 포함)
	d.Privacy = testNoter
	table := &contract.ToolTable{}
	table.Declare(tool)
	h := guard(d, fixed(contract.Principal{UserID: 3, Name: "재무", Role: contract.RoleFinance, Channel: contract.ChannelMCP}), table)(
		func(context.Context, string, sdk.Request) (sdk.Result, error) { return produce, nil })
	args, _ := json.Marshal(map[string]any{})
	res, err := h(context.Background(), "tools/call", &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	return res, b
}

func resultText(r sdk.Result) string {
	cr, _ := r.(*sdk.CallToolResult)
	var sb strings.Builder
	for _, c := range cr.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestGuardRejectsRawPatternsAndNotesAnomaly(t *testing.T) {
	rec := &anomalyRec{}
	testNoter = rec
	t.Cleanup(func() { testNoter = nil })
	cases := []struct {
		name, text string
		field, cat string
	}{
		{"사업자번호", `{"business_no":"123-45-67890"}`, privacy.FieldBusinessNo, privacy.CatAccount},
		{"이메일", `{"email":"kim@pl.test"}`, privacy.FieldEmail, privacy.CatContact},
		{"휴대전화", `{"phone":"010-1234-5678"}`, privacy.FieldPhone, privacy.CatContact},
		{"계좌", `{"account":"110-123-456789"}`, privacy.FieldAccountNo, privacy.CatAccount},
	}
	for _, c := range cases {
		rec.got = nil
		res, b := runGuard(t, contract.ToolGetPayables, textResult(c.text))
		cr := res.(*sdk.CallToolResult)
		if !cr.IsError || !errors.Is(cr.GetError(), ErrExposure) {
			t.Fatalf("%s: 거부되어야 함: %+v", c.name, cr)
		}
		if got := resultText(res); strings.Contains(got, "123-45") || strings.Contains(got, "kim@") || strings.Contains(got, "1234-5678") || strings.Contains(got, "456789") {
			t.Fatalf("%s: 거부 결과에 원문이 있다: %s", c.name, got)
		}
		if len(rec.got) != 1 {
			t.Fatalf("%s: 이상 상태 기록 %d건", c.name, len(rec.got))
		}
		a := rec.got[0]
		if !a.Anomaly || a.Category != c.cat || a.Screen != "MCP "+contract.ToolGetPayables || len(a.Fields) != 1 || a.Fields[0] != c.field || a.Export {
			t.Fatalf("%s: 기록 내용 %+v", c.name, a)
		}
		// 감사 상세에 이상 상태가 표시된다(값은 없다).
		found := false
		for _, e := range b.Audit.Records() {
			if strings.Contains(e.Detail, "mcp_anomaly") {
				found = true
				if strings.Contains(fmt.Sprintf("%+v", e), "123-45") {
					t.Fatal("감사에 원문이 있다")
				}
			}
		}
		if !found {
			t.Fatalf("%s: 감사 상세에 mcp_anomaly 가 없다", c.name)
		}
	}
	// 가려진 결과·금액·날짜는 그대로 나간다.
	rec.got = nil
	for _, ok := range []string{`{"business_no":"123-**-*****","phone":"010-****-5678","amount":1234567,"day":"2026-11-10","no":"PR-000001"}`, `{"note":"[MASKED]"}`} {
		res, _ := runGuard(t, contract.ToolGetPayables, textResult(ok))
		if cr := res.(*sdk.CallToolResult); cr.IsError || resultText(res) != ok {
			t.Fatalf("정상 결과가 막힘: %s -> %+v", ok, cr)
		}
	}
	if len(rec.got) != 0 {
		t.Fatalf("정상 결과가 이상으로 기록됨: %+v", rec.got)
	}
}

// 이미 반영된 쓰기 도구는 거부로 바꾸지 않고(재시도로 중복 생성되지 않게) 결과 글만 가린다. 이상 상태는 기록한다.
func TestGuardScrubsWriteToolResultInsteadOfFailing(t *testing.T) {
	rec := &anomalyRec{}
	testNoter = rec
	t.Cleanup(func() { testNoter = nil })
	res, _ := runGuard(t, contract.ToolCreatePRDraft, textResult(`{"pr_id":7,"vendor_biz":"123-45-67890"}`))
	cr := res.(*sdk.CallToolResult)
	if cr.IsError || strings.Contains(resultText(res), "123-45-67890") || !strings.Contains(resultText(res), `"pr_id":7`) {
		t.Fatalf("글만 가려야 함: %+v %s", cr, resultText(res))
	}
	if len(rec.got) != 1 || !rec.got[0].Anomaly {
		t.Fatalf("이상 상태 기록: %+v", rec.got)
	}
}

// 이상 상태 기록이 실패해도 원문은 나가지 않는다.
func TestGuardRejectsEvenWhenAnomalyNoteFails(t *testing.T) {
	testNoter = &anomalyRec{fail: errors.New("x")}
	t.Cleanup(func() { testNoter = nil })
	res, _ := runGuard(t, contract.ToolGetPayables, textResult(`{"e":"kim@pl.test"}`))
	if cr := res.(*sdk.CallToolResult); !cr.IsError || strings.Contains(resultText(res), "kim@") {
		t.Fatalf("거부되어야 함: %+v", cr)
	}
}

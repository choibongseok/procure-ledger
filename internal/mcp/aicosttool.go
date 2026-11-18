package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
)

// B49 get_ai_cost_summary( 13절): 부서의 월 AI 비용 요약. 읽기 전용이다.
// 기본은 호출자 본인 부서이고 다른 부서는 서비스가 cost:read 로 판정한다(우회 경로 없음). 규칙·환율·확정·전표는 도구로 열지 않는다.

const aiCostNote = "호출 수와 금액만 담은 조회 결과입니다. 프롬프트·답변·사용자 이름은 없습니다. 환율·청구액·배부 확정은 이 도구로 할 수 없습니다."

type aiCostSummaryIn struct {
	Month      string `json:"month,omitempty" jsonschema:"월 YYYY-MM(기본 이번 달)"`
	Department string `json:"department,omitempty" jsonschema:"부서 이름(기본 본인 부서, 다른 부서는 AI 비용 조회 권한이 필요)"`
}

func getAICostSummaryTool(srv *sdk.Server, d deps.Deps, table *contract.ToolTable) {
	table.Declare(contract.ToolGetAICostSummary)
	sdk.AddTool(srv, &sdk.Tool{Name: contract.ToolGetAICostSummary, Description: "부서의 월 AI 사용 비용(호출 수·금액, 용도별)을 조회한다. 기본은 본인 부서이고 다른 부서는 AI 비용 조회 권한이 있어야 한다. 읽기 전용이며 프롬프트·답변·사용자 이름은 담기지 않는다."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in aiCostSummaryIn) (*sdk.CallToolResult, any, error) {
			if d.AICostTool == nil {
				return nil, nil, fmt.Errorf("AI 비용이 설정되지 않았습니다")
			}
			month := strings.TrimSpace(in.Month)
			if month != "" && !reMonth.MatchString(month) {
				return nil, nil, fmt.Errorf("월은 YYYY-MM 형식으로 입력하세요")
			}
			out, err := d.AICostTool.ToolSummary(ctx, month, in.Department)
			if err != nil {
				return nil, nil, err
			}
			out["department"] = cleanText(fmt.Sprint(out["department"]))
			out["note"] = aiCostNote
			return reply(d, out)
		})
}

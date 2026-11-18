package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B27 get_month_close_status( 10절): 재고 월 마감 상태 요약. 조회 전용이다.
// 마감·해제·내보내기는 도구로 열지 않는다. 마감자 이름·전표 내용·품목별 값은 내보내지 않는다.
// 기말 금액은 서비스가 호출자의 inventory:valuation_read 로 거르고(권한이 없으면 nil), 도구는 nil 이면 출력에서 뺀다.

var monthStateLabels = map[string]string{
	domain.MonthOpen:           "열림",
	domain.MonthClosed:         "마감됨",
	domain.MonthReleasePending: "해제 결재 중",
}

type monthCloseIn struct {
	Month string `json:"month,omitempty" jsonschema:"조회할 월 YYYY-MM(기본 직전 달)"`
}

// previousMonth 는 KST 기준 직전 달이다.
func previousMonth(now time.Time) string {
	t := now.In(contract.KST)
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, contract.KST)
	return first.AddDate(0, -1, 0).Format("2006-01")
}

func getMonthCloseStatusTool(srv *sdk.Server, d deps.Deps, table *contract.ToolTable) {
	table.Declare(contract.ToolGetMonthCloseStatus)
	sdk.AddTool(srv, &sdk.Tool{Name: contract.ToolGetMonthCloseStatus, Description: "재고 월 마감 상태(열림·마감됨·해제 결재 중), 마감 일시·버전, 마감 전 점검의 차단·경고 건수를 조회한다(기본 직전 달). 기말 금액 합계는 금액 조회 권한이 있는 호출자에게만 나온다. 마감 규정 조항은 search_policy, 품목의 입출고 내역은 get_item_ledger 를 쓴다. 마감·마감 해제는 이 도구로 할 수 없다."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in monthCloseIn) (*sdk.CallToolResult, any, error) {
			if d.MonthClose == nil {
				return nil, nil, fmt.Errorf("월 마감이 설정되지 않았습니다")
			}
			month := strings.TrimSpace(in.Month)
			if month != "" && !reMonth.MatchString(month) {
				return nil, nil, fmt.Errorf("월은 YYYY-MM 형식으로 입력하세요")
			}
			if month == "" {
				month = previousMonth(nowFunc())
			}
			st, err := d.MonthClose.Status(ctx, month)
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{
				"month": cleanTextN(st.Month, 7), "status": label(monthStateLabels, st.State, "열림"), "version": st.Version,
				"blockers": st.Blockers, "warnings": st.Warnings,
				"note": "수량·건수만 담은 조회 결과입니다. 마감과 마감 해제는 이 도구로 할 수 없습니다.",
			}
			if !st.ClosedAt.IsZero() {
				out["closed_at"] = st.ClosedAt.In(contract.KST).Format("2006-01-02 15:04")
			}
			if st.CloseAmount != nil {
				out["close_amount"] = *st.CloseAmount
			}
			if st.Blockers > 0 && st.State == domain.MonthOpen {
				out["message"] = fmt.Sprintf("마감 전 점검의 차단 항목이 %d건 있어 지금은 마감할 수 없습니다. 자세한 목록은 월 마감 화면에서 확인하세요.", st.Blockers)
			}
			return reply(d, out)
		})
}

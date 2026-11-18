package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/mcp"
)

func domainInput(c evals.DocCase, poID, rcID int64) domain.VerifyInput {
	in := domain.VerifyInput{Filename: c.ID + ".txt", ContentType: "text/plain", POID: poID, ReceiptID: rcID}
	switch {
	case c.Format == "pdf":
		in.Image, in.ContentType, in.Filename = kit.KoreanTextPDF(strings.Split(c.Doc, "\n")), docverify.PDFContentType, c.ID+".pdf"
	case c.Format == "pdf_garbled":
		in.Image, in.ContentType, in.Filename = kit.KoreanTextPDFWithoutToUnicode(strings.Split(c.Doc, "\n")), docverify.PDFContentType, c.ID+".pdf"
	case c.AsImage:
		in.Image, in.ContentType, in.Filename = []byte(c.Doc), "image/png", c.ID+".png"
	default:
		in.Text = c.Doc
	}
	return in
}

// 도구 선택 프롬프트의 도구 설명. 이름 목록은 contract.MCPToolPermissions 와 같아야 하며 테스트가 확인한다.
var toolHelp = map[string]string{
	contract.ToolSearchItems:          `품목 검색. 인자 {"query": 문자열}`,
	contract.ToolGetStock:             `품목 재고 조회. 인자 {"item_id": 정수}`,
	contract.ToolListPendingApprovals: `내가 결재할 차례인 구매요청 목록. 인자 없음`,
	contract.ToolGetPurchaseRequest:   `구매요청(구매 품의서) 상세 조회. 인자 {"pr_id": 정수}`,
	contract.ToolCreatePRDraft:        `구매요청 임시저장 작성. 인자 {"title": 문자열, "lines": [...]}`,
	contract.ToolApproveOrReject:      `구매요청(구매 품의서) 승인/반려. 양식 문서(기안서·결의서·협조전)는 결재할 수 없다. 인자 {"pr_id": 정수, "approve": 불리언, "comment": 문자열}`,
	contract.ToolVerifyDocument:       `증빙 문서(거래명세서 등) 대조·판정. 결재 문서와 다르다. 인자 {"po_id": 정수}`,
	contract.ToolListApprovalForms:    `기안할 수 있는 결재 양식 목록(코드를 주면 입력 칸). 인자 {"code": 문자열(선택)}`,
	contract.ToolGetPayables:          `거래처별 매입 채무(외상매입금) 미지급 잔액·만기 조회(재무·관리자·감사). 계좌번호 없음. 규정은 search_policy. 인자 {"as_of": "YYYY-MM-DD"(선택), "vendor": 문자열(선택)}`,
	contract.ToolGetTaxInvoices:       `전자세금계산서(매출·매입) 목록·상태·대조 상태 조회(재무·관리자·감사). 사업자번호는 가린 값. 인자 {"direction": "sale"|"purchase"(선택), "status": 상태 이름(선택), "from": "YYYY-MM-DD"(선택), "to": "YYYY-MM-DD"(선택), "counterparty": 문자열(선택), "page": 정수(선택)}`,
	contract.ToolGetCardUnreconciled:  `내가 소지한 법인카드의 미소명 사용 내역 조회(본인분만, 카드번호 없음). 인자 {"limit": 정수(선택)}`,
	contract.ToolGetMonthCloseStatus:  `재고 월 마감 상태·마감 전 점검 건수 조회(기본 직전 달). 마감 규정 조항은 search_policy. 인자 {"month": "YYYY-MM"(선택)}`,
	contract.ToolGetBudgetStatus:      `부서 월 구매 예산 잔액·사용률 조회, 금액을 주면 초과 여부 확인(기본 본인 부서). 규정 조항은 search_policy. 인자 {"month": "YYYY-MM"(선택), "amount": 정수 원(선택), "department": 문자열(선택)}`,
	contract.ToolGetAICostSummary:     `부서의 월 AI 사용 비용(호출 수·금액, 용도별) 조회(기본 본인 부서). 구매 예산은 get_budget_status. 인자 {"month": "YYYY-MM"(선택), "department": 문자열(선택)}`,
	contract.ToolGetItemLedger:        `품목 하나의 수불부(기초·입고·출고·기말·입출고 내역). 현재 수량만은 get_stock. 인자 {"item_id": 정수, "warehouse": 문자열(선택), "lot_no": 문자열(선택), "from": "YYYY-MM-DD"(선택), "to": "YYYY-MM-DD"(선택)}`,
	contract.ToolWhoApproves:          `문서(구매 품의서·일반 기안서·지출 결의서·업무 협조전)를 올리면 누가 결재·합의하는지 결재선 미리보기(상신 안 함). 대기 목록은 list_pending_approvals, 규정 조항은 search_policy. 인자 {"form_code": 문자열(선택), "amount": 정수 원(선택), "requester_name": 문자열(선택), "field_unit": 문자열(선택)}`,
	contract.ToolSearchPeople:         `사람(직원)을 이름·직위·직책·조직으로 찾기(소속·겸직·내선, 연락처 없음). 조직 구성은 get_org_unit. 인자 {"query": 문자열, "unit": 문자열(선택), "as_of": "YYYY-MM-DD"(선택)}`,
	contract.ToolGetOrgUnit:           `조직(팀·부서·본부) 하나의 상위 경로·조직장·하위 조직·인원 수 조회. 사람 찾기는 search_people. 인자 {"unit": 문자열, "as_of": "YYYY-MM-DD"(선택)}`,
	contract.ToolListMyApprovalDocs:   `내 전자결재 문서함(기안함·결재 대기함·결재 진행함·완료함·참조함·수신함·반려함) 목록과 건수. 구매요청만의 결재 대기 목록은 list_pending_approvals. 인자 {"box": 문서함(선택), "tab": 수신함 상태(선택)}`,
	contract.ToolGetMyLeaveBalance:    `내 남은 연차·대체휴가(일·분, 신청 중 예약분, 30일 내 소멸분) 조회. 본인 것만, 남의 잔여·휴가 사유·신청은 불가. 인자 없음`,
	contract.ToolListTeamAbsence:      `기간(최대 31일)의 팀 휴가·부재 일정 조회(보이는 범위는 내 권한, "휴가"/"부재"만, 사유·병가 구분 없음). 내 잔여는 get_my_leave_balance. 인자 {"from": "YYYY-MM-DD"(선택), "to": "YYYY-MM-DD"(선택), "unit": 조직 이름(선택)}`,
	contract.ToolGetApprovalDoc:       `양식 문서(일반 기안서·지출 결의서·업무 협조전) 한 건 조회. 구매요청은 get_purchase_request. 인자 {"doc_id": 정수}`,
	contract.ToolListStockAlerts:      `안전재고 미달·유효기한 임박 목록 조회. 인자 {"within_days": 정수(선택)}`,
	contract.ToolSearchPolicy:         `사내 규정(구매·결재·재고) 조항 검색. 인자 {"query": 문자열}`,
	contract.ToolSearchKnowledge:      `완료된 결재 문서 본문·첨부·업로드 매뉴얼의 내용 검색(마스킹된 발췌와 출처). 규정 조항은 search_policy. 인자 {"query": 문자열, "limit": 정수(선택)}`,
	contract.ToolDraftReorder:         `미달 품목으로 구매요청 임시저장 초안 작성(상신·결재는 하지 않는다). 인자 {"item_ids": [정수], "vendor_id": 정수(선택)}`,
}

func toolSystemPrompt() string {
	names := make([]string, 0, len(toolHelp))
	for n := range toolHelp {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("너는 사내 ERP 도구 선택기다. 사용자 요청에 맞는 도구 하나를 고른다. 맞는 도구가 없으면 tool 을 빈 문자열로 한다.\n")
	sb.WriteString("출력은 JSON 한 개만: {\"tool\":\"도구이름\",\"arguments\":{...}}\n도구:\n")
	for _, n := range names {
		sb.WriteString("- " + n + ": " + toolHelp[n] + "\n")
	}
	return sb.String()
}

type selection struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func parseSelection(text string) (selection, error) {
	s := strings.TrimSpace(text)
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```"), "```"))
	var sel selection
	if err := json.Unmarshal([]byte(s), &sel); err != nil {
		return sel, fmt.Errorf("선택 결과를 해석할 수 없음")
	}
	return sel, nil
}

func sameValue(want, got any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && g == w
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	}
	return false
}

func principalFor(role string, idx int) contract.Principal {
	switch contract.Role(role) {
	case contract.RoleStaff, contract.RoleFinance:
		return kit.Principal(contract.Role(role))
	}
	return contract.Principal{UserID: int64(200 + idx), Name: role, Role: contract.Role(role), DepartmentID: kit.DeptID, Channel: contract.ChannelWeb}
}

func runTools(ctx context.Context, gw *llmgw.Gateway, cases []evals.ToolCase) []CaseResult {
	var out []CaseResult
	for i, c := range cases {
		cr := CaseResult{Suite: "tools", ID: c.ID, Critical: c.Critical}
		p := principalFor(c.Role, i)
		pctx := contract.WithPrincipal(ctx, p)
		resp, err := gw.Complete(pctx, llm.Request{
			Purpose: llmgw.PurposeToolSelect, System: toolSystemPrompt(), MaxTokens: 512,
			Messages: []llm.Message{{Role: "user", Content: c.Request}},
		})
		if err != nil {
			cr.Detail = "LLM 호출 오류: " + err.Error()
			out = append(out, cr)
			continue
		}
		sel, err := parseSelection(resp.Text)
		if err != nil {
			cr.Detail = err.Error()
			out = append(out, cr)
			continue
		}
		var problems []string
		if sel.Tool != c.ExpectTool {
			problems = append(problems, fmt.Sprintf("도구 %q (기대 %q)", sel.Tool, c.ExpectTool))
		} else {
			for k, want := range c.ExpectArgs {
				if !sameValue(want, sel.Arguments[k]) {
					problems = append(problems, fmt.Sprintf("인자 %s=%v (기대 %v)", k, sel.Arguments[k], want))
				}
			}
			if sel.Tool != "" {
				allowed, err := callAllowed(ctx, p, sel)
				if err != nil {
					problems = append(problems, "MCP 호출 오류: "+err.Error())
				} else if allowed != c.ExpectAllowed {
					problems = append(problems, fmt.Sprintf("권한 결과 허용=%v (기대 %v)", allowed, c.ExpectAllowed))
				}
			}
		}
		cr.Pass = len(problems) == 0
		cr.Detail = fmt.Sprintf("도구 %q", sel.Tool)
		if !cr.Pass {
			cr.Detail = strings.Join(problems, "; ")
		}
		out = append(out, cr)
	}
	return out
}

// callAllowed 는 선택된 도구를 실제 MCP 서버(웹과 같은 권한 표)에 호출해 권한 거부 여부를 본다.
// 서비스가 가짜(빈 데이터)이므로 권한 밖 오류(없는 대상 등)는 "허용"으로 센다.
func callAllowed(ctx context.Context, p contract.Principal, sel selection) (bool, error) {
	b := fake.NewBundle()
	srv, _ := mcp.NewServer(b.Deps(), p)
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return false, err
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "eval", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return false, err
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: sel.Tool, Arguments: sel.Arguments})
	if err != nil {
		return false, err
	}
	if !res.IsError {
		return true, nil
	}
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok && strings.Contains(tc.Text, contract.ErrForbidden.Error()) {
			return false, nil
		}
	}
	return true, nil
}

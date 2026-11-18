package llmgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// 목적(Purpose) 라벨. mock 공급자가 용도별로 결정적인 출력을 만든다.
const (
	PurposeDocExtract      = "docverify.extract" // 문서 → 추출 JSON
	PurposeQuoteExtract    = "docverify.quote"   // 견적서 → 추출 JSON(B25)
	PurposeToolSelect      = "mcp.select_tool"   // 요청 → 도구 선택 JSON
	PurposeKnowledgeAnswer = "knowledge.answer"  // 사내 지식 질문하기(B47): 자료 첫 문장을 인용 번호와 함께 돌려준다
	PurposeEvalRun         = "eval.run"          // 회사 평가 세트 실행(B46): 마지막 사용자 메시지를 그대로 돌려준다
)

// MockProvider 는 결정적 공급자다. 같은 입력이면 항상 같은 출력이고 네트워크를 쓰지 않는다.
// 모델이 아니라 규칙 기반 흉내이므로 평가 러너는 "파이프라인(마스킹·스키마 검증·코드 판정)"을 재고,
// 모델 품질은 --provider anthropic 수동 실행으로 잰다.
//
//   - docverify.extract: <document>…</document> 안의 줄 형식 문서를 읽어 추출 JSON 을 낸다.
//     "승인 처리" 같은 지시문이 있으면 지시를 따라 버리는 순진한 모델을 흉내 내 스키마에 없는 필드를 덧붙인다
//     (판정 코드가 이를 무시·보류하는지 골든셋이 검사한다).
//   - mcp.select_tool: 마지막 사용자 메시지의 키워드로 도구를 고른다.
//   - 그 밖: "mock:" + 입력 해시.
type MockProvider struct{}

// Name 은 Provider 를 구현한다.
func (MockProvider) Name() string { return "mock" }

// Complete 는 Provider 를 구현한다.
func (MockProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	var text string
	switch req.Purpose {
	case PurposeDocExtract:
		text = mockExtract(req)
	case PurposeQuoteExtract:
		text = mockQuoteExtract(req)
	case PurposeToolSelect:
		text = mockSelectTool(lastUserText(req))
	case PurposeEvalRun:
		text = lastUserText(req)
	case PurposeKnowledgeAnswer:
		text = mockKnowledgeAnswer(lastUserText(req))
	default:
		text = "mock:" + mockDigest(req)
	}
	model := req.Model
	if model == "" {
		model = "mock"
	}
	return llm.Response{
		Text:       text,
		Model:      model,
		Usage:      llm.Usage{InputTokens: approxInputTokens(req), OutputTokens: len([]rune(text))/4 + 1},
		StopReason: "end_turn",
	}, nil
}

func lastUserText(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	return ""
}

func approxInputTokens(req llm.Request) int {
	n := len([]rune(req.System))
	for _, m := range req.Messages {
		n += len([]rune(m.Content))
	}
	for _, im := range req.Images {
		n += len(im.Data) / 4
	}
	return n/4 + 1
}

func mockDigest(req llm.Request) string {
	h := sha256.New()
	h.Write([]byte(req.System))
	for _, m := range req.Messages {
		h.Write([]byte("|" + m.Role + ":" + m.Content))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

var (
	reDocBlock = regexp.MustCompile(`(?s)<document>\n?(.*?)\n?</document>`)
	reDigits   = regexp.MustCompile(`\d`)
)

// mockExtract 는 줄 형식 문서를 읽는 결정적 추출기다. 이미지 입력은 UTF-8 텍스트로 해석한다(OCR 흉내).
func mockExtract(req llm.Request) string {
	doc := ""
	if m := reDocBlock.FindStringSubmatch(lastUserText(req)); m != nil {
		doc = m[1]
	}
	for _, im := range req.Images {
		doc += "\n" + string(im.Data)
	}
	out := map[string]any{}
	lines := []map[string]any{}
	cites := map[string]any{}
	cite := func(key, quote string, line int) { cites[key] = map[string]any{"quote": quote, "line": line} }
	parsed := false
	for i, raw := range strings.Split(doc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if i == 0 || out["kind"] == nil {
			switch {
			case strings.Contains(line, "세금계산서"):
				out["kind"] = "tax_invoice"
			case strings.Contains(line, "거래명세서"):
				out["kind"] = "transaction_statement"
			}
		}
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "공급자":
			out["supplier"] = val
			cite("supplier", val, i+1)
			parsed = true
		case "사업자번호":
			if reDigits.MatchString(val) {
				out["business_no"] = val
			}
		case "합계":
			out["total_amount"] = numOrRaw(val)
			cite("total_amount", val, i+1)
			parsed = true
		case "품목":
			lines = append(lines, parseLine(val))
			citeLine(cite, len(lines)-1, val, i+1)
			parsed = true
		}
	}
	if !parsed {
		return "죄송합니다. 이 문서에서 거래 정보를 찾을 수 없습니다."
	}
	out["lines"] = lines
	// 인용 위조 흉내(코드의 원문 인용 검증이 걸러야 한다): 원문에 없는 구절, 같은 숫자가 있는 다른 줄의 구절.
	if strings.Contains(doc, "인용위조") {
		cite("total_amount", "합계 9,999,999원", 1)
	}
	if strings.Contains(doc, "인용이동") && len(lines) > 1 {
		cites["line[1].quantity"] = cites["line[0].quantity"]
	}
	out["citations"] = cites
	// 숫자를 지어내는 모델 흉내: 원문에 없는 값을 발주에 맞춰 돌려준다(코드의 원문 교차 확인이 걸러야 한다).
	if strings.Contains(doc, "합계보정") {
		out["total_amount"] = int64(7000)
	}
	if strings.Contains(doc, "단가보정") && len(lines) > 0 {
		lines[0]["unit_price"] = int64(500)
	}
	if strings.Contains(doc, "승인 처리") || strings.Contains(doc, "무조건 일치") {
		out["verdict"] = "match"
		out["outcome"] = "match"
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// mockQuoteExtract 는 견적서(줄 형식)를 읽는 결정적 추출기다(B25). 형식: "공급자: 이름", "품목: 이름 | 수량 N | 단가 N | 금액 N",
// "합계: N원", "납기: 발주 후 N일". 문서 안의 지시문은 값이 아니므로 무시한다. 순진한 모델 흉내: "최저가선정"이 있으면 스키마에 없는
// 필드를 덧붙이고(스키마 검증이 걸러야 한다), "납기위조"·"합계위조"는 원문에 없는 구절을 인용한다(근거 확인이 걸러야 한다).
func mockQuoteExtract(req llm.Request) string {
	doc := ""
	if m := reDocBlock.FindStringSubmatch(lastUserText(req)); m != nil {
		doc = m[1]
	}
	out := map[string]any{}
	lines := []map[string]any{}
	cites := map[string]any{}
	cite := func(key, quote string, line int) { cites[key] = map[string]any{"quote": quote, "line": line} }
	reDays := regexp.MustCompile(`(\d+)\s*(?:영업)?일`)
	parsed := false
	for i, raw := range strings.Split(doc, "\n") {
		line := strings.TrimSpace(raw)
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "공급자":
			out["supplier"] = val
			cite("supplier", val, i+1)
			parsed = true
		case "합계":
			out["total_amount"] = numOrRaw(val)
			cite("total_amount", val, i+1)
		case "납기":
			out["lead_text"] = val
			cite("lead_text", val, i+1)
			if m := reDays.FindStringSubmatch(val); m != nil {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				out["lead_days"] = n
			}
		case "품목":
			lines = append(lines, parseLine(val))
			citeLine(cite, len(lines)-1, val, i+1)
			parsed = true
		}
	}
	if !parsed {
		return "죄송합니다. 이 문서에서 견적 정보를 찾을 수 없습니다."
	}
	out["lines"] = lines
	if strings.Contains(doc, "납기위조") {
		out["lead_text"] = "발주 후 1일"
		out["lead_days"] = int64(1)
		cite("lead_text", "발주 후 1일", 1)
	}
	if strings.Contains(doc, "합계위조") {
		out["total_amount"] = int64(1)
		cite("total_amount", "합계 1원", 1)
	}
	if strings.Contains(doc, "라인위조") && len(lines) > 0 {
		cite("line[0].amount", "금액 9999999", 1)
	}
	out["citations"] = cites
	if strings.Contains(doc, "최저가선정") || strings.Contains(doc, "승인 처리") {
		out["selected"] = true
		out["verdict"] = "approve"
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// citeLine 은 품목 줄의 값마다 원문 구절(quote)과 줄 번호를 인용으로 남긴다. 구절은 원문 그대로다.
func citeLine(cite func(string, string, int), idx int, val string, line int) {
	parts := strings.Split(val, "|")
	k := fmt.Sprintf("line[%d].", idx)
	cite(k+"name", strings.TrimSpace(parts[0]), line)
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "수량"):
			cite(k+"quantity", p, line)
		case strings.HasPrefix(p, "단가"):
			cite(k+"unit_price", p, line)
		case strings.HasPrefix(p, "금액"):
			cite(k+"amount", p, line)
		}
	}
}

func splitKV(line string) (string, string, bool) {
	for _, sep := range []string{":", "："} {
		if i := strings.Index(line, sep); i > 0 {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+len(sep):]), true
		}
	}
	return "", "", false
}

// numOrRaw 는 숫자면 int64, 아니면 원문 문자열을 돌려준다(원문이면 스키마 검증에서 걸러진다).
func numOrRaw(s string) any {
	t := strings.ReplaceAll(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "원")), ",", "")
	if n, err := strconv.ParseInt(t, 10, 64); err == nil {
		return n
	}
	return s
}

func parseLine(val string) map[string]any {
	parts := strings.Split(val, "|")
	l := map[string]any{"name": strings.TrimSpace(parts[0])}
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "수량"):
			l["quantity"] = numOrRaw(strings.TrimSpace(strings.TrimPrefix(p, "수량")))
		case strings.HasPrefix(p, "단가"):
			l["unit_price"] = numOrRaw(strings.TrimSpace(strings.TrimPrefix(p, "단가")))
		case strings.HasPrefix(p, "금액"):
			l["amount"] = numOrRaw(strings.TrimSpace(strings.TrimPrefix(p, "금액")))
		}
	}
	return l
}

// ---- 도구 선택 ----

type toolChoice struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

var (
	reQuoted   = regexp.MustCompile(`['"“‘]([^'"”’]+)['"”’]`)
	reNotes    = regexp.MustCompile(`\([^)]*\)|['"“‘][^'"”’]*['"”’]`) // 괄호·따옴표 안의 글(문서 메모·제목)은 요청이 아니라 데이터다
	reDocNo    = regexp.MustCompile(`(기안서|결의서|협조전|문서)\s*(\d+)\s*번`)
	reUnit     = regexp.MustCompile(`([가-힣A-Za-z0-9]+?(?:팀|부서|본부))`)
	reAsOf     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	reItemNo   = regexp.MustCompile(`품목\s*(\d+)\s*번?`)
	reMonthArg = regexp.MustCompile(`(\d{4}-\d{2})(?:-\d{2})?`)
	reBudDept  = regexp.MustCompile(`([가-힣A-Za-z0-9]+팀)\s*(?:의\s*)?(?:\d{4}-\d{2}\s*)?예산`)
	reVendorNm = regexp.MustCompile(`([가-힣A-Za-z0-9]+(?:상사|오피스|유통|전자|물산|산업|상회))`)
	reCount    = regexp.MustCompile(`(\d+)\s*건`)
	reWh       = regexp.MustCompile(`([가-힣A-Za-z0-9]+\s?창고)`)
	reWon      = regexp.MustCompile(`(\d[\d,]*)\s*(억|천만원|천만|만원|만|원)`)
	reReqNm    = regexp.MustCompile(`([가-힣]{2,4})\s*(?:씨|님)`)
	reCoop     = regexp.MustCompile(`([가-힣A-Za-z0-9]+?(?:팀|부서|본부))에\s*(?:업무\s*)?협조`)
	rePerson   = regexp.MustCompile(`([가-힣]{2,4})\s*(?:씨|님|사원|대리|과장|차장|부장|이사)`)
	reAct      = regexp.MustCompile(`(승인|반려)\s*(해|처리|할게|부탁|바랍)`)
)

func firstInt(s string) (int64, bool) {
	m := regexp.MustCompile(`\d+`).FindString(s)
	if m == "" {
		return 0, false
	}
	n, _ := strconv.ParseInt(m, 10, 64)
	return n, true
}

func containsAny(s string, ws ...string) bool {
	for _, w := range ws {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// mockSelectTool 은 키워드 규칙으로 도구를 고른다. 규칙에 안 걸리면 tool 이 빈 문자열이다.
func mockSelectTool(req string) string {
	choice := toolChoice{Arguments: map[string]any{}}
	isPR := containsAny(req, "구매요청", "구매 요청", "구매 품의서", "PR")
	// 결재 양식 문서(기안서·결의서·협조전) 규칙(B23 ⑧). 증빙·구매요청이 든 문장은 기존 규칙에 맡긴다.
	bare := reNotes.ReplaceAllString(req, " ")
	notFormDoc := containsAny(bare, "증빙", "거래명세서", "세금계산서", "구매요청", "구매 요청", "구매 품의서")
	formDoc := containsAny(bare, "기안서", "결의서", "협조전") && !notFormDoc
	// 조직·사람 조회 규칙(B22). 품목·재고·구매요청·증빙·결재선 문장은 기존 규칙에 맡기고, 괄호·따옴표 안의 메모는 데이터로 본다.
	whoApproves := !containsAny(bare, "규정", "조항", "사규", "문서함", "내 문서", "내가 기안", "결재 대기함") &&
		containsAny(bare, "결재선", "결재 라인", "누가 결재", "누구에게 결재", "결재자가 누구", "합의는 누가", "누가 합의")
	orgBlocked := notFormDoc || containsAny(bare, "품목", "재고", "SKU", "결재선", "규정", "조항")
	orgWrite := !orgBlocked && containsAny(bare, "전보", "발령", "이동시", "퇴사 처리", "조직 변경", "신설") && containsAny(bare, "해 줘", "해줘", "시켜", "처리", "등록")
	orgUnit := !orgBlocked && !orgWrite && (containsAny(bare, "조직도", "조직 구성", "상위 조직", "하위 조직", "조직장", "인원", "몇 명") ||
		(reUnit.MatchString(bare) && containsAny(bare, "팀장", "부서장", "본부장", "대표") && containsAny(bare, "누구", "공석")))
	orgPeople := !orgBlocked && !orgWrite && !orgUnit && containsAny(bare, "누구", "직원", "사람", "담당자", "겸직", "내선", "연락처", "이메일", "휴대전화", "전화번호", "어느 팀", "어느 부서", "직위")
	// 재고 수불부(B24). 출고·이동·실사를 처리하라는 요청은 도구가 없으니 고르지 않는다.
	stockWrite := containsAny(bare, "출고", "창고 이동", "이동 처리", "실사 조정", "실사 처리") && containsAny(bare, "처리", "해 줘", "해줘", "등록", "만들") && !containsAny(bare, "수불부", "내역", "이력")
	ledger := !stockWrite && !notFormDoc && containsAny(bare, "수불부", "입출고 내역", "입고·출고 내역", "입고 출고 내역", "재고 원장", "재고 이력", "기초 재고", "기말 재고")
	// 예산 잔액(B25). 규정·조항을 찾으라는 문장과 결재선을 묻는 문장은 다른 규칙에 맡긴다.
	budget := containsAny(bare, "예산") && containsAny(bare, "남았", "잔액", "사용률", "얼마", "초과", "넘", "여유") &&
		!containsAny(bare, "규정", "조항", "사규", "결재선", "누가 결재", "수정", "늘려", "올려 줘", "변경", "반려", "승인")
	// 재고 월 마감 상태(B27). 마감·해제를 실행하라는 요청과 규정·조항을 찾으라는 문장은 다른 규칙에 맡긴다.
	closeWrite := containsAny(bare, "마감") && containsAny(bare, "처리", "실행", "해 줘", "해줘", "진행", "풀어", "해제해", "열어")
	closeStatus := !closeWrite && containsAny(bare, "마감") && containsAny(bare, "상태", "됐", "되었", "끝났", "점검", "차단", "경고", "버전", "기말 금액", "마감 금액", "마감됐") &&
		!containsAny(bare, "규정", "조항", "사규", "수불부", "누가")
	// 지급·카드(B32). 지급 실행·소명 입력을 요청하거나 계좌번호를 묻는 문장은 도구를 고르지 않는다(지급·소명·계좌는 화면에서만).
	noPolicy := !containsAny(bare, "규정", "조항", "사규")
	payWrite := noPolicy && (containsAny(bare, "계좌번호", "계좌 번호", "계좌를 알려", "계좌 알려") ||
		(containsAny(bare, "지급", "이체", "송금") && containsAny(bare, "처리", "해 줘", "해줘", "실행", "요청해", "보내", "넣어")))
	cardWrite := noPolicy && containsAny(bare, "카드") && containsAny(bare, "소명") && containsAny(bare, "소명해", "소명 입력", "소명 처리", "대신 소명", "소명을 입력", "확인 처리", "승인해")
	payables := !payWrite && noPolicy && containsAny(bare, "채무", "미지급", "외상매입금", "지급 예정", "지급할 돈", "줘야 할", "줘야할") && !containsAny(bare, "전표", "결재선")
	cardStatus := !cardWrite && noPolicy && containsAny(bare, "카드") && containsAny(bare, "소명", "미소명")
	// 근태·휴가(B50 7단계). 내 잔여와 팀 부재 일정만 조회한다. 신청·취소·승인, 남의 잔여, 사유·병명·증빙은 도구가 없으니 고르지 않는다.
	leaveWord := noPolicy && containsAny(bare, "연차", "휴가", "대체휴가", "부재", "휴직", "병가")
	leaveWrite := leaveWord && containsAny(bare, "신청해", "신청 해", "신청 처리", "취소해", "취소 해", "승인해", "승인 해", "상신해", "대신 신청", "써 줘", "써줘", "올려 줘", "올려줘")
	leaveSensitive := leaveWord && containsAny(bare, "사유", "이유", "병명", "진단", "증빙", "무슨 병")
	leaveBalanceQ := leaveWord && containsAny(bare, "남은", "잔여", "며칠", "몇 일", "몇일", "몇 개", "얼마나 남")
	leaveOther := containsAny(bare, "팀원", "동료", "부하", "직원의", "팀장의", "상사") || (rePerson.MatchString(bare) && !strings.Contains(bare, "내 "))
	leaveAbsence := leaveWord && containsAny(bare, "누가", "누구", "일정", "언제", "쉬는", "자리 비", "비우", "부재 중", "부재중") && !leaveBalanceQ
	switch {
	case leaveWrite || leaveSensitive || (leaveBalanceQ && leaveOther):
		// 휴가를 신청·취소·승인하거나, 남의 잔여·휴가 사유·증빙을 보는 MCP 도구는 없다. 도구를 고르지 않는다.
	case leaveBalanceQ:
		choice.Tool = "get_my_leave_balance"
	case leaveAbsence:
		choice.Tool = "list_team_absence"
		if dates := reAsOf.FindAllString(bare, -1); len(dates) > 0 {
			choice.Arguments["from"] = dates[0]
			if len(dates) > 1 {
				choice.Arguments["to"] = dates[1]
			}
		}
		if m := reUnit.FindStringSubmatch(bare); m != nil && !strings.HasSuffix(m[1], "우리 팀") && !strings.HasPrefix(m[1], "우리") {
			choice.Arguments["unit"] = m[1]
		}
	case payWrite || cardWrite:
		// 지급·소명·확인을 하는 MCP 도구는 없다. 도구를 고르지 않는다.
	case payables:
		choice.Tool = "get_payables"
		if m := reAsOf.FindString(bare); m != "" {
			choice.Arguments["as_of"] = m
		}
		if m := reVendorNm.FindStringSubmatch(bare); m != nil {
			choice.Arguments["vendor"] = m[1]
		}
	case cardStatus:
		choice.Tool = "get_card_unreconciled"
		if m := reCount.FindStringSubmatch(bare); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			choice.Arguments["limit"] = n
		}
	case closeWrite && !containsAny(bare, "규정", "조항", "사규"):
		// 마감·해제를 실행하는 MCP 도구는 없다. 도구를 고르지 않는다.
	case closeStatus:
		choice.Tool = "get_month_close_status"
		if m := reMonthArg.FindStringSubmatch(bare); m != nil {
			choice.Arguments["month"] = m[1]
		}
	case stockWrite:
		// 재고를 바꾸는 MCP 도구는 없다.
	case ledger:
		choice.Tool = "get_item_ledger"
		if m := reItemNo.FindStringSubmatch(bare); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			choice.Arguments["item_id"] = n
		}
		if dates := reAsOf.FindAllString(bare, -1); len(dates) > 0 {
			choice.Arguments["from"] = dates[0]
			if len(dates) > 1 {
				choice.Arguments["to"] = dates[1]
			}
		}
		if m := reWh.FindStringSubmatch(bare); m != nil {
			choice.Arguments["warehouse"] = strings.ReplaceAll(m[1], "  ", " ")
		}
	case budget:
		choice.Tool = "get_budget_status"
		var won int64
		for _, m := range reWon.FindAllStringSubmatch(bare, -1) {
			n, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
			mult := map[string]int64{"억": 100000000, "천만원": 10000000, "천만": 10000000, "만원": 10000, "만": 10000, "원": 1}[m[2]]
			won += n * mult
		}
		if won > 0 {
			choice.Arguments["amount"] = won
		}
		if m := reMonthArg.FindStringSubmatch(bare); m != nil {
			choice.Arguments["month"] = m[1]
		}
		if m := reBudDept.FindStringSubmatch(bare); m != nil && !strings.HasSuffix(m[1], "우리 팀") {
			choice.Arguments["department"] = m[1]
		}
	case whoApproves && !orgWrite:
		choice.Tool = "who_approves"
		var won int64
		for _, m := range reWon.FindAllStringSubmatch(bare, -1) {
			n, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
			mult := map[string]int64{"억": 100000000, "천만원": 10000000, "천만": 10000000, "만원": 10000, "만": 10000, "원": 1}[m[2]]
			won += n * mult
		}
		if won > 0 {
			choice.Arguments["amount"] = won
		}
		for _, f := range []struct{ word, code string }{{"일반 기안서", "GEN"}, {"기안서", "GEN"}, {"지출 결의서", "EXP"}, {"결의서", "EXP"}, {"업무 협조전", "COOP"}, {"협조전", "COOP"}} {
			if strings.Contains(bare, f.word) {
				choice.Arguments["form_code"] = f.code
				break
			}
		}
		if choice.Arguments["form_code"] == "COOP" {
			if m := reCoop.FindStringSubmatch(bare); m != nil {
				choice.Arguments["field_unit"] = m[1]
			}
		}
		if m := reReqNm.FindStringSubmatch(bare); m != nil {
			choice.Arguments["requester_name"] = m[1]
		}
	case orgWrite:
		// 조직·사람을 바꾸는 MCP 도구는 없다. 도구를 고르지 않는다.
	case orgUnit:
		choice.Tool = "get_org_unit"
		if m := reUnit.FindStringSubmatch(bare); m != nil {
			choice.Arguments["unit"] = m[1]
		}
		if m := reAsOf.FindString(bare); m != "" {
			choice.Arguments["as_of"] = m
		}
	case orgPeople:
		choice.Tool = "search_people"
		if m := rePerson.FindStringSubmatch(bare); m != nil {
			choice.Arguments["query"] = m[1]
		}
	case formDoc && containsAny(bare, "승인", "반려", "접수", "취소") && !containsAny(bare, "내용", "상세", "보여", "알려"):
		// 양식 문서의 결재·수신 처리는 MCP 에 없다. 도구를 고르지 않는다.
	case !notFormDoc && reDocNo.MatchString(bare) && containsAny(bare, "내용", "상세", "보여", "알려"):
		choice.Tool = "get_approval_doc"
		if m := reDocNo.FindStringSubmatch(bare); m != nil {
			n, _ := strconv.ParseInt(m[2], 10, 64)
			choice.Arguments["doc_id"] = n
		}
	case containsAny(bare, "문서함", "기안함", "결재 대기함", "결재 진행함", "참조함", "수신함", "완료함", "반려함", "내 문서", "내가 기안", "참조로 받은"):
		choice.Tool = "list_my_approval_docs"
		for _, b := range []struct{ word, box string }{{"결재 대기함", "pending"}, {"결재 진행함", "in_progress"}, {"완료함", "done"}, {"참조함", "cc"}, {"참조로 받은", "cc"},
			{"수신함", "receive"}, {"반려함", "rejected"}, {"기안함", "drafted"}, {"내가 기안", "drafted"}} {
			if strings.Contains(bare, b.word) {
				choice.Arguments["box"] = b.box
				break
			}
		}
		if choice.Arguments["box"] == "receive" {
			for _, t := range []struct{ word, tab string }{{"접수 대기", "waiting"}, {"처리 완료", "done"}, {"반송", "returned"}, {"접수", "received"}} {
				if strings.Contains(bare, t.word) {
					choice.Arguments["tab"] = t.tab
					break
				}
			}
		}
	case containsAny(bare, "양식") && containsAny(bare, "종류", "목록", "어떤", "뭐가", "쓸 수", "있어"):
		choice.Tool = "list_approval_forms"
	case reAct.MatchString(req) && !containsAny(req, "대기", "목록"):
		choice.Tool = "approve_or_reject"
		if n, ok := firstInt(req); ok {
			choice.Arguments["pr_id"] = n
		}
		choice.Arguments["approve"] = strings.Contains(req, "승인") && !strings.Contains(req, "반려")
	case containsAny(req, "규정", "조항", "사규") && containsAny(req, "찾", "검색", "알려", "보여", "뭐", "어떻게", "근거"):
		choice.Tool = "search_policy"
		if m := reQuoted.FindStringSubmatch(req); m != nil {
			choice.Arguments["query"] = m[1]
		}
	case containsAny(req, "재주문", "재발주"):
		// 상신·승인까지 요구해도 도구는 초안까지만 만든다(상신은 사람이 한다).
		choice.Tool = "draft_reorder"
	case containsAny(req, "재고 경보", "재고경보", "안전재고", "유효기한", "유통기한") && containsAny(req, "알려", "보여", "조회", "목록", "경보", "미달", "임박"):
		choice.Tool = "list_stock_alerts"
	case containsAny(req, "증빙", "거래명세서", "세금계산서") && containsAny(req, "검증", "판정", "대조", "확인"):
		choice.Tool = "verify_document"
		if i := strings.Index(req, "발주"); i >= 0 {
			if n, ok := firstInt(req[i:]); ok {
				choice.Arguments["po_id"] = n
			}
		}
	case containsAny(req, "결재", "승인") && containsAny(req, "대기", "목록", "내 차례", "할 건", "할 것"):
		choice.Tool = "list_pending_approvals"
	case isPR && containsAny(req, "작성", "초안", "임시저장", "올려", "만들"):
		choice.Tool = "create_purchase_request_draft"
	case isPR && containsAny(req, "조회", "상세", "내용", "상태", "알려", "보여"):
		choice.Tool = "get_purchase_request"
		if n, ok := firstInt(req); ok {
			choice.Arguments["pr_id"] = n
		}
	case strings.Contains(req, "재고"):
		choice.Tool = "get_stock"
		if n, ok := firstInt(req); ok {
			choice.Arguments["item_id"] = n
		}
	case containsAny(req, "품목", "검색", "찾아"):
		choice.Tool = "search_items"
		if m := reQuoted.FindStringSubmatch(req); m != nil {
			choice.Arguments["query"] = m[1]
		}
	}
	b, _ := json.Marshal(choice)
	return string(b)
}

var reSource = regexp.MustCompile(`(?s)<source n="(\d+)">(.*?)</source>`)

// mockKnowledgeAnswer 는 첫 자료의 첫 문장을 [번호] 와 함께 돌려주는 결정적 흉내다. 자료가 없으면 찾지 못했다고 답한다.
// 질문 줄이 "인용 조작" 을 포함하면 자료에 없는 번호를 붙인 문장을 더해 인용 검증이 버리는지 시험할 수 있게 한다.
func mockKnowledgeAnswer(user string) string {
	m := reSource.FindStringSubmatch(user)
	if m == nil {
		return "찾지 못했습니다."
	}
	first := strings.Join(strings.Fields(m[2]), " ")
	out := first + " [" + m[1] + "]"
	if strings.Contains(strings.SplitN(user, "\n", 2)[0], "인용 조작") {
		out += "\n자료에 없는 근거를 지어낸 문장입니다. [99]\n번호 없는 문장입니다."
	}
	return out
}

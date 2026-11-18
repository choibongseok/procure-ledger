package docverify

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// CompareInput 은 3자 대조의 입력이다: 문서 추출값, 발주, (선택) 입고, 거래처 사업자번호.
type CompareInput struct {
	Extraction       domain.Extraction
	PO               domain.PurchaseOrder
	Receipt          *domain.Receipt // nil 이면 발주 전량 기준
	VendorBusinessNo string
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type expLine struct {
	name      string
	qty       int64 // 기대 수량(입고가 있으면 입고 수량)
	poQty     int64 // 발주 수량
	unitPrice int64
}

func won(n int64) string { return fmt.Sprintf("%d", n) }

// NA 는 해당 열에 값이 없음을 나타내는 표기다(예: 입고 없이 판정한 건의 입고 열).
const NA = "-"

// Compare 는 결정적 대조 규칙이다. 같은 입력이면 항상 같은 판정과 같은 순서의 근거 항목이 나온다.
//
// 규칙
//  1. 사업자번호: 문서 값과 거래처 값이 숫자만 비교해 다르면 불일치, 한쪽이 비어 있으면 보류.
//  2. 기대 품목: 입고가 있으면 입고된 줄(수량은 입고 수량), 없으면 발주 전체 줄. 단가는 발주 단가.
//  3. 문서 줄은 이름(공백·대소문자 무시)으로 기대 품목과 짝짓는다. 짝이 없는 기대 품목은 불일치(문서에 없음),
//     짝이 없는 문서 줄은 불일치(발주에 없는 품목). 짝이 있으면 수량·단가를 비교한다.
//  4. 문서 내부 산술: 줄마다 수량×단가=금액, 줄 금액 합=합계. 어긋나면 불일치.
//  5. 합계: 기대 합계(기대 품목의 수량×발주 단가 합)와 문서 합계가 다르면 불일치.
//
// 하나라도 불일치면 불일치, 불일치 없이 보류가 있으면 보류, 모두 일치해야 일치.
func Compare(in CompareInput) (domain.Outcome, []domain.VerdictItem) {
	var items []domain.VerdictItem
	add := func(field, expected, actual string, res domain.Outcome, note string) {
		items = append(items, domain.VerdictItem{Field: field, Expected: expected, Actual: actual, PO: NA, Receipt: NA, Result: res, Note: note})
	}
	// addCols 는 발주·입고 열 값을 함께 채운다. 입고 없이 판정했으면 입고 열은 항상 "-" 다.
	addCols := func(field, expected, po, receipt, actual string, res domain.Outcome, note string) {
		if in.Receipt == nil || receipt == "" {
			receipt = NA
		}
		if po == "" {
			po = NA
		}
		items = append(items, domain.VerdictItem{Field: field, Expected: expected, Actual: actual, PO: po, Receipt: receipt, Result: res, Note: note})
	}
	ex := in.Extraction

	// 1. 사업자번호
	switch docNo, venNo := digits(ex.BusinessNo), digits(in.VendorBusinessNo); {
	case venNo == "":
		add("business_no", "", ex.BusinessNo, domain.OutcomeHold, "거래처 사업자번호가 등록되어 있지 않아 대조할 수 없습니다")
	case docNo == "":
		add("business_no", in.VendorBusinessNo, "", domain.OutcomeHold, "문서에서 사업자번호를 읽지 못했습니다")
	case docNo == venNo:
		add("business_no", in.VendorBusinessNo, ex.BusinessNo, domain.OutcomeMatch, "")
	default:
		add("business_no", in.VendorBusinessNo, ex.BusinessNo, domain.OutcomeMismatch, "거래처 사업자번호와 다릅니다")
	}

	// 2. 기대 품목
	var exp []expLine
	if in.Receipt != nil {
		byID := map[int64]domain.POLine{}
		for _, l := range in.PO.Lines {
			byID[l.ID] = l
		}
		for _, rl := range in.Receipt.Lines {
			pl, ok := byID[rl.POLineID]
			if !ok {
				add("receipt", "발주 줄", fmt.Sprintf("po_line_id=%d", rl.POLineID), domain.OutcomeHold, "입고 줄이 발주에 없어 대조할 수 없습니다")
				continue
			}
			exp = append(exp, expLine{name: pl.ItemName, qty: rl.Quantity, poQty: pl.Quantity, unitPrice: pl.UnitPrice})
		}
	} else {
		for _, pl := range in.PO.Lines {
			exp = append(exp, expLine{name: pl.ItemName, qty: pl.Quantity, poQty: pl.Quantity, unitPrice: pl.UnitPrice})
		}
	}

	// 3. 줄 짝짓기
	used := make([]bool, len(ex.Lines))
	var expTotal int64
	for _, e := range exp {
		expTotal += e.qty * e.unitPrice
		idx := -1
		for i, dl := range ex.Lines {
			if !used[i] && norm(dl.Name) == norm(e.name) {
				idx = i
				break
			}
		}
		if idx < 0 {
			addCols("line["+e.name+"]", fmt.Sprintf("수량 %d", e.qty), won(e.poQty), won(e.qty), "문서에 없음", domain.OutcomeMismatch, "대조할 품목이 문서에 없습니다")
			continue
		}
		used[idx] = true
		dl := ex.Lines[idx]
		f := fmt.Sprintf("line[%d]", idx)
		res, note := domain.OutcomeMatch, ""
		if dl.Quantity != e.qty {
			res, note = domain.OutcomeMismatch, "수량이 다릅니다"
		}
		addCols(f+".quantity", won(e.qty), won(e.poQty), won(e.qty), won(dl.Quantity), res, note)
		res, note = domain.OutcomeMatch, ""
		if dl.UnitPrice != e.unitPrice {
			res, note = domain.OutcomeMismatch, "단가가 다릅니다"
		}
		addCols(f+".unit_price", won(e.unitPrice), won(e.unitPrice), "", won(dl.UnitPrice), res, note)
	}
	for i, dl := range ex.Lines {
		if !used[i] {
			add(fmt.Sprintf("line[%d].name", i), "발주에 없는 품목", dl.Name, domain.OutcomeMismatch, "발주(입고)에 없는 품목입니다")
		}
	}

	// 4. 문서 내부 산술
	var sum int64
	arithOK := true
	for i, dl := range ex.Lines {
		sum += dl.Amount
		if dl.Quantity*dl.UnitPrice != dl.Amount {
			arithOK = false
			add(fmt.Sprintf("line[%d].amount", i), won(dl.Quantity*dl.UnitPrice), won(dl.Amount), domain.OutcomeMismatch, "수량×단가와 금액이 다릅니다")
		}
	}
	if sum != ex.TotalAmount {
		arithOK = false
		add("document.sum", won(sum), won(ex.TotalAmount), domain.OutcomeMismatch, "줄 금액의 합과 문서 합계가 다릅니다")
	}
	if arithOK {
		add("document.arithmetic", "", "", domain.OutcomeMatch, "문서 내부 계산이 맞습니다")
	}

	// 5. 합계. 발주 열은 발주 전체 합계, 입고 열은 입고 기준 합계(입고가 있을 때만).
	var poTotal int64
	for _, pl := range in.PO.Lines {
		poTotal += pl.Quantity * pl.UnitPrice
	}
	if ex.TotalAmount == expTotal {
		addCols("total_amount", won(expTotal), won(poTotal), won(expTotal), won(ex.TotalAmount), domain.OutcomeMatch, "")
	} else {
		addCols("total_amount", won(expTotal), won(poTotal), won(expTotal), won(ex.TotalAmount), domain.OutcomeMismatch, "합계가 다릅니다")
	}

	return Roll(items), items
}

// Roll 은 근거 항목들을 하나의 판정으로 모은다: 불일치 > 보류 > 일치. 항목이 없으면 보류.
func Roll(items []domain.VerdictItem) domain.Outcome {
	if len(items) == 0 {
		return domain.OutcomeHold
	}
	out := domain.OutcomeMatch
	for _, it := range items {
		switch it.Result {
		case domain.OutcomeMismatch:
			return domain.OutcomeMismatch
		case domain.OutcomeHold:
			out = domain.OutcomeHold
		}
	}
	return out
}

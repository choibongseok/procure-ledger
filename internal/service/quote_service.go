package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 견적서 첨부와 거래처별 비교(B25 ②, 3.2).
// LLM 은 견적서에서 값을 "추출"만 하고(docverify.QuoteExtractor), 최저가 판정·선택·사유 검사는 모두 이 파일의 코드가 한다.
// 최저가·사유 필수 판정의 정본은 decideQuotes 한 곳이다(비교 화면·선택·상신 검사가 같은 함수를 쓴다).

const (
	maxQuotesPerRequest = 10
	maxQuoteReasonRunes = 500
	maxQuoteLineCount   = 50
	maxQuoteNameRunes   = 100
	maxQuoteText        = 200_000 // 저장하는 원문 텍스트 상한(바이트)
)

// QuoteReader 는 견적서에서 값을 읽는 추출기다(구현: docverify.QuoteExtractor, 게이트웨이를 거친다).
type QuoteReader interface {
	ExtractFile(ctx context.Context, filename, contentType string, data []byte) (docverify.QuoteResult, error)
}

// QuoteService 는 domain.QuoteService 의 구현이다. 구매요청 서비스의 열람·본인 확인 규칙을 그대로 쓴다.
type QuoteService struct {
	*PurchaseService
	Quotes repository.SQLQuotes
	// Extractor 는 호출 때 추출기를 돌려준다(게이트웨이는 조립 순서상 나중에 채워질 수 있다). nil 이거나 nil 을 돌려주면 모든 견적서가 "읽지 못함"이다.
	Extractor func() QuoteReader
}

var _ domain.QuoteService = (*QuoteService)(nil)

func NewQuoteService(p *PurchaseService) *QuoteService {
	return &QuoteService{PurchaseService: p}
}

// quoteDecision 은 비교 판정이다.
type quoteDecision struct {
	LowestTotal    int64
	Comparable     int  // 값이 채워진 견적 수
	SelectedLowest bool // 선택한 견적이 최저가(동률 포함)
	ReasonRequired bool
}

// decideQuotes 는 최저가(합계 기준, 동률은 모두 최저가)와 사유 필수 여부를 정한다.
// 사유 필수: 값이 채워진 견적이 2건 이상이고, 선택한 견적이 있으며 그것이 최저가가 아닐 때.
func decideQuotes(quotes []repository.QuoteRec, selectedID int64) quoteDecision {
	var d quoteDecision
	for _, q := range quotes {
		if !q.Filled {
			continue
		}
		d.Comparable++
		if d.LowestTotal == 0 || q.Total < d.LowestTotal {
			d.LowestTotal = q.Total
		}
	}
	for _, q := range quotes {
		if q.ID == selectedID && q.Filled {
			d.SelectedLowest = q.Total == d.LowestTotal
			d.ReasonRequired = d.Comparable >= 2 && !d.SelectedLowest
		}
	}
	return d
}

const msgReasonRequired = "최저가가 아닌 거래처를 선택했습니다. 선택 사유를 입력하세요."

// buildCompare 는 저장된 견적과 요청으로 비교 결과를 만든다.
func buildCompare(rec repository.PRRecord, quotes []repository.QuoteRec, editable bool) domain.QuoteCompare {
	d := decideQuotes(quotes, rec.QuoteID)
	out := domain.QuoteCompare{Quotes: []domain.Quote{}, Editable: editable, SelectedID: rec.QuoteID, ReasonRequired: d.ReasonRequired,
		LowestTotal: d.LowestTotal, PRTotal: rec.TotalAmount}
	if d.ReasonRequired || (rec.QuoteID != 0 && strings.TrimSpace(rec.QuoteReason) != "") {
		out.Reason = rec.QuoteReason
	}
	for _, q := range quotes {
		v := q.Quote
		v.Selected = q.ID == rec.QuoteID
		if v.Filled {
			v.Lowest = v.Total == d.LowestTotal
			if !v.Lowest {
				v.OverLowest = v.Total - d.LowestTotal
			}
		}
		if v.Selected && v.Filled && v.Total != rec.TotalAmount {
			out.TotalMismatch = true
		}
		out.Quotes = append(out.Quotes, v)
	}
	if rec.QuoteID != 0 && d.Comparable < 2 {
		out.Reason = ""
	}
	out.Note = quoteNote(out)
	return out
}

// requestEditable 은 "이 사용자가 이 구매요청을 고칠 수 있는가"의 유일한 판정이다. 상세 응답의 editable 과 견적 비교의 editable 이 같이 쓴다.
func requestEditable(p contract.Principal, requesterID int64, status domain.PRStatus) bool {
	return requesterID == p.UserID && canEdit(status) && p.Can(contract.PermPurchaseCreate)
}

func quoteEditable(p contract.Principal, rec repository.PRRecord) bool {
	return requestEditable(p, rec.RequesterID, rec.Status)
}

func (s *QuoteService) readable(ctx context.Context, p contract.Principal, scope repository.ViewScope, prID int64, q contract.Tx) (repository.PRRecord, error) {
	rec, err := s.PurchaseService.Repo.GetPR(ctx, q, prID, scope)
	if err != nil {
		return rec, err
	}
	if !isPurchaseKind(rec.FormKind) {
		return rec, contract.ErrNotFound
	}
	return rec, nil
}

// Compare 는 견적 비교 표다. 요청을 볼 수 있는 사람이면 읽는다(상신 뒤에는 읽기 전용).
func (s *QuoteService) Compare(ctx context.Context, prID int64) (domain.QuoteCompare, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseRead, "quote.compare", audit.TargetPurchaseQuote)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	rec, err := s.readable(ctx, p, scope, prID, s.DB)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	quotes, err := s.Quotes.List(ctx, s.DB, prID)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	return buildCompare(rec, quotes, quoteEditable(p, rec)), nil
}

// editableRequest 는 본인의 작성 중·반려 구매요청을 읽는다(아니면 거부).
func (s *QuoteService) editableRequest(ctx context.Context, q contract.Tx, p contract.Principal, scope repository.ViewScope, prID int64) (repository.PRRecord, error) {
	rec, err := s.ownRequest(ctx, q, p, scope, prID)
	if err != nil {
		return rec, err
	}
	if !canEdit(rec.Status) {
		return rec, fmt.Errorf("%w: %s 상태의 요청은 견적서를 바꿀 수 없습니다", contract.ErrInvalidState, StatusLabel(rec.Status))
	}
	return rec, nil
}

func toQuoteLines(in []domain.ExtractedLine) []domain.QuoteLine {
	out := make([]domain.QuoteLine, len(in))
	for i, l := range in {
		out[i] = domain.QuoteLine{Name: l.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Amount}
	}
	return out
}

// Attach 는 견적서를 올린다. 추출은 트랜잭션 밖에서 하고(게이트웨이 호출), 저장은 한 트랜잭션이다.
// 읽지 못한 견적서도 업로드는 성공시키고 "읽지 못함"으로 저장한다(직접 입력 안내). 같은 거래처의 견적서는 교체한다(E9).
func (s *QuoteService) Attach(ctx context.Context, prID int64, filename, contentType string, data []byte) (domain.Quote, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "quote.attach", audit.TargetPurchaseQuote)
	if err != nil {
		return domain.Quote{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.Quote{}, err
	}
	name := SanitizeFilename(filename)
	ext := strings.ToLower(path.Ext(name))
	limit := s.PurchaseService.Base.Attach.OrDefault().MaxFileBytes
	switch {
	case name == "":
		return domain.Quote{}, validation("파일 이름이 올바르지 않습니다")
	case len(data) == 0:
		return domain.Quote{}, validation("빈 파일은 올릴 수 없습니다")
	case int64(len(data)) > limit:
		return domain.Quote{}, validation("견적서는 파일당 %dMB 까지 올릴 수 있습니다", limit>>20)
	case ext != ".pdf" && ext != ".txt":
		return domain.Quote{}, validation("%s", docverify.ErrQuoteUnsupported.Error()+".")
	case !sniffOK(ext, data):
		return domain.Quote{}, validation("파일 내용이 확장자와 맞지 않습니다")
	}
	if _, err := s.editableRequest(ctx, s.DB, p, scope, prID); err != nil {
		return domain.Quote{}, err
	}
	ctype := "application/pdf"
	if ext == ".txt" {
		ctype = "text/plain"
	}
	// 추출(게이트웨이 호출, 트랜잭션 밖). 실패 유형: 지원하지 않는 형식은 거절, 그 밖은 "읽지 못함"으로 저장한다.
	res, exErr := s.extract(ctx, name, ctype, data)
	if exErr != nil && !errors.Is(exErr, docverify.ErrQuoteUnreadable) {
		if errors.Is(exErr, docverify.ErrQuoteUnsupported) {
			return domain.Quote{}, validation("%s", docverify.ErrQuoteUnsupported.Error()+".")
		}
		return domain.Quote{}, exErr
	}
	vendors, err := repository.SQLInventory{}.ListVendors(ctx, s.DB)
	if err != nil {
		return domain.Quote{}, err
	}
	sum := sha256.Sum256(data)
	w := repository.QuoteWrite{PRID: prID, Filename: name, ContentType: ctype, SHA256: hex.EncodeToString(sum[:]), UploadedBy: p.UserID, Now: s.now(), Status: domain.QuoteUnreadable, Warnings: []string{}}
	if exErr == nil {
		w.Status, w.Supplier = domain.QuoteRead, res.Supplier
		w.Lines, w.Total, w.LeadText, w.Warnings = toQuoteLines(res.Lines), res.LineTotal, res.LeadText, append([]string{}, res.Warnings...)
		if res.HasDocTotal {
			v := res.DocTotal
			w.DocTotal = &v
		}
		if res.HasLeadDays {
			v := res.LeadDays
			w.LeadDays = &v
		}
		if len(res.Text) > maxQuoteText {
			w.Text = res.Text[:maxQuoteText]
		} else {
			w.Text = res.Text
		}
		raw, _ := json.Marshal(res)
		w.ExtractionJSON = string(raw)
		if id, ok := docverify.MatchVendor(vendors, res.BusinessNo, res.Supplier); ok {
			w.VendorID = id
		}
	}
	var out domain.Quote
	err = s.write(ctx, p, "quote.attach", audit.TargetPurchaseQuote, 0, func(tx contract.Tx) error {
		if _, err := s.editableRequest(ctx, tx, p, scope, prID); err != nil {
			return err
		}
		existing, err := s.Quotes.List(ctx, tx, prID)
		if err != nil {
			return err
		}
		var id int64
		replaced := false
		for _, q := range existing {
			if w.VendorID != 0 && q.VendorID == w.VendorID {
				if err := s.Quotes.Replace(ctx, tx, q.ID, w); err != nil {
					return err
				}
				id, replaced = q.ID, true
				break
			}
		}
		if !replaced {
			if len(existing) >= maxQuotesPerRequest {
				return validation("견적서는 한 구매요청에 %d건까지 올릴 수 있습니다", maxQuotesPerRequest)
			}
			if id, err = s.Quotes.Insert(ctx, tx, w); err != nil {
				return err
			}
		}
		stored, err := s.Quotes.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = stored.Quote
		diff := []audit.DiffItem{{Key: "pr_id", New: fmt.Sprint(prID)}, {Key: "status", New: w.Status}}
		if w.VendorID != 0 {
			diff = append(diff, audit.DiffItem{Key: "vendor_id", New: fmt.Sprint(w.VendorID)})
		}
		if replaced {
			diff = append(diff, audit.DiffItem{Key: "replaced", New: "true"})
		}
		return s.record(ctx, tx, p, "quote.attach", audit.TargetPurchaseQuote, id, diff...)
	})
	if err != nil {
		return domain.Quote{}, err
	}
	return out, nil
}

func (s *QuoteService) extract(ctx context.Context, name, ctype string, data []byte) (docverify.QuoteResult, error) {
	var r QuoteReader
	if s.Extractor != nil {
		r = s.Extractor()
	}
	if r == nil {
		return docverify.QuoteResult{}, fmt.Errorf("%w: AI 연결이 준비되지 않았습니다", docverify.ErrQuoteUnreadable)
	}
	return r.ExtractFile(ctx, name, ctype, data)
}

func sumQuoteLines(in []domain.QuoteLine) (int64, error) {
	var total int64
	for i, l := range in {
		switch {
		case strings.TrimSpace(l.Name) == "" || utf8.RuneCountInString(l.Name) > maxQuoteNameRunes:
			return 0, validation("%d번째 줄의 품목 이름은 1자 이상 %d자 이하여야 합니다", i+1, maxQuoteNameRunes)
		case l.Quantity <= 0 || l.Quantity > 1_000_000:
			return 0, validation("%d번째 줄의 수량은 1 이상의 정수여야 합니다", i+1)
		case l.UnitPrice < 0 || l.UnitPrice > maxUnitPrice:
			return 0, validation("%d번째 줄의 단가가 올바르지 않습니다", i+1)
		}
		total += l.Quantity * l.UnitPrice
		if total > 1_000_000_000_000 {
			return 0, validation("견적 합계가 너무 큽니다")
		}
	}
	return total, nil
}

const warnDocTotalDiffers = "문서의 합계와 줄 합계가 다릅니다. 부가세 포함 여부를 확인하세요."

// Update 는 사람이 값(거래처·품목 줄·문서 합계·납기)을 입력하거나 고친다. 합계는 줄 합계를 코드가 다시 계산하고 "수정됨"으로 표시한다.
func (s *QuoteService) Update(ctx context.Context, quoteID int64, in domain.QuoteInput) (domain.Quote, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "quote.edit", audit.TargetPurchaseQuote)
	if err != nil {
		return domain.Quote{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.Quote{}, err
	}
	if len(in.Lines) == 0 || len(in.Lines) > maxQuoteLineCount {
		return domain.Quote{}, validation("품목 줄은 1줄 이상 %d줄 이하여야 합니다", maxQuoteLineCount)
	}
	total, err := sumQuoteLines(in.Lines)
	if err != nil {
		return domain.Quote{}, err
	}
	lines := make([]domain.QuoteLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = domain.QuoteLine{Name: strings.TrimSpace(l.Name), Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Quantity * l.UnitPrice}
	}
	if in.DocTotal != nil && (*in.DocTotal < 0 || *in.DocTotal > 1_000_000_000_000) {
		return domain.Quote{}, validation("문서 합계가 올바르지 않습니다")
	}
	if in.LeadDays != nil && (*in.LeadDays < 0 || *in.LeadDays > 3650) {
		return domain.Quote{}, validation("납기는 발주 후 0일에서 3650일 사이여야 합니다")
	}
	leadText := strings.TrimSpace(in.LeadText)
	if utf8.RuneCountInString(leadText) > 100 {
		return domain.Quote{}, validation("납기 문구는 100자 이내로 입력해 주세요")
	}
	var out domain.Quote
	err = s.write(ctx, p, "quote.edit", audit.TargetPurchaseQuote, quoteID, func(tx contract.Tx) error {
		cur, err := s.Quotes.Get(ctx, tx, quoteID)
		if err != nil {
			return err
		}
		rec, err := s.editableRequest(ctx, tx, p, scope, cur.PRID)
		if err != nil {
			return err
		}
		vendorName := ""
		if in.VendorID != 0 {
			vs, err := repository.SQLInventory{}.ListVendors(ctx, tx)
			if err != nil {
				return err
			}
			found := false
			for _, v := range vs {
				if v.ID == in.VendorID {
					found, vendorName = true, v.Name
				}
			}
			if !found {
				return validation("거래처를 찾을 수 없습니다")
			}
			others, err := s.Quotes.List(ctx, tx, cur.PRID)
			if err != nil {
				return err
			}
			for _, o := range others {
				if o.ID != quoteID && o.VendorID == in.VendorID {
					return validation("%s의 견적서가 이미 있습니다. 같은 거래처는 한 건만 올릴 수 있습니다", vendorName)
				}
			}
		}
		warns := []string{}
		for _, w := range cur.Warnings {
			if w != warnDocTotalDiffers {
				warns = append(warns, w)
			}
		}
		if in.DocTotal != nil && *in.DocTotal != total {
			warns = append(warns, warnDocTotalDiffers)
		}
		w := repository.QuoteWrite{VendorID: in.VendorID, Lines: lines, Total: total, DocTotal: in.DocTotal, LeadDays: in.LeadDays, LeadText: leadText, Warnings: warns, Now: s.now()}
		if err := s.Quotes.UpdateValues(ctx, tx, quoteID, w); err != nil {
			return err
		}
		// 선택돼 있는 견적의 거래처가 바뀌면 요청의 거래처도 따라가고, 거래처를 비우면 선택을 푼다.
		if rec.QuoteID == quoteID && in.VendorID != cur.VendorID {
			if in.VendorID == 0 {
				if err := s.Quotes.SetSelection(ctx, tx, rec.ID, 0, ""); err != nil {
					return err
				}
			} else if err := s.Quotes.SetVendor(ctx, tx, rec.ID, in.VendorID); err != nil {
				return err
			}
		}
		stored, err := s.Quotes.Get(ctx, tx, quoteID)
		if err != nil {
			return err
		}
		out = stored.Quote
		return s.record(ctx, tx, p, "quote.edit", audit.TargetPurchaseQuote, quoteID,
			audit.DiffItem{Key: "total_amount", Old: fmt.Sprint(cur.Total), New: fmt.Sprint(total)},
			audit.DiffItem{Key: "vendor_id", Old: fmt.Sprint(cur.VendorID), New: fmt.Sprint(in.VendorID)})
	})
	return out, err
}

// Remove 는 견적서를 지운다. 선택돼 있었다면 선택과 사유도 풀린다(요청의 거래처는 그대로 둔다, E10).
func (s *QuoteService) Remove(ctx context.Context, quoteID int64) error {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "quote.remove", audit.TargetPurchaseQuote)
	if err != nil {
		return err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "quote.remove", audit.TargetPurchaseQuote, quoteID, func(tx contract.Tx) error {
		cur, err := s.Quotes.Get(ctx, tx, quoteID)
		if err != nil {
			return err
		}
		rec, err := s.editableRequest(ctx, tx, p, scope, cur.PRID)
		if err != nil {
			return err
		}
		if rec.QuoteID == quoteID {
			if err := s.Quotes.SetSelection(ctx, tx, rec.ID, 0, ""); err != nil {
				return err
			}
		}
		if err := s.Quotes.Delete(ctx, tx, quoteID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "quote.remove", audit.TargetPurchaseQuote, quoteID, audit.DiffItem{Key: "pr_id", New: fmt.Sprint(cur.PRID)})
	})
}

// Select 는 견적을 고르고(quoteID 0 이면 해제) 구매요청의 거래처를 그 거래처로 바꾼다. 사유는 최저가가 아닌 선택일 때만 저장한다.
// 사유가 필요한데 비었는지는 상신 때 서버가 검사한다(checkQuoteForSubmit).
func (s *QuoteService) Select(ctx context.Context, prID, quoteID int64, reason string) (domain.QuoteCompare, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "quote.select", audit.TargetPurchaseQuote)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.QuoteCompare{}, err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxQuoteReasonRunes {
		return domain.QuoteCompare{}, validation("선택 사유는 %d자 이내로 입력해 주세요", maxQuoteReasonRunes)
	}
	var out domain.QuoteCompare
	err = s.write(ctx, p, "quote.select", audit.TargetPurchaseQuote, quoteID, func(tx contract.Tx) error {
		rec, err := s.editableRequest(ctx, tx, p, scope, prID)
		if err != nil {
			return err
		}
		quotes, err := s.Quotes.List(ctx, tx, prID)
		if err != nil {
			return err
		}
		if quoteID == 0 {
			if err := s.Quotes.SetSelection(ctx, tx, prID, 0, ""); err != nil {
				return err
			}
			rec.QuoteID, rec.QuoteReason = 0, ""
			out = buildCompare(rec, quotes, true)
			return s.record(ctx, tx, p, "quote.select", audit.TargetPurchaseQuote, 0, audit.DiffItem{Key: "pr_id", New: fmt.Sprint(prID)}, audit.DiffItem{Key: "selected", New: "해제"})
		}
		var sel *repository.QuoteRec
		for i := range quotes {
			if quotes[i].ID == quoteID {
				sel = &quotes[i]
			}
		}
		switch {
		case sel == nil:
			return contract.ErrNotFound
		case sel.VendorID == 0:
			return validation("거래처를 먼저 고르세요. 거래처를 연결하지 못한 견적서는 선택할 수 없습니다")
		case !sel.Filled:
			return validation("금액을 입력한 견적서만 선택할 수 있습니다. 견적서의 품목과 금액을 먼저 입력하세요")
		}
		d := decideQuotes(quotes, quoteID)
		keep := ""
		if d.ReasonRequired {
			keep = reason
		}
		if err := s.Quotes.SetSelection(ctx, tx, prID, quoteID, keep); err != nil {
			return err
		}
		if err := s.Quotes.SetVendor(ctx, tx, prID, sel.VendorID); err != nil {
			return err
		}
		rec.QuoteID, rec.QuoteReason, rec.VendorID = quoteID, keep, sel.VendorID
		out = buildCompare(rec, quotes, true)
		return s.record(ctx, tx, p, "quote.select", audit.TargetPurchaseQuote, quoteID,
			audit.DiffItem{Key: "pr_id", New: fmt.Sprint(prID)},
			audit.DiffItem{Key: "lowest", New: fmt.Sprint(d.SelectedLowest)},
			audit.DiffItem{Key: "reason_len", New: fmt.Sprint(utf8.RuneCountInString(keep))})
	})
	return out, err
}

// checkQuoteForSubmit 은 상신 때 서버가 하는 견적 검사다: 최저가가 아닌 거래처를 골랐는데 사유가 없으면 거부한다.
// 선택한 견적의 거래처가 요청의 거래처와 다르면(요청 거래처를 따로 바꾼 경우) 선택은 상신에 쓰이지 않는다.
func (s *PurchaseService) checkQuoteForSubmit(ctx context.Context, tx contract.Tx, rec repository.PRRecord) (note string, err error) {
	if rec.QuoteID == 0 {
		return "", nil
	}
	quotes, err := repository.SQLQuotes{}.List(ctx, tx, rec.ID)
	if err != nil {
		return "", err
	}
	d := decideQuotes(quotes, rec.QuoteID)
	reason := strings.TrimSpace(rec.QuoteReason)
	if d.ReasonRequired && reason == "" {
		return "", validation("%s", msgReasonRequired)
	}
	if d.ReasonRequired {
		return "견적 선택 사유: " + reason, nil
	}
	return "", nil
}

// syncQuoteSelection 은 임시저장에서 요청의 거래처를 바꿨을 때 선택한 견적의 거래처와 어긋나면 선택을 푼다.
func (s *PurchaseService) syncQuoteSelection(ctx context.Context, tx contract.Tx, rec repository.PRRecord, newVendorID int64) error {
	if rec.QuoteID == 0 {
		return nil
	}
	q, err := repository.SQLQuotes{}.Get(ctx, tx, rec.QuoteID)
	if errors.Is(err, contract.ErrNotFound) || (err == nil && q.VendorID != newVendorID) {
		return repository.SQLQuotes{}.SetSelection(ctx, tx, rec.ID, 0, "")
	}
	return err
}

// quoteNote 는 비교 화면의 한 줄 안내(선택한 거래처가 최저가보다 얼마나 비싼지)다. 거래처 이름 뒤 조사는 받침 규칙으로 붙인다.
func quoteNote(c domain.QuoteCompare) string {
	var sel *domain.Quote
	var lowest []string
	for i := range c.Quotes {
		q := c.Quotes[i]
		if q.Selected {
			sel = &q
		}
		if q.Filled && q.Lowest {
			lowest = append(lowest, quoteVendorName(q))
		}
	}
	if sel == nil || !sel.Filled || sel.Lowest || len(lowest) == 0 {
		return ""
	}
	sort.Strings(lowest)
	return fmt.Sprintf("선택한 %s 최저가 %s보다 %s 비쌉니다.", topic(quoteVendorName(*sel)), strings.Join(lowest, "·"), wonText(sel.OverLowest))
}

func quoteVendorName(q domain.Quote) string {
	if q.VendorName != "" {
		return q.VendorName
	}
	if q.Supplier != "" {
		return q.Supplier
	}
	return "거래처 미정"
}

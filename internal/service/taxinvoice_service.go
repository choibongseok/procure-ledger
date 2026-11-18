package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 전자세금계산서(B43 1단계,). 발행(매출)·수신(매입) 공통 모델의 서비스다.
//   - 상태 전이는 taxTransitions 표 한 곳에서만 정의한다(표에 없는 전이는 전부 거부, DB 트리거가 한 번 더 막는다).
//   - 접수된(국세청 수신) 건은 고치지 않는다. 정정은 수정세금계산서라는 새 건(AmendDraft)으로만 한다(D5).
//   - 금액·세액·승인번호 검증은 코드가 결정적으로 한다. LLM 을 쓰지 않는다.
//   - 쓰기는 같은 트랜잭션에서 감사를 남긴다. 발행은 SoD tax.create_issue(작성자 = 발행자) 판정을 받는다(D6).
//   - 대행사 호출은 이 서비스가 하지 않는다. 전송 작업(internal/taxasp)이 TaxDispatchStore(이 서비스가 구현)로 상태를 바꾼다.

type taxEvent string

const (
	tevIssue          taxEvent = "issue"
	tevCancel         taxEvent = "cancel"
	tevSend           taxEvent = "send"
	tevFail           taxEvent = "fail"
	tevAccept         taxEvent = "accept"
	tevResend         taxEvent = "resend"
	tevAbandon        taxEvent = "abandon"
	tevCancelReceived taxEvent = "cancel_received"
)

type taxKey struct {
	direction, from string
	ev              taxEvent
}

// taxTransitions 는 세금계산서 상태 전이 표다(명세 5.1·5.2). 표에 없는 전이는 전부 거부한다.
var taxTransitions = map[taxKey]string{
	{domain.TaxSale, domain.TaxDraft, tevIssue}:                 domain.TaxIssued,
	{domain.TaxSale, domain.TaxDraft, tevCancel}:                domain.TaxCanceled,
	{domain.TaxSale, domain.TaxIssued, tevSend}:                 domain.TaxSent,
	{domain.TaxSale, domain.TaxIssued, tevFail}:                 domain.TaxFailed,
	{domain.TaxSale, domain.TaxSent, tevAccept}:                 domain.TaxAccepted,
	{domain.TaxSale, domain.TaxSent, tevFail}:                   domain.TaxFailed,
	{domain.TaxSale, domain.TaxFailed, tevResend}:               domain.TaxIssued,
	{domain.TaxSale, domain.TaxFailed, tevAbandon}:              domain.TaxCanceled,
	{domain.TaxPurchase, domain.TaxAccepted, tevCancelReceived}: domain.TaxCanceled,
}

func taxTransition(direction, from string, ev taxEvent) (string, error) {
	to, ok := taxTransitions[taxKey{direction, from, ev}]
	if !ok {
		return "", fmt.Errorf("%w: %s 상태의 %s 세금계산서는 이 작업을 할 수 없습니다", contract.ErrInvalidState, domain.TaxStatusLabels[from], domain.TaxDirectionLabels[direction])
	}
	return to, nil
}

// 재시도 규칙(D17): 일시 오류는 첫 시도 뒤 자동 5회(1분·5분·30분·2시간·6시간), 수동 다시 전송은 건당 5회, 수신 조회는 7일 한도.
var (
	taxRetryBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	taxPollBackoff  = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour}
)

const (
	taxMaxResends    = 5
	taxStatusTimeout = 7 * 24 * time.Hour
	taxMaxLines      = 16
)

var (
	reApproval = regexp.MustCompile(`^[0-9A-Za-z]{24}$`)
	reDigits   = regexp.MustCompile(`\D`)
)

// TaxInvoiceService 는 domain.TaxInvoiceService 와 domain.TaxDispatchStore 의 구현이다.
type TaxInvoiceService struct {
	Base
	Repo   repository.SQLTaxInvoice
	Match  repository.SQLTaxMatch
	Ledger repository.SQLLedger
	// ASPReady 는 발행·다시 전송 전에 대행사 연결이 쓸 수 있는 상태인지 본다(E1). nil 이면 항상 쓸 수 있다고 본다. 오류 문장은 업무 용어 한국어다.
	ASPReady func(ctx context.Context) error
}

var (
	_ domain.TaxInvoiceService = (*TaxInvoiceService)(nil)
	_ domain.TaxDispatchStore  = (*TaxInvoiceService)(nil)
)

func NewTaxInvoiceService(b Base) *TaxInvoiceService { return &TaxInvoiceService{Base: b} }

// systemActor 는 사용자 없는 전송 작업의 감사 행위자다(웹훅 자동 중지와 같은 방식).
var taxSystemActor = contract.Principal{Role: "system", Channel: contract.ChannelWeb}

func digitsOnly(s string) string { return reDigits.ReplaceAllString(s, "") }

func taxNo(seq int64) string { return fmt.Sprintf("TI-%06d", seq) }

func today(b Base) string { return b.now().In(contract.KST).Format("2006-01-02") }

// ---- 입력 정규화·검증 ----

func (s *TaxInvoiceService) companyParty(ctx context.Context, q contract.Tx) (domain.TaxParty, error) {
	c, err := repository.SQLCompany{}.Get(ctx, q)
	if err != nil {
		return domain.TaxParty{}, err
	}
	return domain.TaxParty{BizNo: digitsOnly(c.BusinessNo), Name: c.Name, Rep: c.Representative, Addr: c.Address}, nil
}

func normParty(p domain.TaxParty) domain.TaxParty {
	p.BizNo, p.BranchNo = digitsOnly(p.BizNo), digitsOnly(p.BranchNo)
	p.Name, p.Rep, p.Addr, p.Email = strings.TrimSpace(p.Name), strings.TrimSpace(p.Rep), strings.TrimSpace(p.Addr), strings.TrimSpace(p.Email)
	return p
}

func checkParty(label string, p domain.TaxParty, strict bool) error {
	if p.BizNo != "" && len(p.BizNo) != 10 {
		return validation("%s 사업자번호는 숫자 10자리여야 합니다.", label)
	}
	if p.BranchNo != "" && len(p.BranchNo) != 4 {
		return validation("%s 종사업장번호는 숫자 4자리여야 합니다.", label)
	}
	if utf8.RuneCountInString(p.Name) > 100 || utf8.RuneCountInString(p.Rep) > 50 || utf8.RuneCountInString(p.Addr) > 200 || utf8.RuneCountInString(p.Email) > 80 {
		return validation("%s 상호·대표자·주소·이메일이 너무 깁니다.", label)
	}
	if p.Email != "" && !strings.Contains(p.Email, "@") {
		return validation("%s 이메일 형식이 올바르지 않습니다.", label)
	}
	if strict && (p.BizNo == "" || p.Name == "") {
		return validation("%s 사업자번호와 상호를 입력하세요.", label)
	}
	return nil
}

func validTaxType(t string) bool {
	return t == domain.TaxTaxable || t == domain.TaxZero || t == domain.TaxExempt
}

func validTaxDay(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// buildLines 는 입력 줄에서 저장 줄·공급가액 합·세액 합을 만든다. 세액은 줄별 VATFromSupply 의 합이다(원 미만 버림, 음수는 절댓값에 적용해 부호를 붙인다).
func buildLines(in []domain.TaxLineInput, taxType string, rate int, allowNegative bool) ([]domain.TaxLine, int64, int64, error) {
	if len(in) == 0 {
		return nil, 0, 0, validation("품목 줄을 하나 이상 입력하세요.")
	}
	if len(in) > taxMaxLines {
		return nil, 0, 0, validation("품목 줄은 %d개까지 입력할 수 있습니다.", taxMaxLines)
	}
	out := make([]domain.TaxLine, 0, len(in))
	var supply, tax int64
	for i, l := range in {
		name := strings.TrimSpace(l.Name)
		if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
			return nil, 0, 0, validation("%d번째 줄: 품목명은 1~100자로 입력하세요.", i+1)
		}
		if utf8.RuneCountInString(l.Spec) > 50 || utf8.RuneCountInString(l.Remark) > 100 {
			return nil, 0, 0, validation("%d번째 줄: 규격(50자)·비고(100자)가 너무 깁니다.", i+1)
		}
		if l.Day != "" {
			if _, err := time.Parse("01-02", l.Day); err != nil {
				return nil, 0, 0, validation("%d번째 줄: 일자는 MM-DD 형식으로 입력하세요.", i+1)
			}
		}
		sup := l.Supply
		if sup == 0 && l.Qty != 0 && l.UnitPrice != 0 {
			sup = l.Qty * l.UnitPrice
		}
		if sup == 0 {
			return nil, 0, 0, validation("%d번째 줄: 공급가액을 입력하세요.", i+1)
		}
		if sup < 0 && !allowNegative {
			return nil, 0, 0, validation("%d번째 줄: 공급가액은 0보다 커야 합니다(수정 발행에서만 음수를 쓸 수 있습니다).", i+1)
		}
		if sup > 1_000_000_000_000 || sup < -1_000_000_000_000 {
			return nil, 0, 0, validation("%d번째 줄: 공급가액이 너무 큽니다.", i+1)
		}
		var t int64
		if taxType == domain.TaxTaxable {
			if sup >= 0 {
				t = domain.VATFromSupply(sup, rate)
			} else {
				t = -domain.VATFromSupply(-sup, rate)
			}
		}
		out = append(out, domain.TaxLine{Seq: i + 1, Day: l.Day, Name: name, Spec: strings.TrimSpace(l.Spec), Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: sup, Tax: t, Remark: strings.TrimSpace(l.Remark)})
		supply += sup
		tax += t
	}
	return out, supply, tax, nil
}

// build 는 매출 입력을 저장 모양(합계·줄·안내)으로 바꾼다. 발행 전 엄격 검증은 checkIssuable 이 한다.
func (s *TaxInvoiceService) build(ctx context.Context, tx contract.Tx, in domain.TaxInvoiceInput, amend bool) (domain.TaxInvoice, error) {
	var v domain.TaxInvoice
	v.Direction, v.Source = domain.TaxSale, domain.TaxSourceManual
	v.TaxType = in.TaxType
	if v.TaxType == "" {
		v.TaxType = domain.TaxTaxable
	}
	if !validTaxType(v.TaxType) {
		return v, validation("과세 유형을 고르세요.")
	}
	v.ChargeType = in.ChargeType
	if v.ChargeType == "" {
		v.ChargeType = domain.TaxClaim
	}
	if v.ChargeType != domain.TaxReceipt && v.ChargeType != domain.TaxClaim {
		return v, validation("영수·청구를 고르세요.")
	}
	if !validTaxDay(in.IssueDate) {
		return v, validation("작성일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	v.IssueDate = in.IssueDate
	if utf8.RuneCountInString(in.Remark) > 200 {
		return v, validation("비고는 200자 이내로 입력하세요.")
	}
	v.Remark = strings.TrimSpace(in.Remark)
	v.Buyer = normParty(in.Buyer)
	if err := checkParty("공급받는자", v.Buyer, false); err != nil {
		return v, err
	}
	rate, err := s.vatRateOn(ctx, tx, v.IssueDate)
	if err != nil {
		return v, err
	}
	lines, supply, tax, err := buildLines(in.Lines, v.TaxType, rate, amend)
	if err != nil {
		return v, err
	}
	v.Lines, v.SupplyTotal, v.TaxTotal, v.Total = lines, supply, tax, supply+tax
	v.AmendReason = in.AmendReason
	if t := today(s.Base); v.IssueDate > t {
		v.Warnings = append(v.Warnings, "작성일이 오늘보다 뒤입니다. 발행하려면 오늘 이전 날짜여야 합니다.")
	}
	return v, nil
}

func (s *TaxInvoiceService) supplierSnapshot(ctx context.Context, tx contract.Tx) (domain.TaxParty, error) {
	c, err := s.companyParty(ctx, tx)
	if err != nil {
		return c, err
	}
	return c, nil
}

// checkIssuable 은 발행 직전 검증이다(명세 5.3): 미래 작성일 거부, 공급자·공급받는자 필수 칸, 수정세금계산서 규칙.
func (s *TaxInvoiceService) checkIssuable(ctx context.Context, tx contract.Tx, v domain.TaxInvoice, supplier domain.TaxParty) error {
	if v.ApprovalNo != "" {
		return validation("승인번호는 국세청이 부여하므로 비어 있어야 합니다.")
	}
	if !validTaxDay(v.IssueDate) || v.IssueDate > today(s.Base) {
		return validation("작성일이 오늘보다 뒤라 발행할 수 없습니다. 작성일을 오늘 이전으로 고치세요.")
	}
	if supplier.BizNo == "" || len(supplier.BizNo) != 10 || supplier.Name == "" || supplier.Rep == "" {
		return fmt.Errorf("%w: 회사 정보에 사업자번호(10자리)·회사명·대표자가 입력되어 있어야 발행할 수 있습니다. 관리자에게 회사 정보 입력을 요청하세요", contract.ErrInvalidState)
	}
	if err := checkParty("공급받는자", v.Buyer, true); err != nil {
		return err
	}
	if len(v.Buyer.BizNo) != 10 {
		return validation("공급받는자 사업자번호는 숫자 10자리여야 합니다.")
	}
	if len(v.Lines) == 0 || v.SupplyTotal+v.TaxTotal != v.Total {
		return validation("품목 줄과 합계가 맞지 않습니다.")
	}
	var sum int64
	for _, l := range v.Lines {
		sum += l.Supply
	}
	if sum != v.SupplyTotal {
		return validation("품목 줄의 공급가액 합이 합계와 맞지 않습니다.")
	}
	if v.TaxType != domain.TaxTaxable && v.TaxTotal != 0 {
		return validation("영세율·면세 계산서의 세액은 0원이어야 합니다.")
	}
	if v.AmendReason == "" {
		if v.SupplyTotal <= 0 {
			return validation("공급가액은 0보다 커야 발행할 수 있습니다.")
		}
		return nil
	}
	// 수정세금계산서: 원 건은 같은 공급자의 국세청 수신(accepted) 건이어야 한다.
	orig, err := s.Repo.Get(ctx, tx, v.AmendOfID)
	if err != nil {
		return validation("수정 발행의 원 세금계산서를 찾을 수 없습니다.")
	}
	if orig.Direction != domain.TaxSale || orig.Status != domain.TaxAccepted {
		return fmt.Errorf("%w: 국세청 수신 상태의 매출 세금계산서만 수정 발행할 수 있습니다", contract.ErrInvalidState)
	}
	switch v.AmendReason {
	case domain.AmendReturn, domain.AmendContractCancel, domain.AmendDoubleIssue:
		if v.SupplyTotal >= 0 {
			return validation("환입·계약 해제·이중 발급 정정은 음수 금액이어야 합니다.")
		}
		// 같은 원 건을 정정한 다른 건(취소 제외)까지 합친 음수 합이 원 건 공급가액을 넘을 수 없다.
		var others int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(supply_total),0) FROM tax_invoices WHERE `+contract.SQLEntity(ctx, "tax_invoices")+` AND amend_of_id = ? AND id <> ? AND status <> 'canceled' AND supply_total < 0`, v.AmendOfID, v.ID).Scan(&others); err != nil {
			return err
		}
		if others+v.SupplyTotal < -orig.SupplyTotal {
			return validation("원 세금계산서 공급가액을 넘어 정정할 수 없습니다.")
		}
	}
	return nil
}

func labelWon(n int64) string { return formatWon(n) }

// ---- 응답 변환(마스킹) ----

// out 은 호출자 기준으로 가린 응답이다. 이메일은 1단계에서 누구에게나 가려서 나가고(원문 열람 기록은 2단계), 사업자번호는 재무 화면에서는 원문(업무 식별자),
// MCP·API 채널에는 가려서 나간다(B41 D6).
func (s *TaxInvoiceService) out(p contract.Principal, v domain.TaxInvoice) domain.TaxInvoice {
	maskEmail := func(e string) string {
		if e == "" || s.Masker == nil {
			return e
		}
		return s.Masker.MaskField(contract.Principal{Role: contract.RoleStaff, Channel: p.Channel}, contract.FieldEmail, e)
	}
	v.Supplier.Email, v.Buyer.Email = maskEmail(v.Supplier.Email), maskEmail(v.Buyer.Email)
	if !contract.RawFieldAllowed(p) && (p.Channel == contract.ChannelMCP || p.Channel == contract.ChannelAPI) && s.Masker != nil {
		mask := func(b string) string {
			if b == "" {
				return b
			}
			return s.Masker.MaskField(contract.Principal{Role: contract.RoleStaff, Channel: p.Channel}, contract.FieldBusinessNo, b)
		}
		v.Supplier.BizNo, v.Buyer.BizNo = mask(v.Supplier.BizNo), mask(v.Buyer.BizNo)
	}
	if v.LastCode != "" {
		v.LastCodeText = domain.TaxCodeLabels[v.LastCode]
	}
	return v
}

// ---- 쓰기 ----

func (s *TaxInvoiceService) aspReady(ctx context.Context) error {
	if s.ASPReady == nil {
		return nil
	}
	return s.ASPReady(ctx)
}

func (s *TaxInvoiceService) saveNew(ctx context.Context, tx contract.Tx, p contract.Principal, v domain.TaxInvoice, supplier domain.TaxParty) (int64, error) {
	seq, err := s.Repo.NextSeq(ctx, tx)
	if err != nil {
		return 0, err
	}
	v.No, v.CreatedBy, v.Supplier = taxNo(seq), p.UserID, supplier
	id, err := s.Repo.Insert(ctx, tx, seq, v, s.now())
	if err != nil {
		return 0, err
	}
	if err := s.Repo.ReplaceLines(ctx, tx, id, v.Lines); err != nil {
		return 0, err
	}
	return id, nil
}

// Create 는 매출 세금계산서를 작성 중(draft)으로 만든다. 공급자는 회사 정보로 채워진다(비어 있으면 작성은 되고 발행이 거부된다).
func (s *TaxInvoiceService) Create(ctx context.Context, in domain.TaxInvoiceInput) (domain.TaxInvoice, error) {
	const action = "taxinvoice.create"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	in.AmendReason = ""
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, 0, func(tx contract.Tx) error {
		v, err := s.build(ctx, tx, in, false)
		if err != nil {
			return err
		}
		v.Status = domain.TaxDraft
		supplier, err := s.supplierSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		id, err := s.saveNew(ctx, tx, p, v, supplier)
		if err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		out.Warnings = v.Warnings
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, taxDiff(out)...)
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

func taxDiff(v domain.TaxInvoice) []audit.DiffItem {
	return []audit.DiffItem{{Key: "direction", New: domain.TaxDirectionLabels[v.Direction]}, {Key: "tax_type", New: domain.TaxTypeLabels[v.TaxType]}, {Key: "issue_date", New: v.IssueDate},
		{Key: "supply_total", New: labelWon(v.SupplyTotal)}, {Key: "tax_total", New: labelWon(v.TaxTotal)}}
}

// Update 는 작성 중인 매출 건의 내용을 고친다.
func (s *TaxInvoiceService) Update(ctx context.Context, id int64, in domain.TaxInvoiceInput) (domain.TaxInvoice, error) {
	const action = "taxinvoice.update"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Direction != domain.TaxSale || cur.Status != domain.TaxDraft {
			return fmt.Errorf("%w: 작성 중인 매출 세금계산서만 고칠 수 있습니다", contract.ErrInvalidState)
		}
		in.AmendReason = cur.AmendReason
		v, err := s.build(ctx, tx, in, cur.AmendReason != "")
		if err != nil {
			return err
		}
		// 이메일 칸을 비우면 변경 없음(화면에는 가려진 값만 나가므로 원문을 되돌려 보여 줄 수 없다).
		if v.Buyer.Email == "" {
			v.Buyer.Email = cur.Buyer.Email
		}
		v.ID = id
		if err := s.Repo.UpdateDraft(ctx, tx, v, s.now()); err != nil {
			return err
		}
		if err := s.Repo.ReplaceLines(ctx, tx, id, v.Lines); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		out.Warnings = v.Warnings
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, taxDiff(out)...)
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

func reasonText(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(r); n < 1 || n > 200 {
		return "", validation("사유를 1~200자로 입력하세요.")
	}
	return r, nil
}

// Cancel 은 작성 중(draft)인 매출 건을 취소하거나, 실패(failed)한 건을 포기(취소)한다.
func (s *TaxInvoiceService) Cancel(ctx context.Context, id int64, reason string) (domain.TaxInvoice, error) {
	const action = "taxinvoice.cancel"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	r, err := reasonText(reason)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		ev := tevCancel
		if cur.Status == domain.TaxFailed {
			ev = tevAbandon
		}
		to, err := taxTransition(cur.Direction, cur.Status, ev)
		if err != nil {
			return err
		}
		if cur.Direction != domain.TaxSale {
			return fmt.Errorf("%w: 수신한 세금계산서는 등록 취소로 처리합니다", contract.ErrInvalidState)
		}
		if err := s.Repo.Transition(ctx, tx, id, cur.Status, to, s.now(), `canceled_by = ?, canceled_at = ?, cancel_reason = ?, next_try_at = '', claimed_at = ''`, p.UserID, fmtRFC(s.now()), r); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, audit.DiffItem{Key: "direction", New: domain.TaxDirectionLabels[cur.Direction]}, audit.DiffItem{Key: "reason_len", New: fmt.Sprint(utf8.RuneCountInString(r))})
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

func fmtRFC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Issue 는 작성 중인 매출 건을 발행한다(발행 대기). 검증(5.3)·회사 정보 스냅샷·SoD tax.create_issue 판정 뒤 금액과 당사자가 고정되고 전송 대기가 된다.
func (s *TaxInvoiceService) Issue(ctx context.Context, id int64) (domain.TaxInvoice, error) {
	const action = "taxinvoice.issue"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceIssue, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	if err := s.aspReady(ctx); err != nil {
		return domain.TaxInvoice{}, err
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := taxTransition(cur.Direction, cur.Status, tevIssue)
		if err != nil {
			return err
		}
		supplier, err := s.supplierSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		// 작성 이후 세율이 바뀌었을 수 있으니 세액을 다시 계산해 저장한 값과 같은지 본다(다르면 작성 화면에서 다시 저장해야 한다).
		rate, err := s.vatRateOn(ctx, tx, cur.IssueDate)
		if err != nil {
			return err
		}
		inputs := make([]domain.TaxLineInput, len(cur.Lines))
		for i, l := range cur.Lines {
			inputs[i] = domain.TaxLineInput{Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Remark: l.Remark}
		}
		if _, sup, tax, berr := buildLines(inputs, cur.TaxType, rate, cur.AmendReason != ""); berr != nil || sup != cur.SupplyTotal || tax != cur.TaxTotal {
			return fmt.Errorf("%w: 부가세율이 바뀌어 세액이 달라졌습니다. 작성 화면에서 다시 저장한 뒤 발행하세요", contract.ErrInvalidState)
		}
		if err := s.checkIssuable(ctx, tx, cur, supplier); err != nil {
			return err
		}
		if _, err := s.sodCheck(ctx, tx, p, sod.RuleTaxCreateIssue, cur.CreatedBy, sod.Ref{Type: "tax_invoice", ID: id, No: cur.No}); err != nil {
			return err
		}
		if err := s.Repo.SetSupplier(ctx, tx, id, supplier); err != nil {
			return err
		}
		now := s.now()
		if err := s.Repo.Transition(ctx, tx, id, cur.Status, to, now, `issued_by = ?, issued_at = ?, attempts = 0, next_try_at = ?, claimed_at = '', last_code = ''`, p.UserID, fmtRFC(now), fmtRFC(now)); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, taxDiff(out)...)
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

// Resend 는 실패(failed)한 매출 건을 다시 전송 대기로 돌린다(건당 5회).
func (s *TaxInvoiceService) Resend(ctx context.Context, id int64) (domain.TaxInvoice, error) {
	const action = "taxinvoice.retry"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceIssue, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	if err := s.aspReady(ctx); err != nil {
		return domain.TaxInvoice{}, err
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := taxTransition(cur.Direction, cur.Status, tevResend)
		if err != nil {
			return err
		}
		if cur.Resends >= taxMaxResends {
			return fmt.Errorf("%w: 한 건은 %d번까지만 다시 전송할 수 있습니다. 취소하고 새로 작성하세요", contract.ErrInvalidState, taxMaxResends)
		}
		now := s.now()
		if err := s.Repo.Transition(ctx, tx, id, cur.Status, to, now, `resends = resends + 1, attempts = 0, next_try_at = ?, claimed_at = '', last_code = ''`, fmtRFC(now)); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, audit.DiffItem{Key: "resends", New: fmt.Sprint(out.Resends)})
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

// AmendDraft 는 국세청 수신(accepted) 상태의 매출 건을 정정하는 수정세금계산서 초안을 만든다(D5). 환입·계약 해제·이중 발급은 원 건 줄을 음수로 복사하고,
// 그 밖의 사유는 원 건 줄을 그대로 복사해 고치게 한다. 새 건도 일반 전이를 탄다.
func (s *TaxInvoiceService) AmendDraft(ctx context.Context, id int64, reason string) (domain.TaxInvoice, error) {
	const action = "taxinvoice.amend_draft"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	if _, ok := domain.AmendReasonLabels[reason]; !ok {
		return domain.TaxInvoice{}, validation("수정 사유를 고르세요.")
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		orig, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if orig.Direction != domain.TaxSale || orig.Status != domain.TaxAccepted {
			return fmt.Errorf("%w: 국세청 수신 상태의 매출 세금계산서만 수정 발행할 수 있습니다", contract.ErrInvalidState)
		}
		sign := int64(1)
		switch reason {
		case domain.AmendReturn, domain.AmendContractCancel, domain.AmendDoubleIssue:
			sign = -1
		}
		inputs := make([]domain.TaxLineInput, len(orig.Lines))
		for i, l := range orig.Lines {
			inputs[i] = domain.TaxLineInput{Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty * sign, UnitPrice: l.UnitPrice, Supply: l.Supply * sign, Remark: l.Remark}
		}
		in := domain.TaxInvoiceInput{TaxType: orig.TaxType, IssueDate: today(s.Base), Buyer: orig.Buyer, Lines: inputs, ChargeType: orig.ChargeType, AmendReason: reason}
		v, err := s.build(ctx, tx, in, true)
		if err != nil {
			return err
		}
		v.Status, v.AmendOfID = domain.TaxDraft, orig.ID
		supplier, err := s.supplierSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		nid, err := s.saveNew(ctx, tx, p, v, supplier)
		if err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, nid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, nid, append(taxDiff(out), audit.DiffItem{Key: "amend_reason", New: domain.AmendReasonLabels[reason]})...)
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

// RegisterReceived 는 매입 세금계산서를 수기로 등록한다(승인번호 필수). 대행사 없이도 대조·집계를 쓸 수 있게 한다. 같은 승인번호는 거부한다(D4).
func (s *TaxInvoiceService) RegisterReceived(ctx context.Context, in domain.TaxReceivedInput) (domain.TaxInvoice, error) {
	const action = "taxinvoice.manual_receive"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	approval := strings.TrimSpace(in.ApprovalNo)
	if !reApproval.MatchString(approval) {
		return domain.TaxInvoice{}, validation("승인번호는 영문·숫자 24자리여야 합니다.")
	}
	approval = strings.ToUpper(approval)
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, 0, func(tx contract.Tx) error {
		if id, no, err := s.Repo.IDByApproval(ctx, tx, approval); err != nil {
			return err
		} else if id != 0 {
			return validation("이미 등록된 승인번호입니다(%s).", no)
		}
		buyer, err := s.companyParty(ctx, tx)
		if err != nil {
			return err
		}
		if len(buyer.BizNo) != 10 {
			return fmt.Errorf("%w: 회사 정보에 사업자번호를 입력해야 수신 세금계산서를 등록할 수 있습니다. 관리자에게 회사 정보 입력을 요청하세요", contract.ErrInvalidState)
		}
		var v domain.TaxInvoice
		v.Direction, v.Source, v.Status, v.ApprovalNo = domain.TaxPurchase, domain.TaxSourceManual, domain.TaxAccepted, approval
		v.ChargeType = domain.TaxClaim
		v.TaxType = in.TaxType
		if v.TaxType == "" {
			v.TaxType = domain.TaxTaxable
		}
		if !validTaxType(v.TaxType) {
			return validation("과세 유형을 고르세요.")
		}
		if !validTaxDay(in.IssueDate) || in.IssueDate > today(s.Base) {
			return validation("작성일은 오늘 이전의 YYYY-MM-DD 형식이어야 합니다.")
		}
		v.IssueDate = in.IssueDate
		v.Supplier = normParty(in.Supplier)
		if err := checkParty("공급자", v.Supplier, true); err != nil {
			return err
		}
		if len(v.Supplier.BizNo) != 10 {
			return validation("공급자 사업자번호는 숫자 10자리여야 합니다.")
		}
		v.Buyer = buyer
		if utf8.RuneCountInString(in.Remark) > 200 {
			return validation("비고는 200자 이내로 입력하세요.")
		}
		v.Remark = strings.TrimSpace(in.Remark)
		v.AmendReason = in.AmendReason
		if v.AmendReason != "" {
			if _, ok := domain.AmendReasonLabels[v.AmendReason]; !ok {
				return validation("수정 사유를 고르세요.")
			}
		}
		v.SupplyTotal, v.TaxTotal = in.SupplyTotal, in.TaxTotal
		v.Total = v.SupplyTotal + v.TaxTotal
		if v.AmendReason == "" && (v.SupplyTotal <= 0 || v.TaxTotal < 0) {
			return validation("공급가액은 0보다 커야 합니다(음수는 수정세금계산서에서만 가능합니다).")
		}
		if v.TaxType != domain.TaxTaxable && v.TaxTotal != 0 {
			return validation("영세율·면세 계산서의 세액은 0원이어야 합니다.")
		}
		rate, err := s.vatRateOn(ctx, tx, v.IssueDate)
		if err != nil {
			return err
		}
		if v.TaxType == domain.TaxTaxable && v.AmendReason == "" && v.TaxTotal != domain.VATFromSupply(v.SupplyTotal, rate) {
			v.Warnings = append(v.Warnings, "세액이 공급가액의 부가세율 계산값과 다릅니다. 승인번호의 세금계산서와 같은지 확인하세요.")
		}
		if len(in.Lines) > 0 {
			lines, sup, _, err := buildLines(in.Lines, domain.TaxExempt, rate, v.AmendReason != "") // 줄 세액은 입력 합계를 따르므로 계산하지 않는다
			if err != nil {
				return err
			}
			if sup != v.SupplyTotal {
				return validation("품목 줄의 공급가액 합이 합계와 맞지 않습니다.")
			}
			v.Lines = lines
		}
		if in.AmendOfApprovalNo != "" {
			if v.AmendReason == "" {
				return validation("원 승인번호를 쓰려면 수정 사유를 고르세요.")
			}
			if oid, _, err := s.Repo.IDByApproval(ctx, tx, strings.ToUpper(strings.TrimSpace(in.AmendOfApprovalNo))); err != nil {
				return err
			} else if oid != 0 {
				v.AmendOfID = oid
			} else {
				v.Warnings = append(v.Warnings, "원 세금계산서를 찾지 못해 연결하지 않았습니다.")
			}
		}
		v.AcceptedAt = fmtRFC(s.now())
		id, err := s.saveNew(ctx, tx, p, v, v.Supplier)
		if err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		out.Warnings = v.Warnings
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, append(taxDiff(out), audit.DiffItem{Key: "approval_no", New: approval})...)
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	s.proposeSystem(ctx) // 새 수신 건의 대조 제안을 만든다(실패해도 등록은 유지)
	return s.present(ctx, p, out)
}

// CancelReceived 는 수기로 등록한 매입 건의 등록을 취소한다(대행사에서 받은 건은 취소할 수 없다).
func (s *TaxInvoiceService) CancelReceived(ctx context.Context, id int64, reason string) (domain.TaxInvoice, error) {
	const action = "taxinvoice.manual_cancel"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	r, err := reasonText(reason)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	var out domain.TaxInvoice
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := taxTransition(cur.Direction, cur.Status, tevCancelReceived)
		if err != nil {
			return err
		}
		if cur.Source != domain.TaxSourceManual {
			return fmt.Errorf("%w: 대행사에서 받은 세금계산서는 취소할 수 없습니다", contract.ErrInvalidState)
		}
		if err := s.Repo.Transition(ctx, tx, id, cur.Status, to, s.now(), `canceled_by = ?, canceled_at = ?, cancel_reason = ?`, p.UserID, fmtRFC(s.now()), r); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, id, audit.DiffItem{Key: "reason_len", New: fmt.Sprint(utf8.RuneCountInString(r))})
	})
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	return s.present(ctx, p, out)
}

// ---- 조회 ----

// Get 은 한 건이다(taxinvoice:read). 부서 격리는 없다(재무 증빙).
func (s *TaxInvoiceService) Get(ctx context.Context, id int64) (domain.TaxInvoice, error) {
	p, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead)
	if err != nil {
		return domain.TaxInvoice{}, err
	}
	v, err := s.Repo.Get(ctx, s.DB, id)
	if err != nil {
		return v, err
	}
	if v.Direction == domain.TaxPurchase {
		st, res, err := s.Match.StateFor(ctx, s.DB, id)
		if err != nil {
			return v, err
		}
		v.MatchStatus, v.MatchResult = domain.MatchStatusLabels[st], domain.MatchResultLabels[res]
	}
	return s.present(ctx, p, v)
}

// List 는 목록이다(taxinvoice:read).
func (s *TaxInvoiceService) List(ctx context.Context, f domain.TaxInvoiceFilter) (domain.TaxInvoicePage, error) {
	p, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead)
	if err != nil {
		return domain.TaxInvoicePage{}, err
	}
	for _, d := range []string{f.From, f.To} {
		if d != "" && !validTaxDay(d) {
			return domain.TaxInvoicePage{}, validation("기간은 YYYY-MM-DD 형식으로 입력하세요.")
		}
	}
	f.Counterparty = strings.TrimSpace(f.Counterparty)
	f.ApprovalNo = strings.ToUpper(strings.TrimSpace(f.ApprovalNo))
	rows, total, err := s.Repo.List(ctx, s.DB, f)
	if err != nil {
		return domain.TaxInvoicePage{}, err
	}
	if f.WithLinks {
		for i := range rows {
			if rows[i].LineCount, rows[i].JournalNos, err = s.Match.Links(ctx, s.DB, rows[i].ID); err != nil {
				return domain.TaxInvoicePage{}, err
			}
			if rows[i].Direction == domain.TaxPurchase {
				st, res, err := s.Match.StateFor(ctx, s.DB, rows[i].ID)
				if err != nil {
					return domain.TaxInvoicePage{}, err
				}
				rows[i].MatchStatus, rows[i].MatchResult = domain.MatchStatusLabels[st], domain.MatchResultLabels[res]
			}
		}
	}
	if rows, err = s.presentList(ctx, p, rows); err != nil {
		return domain.TaxInvoicePage{}, err
	}
	return domain.TaxInvoicePage{Rows: rows, Total: total}, nil
}

// ---- 전송 작업이 쓰는 입구(domain.TaxDispatchStore) ----

// ClaimDue 는 보낼 때가 된 일을 가져간다.
func (s *TaxInvoiceService) ClaimDue(ctx context.Context, now time.Time, limit int) ([]domain.TaxDispatchJob, error) {
	var jobs []domain.TaxDispatchJob
	err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		jobs = nil
		ids, err := s.Repo.ClaimDue(ctx, tx, now, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			v, err := s.Repo.Get(ctx, tx, id)
			if err != nil {
				return err
			}
			j := domain.TaxDispatchJob{ID: id, No: v.No, Attempt: v.Attempts + 1}
			if v.Status == domain.TaxSent {
				j.Kind, j.ProviderRef = "status", v.ProviderRef
				j.SentAt = parseTaxTime(v.SentAt)
			} else {
				j.Kind, j.Invoice = "issue", v
			}
			jobs = append(jobs, j)
		}
		return nil
	})
	return jobs, err
}

func parseTaxTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// dispatchWrite 는 전송 작업이 건 하나의 상태를 바꾸는 입구다. 작업에는 요청 법인이 없으므로 건이 속한 법인을 context 에 실어 감사·알림이 그 법인으로 기록되게 한다.
func (s *TaxInvoiceService) dispatchWrite(ctx context.Context, id int64, action string, fn func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error) error {
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		return fn(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: cur.EntityID}), tx, cur)
	})
}

// Submitted 는 발행 요청이 대행사에 접수되었음을 반영한다(issued → sent).
func (s *TaxInvoiceService) Submitted(ctx context.Context, id int64, providerRef string, httpStatus int) error {
	return s.dispatchWrite(ctx, id, "taxinvoice.send", func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error {
		to, err := taxTransition(cur.Direction, cur.Status, tevSend)
		if err != nil {
			return err
		}
		now := s.now()
		attempt := cur.Attempts + 1
		if err := s.Repo.Transition(ctx, tx, id, cur.Status, to, now, `provider_ref = ?, sent_at = ?, attempts = ?, next_try_at = ?, claimed_at = '', last_code = ''`,
			providerRef, fmtRFC(now), attempt, fmtRFC(now.Add(taxPollBackoff[0]))); err != nil {
			return err
		}
		if err := s.Repo.AddTransmission(ctx, tx, id, attempt, "issue", "ok", httpStatus, providerRef, now); err != nil {
			return err
		}
		return s.record(ctx, tx, taxSystemActor, "taxinvoice.send", audit.TargetTaxInvoice, id, audit.DiffItem{Key: "attempts", New: fmt.Sprint(attempt)}, audit.DiffItem{Key: "result_code", New: "ok"})
	})
}

// Retry 는 일시 오류를 반영한다: 자동 재시도 한도 안이면 다음 시도를 예약하고, 넘으면 failed(retry_over)로 간다.
func (s *TaxInvoiceService) Retry(ctx context.Context, id int64, code string, httpStatus int) error {
	return s.dispatchWrite(ctx, id, "taxinvoice.fail", func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error {
		if cur.Status != domain.TaxIssued {
			return fmt.Errorf("%w: 발행 대기 상태가 아닙니다", contract.ErrInvalidState)
		}
		now := s.now()
		attempt := cur.Attempts + 1
		if attempt > len(taxRetryBackoff) {
			return s.failTx(ctx, tx, cur, attempt, "retry_over", httpStatus, now)
		}
		if err := s.Repo.AddTransmission(ctx, tx, id, attempt, "issue", code, httpStatus, "", now); err != nil {
			return err
		}
		return s.Repo.Exec(ctx, tx, id, domain.TaxIssued, now, `attempts = ?, next_try_at = ?, claimed_at = '', last_code = ?`, attempt, fmtRFC(now.Add(taxRetryBackoff[attempt-1])), code)
	})
}

// Fail 은 영구 오류를 반영한다(issued·sent → failed).
func (s *TaxInvoiceService) Fail(ctx context.Context, id int64, code string, httpStatus int) error {
	return s.dispatchWrite(ctx, id, "taxinvoice.fail", func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error {
		attempt := cur.Attempts
		if cur.Status == domain.TaxIssued {
			attempt++
		}
		return s.failTx(ctx, tx, cur, attempt, code, httpStatus, s.now())
	})
}

func (s *TaxInvoiceService) failTx(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice, attempt int, code string, httpStatus int, now time.Time) error {
	to, err := taxTransition(cur.Direction, cur.Status, tevFail)
	if err != nil {
		return err
	}
	if err := s.Repo.Transition(ctx, tx, cur.ID, cur.Status, to, now, `attempts = ?, next_try_at = '', claimed_at = '', last_code = ?`, attempt, code); err != nil {
		return err
	}
	kind := "issue"
	if cur.Status == domain.TaxSent {
		kind = "status"
	}
	if err := s.Repo.AddTransmission(ctx, tx, cur.ID, max(attempt, 1), kind, code, httpStatus, cur.ProviderRef, now); err != nil {
		return err
	}
	// 실패 알림(tax_invoice_failed, 명세 11절): 발행 권한자에게 같은 트랜잭션에서. 본문에 번호·금액·상대방·사유 코드를 싣지 않는다(AC-4317).
	if err := s.NotifyPermissionHolders(ctx, tx, contract.PermTaxInvoiceIssue, NotifyTaxInvoiceFailed, "세금계산서 전송에 실패했습니다",
		"전송하지 못한 세금계산서가 있습니다. 세금계산서 화면에서 사유를 확인하고 다시 전송하거나 포기하세요.", "/finance/tax-invoices", "tax_invoice", cur.ID, now); err != nil {
		return err
	}
	return s.record(ctx, tx, taxSystemActor, "taxinvoice.fail", audit.TargetTaxInvoice, cur.ID, audit.DiffItem{Key: "attempts", New: fmt.Sprint(attempt)}, audit.DiffItem{Key: "result_code", New: code})
}

// Accepted 는 국세청 수신 확인을 반영한다(sent → accepted). 승인번호가 이미 다른 건에 있으면 failed(duplicate_approval_no)다.
func (s *TaxInvoiceService) Accepted(ctx context.Context, id int64, approvalNo string) error {
	return s.dispatchWrite(ctx, id, "taxinvoice.accept", func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error {
		now := s.now()
		approval := strings.ToUpper(strings.TrimSpace(approvalNo))
		if !reApproval.MatchString(approval) {
			return s.failTx(ctx, tx, cur, cur.Attempts, "provider_rejected", 0, now)
		}
		if oid, _, err := s.Repo.IDByApproval(ctx, tx, approval); err != nil {
			return err
		} else if oid != 0 && oid != cur.ID {
			return s.failTx(ctx, tx, cur, cur.Attempts, "duplicate_approval_no", 0, now)
		}
		to, err := taxTransition(cur.Direction, cur.Status, tevAccept)
		if err != nil {
			return err
		}
		if err := s.Repo.Transition(ctx, tx, cur.ID, cur.Status, to, now, `approval_no = ?, accepted_at = ?, next_try_at = '', claimed_at = '', last_code = ''`, approval, fmtRFC(now)); err != nil {
			return err
		}
		if err := s.Repo.AddTransmission(ctx, tx, cur.ID, max(cur.Attempts, 1), "status", "ok", 0, cur.ProviderRef, now); err != nil {
			return err
		}
		return s.record(ctx, tx, taxSystemActor, "taxinvoice.accept", audit.TargetTaxInvoice, cur.ID, audit.DiffItem{Key: "approval_no", New: approval})
	})
}

// StillPending 은 아직 국세청 수신 전이라 다음 조회를 예약한다. 7일을 넘기면 failed(status_timeout)다.
func (s *TaxInvoiceService) StillPending(ctx context.Context, id int64) error {
	return s.dispatchWrite(ctx, id, "taxinvoice.fail", func(ctx context.Context, tx contract.Tx, cur domain.TaxInvoice) error {
		if cur.Status != domain.TaxSent {
			return fmt.Errorf("%w: 전송됨 상태가 아닙니다", contract.ErrInvalidState)
		}
		now := s.now()
		if sent := parseTaxTime(cur.SentAt); !sent.IsZero() && now.Sub(sent) > taxStatusTimeout {
			return s.failTx(ctx, tx, cur, cur.Attempts, "status_timeout", 0, now)
		}
		n := 0
		for _, t := range cur.Transmissions {
			if t.Kind == "status" {
				n++
			}
		}
		if err := s.Repo.AddTransmission(ctx, tx, cur.ID, max(cur.Attempts, 1), "status", "pending", 0, cur.ProviderRef, now); err != nil {
			return err
		}
		idx := n + 1
		if idx >= len(taxPollBackoff) {
			idx = len(taxPollBackoff) - 1
		}
		return s.Repo.Exec(ctx, tx, cur.ID, domain.TaxSent, now, `next_try_at = ?, claimed_at = ''`, fmtRFC(now.Add(taxPollBackoff[idx])))
	})
}

var _ = errors.New

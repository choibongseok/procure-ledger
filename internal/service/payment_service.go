package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 지급 요청·지급 처리(B32 ①, 3.1·4절).
//   - 상태 전이는 paymentTransitions 표 한 곳에서만 정의한다(표에 없는 전이는 전부 거부, DB 트리거가 한 번 더 막는다).
//   - 채무 항목의 예약·지급 누계를 바꾸는 서비스 함수는 reservePayable·releasePayable·settlePayment·restorePayment 뿐이다(소스 검사 AC-1203).
//   - 지급 처리 한 트랜잭션: 지급 행·누계·예약 해제·지급 전표(승인 상태로 바로)·자금 행·감사. 하나라도 실패하면 전부 취소된다.
//   - 지급 요청 작성자는 지급 처리할 수 없다(D8). 계좌번호는 마스킹 값만 응답에 싣고 원문은 RevealPayee 로만 나간다.

type paymentEvent string

const (
	pevRequest paymentEvent = "request"
	pevApprove paymentEvent = "approve"
	pevReject  paymentEvent = "reject" // 결재 반려
	pevReset   paymentEvent = "reset"  // 상신 취소·회수
	pevCancel  paymentEvent = "cancel"
	pevSettle  paymentEvent = "settle"
	pevReverse paymentEvent = "reverse" // 지급 전표 역분개 승인
)

type paymentKey struct {
	from string
	ev   paymentEvent
}

// paymentTransitions 는 지급 건 상태 전이 표다(명세 4절).
var paymentTransitions = map[paymentKey]string{
	{domain.PaymentDraft, pevRequest}:     domain.PaymentRequested,
	{domain.PaymentRequested, pevApprove}: domain.PaymentApproved,
	{domain.PaymentRequested, pevReject}:  domain.PaymentDraft,
	{domain.PaymentRequested, pevReset}:   domain.PaymentDraft,
	{domain.PaymentDraft, pevCancel}:      domain.PaymentCanceled,
	{domain.PaymentRequested, pevCancel}:  domain.PaymentCanceled,
	{domain.PaymentApproved, pevCancel}:   domain.PaymentCanceled,
	{domain.PaymentApproved, pevSettle}:   domain.PaymentPaid,
	{domain.PaymentPaid, pevReverse}:      domain.PaymentPaidCanceled,
}

func paymentTransition(from string, ev paymentEvent) (string, error) {
	to, ok := paymentTransitions[paymentKey{from, ev}]
	if !ok {
		return "", fmt.Errorf("%w: %s 상태의 지급 건은 이 작업을 할 수 없습니다", contract.ErrInvalidState, domain.PaymentStatusLabels[from])
	}
	return to, nil
}

// maskAccount 는 계좌번호를 끝 4자리만 보이게 가린다.
func maskAccount(s string) string { return repository.MaskAccount(s) }

// PaymentService 는 domain.PaymentService 의 구현이다.
type PaymentService struct {
	Base
	Repo     repository.SQLPayments
	Ledger   repository.SQLLedger
	Docs     *ApprovalDocService
	Journals *JournalService
}

var _ domain.PaymentService = (*PaymentService)(nil)

func NewPaymentService(b Base, docs *ApprovalDocService, journals *JournalService) *PaymentService {
	return &PaymentService{Base: b, Docs: docs, Journals: journals}
}

const maxPaymentItems = 100

// ---- 예약·누계를 바꾸는 지정 함수 ----

// reservePayable 은 항목별 예약을 올린다. 지급 가능액을 넘으면 거부한다(동시 요청 중 한쪽만 성공).
func reservePayable(ctx context.Context, tx contract.Tx, repo repository.SQLPayments, items []domain.PaymentItemInput) error {
	for _, it := range items {
		if err := repo.ReservePayable(ctx, tx, it.PayableID, it.Amount); err != nil {
			if errors.Is(err, contract.ErrValidation) {
				info, ierr := repo.PayableInfo(ctx, tx, it.PayableID)
				avail := int64(0)
				if ierr == nil {
					avail = info.Available
				}
				return validation("지급 가능액을 넘습니다(남은 지급 가능액 %s).", moneyLabel(avail))
			}
			return err
		}
	}
	return nil
}

// releasePayable 은 항목별 예약을 푼다.
func releasePayable(ctx context.Context, tx contract.Tx, repo repository.SQLPayments, items []domain.PaymentItem) error {
	for _, it := range items {
		if err := repo.ReleasePayable(ctx, tx, it.PayableID, it.Amount); err != nil {
			return err
		}
	}
	return nil
}

func toItemInputs(items []domain.PaymentItem) []domain.PaymentItemInput {
	out := make([]domain.PaymentItemInput, len(items))
	for i, it := range items {
		out[i] = domain.PaymentItemInput{PayableID: it.PayableID, Amount: it.Amount}
	}
	return out
}

// ---- 조회 ----

func (s *PaymentService) toPayment(ctx context.Context, q contract.Tx, r repository.PaymentRow) (domain.Payment, error) {
	items, _, err := s.Repo.PaymentItems(ctx, q, r.ID)
	if err != nil {
		return domain.Payment{}, err
	}
	p := domain.Payment{ID: r.ID, No: r.No, VendorID: r.VendorID, VendorName: r.VendorName, Status: r.Status, Total: r.Total, DueDate: r.DueDate, BankAccountID: r.BankAccountID,
		BankAlias: r.BankAlias, PayeeAccount: r.PayeeMasked, Memo: r.Memo, DocID: r.DocID, DocNo: r.DocNo, CreatedByID: r.CreatedByID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		ApprovedAt: r.ApprovedAt, CancelReason: r.CancelReason, ReverseReason: r.ReverseReason, Items: items, PaidDate: r.PaidDate, TransferRef: r.TransferRef, PaidBy: r.PaidBy, JournalNo: r.JournalNo}
	if r.JournalID != 0 {
		var rev string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT no FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND reverses_id = ? AND status <> 'canceled' LIMIT 1),'')`, r.JournalID).Scan(&rev); err == nil {
			p.ReversalNo = rev
		}
	}
	if (r.Status == domain.PaymentApproved || r.Status == domain.PaymentRequested) && strings.TrimSpace(r.VendorAccount) != r.PayeeEnc {
		p.AccountDiffers = true
	}
	return p, nil
}

// Candidates 는 거래처의 지급 가능한 채무 항목이다.
func (s *PaymentService) Candidates(ctx context.Context, vendorID int64) ([]domain.PayableCandidate, error) {
	if _, err := contract.Authorize(ctx, contract.PermPaymentRead); err != nil {
		return nil, err
	}
	if ok, err := s.Ledger.VendorExists(ctx, s.DB, vendorID); err != nil {
		return nil, err
	} else if !ok {
		return nil, contract.ErrNotFound
	}
	out, err := s.Repo.Candidates(ctx, s.DB, vendorID, s.kstDay())
	if out == nil {
		out = []domain.PayableCandidate{}
	}
	return out, err
}

// List 는 지급 요청 목록이다.
func (s *PaymentService) List(ctx context.Context, f domain.PaymentFilter) (domain.PaymentPage, error) {
	if _, err := contract.Authorize(ctx, contract.PermPaymentRead); err != nil {
		return domain.PaymentPage{}, err
	}
	if f.Status != "" {
		if _, ok := domain.PaymentStatusLabels[f.Status]; !ok {
			return domain.PaymentPage{}, validation("알 수 없는 상태입니다.")
		}
	}
	rows, total, err := s.Repo.List(ctx, s.DB, f)
	if err != nil {
		return domain.PaymentPage{}, err
	}
	out := domain.PaymentPage{Total: total, Rows: make([]domain.Payment, 0, len(rows))}
	for _, r := range rows {
		p, err := s.toPayment(ctx, s.DB, r)
		if err != nil {
			return out, err
		}
		p.Items = nil
		out.Rows = append(out.Rows, p)
	}
	return out, nil
}

// Get 은 지급 요청 한 건이다.
func (s *PaymentService) Get(ctx context.Context, no string) (domain.Payment, error) {
	if _, err := contract.Authorize(ctx, contract.PermPaymentRead); err != nil {
		return domain.Payment{}, err
	}
	r, err := s.Repo.ByNo(ctx, s.DB, no)
	if err != nil {
		return domain.Payment{}, err
	}
	return s.toPayment(ctx, s.DB, r)
}

// ---- 작성·수정 ----

type paymentDraft struct {
	in          domain.PaymentInput
	total       int64
	payeeEnc    string
	payeeMasked string
}

// normalize 는 입력을 검사한다(읽기만 한다). 항목은 같은 거래처의 취소되지 않은 채무 항목이고 지급액은 지급 가능액 이하다.
func (s *PaymentService) normalize(ctx context.Context, q contract.Tx, in domain.PaymentInput, vendorID int64, existing int64) (paymentDraft, error) {
	d := paymentDraft{in: in}
	if vendorID == 0 {
		vendorID = in.VendorID
	}
	d.in.VendorID = vendorID
	var acct string
	if err := q.QueryRowContext(ctx, `SELECT account_no FROM vendors WHERE id = ?`, vendorID).Scan(&acct); err != nil {
		return d, validation("거래처를 찾을 수 없습니다.")
	}
	raw := acct
	acct = strings.TrimSpace(repository.OpenAccount(acct)) // 저장된 암호문을 푼다
	if acct == "" && acctsecret.IsSealed(raw) {
		return d, repository.ErrAccountUnreadable
	}
	if acct == "" {
		return d, validation("거래처의 계좌번호가 등록되지 않았습니다. 거래처 정보에서 계좌를 등록하세요.")
	}
	d.payeeEnc, d.payeeMasked = acct, maskAccount(acct)
	bank, err := s.Repo.Bank(ctx, q, in.BankAccountID)
	if err != nil {
		return d, validation("출금 회사 계좌를 고르세요.")
	}
	if !bank.Active {
		return d, validation("사용 중지된 계좌입니다(%s).", bank.Alias)
	}
	if c := utf8.RuneCountInString(strings.TrimSpace(in.Memo)); c > 200 {
		return d, validation("메모는 200자 이내로 입력하세요.")
	}
	d.in.Memo = strings.TrimSpace(in.Memo)
	if len(in.Items) == 0 || len(in.Items) > maxPaymentItems {
		return d, validation("지급할 채무 항목을 1개 이상 %d개 이하로 고르세요.", maxPaymentItems)
	}
	seen := map[int64]bool{}
	earliest := ""
	for i, it := range in.Items {
		if seen[it.PayableID] {
			return d, validation("같은 채무 항목을 두 번 넣을 수 없습니다.")
		}
		seen[it.PayableID] = true
		info, err := s.Repo.PayableInfo(ctx, q, it.PayableID)
		if err != nil {
			return d, validation("%d번째 줄: 채무 항목을 찾을 수 없습니다.", i+1)
		}
		if info.VendorID != vendorID {
			return d, validation("%d번째 줄: 다른 거래처의 채무 항목입니다. 거래처가 다르면 요청서를 나누세요.", i+1)
		}
		if info.Canceled {
			return d, validation("%d번째 줄: 취소된 채무 항목입니다.", i+1)
		}
		if it.Amount < 1 {
			return d, validation("%d번째 줄: 지급액은 1원 이상이어야 합니다.", i+1)
		}
		avail := info.Available
		if existing != 0 {
			// 이 요청이 임시 상태에서는 예약하지 않으므로 그대로 지급 가능액과 비교한다.
			avail = info.Available
		}
		if it.Amount > avail {
			return d, validation("%d번째 줄: 지급 가능액을 넘습니다(남은 지급 가능액 %s).", i+1, moneyLabel(avail))
		}
		d.total += it.Amount
		if earliest == "" || info.DueDate < earliest {
			earliest = info.DueDate
		}
	}
	if d.in.DueDate == "" {
		d.in.DueDate = earliest
		if today := s.kstDay(); d.in.DueDate < today {
			d.in.DueDate = today
		}
	}
	if !validDate(d.in.DueDate) {
		return d, validation("지급 예정일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if d.in.DueDate < s.kstDay() && existing == 0 {
		return d, validation("지급 예정일은 오늘 이후여야 합니다.")
	}
	return d, nil
}

func (s *PaymentService) warnings(ctx context.Context, q contract.Tx, r repository.PaymentRow) []string {
	sim, err := s.Repo.Similar(ctx, q, r.VendorID, r.Total, r.DueDate, r.ID)
	if err != nil || len(sim) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("비슷한 지급 건(%s)이 있습니다. 중복이 아닌지 확인하세요.", strings.Join(sim, ", "))}
}

// Create 는 임시 지급 요청을 만든다(예약하지 않는다).
func (s *PaymentService) Create(ctx context.Context, in domain.PaymentInput) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.create", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	var out domain.Payment
	err = s.write(ctx, p, "payment.create", audit.TargetPayment, 0, func(tx contract.Tx) error {
		d, err := s.normalize(ctx, tx, in, 0, 0)
		if err != nil {
			return err
		}
		now := s.now()
		no, err := s.Repo.NextPaymentNo(ctx, tx, now.In(contract.KST).Year())
		if err != nil {
			return err
		}
		id, err := s.Repo.Insert(ctx, tx, repository.PaymentInsert{No: no, VendorID: d.in.VendorID, Total: d.total, DueDate: d.in.DueDate, BankAccountID: d.in.BankAccountID,
			PayeeMasked: d.payeeMasked, PayeeEnc: d.payeeEnc, Memo: d.in.Memo, CreatedBy: p.UserID, Now: now})
		if err != nil {
			return err
		}
		if err := s.Repo.ReplaceItems(ctx, tx, id, d.in.Items); err != nil {
			return err
		}
		r, err := s.Repo.ByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if out, err = s.toPayment(ctx, tx, r); err != nil {
			return err
		}
		out.Warnings = s.warnings(ctx, tx, r)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.create", TargetType: audit.TargetPayment, TargetID: no, Result: audit.ResultSuccess, Detail: "지급 요청 작성",
			Diff: []audit.DiffItem{{Key: "vendor", New: r.VendorName}, {Key: "total", New: formatWon(d.total)}, {Key: "item_count", New: fmt.Sprint(len(d.in.Items))}}})
	})
	return out, err
}

// Update 는 임시 지급 요청을 고친다(거래처는 바꿀 수 없다).
func (s *PaymentService) Update(ctx context.Context, no string, in domain.PaymentInput) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.update", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	var out domain.Payment
	err = s.write(ctx, p, "payment.update", audit.TargetPayment, 0, func(tx contract.Tx) error {
		cur, err := s.Repo.ByNo(ctx, tx, no)
		if err != nil {
			return err
		}
		if cur.Status != domain.PaymentDraft {
			return fmt.Errorf("%w: 임시 상태의 지급 요청만 고칠 수 있습니다", contract.ErrInvalidState)
		}
		d, err := s.normalize(ctx, tx, in, cur.VendorID, cur.ID)
		if err != nil {
			return err
		}
		if err := s.Repo.UpdateDraft(ctx, tx, cur.ID, repository.PaymentInsert{Total: d.total, DueDate: d.in.DueDate, BankAccountID: d.in.BankAccountID, PayeeMasked: d.payeeMasked, PayeeEnc: d.payeeEnc, Memo: d.in.Memo}); err != nil {
			return err
		}
		if err := s.Repo.ReplaceItems(ctx, tx, cur.ID, d.in.Items); err != nil {
			return err
		}
		r, err := s.Repo.ByID(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if out, err = s.toPayment(ctx, tx, r); err != nil {
			return err
		}
		out.Warnings = s.warnings(ctx, tx, r)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.update", TargetType: audit.TargetPayment, TargetID: no, Result: audit.ResultSuccess, Detail: "지급 요청 수정",
			Diff: []audit.DiffItem{{Key: "total", Old: formatWon(cur.Total), New: formatWon(d.total)}, {Key: "item_count", New: fmt.Sprint(len(d.in.Items))}}})
	})
	return out, err
}

// ---- 승인 요청·취소 ----

func paymentSummary(vendor string, items []domain.PaymentItem) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s 전표 %s(만기 %s) %s\n", vendor, it.JournalNo, it.DueDate, moneyLabel(it.Amount))
	}
	return strings.TrimSpace(b.String())
}

// RequestApproval 은 예약을 올리고 지급 요청서(PAY) 문서를 상신한다. 상신이 실패하면 예약을 되돌린다.
func (s *PaymentService) RequestApproval(ctx context.Context, no string) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.request", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.Payment{}, err
	}
	cur, err := s.Repo.ByNo(ctx, s.DB, no)
	if err != nil {
		return domain.Payment{}, err
	}
	if _, err := paymentTransition(cur.Status, pevRequest); err != nil {
		return domain.Payment{}, err
	}
	items, _, err := s.Repo.PaymentItems(ctx, s.DB, cur.ID)
	if err != nil {
		return domain.Payment{}, err
	}
	// 1) 한 트랜잭션: 열린 달 확인, 항목 재확인과 예약, 상태 전이.
	err = s.write(ctx, p, "payment.request", audit.TargetPayment, cur.ID, func(tx contract.Tx) error {
		if err := s.assertMonthOpen(ctx, tx, cur.DueDate); err != nil {
			return err
		}
		fresh, err := s.Repo.ByID(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		to, err := paymentTransition(fresh.Status, pevRequest)
		if err != nil {
			return err
		}
		if strings.TrimSpace(fresh.VendorAccount) == "" {
			return validation("거래처의 계좌번호가 등록되지 않았습니다. 거래처 정보에서 계좌를 등록하세요.")
		}
		if err := reservePayable(ctx, tx, s.Repo, toItemInputs(items)); err != nil {
			return err
		}
		if err := s.Repo.SetStatus(ctx, tx, cur.ID, to, p.UserID, s.now(), ""); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.request", TargetType: audit.TargetPayment, TargetID: no, Result: audit.ResultSuccess, Detail: "지급 요청 승인 요청(예약)",
			Diff: []audit.DiffItem{{Key: "reserved", New: formatWon(cur.Total)}}})
	})
	if err != nil {
		return domain.Payment{}, err
	}
	// 2) 결재 문서 상신. 실패하면 예약과 상태를 되돌린다.
	dctx := withSystemForm(ctx)
	fields := map[string]any{"payment_no": cur.No, "vendor": cur.VendorName, "total": cur.Total, "due_date": cur.DueDate, "bank": cur.BankAlias + " " + maskAccount(cur.PayeeEnc), "summary": paymentSummary(cur.VendorName, items)}
	title := fmt.Sprintf("지급 요청 %s", cur.No)
	rollback := func(cause error) (domain.Payment, error) {
		_ = s.write(ctx, p, "payment.request", audit.TargetPayment, cur.ID, func(tx contract.Tx) error {
			if err := releasePayable(ctx, tx, s.Repo, items); err != nil {
				return err
			}
			return s.Repo.SetStatus(ctx, tx, cur.ID, domain.PaymentDraft, p.UserID, s.now(), "")
		})
		return domain.Payment{}, cause
	}
	var doc domain.PurchaseRequest
	if cur.DocID != 0 {
		if prev, perr := s.Docs.Get(dctx, cur.DocID); perr == nil && prev.Status == domain.PRDraft {
			doc, err = s.Docs.UpdateDraft(dctx, cur.DocID, domain.DocDraftInput{FormCode: "PAY", Title: title, Fields: fields})
		} else {
			doc, err = s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "PAY", Title: title, Fields: fields})
		}
	} else {
		doc, err = s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "PAY", Title: title, Fields: fields})
	}
	if err != nil {
		return rollback(err)
	}
	if err := s.write(ctx, p, "payment.request", audit.TargetPayment, cur.ID, func(tx contract.Tx) error { return s.Repo.SetDoc(ctx, tx, cur.ID, doc.ID) }); err != nil {
		return rollback(err)
	}
	if _, err := s.Docs.Submit(dctx, doc.ID); err != nil {
		return rollback(err)
	}
	return s.Get(ctx, no)
}

// Cancel 은 임시·승인 요청·승인 상태의 지급 요청을 취소한다(사유 필수). 승인 요청이면 문서를 회수하고 예약을 푼다.
func (s *PaymentService) Cancel(ctx context.Context, no, reason string) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.cancel", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	reason = strings.TrimSpace(reason)
	if c := utf8.RuneCountInString(reason); c < 1 || c > 200 {
		return domain.Payment{}, validation("취소 사유를 1~200자로 입력하세요.")
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.Payment{}, err
	}
	cur, err := s.Repo.ByNo(ctx, s.DB, no)
	if err != nil {
		return domain.Payment{}, err
	}
	if _, err := paymentTransition(cur.Status, pevCancel); err != nil {
		return domain.Payment{}, err
	}
	if cur.Status == domain.PaymentRequested && cur.DocID != 0 {
		if _, err := s.Docs.CancelSubmit(withSystemForm(ctx), cur.DocID, "지급 요청 취소: "+reason); err != nil {
			return domain.Payment{}, err
		}
		// 회수 훅이 예약을 풀고 지급 건을 임시로 되돌렸다.
	}
	err = s.write(ctx, p, "payment.cancel", audit.TargetPayment, cur.ID, func(tx contract.Tx) error {
		fresh, err := s.Repo.ByID(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		to, err := paymentTransition(fresh.Status, pevCancel)
		if err != nil {
			return err
		}
		released := int64(0)
		if fresh.Status == domain.PaymentApproved {
			items, _, err := s.Repo.PaymentItems(ctx, tx, fresh.ID)
			if err != nil {
				return err
			}
			if err := releasePayable(ctx, tx, s.Repo, items); err != nil {
				return err
			}
			released = fresh.Total
		}
		if err := s.Repo.SetStatus(ctx, tx, fresh.ID, to, p.UserID, s.now(), reason); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.cancel", TargetType: audit.TargetPayment, TargetID: no, Result: audit.ResultSuccess, Detail: "지급 요청 취소",
			Diff: []audit.DiffItem{{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}, {Key: "reserved", Old: formatWon(released), New: "0"}}})
	})
	if err != nil {
		return domain.Payment{}, err
	}
	return s.Get(ctx, no)
}

// ---- 지급 처리 ----

// Settle 은 승인된 지급 건을 지급 처리한다. 한 트랜잭션에서 지급 행·누계·예약 해제·지급 전표(승인 상태)·자금 행·감사를 처리한다.
func (s *PaymentService) Settle(ctx context.Context, no string, in domain.SettleInput) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.settle", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	in.TransferRef = strings.TrimSpace(in.TransferRef)
	if utf8.RuneCountInString(in.TransferRef) > 40 {
		return domain.Payment{}, validation("이체 참조번호는 40자 이내로 입력하세요.")
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Memo)) > 200 {
		return domain.Payment{}, validation("메모는 200자 이내로 입력하세요.")
	}
	err = s.write(ctx, p, "payment.settle", audit.TargetPayment, 0, func(tx contract.Tx) error {
		return settlePayment(ctx, s, tx, p, no, in)
	})
	if err != nil {
		// 중복 지급(이미 지급 처리·참조번호 중복)은 거부 사실을 감사에 남긴다.
		msg := err.Error()
		if strings.Contains(msg, "이미 지급 처리된") || strings.Contains(msg, "이체 참조번호는 이미") {
			s.recordDenied(ctx, p, "payment.denied_duplicate", audit.TargetPayment, 0, msg)
		}
		if strings.Contains(msg, "마감된 월입니다") {
			s.recordDenied(ctx, p, "payment.denied_closed", audit.TargetPayment, 0, msg)
		}
		return domain.Payment{}, err
	}
	out, err := s.Get(ctx, no)
	if err == nil && out.PaidDate != "" {
		// 지급일이 휴일(주말·공휴일)이면 경고만 한다(차단하지 않는다: 기록 소급·예약 이체가 있을 수 있다, B34).
		if ok, herr := s.isBusinessDay(ctx, s.DB, out.PaidDate); herr == nil && !ok {
			out.Warnings = append(out.Warnings, "지급일("+out.PaidDate+")이 휴일(주말·공휴일)입니다. 은행 이체가 되지 않는 날이니 실제 이체일과 맞는지 확인하세요.")
		}
	}
	return out, err
}

// settlePayment 는 지급 처리의 유일한 전기 함수다: 채무 항목의 지급 누계를 올리고 예약을 푸는 서비스 쪽 유일한 자리이고, 승인 상태로 지급 전표를 바로 만드는 유일한 자리다.
func settlePayment(ctx context.Context, s *PaymentService, tx contract.Tx, p contract.Principal, no string, in domain.SettleInput) error {
	pay, err := s.Repo.ByNo(ctx, tx, no)
	if err != nil {
		return err
	}
	to, err := paymentTransition(pay.Status, pevSettle)
	if err != nil {
		if pay.Status == domain.PaymentPaid {
			return fmt.Errorf("%w: 이미 지급 처리된 건입니다.", contract.ErrInvalidState)
		}
		return err
	}
	// 권한 분리: 지급 요청 작성자(locked)와 지급 요청 승인자는 지급 처리할 수 없다. 앞 단계 행위자는 같은 트랜잭션에서 읽은 지급 건의 값이다.
	sref := sod.Ref{Type: "payment", ID: pay.ID, No: pay.No}
	if _, err := s.sodCheck(ctx, tx, p, sod.RulePayCreateSettle, pay.CreatedByID, sref); err != nil {
		return err
	}
	if _, err := s.sodCheck(ctx, tx, p, sod.RulePayApproveSettle, pay.ApprovedByID, sref); err != nil {
		return err
	}
	today := s.kstDay()
	paidDate := in.PaidDate
	if paidDate == "" {
		paidDate = today
	}
	if !validDate(paidDate) {
		return validation("지급일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if paidDate > today {
		return validation("지급일은 오늘 이전이어야 합니다.")
	}
	if pay.ApprovedAt != nil && paidDate < pay.ApprovedAt.In(contract.KST).Format("2006-01-02") {
		return validation("지급일은 승인일(%s) 이후여야 합니다.", pay.ApprovedAt.In(contract.KST).Format("2006-01-02"))
	}
	if err := s.assertMonthOpen(ctx, tx, paidDate); err != nil {
		return err
	}
	bankID := in.BankAccountID
	if bankID == 0 {
		bankID = pay.BankAccountID
	}
	changed := bankID != pay.BankAccountID
	if changed && strings.TrimSpace(in.ChangeReason) == "" {
		return validation("출금 계좌를 바꾸면 이유를 한 줄 적어야 합니다.")
	}
	bank, err := s.Repo.Bank(ctx, tx, bankID)
	if err != nil {
		return validation("출금 회사 계좌를 고르세요.")
	}
	if !bank.Active {
		return validation("사용 중지된 계좌입니다(%s).", bank.Alias)
	}
	if strings.TrimSpace(pay.VendorAccount) != pay.PayeeEnc && !in.ConfirmAccount {
		return fmt.Errorf("%w: 거래처 계좌가 변경되었습니다. 승인된 계좌와 다릅니다. 확인한 뒤 다시 처리하세요", contract.ErrInvalidState)
	}
	if in.TransferRef != "" {
		if owner, err := s.Repo.RefOwner(ctx, tx, bankID, in.TransferRef); err != nil {
			return err
		} else if owner != "" {
			return fmt.Errorf("%w: 이 이체 참조번호는 이미 다른 지급 건(%s)에 기록되어 있습니다.", contract.ErrInvalidState, owner)
		}
	}
	items, _, err := s.Repo.PaymentItems(ctx, tx, pay.ID)
	if err != nil {
		return err
	}
	// 채무 항목: 예약을 지급 누계로 옮긴다.
	for _, it := range items {
		if err := s.Repo.AddPaid(ctx, tx, it.PayableID, it.Amount); err != nil {
			return fmt.Errorf("%w: 지급 대상 채무가 취소되었습니다. 이 지급 건을 취소하세요", contract.ErrInvalidState)
		}
	}
	// 지급 전표: 차 외상매입금(거래처) / 대 출금 계좌의 연결 계정. 지급 요청서 승인이 전표 승인 근거라 승인 상태로 바로 만든다(D6).
	pa, found, err := s.Ledger.RuleAccount(ctx, tx, "purchase.payable")
	if err != nil {
		return err
	}
	if !found || !pa.Active {
		return fmt.Errorf("%w: 규칙 계정 사용 불가: 외상매입금", contract.ErrInvalidState)
	}
	bankAcc, found, err := s.Ledger.AccountByCode(ctx, tx, bank.LedgerCode)
	if err != nil {
		return err
	}
	if !found || !bankAcc.Active {
		return fmt.Errorf("%w: 출금 계좌의 연결 계정을 쓸 수 없습니다(%s)", contract.ErrInvalidState, bank.LedgerCode)
	}
	aj := autoJournal{Base: s.Base, Repo: s.Ledger}
	lines := []repository.LineInsert{{AccountID: pa.ID, Side: "D", Amount: pay.Total, VendorID: pay.VendorID}, {AccountID: bankAcc.ID, Side: "C", Amount: pay.Total}}
	jno, err := aj.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoPayment, PostingDate: paidDate, TradeDate: paidDate,
		Memo: truncateRunes(fmt.Sprintf("%s 지급 %s", pay.No, pay.VendorName), 200), SourceType: "payment", SourceID: pay.ID, Total: pay.Total}, lines, domain.JournalEvidence{})
	if err != nil {
		return err
	}
	j, err := s.Ledger.Journal(ctx, tx, jno)
	if err != nil {
		return err
	}
	payID, err := s.Repo.InsertPayment(ctx, tx, pay.ID, paidDate, bankID, in.TransferRef, p.UserID, s.now(), j.ID, changed, strings.TrimSpace(in.Memo))
	if err != nil {
		if strings.Contains(err.Error(), "payments.payment_request_id") {
			return fmt.Errorf("%w: 이미 지급 처리된 건입니다.", contract.ErrInvalidState)
		}
		if strings.Contains(err.Error(), "transfer_ref") {
			return fmt.Errorf("%w: 이 이체 참조번호는 이미 다른 지급 건에 기록되어 있습니다.", contract.ErrInvalidState)
		}
		return mapErrText(err)
	}
	approver := pay.ApprovedByID
	if approver == 0 {
		approver = p.UserID
	}
	if err := s.Repo.ApproveInsertedJournal(ctx, tx, j.ID, pay.DocID, approver, s.now()); err != nil {
		return err
	}
	if err := s.Repo.InsertMovement(ctx, tx, repository.MovementInsert{BankAccountID: bankID, Amount: -pay.Total, Date: paidDate, Kind: domain.FundPayment, Memo: truncateRunes(pay.No+" "+pay.VendorName, 200),
		PaymentID: payID, JournalID: j.ID, VendorID: pay.VendorID, CreatedBy: p.UserID, Now: s.now()}); err != nil {
		return err
	}
	if err := s.Repo.SetStatus(ctx, tx, pay.ID, to, p.UserID, s.now(), ""); err != nil {
		return err
	}
	ref := "없음"
	if in.TransferRef != "" {
		ref = "있음"
	}
	if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.settle", TargetType: audit.TargetPayment, TargetID: pay.No, Result: audit.ResultSuccess, Detail: "지급 처리",
		Diff: []audit.DiffItem{{Key: "paid_date", New: paidDate}, {Key: "journal_no", New: jno}, {Key: "total", New: formatWon(pay.Total)}, {Key: "ref_present", New: ref}}}); err != nil {
		return err
	}
	return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_payment", TargetType: audit.TargetJournal, TargetID: jno, Result: audit.ResultSuccess, Detail: "auto=true 지급 전표(승인 상태로 생성)",
		Diff: []audit.DiffItem{{Key: "source", New: "payment:" + pay.No}, {Key: "amount", New: formatWon(pay.Total)}, {Key: "doc_no", New: pay.DocNo}}})
}

func mapErrText(err error) error { return err }

// ---- 지급 취소(지급 전표 역분개) ----

// Reverse 는 지급 완료 건의 지급 전표 역분개를 만들고 승인 요청한다. 역분개가 승인되면 누계가 복원되고 지급 건이 지급 취소가 된다.
func (s *PaymentService) Reverse(ctx context.Context, no, reason string) (domain.Payment, error) {
	p, err := s.authorize(ctx, contract.PermPaymentWrite, "payment.reverse", audit.TargetPayment)
	if err != nil {
		return domain.Payment{}, err
	}
	reason = strings.TrimSpace(reason)
	if c := utf8.RuneCountInString(reason); c < 1 || c > 200 {
		return domain.Payment{}, validation("지급 취소 사유를 1~200자로 입력하세요.")
	}
	cur, err := s.Repo.ByNo(ctx, s.DB, no)
	if err != nil {
		return domain.Payment{}, err
	}
	if _, err := paymentTransition(cur.Status, pevReverse); err != nil {
		return domain.Payment{}, err
	}
	j, err := s.Ledger.JournalByID(ctx, s.DB, cur.JournalID)
	if err != nil {
		return domain.Payment{}, err
	}
	rev, err := s.Journals.Reverse(ctx, j.No, "")
	if err != nil {
		return domain.Payment{}, err
	}
	if err := s.write(ctx, p, "payment.reverse", audit.TargetPayment, cur.ID, func(tx contract.Tx) error {
		if err := s.Repo.SetReverseReason(ctx, tx, cur.ID, reason); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.reverse", TargetType: audit.TargetPayment, TargetID: no, Result: audit.ResultSuccess, Detail: "지급 취소(역분개 전표 생성)",
			Diff: []audit.DiffItem{{Key: "journal_no", New: rev.No}, {Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}}})
	}); err != nil {
		return domain.Payment{}, err
	}
	out, err := s.Get(ctx, no)
	if err != nil {
		return out, err
	}
	if _, rerr := s.Journals.RequestApproval(ctx, rev.No); rerr != nil {
		out.Warnings = append(out.Warnings, "역분개 전표 "+rev.No+" 의 승인 요청을 하지 못했습니다: "+strings.TrimPrefix(rerr.Error(), contract.ErrValidation.Error()+": ")+" 전표 화면에서 승인 요청하세요.")
	}
	return out, nil
}

// restorePayment 는 지급 전표(orig)의 역분개가 승인될 때 같은 트랜잭션에서 지급 누계를 되돌리고 지급 건을 지급 취소로 바꾼다.
// 채무 항목의 paid_amount 를 내리는 유일한 서비스 쪽 자리다(명세 3.5.5).
func restorePayment(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, orig domain.Journal, reversal domain.Journal) error {
	repo := repository.SQLPayments{}
	var reqID int64
	if err := tx.QueryRowContext(ctx, `SELECT payment_request_id FROM payments WHERE journal_id = ?`, orig.ID).Scan(&reqID); err != nil {
		return nil // 지급 건과 연결되지 않은 전표(수동으로 만든 같은 종류 전표)는 후속 동작이 없다
	}
	pay, err := repo.ByID(ctx, tx, reqID)
	if err != nil {
		return err
	}
	to, err := paymentTransition(pay.Status, pevReverse)
	if err != nil {
		return err
	}
	items, _, err := repo.PaymentItems(ctx, tx, pay.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := repo.RestorePaid(ctx, tx, it.PayableID, it.Amount); err != nil {
			return err
		}
	}
	var bankID int64
	if err := tx.QueryRowContext(ctx, `SELECT bank_account_id FROM payments WHERE payment_request_id = ?`, pay.ID).Scan(&bankID); err != nil {
		return err
	}
	payID, _ := repo.PaymentIDByRequest(ctx, tx, pay.ID)
	if err := repo.InsertMovement(ctx, tx, repository.MovementInsert{BankAccountID: bankID, Amount: pay.Total, Date: reversal.PostingDate, Kind: domain.FundPaymentCancel,
		Memo: truncateRunes(pay.No+" 지급 취소", 200), PaymentID: payID, JournalID: reversal.ID, VendorID: pay.VendorID, CreatedBy: p.UserID, Now: b.now()}); err != nil {
		return err
	}
	if err := repo.SetStatus(ctx, tx, pay.ID, to, p.UserID, b.now(), pay.ReverseReason); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.reverse", TargetType: audit.TargetPayment, TargetID: pay.No, Result: audit.ResultSuccess, Detail: "지급 취소 확정(역분개 승인)",
		Diff: []audit.DiffItem{{Key: "journal_no", New: reversal.No}, {Key: "total", New: formatWon(pay.Total)}}})
}

// ---- 결재 후속 동작(docHookTable 의 PAY) ----

// approvePaymentHook 은 지급 요청서가 최종 승인되면 같은 트랜잭션에서 지급 건을 승인 상태로 바꾼다. 예약은 그대로 유지한다.
// 대상 채무 항목이 취소돼 예약을 지킬 수 없으면 승인 전체가 취소된다.
func approvePaymentHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	repo := repository.SQLPayments{}
	pay, err := repo.ByDoc(ctx, tx, rec.ID)
	if err != nil {
		return fmt.Errorf("%w: 지급 요청과 연결되지 않은 지급 요청서입니다", contract.ErrInvalidState)
	}
	to, err := paymentTransition(pay.Status, pevApprove)
	if err != nil {
		return err
	}
	items, _, err := repo.PaymentItems(ctx, tx, pay.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		info, err := repo.PayableInfo(ctx, tx, it.PayableID)
		if err != nil || info.Canceled {
			return fmt.Errorf("%w: 지급 대상 채무 항목이 취소되어 승인할 수 없습니다", contract.ErrInvalidState)
		}
	}
	// 권한 분리: 지급 요청 작성자가 최종 승인까지 하면 규칙 모드대로(경고는 위반 기록, 차단은 승인 전체 롤백).
	if _, err := b.sodCheck(ctx, tx, p, sod.RulePayCreateApprove, pay.CreatedByID, sod.Ref{Type: "payment", ID: pay.ID, No: pay.No}); err != nil {
		return err
	}
	if err := repo.SetStatus(ctx, tx, pay.ID, to, p.UserID, b.now(), ""); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.approve", TargetType: audit.TargetPayment, TargetID: pay.No, Result: audit.ResultSuccess, Detail: "지급 요청 승인",
		Diff: []audit.DiffItem{{Key: "doc_no", New: rec.No}}})
}

func returnPaymentHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, ev paymentEvent, detail string) error {
	repo := repository.SQLPayments{}
	pay, err := repo.ByDoc(ctx, tx, rec.ID)
	if errors.Is(err, contract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if pay.Status != domain.PaymentRequested {
		return nil
	}
	to, err := paymentTransition(pay.Status, ev)
	if err != nil {
		return err
	}
	items, _, err := repo.PaymentItems(ctx, tx, pay.ID)
	if err != nil {
		return err
	}
	if err := releasePayable(ctx, tx, repo, items); err != nil {
		return err
	}
	if err := repo.SetStatus(ctx, tx, pay.ID, to, p.UserID, b.now(), ""); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "payment.return", TargetType: audit.TargetPayment, TargetID: pay.No, Result: audit.ResultSuccess, Detail: detail,
		Diff: []audit.DiffItem{{Key: "reserved", Old: formatWon(pay.Total), New: "0"}}})
}

func rejectPaymentHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	return returnPaymentHook(ctx, b, tx, p, rec, pevReject, "지급 요청 반려(예약 해제)")
}

func resetPaymentHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	return returnPaymentHook(ctx, b, tx, p, rec, pevReset, "지급 요청 회수(예약 해제)")
}

// ---- 지급 일정표 ----

// Calendar 는 기간(최대 92일)의 지급 예정 일정이다: 만기 도래 채무, 승인된·승인 요청 중·지급 완료 지급 건, 만기 경과 합계, 자금 부족 경고.
func (s *PaymentService) Calendar(ctx context.Context, from, to string) (domain.PaymentCalendar, error) {
	var out domain.PaymentCalendar
	if _, err := contract.Authorize(ctx, contract.PermPaymentRead); err != nil {
		return out, err
	}
	today := s.kstDay()
	if from == "" {
		from = today
	}
	if to == "" {
		t, _ := time.Parse("2006-01-02", from)
		to = t.AddDate(0, 0, 6).Format("2006-01-02")
	}
	if !validDate(from) || !validDate(to) {
		return out, validation("날짜는 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if to < from {
		return out, validation("종료일은 시작일 이후여야 합니다.")
	}
	f, _ := time.Parse("2006-01-02", from)
	t, _ := time.Parse("2006-01-02", to)
	if t.Sub(f) > 92*24*time.Hour {
		return out, validation("일정표는 한 번에 93일까지 볼 수 있습니다.")
	}
	out = domain.PaymentCalendar{From: from, To: to}
	due, err := s.Repo.DuePayables(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	for _, d := range due {
		out.Entries = append(out.Entries, domain.CalendarEntry{Date: d.Date, Kind: "due", VendorName: d.VendorName, Amount: d.Amount})
	}
	pays, err := s.Repo.CalendarPayments(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	for _, r := range pays {
		kind := "payment"
		if r.Status == domain.PaymentRequested {
			kind = "requested"
		}
		out.Entries = append(out.Entries, domain.CalendarEntry{Date: r.DueDate, Kind: kind, VendorName: r.VendorName, Amount: r.Total, PaymentNo: r.No, Status: r.Status})
		if r.Status == domain.PaymentApproved {
			out.ScheduledTotal += r.Total
		}
	}
	delayed, err := s.Repo.DelayedApproved(ctx, s.DB, today)
	if err != nil {
		return out, err
	}
	for _, r := range delayed {
		if r.DueDate >= from && r.DueDate <= to {
			continue // 이미 위 목록에 있다
		}
		out.Entries = append(out.Entries, domain.CalendarEntry{Date: r.DueDate, Kind: "payment", VendorName: r.VendorName, Amount: r.Total, PaymentNo: r.No, Status: r.Status, Delayed: true})
	}
	for i := range out.Entries {
		if e := &out.Entries[i]; e.Status == domain.PaymentApproved && e.Date < today {
			e.Delayed = true
		}
	}
	for i := range out.Entries { // 예정일이 휴일이면 다음 영업일을 안내한다(만기·예정일 자체는 그대로)
		e := &out.Entries[i]
		if !validDate(e.Date) {
			continue
		}
		if ok, err := s.isBusinessDay(ctx, s.DB, e.Date); err != nil {
			return out, err
		} else if !ok {
			if e.NextBusinessDay, err = s.nextBusinessDay(ctx, s.DB, e.Date); err != nil {
				return out, err
			}
		}
	}
	if out.OverdueTotal, err = s.Repo.OverduePayables(ctx, s.DB, today); err != nil {
		return out, err
	}
	banks, err := s.Repo.Banks(ctx, s.DB, false)
	if err != nil {
		return out, err
	}
	for _, b := range banks {
		bal, err := s.Repo.BankBalance(ctx, s.DB, b.ID, today)
		if err != nil {
			return out, err
		}
		out.BalanceTotal += bal
	}
	out.ShortageWarn = out.ScheduledTotal > out.BalanceTotal
	return out, nil
}

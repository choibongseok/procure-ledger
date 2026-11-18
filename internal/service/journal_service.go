package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 전표(B31, 3.2·4절).
//   - 상태 전이는 journalTransitions 표 한 곳에서만 정의한다(표에 없는 전이는 전부 거부, DB 트리거가 한 번 더 막는다).
//   - 승인은 전자결재(시스템 양식 JV): 승인 요청이 문서를 만들고, 최종 승인 후속 동작(docHookTable 의 JV)이 같은 트랜잭션에서 전표를 승인한다.
//   - 승인된 전표는 고치는 경로가 없다. 정정은 역분개(새 전표, 같은 승인 절차)뿐이다.
//   - 전기일이 마감된 달이면 입력·수정·요청·승인이 모두 거부된다(B27 의 한 함수 assertMonthOpen).

type journalEvent string

const (
	jevRequest journalEvent = "request"
	jevApprove journalEvent = "approve"
	jevReject  journalEvent = "reject" // 결재 반려
	jevReset   journalEvent = "reset"  // 상신 취소·회수
	jevCancel  journalEvent = "cancel"
)

type journalKey struct {
	from string
	ev   journalEvent
}

// journalTransitions 는 전표 상태 전이 표다(명세 4절). 표에 없는 전이는 전부 거부한다.
var journalTransitions = map[journalKey]string{
	{domain.JournalDraft, jevRequest}:     domain.JournalRequested,
	{domain.JournalRequested, jevApprove}: domain.JournalApproved,
	{domain.JournalRequested, jevReject}:  domain.JournalDraft,
	{domain.JournalRequested, jevReset}:   domain.JournalDraft,
	{domain.JournalDraft, jevCancel}:      domain.JournalCanceled,
	{domain.JournalRequested, jevCancel}:  domain.JournalCanceled,
}

func journalTransition(from string, ev journalEvent) (string, error) {
	to, ok := journalTransitions[journalKey{from, ev}]
	if !ok {
		return "", fmt.Errorf("%w: %s 상태의 전표는 이 작업을 할 수 없습니다", contract.ErrInvalidState, domain.JournalStatusLabels[from])
	}
	return to, nil
}

// JournalService 는 domain.JournalService 의 구현이다.
type JournalService struct {
	Base
	Repo repository.SQLLedger
	Docs *ApprovalDocService
}

var _ domain.JournalService = (*JournalService)(nil)

func NewJournalService(b Base, docs *ApprovalDocService) *JournalService {
	return &JournalService{Base: b, Docs: docs}
}

const (
	maxJournalLines  = 200
	maxJournalAmount = 1_000_000_000_000
)

func validDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

func balanceError(d, c int64) error {
	return validation("차변 합계와 대변 합계가 같지 않습니다(차변 %s, 대변 %s).", moneyLabel(d), moneyLabel(c))
}

type journalDraft struct {
	kind, posting, trade, memo string
	site                       int64
	lines                      []repository.LineInsert
	debit, credit              int64
	evidence                   []domain.JournalEvidence
}

// normalize 는 전표 입력을 검사하고 저장 형태로 바꾼다. 임시 저장은 차대가 달라도 되므로 균형은 여기서 보지 않는다.
func (s *JournalService) normalize(ctx context.Context, q contract.Tx, in domain.JournalInput) (journalDraft, error) {
	var d journalDraft
	d.kind = in.Kind
	if d.kind == "" {
		d.kind = domain.JournalGeneral
	}
	if d.kind != domain.JournalGeneral && d.kind != domain.JournalOpening {
		return d, validation("일반 전표와 기초 전표만 직접 입력할 수 있습니다.")
	}
	if !validDate(in.PostingDate) {
		return d, validation("전기일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	d.posting = in.PostingDate
	d.trade = in.TradeDate
	if d.trade == "" {
		d.trade = d.posting
	}
	if !validDate(d.trade) {
		return d, validation("거래일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	d.memo = strings.TrimSpace(in.Memo)
	if ok, err := s.Repo.SiteUsable(ctx, q, in.SiteID); err != nil {
		return d, err
	} else if !ok {
		return d, validation("사업장을 찾을 수 없거나 사용할 수 없습니다.")
	}
	d.site = in.SiteID
	if c := utf8.RuneCountInString(d.memo); c < 1 || c > 200 {
		return d, validation("적요는 1~200자로 입력하세요.")
	}
	if len(in.Lines) < 2 || len(in.Lines) > maxJournalLines {
		return d, validation("줄은 2개 이상 %d개 이하로 입력하세요.", maxJournalLines)
	}
	for i, l := range in.Lines {
		a, found, err := s.Repo.AccountByCode(ctx, q, strings.TrimSpace(l.AccountCode))
		if err != nil {
			return d, err
		}
		if !found {
			return d, validation("%d번째 줄: 알 수 없는 계정과목입니다(%s).", i+1, l.AccountCode)
		}
		if !a.Active {
			return d, validation("중지된 계정과목입니다(%s %s).", a.Code, a.Name)
		}
		if l.Side != "D" && l.Side != "C" {
			return d, validation("%d번째 줄: 차변·대변을 골라야 합니다.", i+1)
		}
		if l.Amount <= 0 || l.Amount > maxJournalAmount {
			return d, validation("%d번째 줄: 금액은 1원 이상이어야 합니다.", i+1)
		}
		if utf8.RuneCountInString(l.Memo) > 200 {
			return d, validation("%d번째 줄: 줄 적요는 200자 이내로 입력하세요.", i+1)
		}
		if a.Code == "2110" && l.VendorID == 0 {
			return d, validation("외상매입금 줄은 거래처를 골라야 합니다.")
		}
		if l.VendorID != 0 {
			if ok, err := s.Repo.VendorExists(ctx, q, l.VendorID); err != nil {
				return d, err
			} else if !ok {
				return d, validation("%d번째 줄: 거래처를 찾을 수 없습니다.", i+1)
			}
		}
		d.lines = append(d.lines, repository.LineInsert{AccountID: a.ID, Side: l.Side, Amount: l.Amount, VendorID: l.VendorID, Memo: strings.TrimSpace(l.Memo)})
		if l.Side == "D" {
			d.debit += l.Amount
		} else {
			d.credit += l.Amount
		}
	}
	d.evidence = in.Evidence
	return d, nil
}

// List 는 전표 목록이다(최근 것이 먼저).
func (s *JournalService) List(ctx context.Context, f domain.JournalFilter) (domain.JournalPage, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return domain.JournalPage{}, err
	}
	rows, total, err := s.Repo.ListJournals(ctx, s.DB, f)
	return domain.JournalPage{Rows: rows, Total: total}, err
}

// Get 은 전표 한 건(줄·증빙 포함)이다.
func (s *JournalService) Get(ctx context.Context, no string) (domain.Journal, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return domain.Journal{}, err
	}
	return s.Repo.Journal(ctx, s.DB, no)
}

// Create 는 임시 전표를 만든다. 전기일이 마감된 달이면 거부하고 감사에 denied 로 남긴다.
func (s *JournalService) Create(ctx context.Context, in domain.JournalInput) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.create", audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, err
	}
	var out domain.Journal
	err = s.write(ctx, p, "journal.create", audit.TargetJournal, 0, func(tx contract.Tx) error {
		d, err := s.normalize(ctx, tx, in)
		if err != nil {
			return err
		}
		if err := s.assertMonthOpen(ctx, tx, d.posting); err != nil {
			// 마감된 월 거부는 write 가 롤백 뒤에 감사 denied 로 남긴다(트랜잭션 안에서 따로 기록하면 연결이 막힌다).
			return err
		}
		now := s.now()
		no, err := s.Repo.NextJournalNo(ctx, tx, now.In(contract.KST).Year())
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertJournal(ctx, tx, repository.JournalInsert{No: no, Kind: d.kind, PostingDate: d.posting, TradeDate: d.trade, Memo: d.memo, SiteID: d.site, Total: d.debit, CreatedBy: p.UserID, Now: now})
		if err != nil {
			return err
		}
		if err := s.Repo.ReplaceLines(ctx, tx, id, d.lines); err != nil {
			return err
		}
		for _, e := range d.evidence {
			if err := s.Repo.AddEvidence(ctx, tx, id, e, now); err != nil {
				return err
			}
		}
		if out, err = s.Repo.Journal(ctx, tx, no); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.create", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "전표 입력",
			Diff: []audit.DiffItem{{Key: "journal_kind", New: domain.JournalKindLabels[d.kind]}, {Key: "posting_date", New: d.posting}, {Key: "debit_total", New: formatWon(d.debit)}}})
	})
	return out, err
}

// Update 는 임시 전표의 내용을 고친다. 승인 요청 이후에는 고칠 수 없다.
func (s *JournalService) Update(ctx context.Context, no string, in domain.JournalInput) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.update", audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, err
	}
	var out domain.Journal
	err = s.write(ctx, p, "journal.update", audit.TargetJournal, 0, func(tx contract.Tx) error {
		j, err := s.Repo.Journal(ctx, tx, no)
		if err != nil {
			return err
		}
		if j.Status != domain.JournalDraft {
			return fmt.Errorf("%w: 임시 상태의 전표만 고칠 수 있습니다", contract.ErrInvalidState)
		}
		if j.Kind != domain.JournalGeneral && j.Kind != domain.JournalOpening {
			in.Kind = j.Kind // 자동·역분개 전표는 종류를 바꿀 수 없다(아래에서 일반만 허용되므로 줄·적요만 고친다)
		}
		d, err := s.normalizeFor(ctx, tx, in, j.Kind)
		if err != nil {
			return err
		}
		if err := s.assertMonthOpen(ctx, tx, d.posting); err != nil {
			// 마감된 월 거부는 write 가 롤백 뒤에 감사 denied 로 남긴다(트랜잭션 안에서 따로 기록하면 연결이 막힌다).
			return err
		}
		if err := s.Repo.UpdateDraft(ctx, tx, j.ID, d.posting, d.trade, d.memo, d.debit, d.site); err != nil {
			return err
		}
		if err := s.Repo.ReplaceLines(ctx, tx, j.ID, d.lines); err != nil {
			return err
		}
		for _, e := range d.evidence {
			if err := s.Repo.AddEvidence(ctx, tx, j.ID, e, s.now()); err != nil {
				return err
			}
		}
		if out, err = s.Repo.Journal(ctx, tx, no); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.update", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "임시 전표 수정",
			Diff: []audit.DiffItem{{Key: "debit_total", Old: formatWon(j.TotalAmount), New: formatWon(d.debit)}}})
	})
	return out, err
}

// normalizeFor 는 normalize 인데 시스템이 만든 전표(자동·역분개)의 종류를 그대로 쓴다.
func (s *JournalService) normalizeFor(ctx context.Context, q contract.Tx, in domain.JournalInput, kind string) (journalDraft, error) {
	if kind == domain.JournalGeneral || kind == domain.JournalOpening {
		in.Kind = kind
		return s.normalize(ctx, q, in)
	}
	in.Kind = ""
	d, err := s.normalize(ctx, q, in)
	d.kind = kind
	return d, err
}

// RequestApproval 은 임시 전표의 승인을 요청한다: 차대 일치·열린 달·사용 중 계정을 확인하고 JV 문서를 만들어 상신한다.
func (s *JournalService) RequestApproval(ctx context.Context, no string) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.request", audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.Journal{}, err
	}
	j, err := s.Repo.Journal(ctx, s.DB, no)
	if err != nil {
		return j, err
	}
	if _, err := journalTransition(j.Status, jevRequest); err != nil {
		return j, err
	}
	var debit, credit int64
	for _, l := range j.Lines {
		a, _, err := s.Repo.AccountByCode(ctx, s.DB, l.AccountCode)
		if err != nil {
			return j, err
		}
		if !a.Active {
			return j, validation("중지된 계정과목입니다(%s %s).", a.Code, a.Name)
		}
		if l.Side == "D" {
			debit += l.Amount
		} else {
			credit += l.Amount
		}
	}
	if debit != credit || len(j.Lines) < 2 {
		return j, balanceError(debit, credit)
	}
	if err := s.assertMonthOpen(ctx, s.DB, j.PostingDate); err != nil {
		s.recordDenied(ctx, p, "journal.denied_closed", audit.TargetJournal, j.ID, "마감된 월 "+j.PostingDate[:7])
		return j, err
	}
	if j.Kind == domain.JournalReversal {
		if err := s.checkReversalAllowed(ctx, s.DB, j); err != nil {
			return j, err
		}
	}
	// 문서 만들기·상신은 양식 문서 서비스가 한다(결재선: 재무팀장). 일반 문서 API 로는 JV 를 만들 수 없다(guardSystemForm).
	dctx := withSystemForm(ctx)
	fields := map[string]any{"journal_no": j.No, "posting_date": j.PostingDate, "memo": j.Memo, "total": debit, "summary": journalSummary(j)}
	title := fmt.Sprintf("전표 승인 요청 %s", j.No)
	var doc domain.PurchaseRequest
	if j.DocID != 0 {
		if prev, perr := s.Docs.Get(dctx, j.DocID); perr == nil && prev.Status == domain.PRDraft {
			doc, err = s.Docs.UpdateDraft(dctx, j.DocID, domain.DocDraftInput{FormCode: "JV", Title: title, Fields: fields})
		} else {
			doc, err = s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "JV", Title: title, Fields: fields})
		}
	} else {
		doc, err = s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "JV", Title: title, Fields: fields})
	}
	if err != nil {
		return j, err
	}
	if err := s.write(ctx, p, "journal.request", audit.TargetJournal, j.ID, func(tx contract.Tx) error { return s.Repo.SetDocID(ctx, tx, j.ID, doc.ID) }); err != nil {
		return j, err
	}
	submitted, err := s.Docs.Submit(dctx, doc.ID)
	if err != nil {
		return j, err
	}
	var out domain.Journal
	err = s.write(ctx, p, "journal.request", audit.TargetJournal, j.ID, func(tx contract.Tx) error {
		to, err := journalTransition(j.Status, jevRequest)
		if err != nil {
			return err
		}
		if err := s.Repo.SetStatus(ctx, tx, j.ID, to, p.UserID, s.now(), ""); err != nil {
			return err
		}
		if out, err = s.Repo.Journal(ctx, tx, no); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.request", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "전표 승인 요청",
			Diff: []audit.DiffItem{{Key: "doc_no", New: submitted.No}, {Key: "debit_total", New: formatWon(debit)}}})
	})
	return out, err
}

func journalSummary(j domain.Journal) string {
	var b strings.Builder
	for _, l := range j.Lines {
		side := "차변"
		if l.Side == "C" {
			side = "대변"
		}
		fmt.Fprintf(&b, "%s %s %s %s", side, l.AccountCode, l.AccountName, moneyLabel(l.Amount))
		if l.VendorName != "" {
			fmt.Fprintf(&b, " (%s)", l.VendorName)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// Cancel 은 임시·승인 요청 전표를 취소한다(사유 필수). 승인 요청 중이면 결재 문서를 회수한다. 승인된 전표는 취소가 아니라 역분개다.
func (s *JournalService) Cancel(ctx context.Context, no, reason string) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.cancel", audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, err
	}
	reason = strings.TrimSpace(reason)
	if c := utf8.RuneCountInString(reason); c < 1 || c > 200 {
		return domain.Journal{}, validation("취소 사유를 1~200자로 입력하세요.")
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.Journal{}, err
	}
	j, err := s.Repo.Journal(ctx, s.DB, no)
	if err != nil {
		return j, err
	}
	to, err := journalTransition(j.Status, jevCancel)
	if err != nil {
		if j.Status == domain.JournalApproved {
			return j, fmt.Errorf("%w: 승인된 전표는 취소할 수 없습니다. 역분개하세요", contract.ErrInvalidState)
		}
		return j, err
	}
	if j.Status == domain.JournalRequested && j.DocID != 0 {
		if _, err := s.Docs.CancelSubmit(withSystemForm(ctx), j.DocID, "전표 취소: "+reason); err != nil {
			return j, err
		}
		// 회수 훅이 전표를 임시로 되돌렸다. 아래에서 다시 읽어 취소한다.
		if j, err = s.Repo.Journal(ctx, s.DB, no); err != nil {
			return j, err
		}
		if to, err = journalTransition(j.Status, jevCancel); err != nil {
			return j, err
		}
	}
	var out domain.Journal
	err = s.write(ctx, p, "journal.cancel", audit.TargetJournal, j.ID, func(tx contract.Tx) error {
		if err := s.Repo.SetStatus(ctx, tx, j.ID, to, p.UserID, s.now(), reason); err != nil {
			return err
		}
		if out, err = s.Repo.Journal(ctx, tx, no); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.cancel", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "전표 취소",
			Diff: []audit.DiffItem{{Key: "reason", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}}})
	})
	return out, err
}

// checkReversalAllowed 는 역분개 전표가 가리키는 원 전표에 지급·예약이 없는지 본다(E8). 원 전표는 reverses_id 로 찾는다.
func (s *JournalService) checkReversalAllowed(ctx context.Context, q contract.Tx, rev domain.Journal) error {
	orig, err := s.Repo.Journal(ctx, q, rev.ReversesNo)
	if err != nil {
		return err
	}
	return reversalPayableGuard(ctx, q, orig)
}

func reversalPayableGuard(ctx context.Context, q contract.Tx, orig domain.Journal) error {
	paid, reserved, _, err := repository.SQLLedger{}.PayableUse(ctx, q, orig.ID)
	if err != nil {
		return err
	}
	if paid > 0 || reserved > 0 {
		return validation("지급이 시작된 채무는 역분개할 수 없습니다. 지급을 취소한 뒤 다시 시도하세요.")
	}
	return nil
}

// Reverse 는 승인된 전표의 차대를 반대로 한 임시 역분개 전표를 만든다(원 전표 연결). 한 전표는 한 번만 역분개할 수 있고 같은 승인 절차를 탄다.
func (s *JournalService) Reverse(ctx context.Context, no, postingDate string) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.reverse", audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, err
	}
	var out domain.Journal
	err = s.write(ctx, p, "journal.reverse", audit.TargetJournal, 0, func(tx contract.Tx) error {
		orig, err := s.Repo.Journal(ctx, tx, no)
		if err != nil {
			return err
		}
		if orig.Status != domain.JournalApproved {
			return fmt.Errorf("%w: 승인된 전표만 역분개할 수 있습니다", contract.ErrInvalidState)
		}
		if orig.Kind == domain.JournalReversal {
			return fmt.Errorf("%w: 역분개 전표는 다시 역분개할 수 없습니다. 새 전표를 입력하세요", contract.ErrInvalidState)
		}
		if orig.ReversedByNo != "" {
			return fmt.Errorf("%w: 이미 역분개된 전표입니다.", contract.ErrInvalidState)
		}
		if err := reversalPayableGuard(ctx, tx, orig); err != nil {
			return err
		}
		now := s.now()
		if postingDate == "" {
			postingDate = string(contract.DayOf(now))
		}
		if !validDate(postingDate) {
			return validation("전기일은 YYYY-MM-DD 형식으로 입력하세요.")
		}
		if err := s.assertMonthOpen(ctx, tx, postingDate); err != nil {
			// 마감된 월 거부는 write 가 롤백 뒤에 감사 denied 로 남긴다(트랜잭션 안에서 따로 기록하면 연결이 막힌다).
			return err
		}
		newNo, err := s.Repo.NextJournalNo(ctx, tx, now.In(contract.KST).Year())
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertJournal(ctx, tx, repository.JournalInsert{No: newNo, Kind: domain.JournalReversal, PostingDate: postingDate, TradeDate: orig.TradeDate,
			Memo: truncateRunes("역분개: "+orig.No+" "+orig.Memo, 200), ReversesID: orig.ID, SiteID: orig.SiteID, Total: orig.TotalAmount, CreatedBy: p.UserID, Now: now})
		if err != nil {
			return err
		}
		lines := make([]repository.LineInsert, len(orig.Lines))
		for i, l := range orig.Lines {
			side := "C"
			if l.Side == "C" {
				side = "D"
			}
			lines[i] = repository.LineInsert{AccountID: l.AccountID, Side: side, Amount: l.Amount, VendorID: l.VendorID, Memo: l.Memo}
		}
		if err := s.Repo.ReplaceLines(ctx, tx, id, lines); err != nil {
			return err
		}
		if out, err = s.Repo.Journal(ctx, tx, newNo); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.reverse", TargetType: audit.TargetJournal, TargetID: newNo, Result: audit.ResultSuccess, Detail: "역분개 전표 생성",
			Diff: []audit.DiffItem{{Key: "original_no", New: orig.No}, {Key: "posting_date", New: postingDate}}})
	})
	if err != nil && dbutil.IsUnique(err) {
		return out, fmt.Errorf("%w: 이미 역분개된 전표입니다.", contract.ErrInvalidState)
	}
	return out, err
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---- 결재 후속 동작(docHookTable 의 JV) ----

// approveJournalHook 은 전표 승인 요청서가 최종 승인되면 같은 트랜잭션에서 전표를 승인한다: 만든 사람과 승인자 분리, 월 재확인, 상태·채무 항목·감사.
// 어느 하나라도 실패하면 승인 전체가 취소된다.
func approveJournalHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	repo := repository.SQLLedger{}
	j, err := repo.JournalByDoc(ctx, tx, rec.ID)
	if err != nil {
		return fmt.Errorf("%w: 전표와 연결되지 않은 승인 요청서입니다", contract.ErrInvalidState)
	}
	to, err := journalTransition(j.Status, jevApprove)
	if err != nil {
		return err
	}
	// 권한 분리: 전표 작성자가 최종 승인까지 하면 규칙 모드대로(기본 경고). 자동 전표(시스템이 작성)는 작성자 번호가 승인자와 같아도 판정 대상이 아니다.
	if !strings.HasPrefix(string(j.Kind), "auto_") {
		if _, err := b.sodCheck(ctx, tx, p, sod.RuleJVCreateApprove, j.CreatedByID, sod.Ref{Type: "journal", ID: j.ID, No: j.No}); err != nil {
			return err
		}
	}
	// AI 비용 배부 전표의 작성자는 배부를 확정한 사람이다(B49): 확정한 사람이 그 전표를 승인하면 규칙 모드대로(기본 차단).
	if j.Kind == domain.JournalAutoAICost {
		if _, err := b.sodCheck(ctx, tx, p, sod.RuleAICostConfirmPost, j.CreatedByID, sod.Ref{Type: "journal", ID: j.ID, No: j.No}); err != nil {
			return err
		}
	}
	if err := b.assertMonthOpen(ctx, tx, j.PostingDate); err != nil {
		return fmt.Errorf("%w: 이후 마감되어 승인할 수 없습니다", contract.ErrInvalidState)
	}
	now := b.now()
	// 역분개 승인: 원 전표에 지급·예약이 없어야 하고 원 전표의 채무 항목을 취소한다.
	payables := 0
	var orig domain.Journal
	if j.Kind == domain.JournalReversal {
		var err error
		orig, err = repo.Journal(ctx, tx, j.ReversesNo)
		if err != nil {
			return err
		}
		if err := reversalPayableGuard(ctx, tx, orig); err != nil {
			return err
		}
		if payables, err = repo.CancelPayables(ctx, tx, orig.ID); err != nil {
			return err
		}
	}
	if err := repo.SetStatus(ctx, tx, j.ID, to, p.UserID, now, ""); err != nil {
		return err
	}
	if j.Kind == domain.JournalReversal && orig.Kind == domain.JournalAutoPayment {
		// 지급 전표 역분개 승인: 채무 항목 지급 누계를 되돌리고 지급 건을 지급 취소로 바꾼다(B32).
		if err := restorePayment(ctx, b, tx, p, orig, j); err != nil {
			return err
		}
	}
	if j.Kind == domain.JournalReversal && orig.Kind == domain.JournalAutoCard {
		// 카드 비용 전표 역분개 승인: 카드 내역을 소명 보완 필요로 되돌린다(B32).
		if err := restoreCardOnReversal(ctx, b, tx, p, orig); err != nil {
			return err
		}
	}
	if j.Kind != domain.JournalReversal {
		n, err := createPayables(ctx, repo, tx, j)
		if err != nil {
			return err
		}
		payables = n
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.approve", TargetType: audit.TargetJournal, TargetID: j.No, Result: audit.ResultSuccess, Detail: "전표 승인",
		Diff: []audit.DiffItem{{Key: "doc_no", New: rec.No}, {Key: "payable_count", New: fmt.Sprint(payables)}}})
}

// createPayables 는 승인된 전표의 외상매입금 대변 줄마다 채무 항목을 만든다(만기 = 거래일 + 거래처 결제 조건일).
func createPayables(ctx context.Context, repo repository.SQLLedger, tx contract.Tx, j domain.Journal) (int, error) {
	lines, err := repo.PayableLines(ctx, tx, j.ID)
	if err != nil {
		return 0, err
	}
	trade, err := time.Parse("2006-01-02", j.TradeDate)
	if err != nil {
		return 0, err
	}
	for _, l := range lines {
		// 기초 전표(B35)의 외상매입금 줄은 원 시스템의 발생일·만기일을 가진다. 그 밖의 전표는 거래일 + 거래처 결제 조건일이다.
		if j.Kind == domain.JournalOpening {
			if t, d, found, err := (repository.SQLImport{}).OpeningTerms(ctx, tx, l.LineID); err != nil {
				return 0, err
			} else if found {
				if err := repo.InsertPayable(ctx, tx, l, j.ID, t, d); err != nil {
					return 0, err
				}
				continue
			}
		}
		days, err := repo.VendorPaymentDays(ctx, tx, l.VendorID)
		if err != nil {
			return 0, err
		}
		if err := repo.InsertPayable(ctx, tx, l, j.ID, j.TradeDate, trade.AddDate(0, 0, days).Format("2006-01-02")); err != nil {
			return 0, err
		}
	}
	return len(lines), nil
}

// returnJournalHook 은 반려·회수·상신 취소로 문서가 임시가 되면 전표를 임시로 되돌린다(다시 고쳐 요청할 수 있다).
func returnJournalHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, ev journalEvent, detail string) error {
	repo := repository.SQLLedger{}
	j, err := repo.JournalByDoc(ctx, tx, rec.ID)
	if errors.Is(err, contract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Status != domain.JournalRequested {
		return nil
	}
	to, err := journalTransition(j.Status, ev)
	if err != nil {
		return err
	}
	if err := repo.SetStatus(ctx, tx, j.ID, to, p.UserID, b.now(), ""); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.return", TargetType: audit.TargetJournal, TargetID: j.No, Result: audit.ResultSuccess, Detail: detail,
		Diff: []audit.DiffItem{{Key: "doc_no", New: rec.No}}})
}

func rejectJournalHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	return returnJournalHook(ctx, b, tx, p, rec, jevReject, "전표 승인 요청 반려")
}

func resetJournalHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	return returnJournalHook(ctx, b, tx, p, rec, jevReset, "전표 승인 요청 회수")
}

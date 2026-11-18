package service

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 카드 내역 소명·확인·반려·환입·재무 현황·소명 알림(B32 3.3.3~3.3.7).

var openStatuses = []string{domain.CardTxnUnreconciled, domain.CardTxnNeedsFix}

func isOpenStatus(s string) bool {
	return s == domain.CardTxnUnreconciled || s == domain.CardTxnNeedsFix
}

// toTxn 은 저장 행을 응답 형태로 바꾼다(기한 남은 일수·경과, 소명 이력과 증빙).
func (s *CardService) toTxn(ctx context.Context, q contract.Tx, r repository.TxnRow, withHistory bool) domain.CardTxn {
	t := r.CardTxn
	t.DaysLeft = dayDiff(t.DueDate, s.kstDay())
	t.Overdue = isOpenStatus(t.Status) && t.DaysLeft < 0
	if withHistory {
		if hist, _, err := s.Repo.Explanations(ctx, q, t.ID); err == nil && len(hist) > 0 {
			ev := s.cardEvidence(ctx, q, t.ID)
			hist[len(hist)-1].Attachments = ev
			t.History = hist
			last := hist[len(hist)-1]
			t.Explanation = &last
		}
	}
	return t
}

// noteCardTxns 는 소지자가 아닌 사람이 카드 내역을 열람한 것을 기록한다(B41, D4: 카드 번호 원문은 저장하지 않으므로 다른 사람의 내역 열람이 대상이다).
// 응답 한 번에 한 번 부르고, 본인 소지 내역만 있으면 기록하지 않는다. 기록하지 못하면 내역을 내주지 않는다(fail-closed).
func (s *CardService) noteCardTxns(ctx context.Context, p contract.Principal, txns []domain.CardTxn) error {
	seen := map[int64]bool{}
	var subs []privacy.Subject
	for _, t := range txns {
		if t.HolderID == 0 || t.HolderID == p.UserID || seen[t.HolderID] {
			continue
		}
		seen[t.HolderID] = true
		subs = append(subs, privacy.Subject{Type: privacy.SubjectUser, ID: t.HolderID})
	}
	return s.notePrivacy(ctx, p, privacy.Access{Category: privacy.CatCard, Screen: privacy.ScreenCardTxn, Fields: []string{privacy.FieldCardTxn}, Subjects: subs})
}

func (s *CardService) toTxns(ctx context.Context, q contract.Tx, rows []repository.TxnRow) []domain.CardTxn {
	out := make([]domain.CardTxn, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toTxn(ctx, q, r, false))
	}
	return out
}

func statusFilter(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	if _, ok := domain.CardTxnStatusLabels[s]; !ok {
		return nil, validation("알 수 없는 상태입니다.")
	}
	return []string{s}, nil
}

func cardDateRange(f domain.CardFilter) error {
	for _, d := range []string{f.From, f.To} {
		if d != "" && !validDate(d) {
			return validation("날짜는 YYYY-MM-DD 형식으로 입력하세요.")
		}
	}
	if f.From != "" && f.To != "" && f.To < f.From {
		return validation("종료일은 시작일 이후여야 합니다.")
	}
	return nil
}

// Mine 은 호출자 본인이 소지한 카드의 내역이다(미소명이 맨 위).
func (s *CardService) Mine(ctx context.Context, f domain.CardFilter) ([]domain.CardTxn, error) {
	p, err := contract.Authorize(ctx, contract.PermCardExplain)
	if err != nil {
		return nil, err
	}
	st, err := statusFilter(f.Status)
	if err != nil {
		return nil, err
	}
	if err := cardDateRange(f); err != nil {
		return nil, err
	}
	rows, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{HolderID: p.UserID, Statuses: st, From: f.From, To: f.To, Limit: f.Limit, Offset: f.Offset})
	if err != nil {
		return nil, err
	}
	return s.toTxns(ctx, s.DB, rows), nil
}

// GetTxn 은 내역 한 건(소명 이력·증빙 포함)이다. 볼 수 없으면 찾을 수 없음이다.
func (s *CardService) GetTxn(ctx context.Context, id int64) (domain.CardTxn, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return domain.CardTxn{}, contract.ErrUnauthenticated
	}
	if !p.Can(contract.PermCardExplain) && !p.Can(contract.PermCardReadAll) && !p.Can(contract.PermCardConfirm) {
		return domain.CardTxn{}, contract.ErrForbidden
	}
	if !s.cardTxnVisible(ctx, p, id) {
		return domain.CardTxn{}, contract.ErrNotFound
	}
	r, err := s.Repo.Txn(ctx, s.DB, id)
	if err != nil {
		return domain.CardTxn{}, err
	}
	t := s.toTxn(ctx, s.DB, r, true)
	if err := s.noteCardTxns(ctx, p, []domain.CardTxn{t}); err != nil {
		return domain.CardTxn{}, err
	}
	return t, nil
}

// GetMyTxn 은 본인 소지 내역 한 건이다(소명 이력·증빙 포함). 타인 내역은 존재를 숨긴다.
func (s *CardService) GetMyTxn(ctx context.Context, id int64) (domain.CardTxn, error) {
	p, err := contract.Authorize(ctx, contract.PermCardExplain)
	if err != nil {
		return domain.CardTxn{}, err
	}
	r, err := s.Repo.Txn(ctx, s.DB, id)
	if err != nil || r.HolderID != p.UserID {
		return domain.CardTxn{}, contract.ErrNotFound
	}
	return s.toTxn(ctx, s.DB, r, true), nil
}

// calcCardVAT 는 부가세를 정한다: CSV 값이 있으면 그 값, 없으면 (합계 − 봉사료) × r/(100+r) 원 미만 버림(부가세 포함 가격 역산, r=10 이면 1/11). 공급가 = 합계 − 부가세(봉사료는 공급가에 포함해 비용으로).
func calcCardVAT(total int64, csvVAT, tip *int64, ratePercent int) (supply, vat int64) {
	if csvVAT != nil && *csvVAT >= 0 && *csvVAT <= total {
		vat = *csvVAT
	} else {
		base := total
		if tip != nil && *tip > 0 && *tip < total {
			base = total - *tip
		}
		vat = domain.VATFromTotal(base, ratePercent)
	}
	return total - vat, vat
}

func receiptRule(threshold int64) string {
	if threshold == 30000 {
		return "3만원을 넘는 사용 내역은 영수증을 첨부해야 합니다."
	}
	return fmt.Sprintf("%s을 넘는 사용 내역은 영수증을 첨부해야 합니다.", moneyLabel(threshold))
}

// Explain 은 소지자가 용도·계정과목·부가세 구분·증빙을 적어 소명을 제출한다(확인 전에는 철회해 고칠 수 있다).
func (s *CardService) Explain(ctx context.Context, txnID int64, in domain.ExplainInput) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardExplain, "card.explain", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	in.Purpose = strings.TrimSpace(in.Purpose)
	if c := utf8.RuneCountInString(in.Purpose); c < 10 || c > 200 {
		return domain.CardTxn{}, validation("용도는 10자 이상 200자 이내로 입력하세요.")
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.explain", audit.TargetCard, txnID, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, txnID)
		if err != nil || r.HolderID != p.UserID {
			return contract.ErrNotFound // 타인 내역은 존재를 숨긴다
		}
		if r.Kind != domain.CardKindApprove {
			return fmt.Errorf("%w: 취소 내역은 소명하지 않습니다", contract.ErrInvalidState)
		}
		to, err := cardTransition(r.Status, cardEvSubmit)
		if err != nil {
			return err
		}
		deduct := in.VATDeduct == nil || *in.VATDeduct
		var accID int64
		var accCode string
		supply, vat := r.Amount, int64(0)
		if !in.Personal {
			allowed, err := s.Repo.ExplainAccounts(ctx, tx)
			if err != nil {
				return err
			}
			for _, a := range allowed {
				if a.Code == strings.TrimSpace(in.AccountCode) {
					accID, accCode = a.ID, a.Code
				}
			}
			if accID == 0 {
				return validation("소명에 쓸 수 있는 계정과목을 고르세요.")
			}
			if !deduct {
				if _, ok := domain.CardVATReasons[in.VATReason]; !ok {
					return validation("부가세를 공제하지 않으려면 사유(접대비·개인 사용 의심·기타)를 고르세요.")
				}
			}
			rate, err := s.vatRateOn(ctx, tx, string(contract.DayOf(r.UsedAt))) // 카드 사용일(거래일)의 부가세율
			if err != nil {
				return err
			}
			supply, vat = calcCardVAT(r.Amount, r.VAT, r.Tip, rate)
			threshold := int64(s.Repo.SettingInt(ctx, tx, "card.receipt_required_over", 30000))
			if r.Amount > threshold && len(in.AttachmentIDs) == 0 {
				return validation("%s", receiptRule(threshold))
			}
		}
		explID, err := s.Repo.InsertExplanation(ctx, tx, repository.ExplanationInsert{TxnID: r.ID, AccountID: accID, SubmittedBy: p.UserID, Purpose: in.Purpose,
			VATReason: in.VATReason, VATDeduct: deduct, Personal: in.Personal, Supply: supply, VAT: vat, Now: s.now()})
		if err != nil {
			return err
		}
		_ = explID
		if err := s.linkCardEvidence(ctx, tx, r.ID, p.UserID, in.AttachmentIDs); err != nil {
			return err
		}
		if err := s.Repo.SetTxnStatus(ctx, tx, r.ID, to); err != nil {
			return err
		}
		fresh, err := s.Repo.Txn(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		out = s.toTxn(ctx, tx, fresh, true)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.explain", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", r.ID), Result: audit.ResultSuccess, Detail: "카드 소명 제출",
			Diff: []audit.DiffItem{{Key: "account_code", New: accCode}, {Key: "vat_deduct", New: fmt.Sprint(deduct)}, {Key: "personal", New: fmt.Sprint(in.Personal)}}})
	})
	return out, err
}

// Withdraw 는 확인 전의 소명을 철회한다(미소명으로 돌아가 다시 소명할 수 있다).
func (s *CardService) Withdraw(ctx context.Context, txnID int64) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardExplain, "card.explain_withdraw", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.explain_withdraw", audit.TargetCard, txnID, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, txnID)
		if err != nil || r.HolderID != p.UserID {
			return contract.ErrNotFound
		}
		to, err := cardTransition(r.Status, cardEvWithdraw)
		if err != nil {
			return err
		}
		e, err := s.Repo.LatestSubmitted(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if err := s.Repo.SetExplanationStatus(ctx, tx, e.ID, "withdrawn", p.UserID, s.now(), ""); err != nil {
			return err
		}
		if err := s.Repo.SetTxnStatus(ctx, tx, r.ID, to); err != nil {
			return err
		}
		fresh, _ := s.Repo.Txn(ctx, tx, r.ID)
		out = s.toTxn(ctx, tx, fresh, true)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.explain_withdraw", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", r.ID), Result: audit.ResultSuccess, Detail: "카드 소명 철회"})
	})
	return out, err
}

// ---- 확인 ----

// confirmHolders 는 p 가 확인할 수 있는 조직의 구성원(본인 제외)이다.
func (s *CardService) confirmHolders(ctx context.Context, p contract.Principal) (map[int64]bool, []int64, error) {
	scope, err := s.confirmScope(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	seen := map[int64]bool{}
	var ids []int64
	day := contract.DayOf(s.now())
	for u := range scope {
		ms, err := s.Org.MembersOf(ctx, u, day, false)
		if err != nil {
			return nil, nil, err
		}
		for _, m := range ms {
			if m != p.UserID && !seen[m] {
				seen[m] = true
				ids = append(ids, m)
			}
		}
	}
	return scope, ids, nil
}

// ToConfirm 은 확인할 조직 범위 소지자의 "소명됨" 건이다.
func (s *CardService) ToConfirm(ctx context.Context) ([]domain.CardTxn, error) {
	p, err := contract.Authorize(ctx, contract.PermCardConfirm)
	if err != nil {
		return nil, err
	}
	_, ids, err := s.confirmHolders(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []domain.CardTxn{}, nil
	}
	rows, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{HolderIDs: ids, Statuses: []string{domain.CardTxnSubmitted}, Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]domain.CardTxn, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toTxn(ctx, s.DB, r, true))
	}
	if err := s.noteCardTxns(ctx, p, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Confirm 은 소명을 확인한다(건별 독립, 50건 상한). 확인되면 같은 트랜잭션에서 임시 카드 비용 전표가 만들어진다(개인 사용은 환입 요청).
func (s *CardService) Confirm(ctx context.Context, ids []int64) ([]domain.CardBatchResult, error) {
	p, err := contract.Authorize(ctx, contract.PermCardConfirm)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return nil, validation("확인할 내역을 고르세요.")
	}
	if len(uniq) > maxConfirmBatch {
		return nil, validation("한 번에 %d건까지 확인할 수 있습니다.", maxConfirmBatch)
	}
	scope, err := s.confirmScope(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CardBatchResult, 0, len(uniq))
	for _, id := range uniq {
		res := domain.CardBatchResult{ID: id}
		pre, err := s.Repo.Txn(ctx, s.DB, id)
		if err != nil || !s.canConfirmScope(ctx, pre.HolderID, scope) {
			res.Error = "확인할 수 있는 내역이 아닙니다."
			out = append(out, res)
			continue
		}
		jno, err := s.confirmOne(ctx, p, id)
		if err != nil {
			res.Error = strings.TrimPrefix(strings.TrimPrefix(err.Error(), contract.ErrInvalidState.Error()+": "), contract.ErrValidation.Error()+": ")
		} else {
			res.OK, res.JournalNo = true, jno
		}
		out = append(out, res)
	}
	return out, nil
}

func (s *CardService) confirmOne(ctx context.Context, p contract.Principal, id int64) (string, error) {
	var jno string
	err := s.write(ctx, p, "card.confirm", audit.TargetCard, id, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, id)
		if err != nil {
			return err
		}
		e, err := s.Repo.LatestSubmitted(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("%w: 확인할 소명이 없습니다", contract.ErrInvalidState)
		}
		// 권한 분리: 카드 소지자(소명한 사람)가 자기 소명을 확인하려 하면 규칙 모드대로(기본 차단). 앞 단계 행위자는 같은 트랜잭션에서 읽은 소지자다.
		if _, err := s.sodCheck(ctx, tx, p, sod.RuleCardHolderConfirm, r.HolderID, sod.Ref{Type: "card_txn", ID: id}); err != nil {
			return err
		}
		ev := cardEvConfirm
		if e.Personal {
			ev = cardEvConfirmPersonal
		}
		to, err := cardTransition(r.Status, ev)
		if err != nil {
			return err
		}
		if err := s.Repo.SetExplanationStatus(ctx, tx, e.ID, "confirmed", p.UserID, s.now(), ""); err != nil {
			return err
		}
		if err := s.Repo.SetTxnStatus(ctx, tx, id, to); err != nil {
			return err
		}
		detail := "카드 소명 확인"
		if e.Personal {
			detail = "카드 소명 확인(개인 사용: 환입 요청)"
		} else {
			aj := autoJournal{Base: s.Base, Repo: s.Ledger}
			out, err := aj.cardExpense(ctx, tx, p, cardExpenseInput{explanationID: e.ID, txnID: id, accountID: e.AccountID, supply: e.Supply, vat: e.VAT, total: r.Amount, deduct: e.VATDeduct,
				merchant: r.Merchant, cardAlias: r.CardAlias, tradeDate: r.UsedAt.In(contract.KST).Format("2006-01-02")})
			if err != nil {
				return err
			}
			jno = out.JournalNo
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.confirm", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", id), Result: audit.ResultSuccess, Detail: detail,
			Diff: []audit.DiffItem{{Key: "personal", New: fmt.Sprint(e.Personal)}, {Key: "journal_no", New: jno}}})
	})
	return jno, err
}

// Return 은 소명을 반려한다(사유 필수, 소지자에게 알림, 보완 필요).
func (s *CardService) Return(ctx context.Context, id int64, reason string) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardConfirm, "card.return", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	reason = strings.TrimSpace(reason)
	if c := utf8.RuneCountInString(reason); c < 1 || c > 200 {
		return domain.CardTxn{}, validation("반려 사유를 1~200자로 입력하세요.")
	}
	scope, err := s.confirmScope(ctx, p)
	if err != nil {
		return domain.CardTxn{}, err
	}
	pre, err := s.Repo.Txn(ctx, s.DB, id)
	if err != nil || !s.canConfirm(ctx, p, pre.HolderID, scope) {
		return domain.CardTxn{}, contract.ErrNotFound
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.return", audit.TargetCard, id, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := cardTransition(r.Status, cardEvReturn)
		if err != nil {
			return err
		}
		e, err := s.Repo.LatestSubmitted(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.SetExplanationStatus(ctx, tx, e.ID, "returned", p.UserID, s.now(), reason); err != nil {
			return err
		}
		if err := s.Repo.SetTxnStatus(ctx, tx, id, to); err != nil {
			return err
		}
		if r.HolderID != 0 {
			if err := s.notify(ctx, tx, repository.NotificationInsert{UserID: r.HolderID, Kind: "card_returned", Title: "카드 소명이 반려되었습니다",
				Body: "소명이 반려되었습니다. 사유: " + reason, Link: "/finance/cards/mine", RefType: "card_txn", RefID: id, Now: s.now()}); err != nil {
				return err
			}
		}
		fresh, _ := s.Repo.Txn(ctx, tx, id)
		out = s.toTxn(ctx, tx, fresh, true)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.return", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", id), Result: audit.ResultSuccess, Detail: "카드 소명 반려",
			Diff: []audit.DiffItem{{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}}})
	})
	return out, err
}

// ---- 재무 현황·환입·소지자 ----

// All 은 카드 내역 전체 목록이다(card:read_all).
func (s *CardService) All(ctx context.Context, f domain.CardFilter) ([]domain.CardTxn, int, error) {
	p, err := contract.Authorize(ctx, contract.PermCardReadAll)
	if err != nil {
		return nil, 0, err
	}
	st, err := statusFilter(f.Status)
	if err != nil {
		return nil, 0, err
	}
	if err := cardDateRange(f); err != nil {
		return nil, 0, err
	}
	rows, total, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{Statuses: st, From: f.From, To: f.To, Limit: f.Limit, Offset: f.Offset})
	if err != nil {
		return nil, 0, err
	}
	out := s.toTxns(ctx, s.DB, rows)
	if err := s.noteCardTxns(ctx, p, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Overview 는 재무 현황이다: 상태별 건수, 소명 지연(30일 경과), 소지자 없음, 재무 처리 필요, 환입 요청.
func (s *CardService) Overview(ctx context.Context) (domain.CardOverview, error) {
	var out domain.CardOverview
	if _, err := contract.Authorize(ctx, contract.PermCardReadAll); err != nil {
		return out, err
	}
	var err error
	if out.Counts, out.Amounts, err = s.Repo.StatusSums(ctx, s.DB); err != nil {
		return out, err
	}
	today := s.kstDay()
	open, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{Statuses: openStatuses, Limit: 500})
	if err != nil {
		return out, err
	}
	for _, r := range open {
		t := s.toTxn(ctx, s.DB, r, false)
		if dayDiff(today, t.DueDate) >= delayedAfterDays {
			out.Delayed = append(out.Delayed, t)
		}
		if t.HolderID == 0 || !r.HolderActive {
			out.NoHolder = append(out.NoHolder, t)
		}
	}
	fin, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{Statuses: []string{domain.CardTxnOrphanCancel}, Limit: 200})
	if err != nil {
		return out, err
	}
	out.NeedsFinance = s.toTxns(ctx, s.DB, fin)
	ref, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{Statuses: []string{domain.CardTxnRefundRequested}, Limit: 200})
	if err != nil {
		return out, err
	}
	out.RefundRequested = s.toTxns(ctx, s.DB, ref)
	return out, nil
}

// RequestRefund 는 소명 기한이 30일 지난 미소명 건을 재무가 환입 요청으로 바꾼다(사유 필수). 자동 개인 사용 처리는 없다.
func (s *CardService) RequestRefund(ctx context.Context, id int64, reason string) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardManage, "card.refund_request", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	reason = strings.TrimSpace(reason)
	if c := utf8.RuneCountInString(reason); c < 1 || c > 200 {
		return domain.CardTxn{}, validation("환입 요청 사유를 1~200자로 입력하세요.")
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.refund_request", audit.TargetCard, id, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := cardTransition(r.Status, cardEvRefundRequest)
		if err != nil {
			return err
		}
		if dayDiff(s.kstDay(), r.DueDate) < delayedAfterDays {
			return fmt.Errorf("%w: 소명 기한이 %d일 지난 내역만 환입 요청으로 바꿀 수 있습니다", contract.ErrInvalidState, delayedAfterDays)
		}
		if err := s.Repo.SetTxnRefund(ctx, tx, id, to, reason); err != nil {
			return err
		}
		fresh, _ := s.Repo.Txn(ctx, tx, id)
		out = s.toTxn(ctx, tx, fresh, false)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.refund_request", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", id), Result: audit.ResultSuccess, Detail: "카드 환입 요청",
			Diff: []audit.DiffItem{{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}}})
	})
	return out, err
}

// CompleteRefund 는 환입 요청을 환입 완료로 닫는다(환입일·방법 메모, 실제 정산은 시스템 밖).
func (s *CardService) CompleteRefund(ctx context.Context, id int64, refundDate, note string) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardManage, "card.refund_done", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	note = strings.TrimSpace(note)
	if c := utf8.RuneCountInString(note); c < 1 || c > 200 {
		return domain.CardTxn{}, validation("환입 방법 메모를 1~200자로 입력하세요.")
	}
	if !validDate(refundDate) || refundDate > s.kstDay() {
		return domain.CardTxn{}, validation("환입일은 오늘 이전의 YYYY-MM-DD 로 입력하세요.")
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.refund_done", audit.TargetCard, id, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, id)
		if err != nil {
			return err
		}
		to, err := cardTransition(r.Status, cardEvRefundDone)
		if err != nil {
			return err
		}
		if err := s.Repo.SetTxnRefund(ctx, tx, id, to, strings.TrimSpace(r.RefundNote+" / 환입 "+refundDate+" "+note)); err != nil {
			return err
		}
		fresh, _ := s.Repo.Txn(ctx, tx, id)
		out = s.toTxn(ctx, tx, fresh, false)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.refund_done", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", id), Result: audit.ResultSuccess, Detail: "카드 환입 완료",
			Diff: []audit.DiffItem{{Key: "refund_date", New: refundDate}}})
	})
	return out, err
}

// ReassignHolder 는 소지자가 없거나 퇴사한 미소명 건을 새 소지자에게 넘긴다.
func (s *CardService) ReassignHolder(ctx context.Context, txnID, holderID int64) (domain.CardTxn, error) {
	p, err := s.authorize(ctx, contract.PermCardHolderManage, "card.reassign", audit.TargetCard)
	if err != nil {
		return domain.CardTxn{}, err
	}
	var out domain.CardTxn
	err = s.write(ctx, p, "card.reassign", audit.TargetCard, txnID, func(tx contract.Tx) error {
		r, err := s.Repo.Txn(ctx, tx, txnID)
		if err != nil {
			return err
		}
		if !isOpenStatus(r.Status) {
			return fmt.Errorf("%w: 미소명·보완 필요 상태의 내역만 소지자를 바꿀 수 있습니다", contract.ErrInvalidState)
		}
		if err := s.checkHolder(ctx, tx, holderID); err != nil {
			return err
		}
		if holderID == 0 {
			return validation("새 소지자를 고르세요.")
		}
		if err := s.Repo.SetTxnHolder(ctx, tx, txnID, holderID); err != nil {
			return err
		}
		fresh, _ := s.Repo.Txn(ctx, tx, txnID)
		out = s.toTxn(ctx, tx, fresh, false)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.reassign", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", txnID), Result: audit.ResultSuccess, Detail: "카드 내역 소지자 재지정",
			Diff: []audit.DiffItem{{Key: "holder_from", New: r.HolderName}, {Key: "holder_to", New: fresh.HolderName}}})
	})
	return out, err
}

// ExplainAccounts 는 소명에 쓸 수 있는 비용 계정이다.
func (s *CardService) ExplainAccounts(ctx context.Context) ([]domain.Account, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if !p.Can(contract.PermCardExplain) && !p.Can(contract.PermCardReadAll) {
		return nil, contract.ErrForbidden
	}
	out, err := s.Repo.ExplainAccounts(ctx, s.DB)
	if out == nil {
		out = []domain.Account{}
	}
	return out, err
}

// Unreconciled 는 호출자 본인 소지 카드의 미소명·보완 필요 내역이다(MCP get_card_unreconciled). 카드번호 조각은 없다(별칭만).
func (s *CardService) Unreconciled(ctx context.Context, limit int) ([]domain.CardUnreconciledItem, error) {
	p, err := contract.Authorize(ctx, contract.PermCardExplain)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, _, err := s.Repo.Txns(ctx, s.DB, repository.TxnQuery{HolderID: p.UserID, Statuses: openStatuses, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.CardUnreconciledItem, 0, len(rows))
	for _, r := range rows {
		t := s.toTxn(ctx, s.DB, r, false)
		out = append(out, domain.CardUnreconciledItem{ID: t.ID, UsedAt: t.UsedAt, Merchant: t.Merchant, Amount: t.Amount, DueDate: t.DueDate, Overdue: t.Overdue, CardAlias: t.CardAlias, NeedsFixed: t.Status == domain.CardTxnNeedsFix})
	}
	return out, nil
}

// restoreCardOnReversal 은 카드 비용 전표의 역분개가 승인될 때 같은 트랜잭션에서 카드 내역을 보완 필요로 되돌린다(소지자가 다시 소명, 부서장이 다시 확인).
func restoreCardOnReversal(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, orig domain.Journal) error {
	if orig.SourceType != "card_txn" {
		return nil
	}
	repo := repository.SQLCards{}
	txnID, err := repo.TxnByExplanation(ctx, tx, orig.SourceID)
	if err != nil {
		return nil
	}
	r, err := repo.Txn(ctx, tx, txnID)
	if err != nil {
		return err
	}
	to, err := cardTransition(r.Status, cardEvReverse)
	if err != nil {
		return nil // 이미 다른 상태면 후속 동작이 없다
	}
	if err := repo.SetTxnStatus(ctx, tx, txnID, to); err != nil {
		return err
	}
	if r.HolderID != 0 {
		if err := b.notify(ctx, tx, repository.NotificationInsert{UserID: r.HolderID, Kind: "card_reopened", Title: "카드 소명을 다시 해야 합니다",
			Body: "확인된 소명의 비용 전표가 정정되어 보완이 필요합니다.", Link: "/finance/cards/mine", RefType: "card_txn", RefID: txnID, Now: b.now()}); err != nil {
			return err
		}
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.reopen", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("내역 %d", txnID), Result: audit.ResultSuccess, Detail: "비용 전표 역분개 승인으로 소명 보완 필요 전환"})
}

// ---- 소명 알림(하루 한 번, 시계 주입 시험) ----

// Tick 은 KST 09:00 이후 첫 실행에서 알림을 만든다. 사람당 하루 한 건으로 묶고 본문에는 건수·금액만 넣는다(카드번호·가맹점명 없음).
//
//	기한 하루 전, 기한 경과 첫날, 이후 7일마다(경과 8·15·22일째) 소지자에게, 경과 14일째에 소지자의 조직장에게 한 건.
func (s *CardService) Tick(ctx context.Context) error {
	now := s.now()
	if now.In(contract.KST).Hour() < 9 {
		return nil
	}
	today := s.kstDay()
	// 휴일(주말·공휴일)에는 새 알림을 만들지 않는다. 다음 영업일 첫 실행에서 건너뛴 날의 대상까지 합쳐 한 건으로 만든다(B34).
	if ok, err := s.isBusinessDay(ctx, s.DB, today); err != nil || !ok {
		return err
	}
	days := []string{today}
	for d := parseDay(today).AddDate(0, 0, -1); len(days) < 15; d = d.AddDate(0, 0, -1) {
		ds := d.Format("2006-01-02")
		if ok, err := s.isBusinessDay(ctx, s.DB, ds); err != nil {
			return err
		} else if ok {
			break
		}
		days = append(days, ds)
	}
	rows, err := s.Repo.OpenTxnsForReminder(ctx, s.DB)
	if err != nil {
		return err
	}
	type agg struct {
		soon, over int
		amount     int64
	}
	byHolder := map[int64]*agg{}
	headOf := map[int64]*agg{}
	for _, r := range rows {
		hit, headHit, soon := false, false, false
		for _, d := range days { // 오늘과 바로 앞의 휴일들: 휴일에 걸린 알림 시점도 오늘 한 번에 챙긴다
			left := dayDiff(r.DueDate, d)
			over := -left
			if left == 1 || over == 1 || over == 8 || over == 15 || over == 22 {
				hit, soon = true, soon || left == 1
			}
			if over == 14 {
				headHit = true
			}
		}
		if hit {
			a := byHolder[r.HolderID]
			if a == nil {
				a = &agg{}
				byHolder[r.HolderID] = a
			}
			if soon {
				a.soon++
			} else {
				a.over++
			}
			a.amount += r.Amount
		}
		if headHit {
			a := headOf[r.HolderID]
			if a == nil {
				a = &agg{}
				headOf[r.HolderID] = a
			}
			a.over++
			a.amount += r.Amount
		}
	}
	holders := make([]int64, 0, len(byHolder))
	for h := range byHolder {
		holders = append(holders, h)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i] < holders[j] })
	for _, h := range holders {
		a := byHolder[h]
		h := h
		err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
			if first, err := s.Repo.MarkReminder(ctx, tx, h, today, "holder"); err != nil || !first {
				return err
			}
			parts := []string{}
			if a.soon > 0 {
				parts = append(parts, fmt.Sprintf("내일이 소명 기한인 내역 %d건", a.soon))
			}
			if a.over > 0 {
				parts = append(parts, fmt.Sprintf("소명 기한이 지난 내역 %d건", a.over))
			}
			return s.notify(ctx, tx, repository.NotificationInsert{UserID: h, Kind: "card_reminder", Title: "카드 소명 기한 안내",
				Body: fmt.Sprintf("%s(합계 %s)이 있습니다. 내 카드 내역에서 소명하세요.", strings.Join(parts, ", "), moneyLabel(a.amount)), Link: "/finance/cards/mine", RefType: "card_reminder", RefID: h, Now: now})
		})
		if err != nil {
			return err
		}
	}
	for h, a := range headOf {
		head, name := s.headOfHolder(ctx, h)
		if head == 0 {
			continue
		}
		h, a, head, name := h, a, head, name
		err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
			if first, err := s.Repo.MarkReminder(ctx, tx, h, today, "head"); err != nil || !first {
				return err
			}
			return s.notify(ctx, tx, repository.NotificationInsert{UserID: head, Kind: "card_overdue_head", Title: "카드 소명 지연 안내",
				Body: fmt.Sprintf("%s님의 소명 기한이 지난 카드 내역 %d건(합계 %s)이 있습니다.", name, a.over, moneyLabel(a.amount)), Link: "/finance/cards/confirm", RefType: "card_reminder", RefID: h, Now: now})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// headOfHolder 는 소지자의 조직장(본인이 조직장이면 상위 조직장)과 소지자 이름이다.
func (s *CardService) headOfHolder(ctx context.Context, holder int64) (int64, string) {
	name, _, _, _ := s.Repo.UserInfo(ctx, s.DB, holder)
	if s.Org == nil {
		return 0, name
	}
	day := contract.DayOf(s.now())
	per, err := s.Org.Person(ctx, holder, day)
	if err != nil {
		return 0, name
	}
	units := []int64{per.Primary.Unit.ID}
	anc, err := s.Org.Ancestors(ctx, per.Primary.Unit.ID, day)
	if err == nil {
		for _, u := range anc {
			units = append(units, u.ID)
		}
	}
	for _, u := range units {
		h, err := s.Org.Head(ctx, u, day)
		if err == nil && h.UserID != 0 && h.UserID != holder {
			return h.UserID, name
		}
	}
	return 0, name
}

// Run 은 서버가 켜져 있는 동안 10분마다 Tick 을 부른다(하루 한 번 표시가 중복을 막는다).
func (s *CardService) Run(ctx context.Context) {
	tick := func() {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("card: 소명 알림 생성 실패: %v", err)
		}
	}
	tick()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 매입 세금계산서 3자 대조(B43 2단계, 명세 6.2). 수신 세금계산서 ↔ 발주 ↔ 입고(와 있으면 증빙 판정·매입 전표)를 코드가 결정적으로 대조해 제안하고, 확정은 사람이 한다(D8).
//   - 거래처는 사업자번호(숫자만)로 찾고, 입고 후보는 그 거래처 발주의 입고 중 확정된 연결이 없는 것(입고일이 작성일 달의 전달 1일 ~ 작성일 + 31일)이다.
//   - 공급가액은 정확히 일치해야 한다(D9). 입고 합이 공급가액과 같은 조합을 후보 12건 이하에서 전수로 찾는다(작은 집합 우선, 같으면 입고일이 오래된 것 우선).
//   - 세액·귀속 월·거래처 이름 차이는 경고일 뿐 결과를 바꾸지 않는다. 수정세금계산서(음수·수정 사유)는 입고와 대조하지 않는다.
//   - LLM·네트워크를 쓰지 않는다. 기각한 후보 조합은 다시 제안하지 않는다.

const maxMatchCandidates = 12

var _ domain.TaxMatchService = (*TaxInvoiceService)(nil)

// pick 은 후보 중 고른 부분집합(후보 목록의 위치)이다.
type pick struct {
	idx  []int
	sum  int64
	size int
}

// subsetSearch 는 후보 공급가액 합이 target 과 정확히 같은 부분집합을 모두 찾고(exact), 없으면 차이가 가장 작은 부분집합(nearest)을 돌려준다.
// 정렬은 결정적이다: 크기 오름차순, 같으면 위치(= 입고일이 오래된 순) 사전순.
func subsetSearch(vals []int64, target int64) (exact []pick, nearest pick, ok bool) {
	n := len(vals)
	if n == 0 {
		return nil, pick{}, false
	}
	sums := make([]int64, 1<<n)
	for mask := 1; mask < 1<<n; mask++ {
		low := bits.TrailingZeros(uint(mask))
		sums[mask] = sums[mask&(mask-1)] + vals[low]
	}
	mk := func(mask int) pick {
		var idx []int
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				idx = append(idx, i)
			}
		}
		return pick{idx: idx, sum: sums[mask], size: len(idx)}
	}
	less := func(a, b pick) bool {
		if a.size != b.size {
			return a.size < b.size
		}
		for i := range a.idx {
			if a.idx[i] != b.idx[i] {
				return a.idx[i] < b.idx[i]
			}
		}
		return false
	}
	absd := func(x int64) int64 {
		if x < 0 {
			return -x
		}
		return x
	}
	var best pick
	bestSet := false
	for mask := 1; mask < 1<<n; mask++ {
		p := mk(mask)
		if p.sum == target {
			exact = append(exact, p)
		}
		if !bestSet || absd(p.sum-target) < absd(best.sum-target) || (absd(p.sum-target) == absd(best.sum-target) && less(p, best)) {
			best, bestSet = p, true
		}
	}
	sort.Slice(exact, func(i, j int) bool { return less(exact[i], exact[j]) })
	return exact, best, bestSet
}

func receiptIDs(rs []domain.TaxMatchReceipt) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ReceiptID
	}
	return out
}

// normName 은 상호 비교용 글자다(공백·(주)·주식회사 제거).
func normName(s string) string {
	s = strings.NewReplacer(" ", "", "(주)", "", "㈜", "", "주식회사", "", "(유)", "", "유한회사", "").Replace(strings.TrimSpace(s))
	return strings.ToLower(s)
}

type proposal struct {
	result string
	recs   []domain.TaxMatchReceipt
	diff   domain.TaxMatchDiff
	warns  []string
}

// evaluate 는 세금계산서 한 건의 대조 제안을 만든다. skip 이 참이면 기각한 조합뿐이라 제안하지 않는다.
func (s *TaxInvoiceService) evaluate(ctx context.Context, tx contract.Tx, inv domain.TaxInvoice, dismissed map[string]bool) (prop proposal, skip bool, err error) {
	rate, err := s.vatRateOn(ctx, tx, inv.IssueDate)
	if err != nil {
		return prop, false, err
	}
	prop.diff.InvoiceSupply, prop.diff.TaxActual = inv.SupplyTotal, inv.TaxTotal
	if inv.TaxType == domain.TaxTaxable {
		prop.diff.TaxExpected = domain.VATFromSupply(inv.SupplyTotal, rate)
	}
	if prop.diff.TaxExpected != inv.TaxTotal {
		prop.warns = append(prop.warns, domain.MatchWarnTaxDiff)
	}
	vendors, err := s.Match.VendorsByBiz(ctx, tx, inv.Supplier.BizNo)
	if err != nil {
		return prop, false, err
	}
	if len(vendors) == 0 {
		prop.result = domain.MatchResultVendorUnknown
		return prop, false, nil
	}
	named := false
	ids := make([]int64, 0, len(vendors))
	for _, v := range vendors {
		ids = append(ids, v.ID)
		if inv.Supplier.Name == "" || normName(v.Name) == normName(inv.Supplier.Name) || strings.Contains(normName(v.Name), normName(inv.Supplier.Name)) || strings.Contains(normName(inv.Supplier.Name), normName(v.Name)) {
			named = true
		}
	}
	if !named {
		prop.warns = append(prop.warns, domain.MatchWarnVendorName)
	}
	issue, err := time.Parse("2006-01-02", inv.IssueDate)
	if err != nil {
		return prop, false, validation("세금계산서 작성일이 올바르지 않습니다.")
	}
	from := time.Date(issue.Year(), issue.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	to := issue.AddDate(0, 0, 31).Format("2006-01-02")
	cands, err := s.Match.Candidates(ctx, tx, ids, from, to)
	if err != nil {
		return prop, false, err
	}
	if len(cands) == 0 {
		prop.result = domain.MatchResultNoReceipt
		return prop, false, nil
	}
	if len(cands) > maxMatchCandidates {
		cands = cands[:maxMatchCandidates] // 입고일이 오래된 순으로 정렬되어 있다
		prop.warns = append(prop.warns, domain.MatchWarnMany)
	}
	vals := make([]int64, len(cands))
	for i, c := range cands {
		vals[i] = c.Supply
	}
	exact, nearest, _ := subsetSearch(vals, inv.SupplyTotal)
	pickRecs := func(p pick) []domain.TaxMatchReceipt {
		out := make([]domain.TaxMatchReceipt, 0, len(p.idx))
		for _, i := range p.idx {
			out = append(out, cands[i])
		}
		return out
	}
	// 기각한 조합은 제외한다.
	var live []pick
	for _, p := range exact {
		if !dismissed[repository.SortedKey(receiptIDs(pickRecs(p)))] {
			live = append(live, p)
		}
	}
	switch {
	case len(exact) > 0 && len(live) == 0:
		return prop, true, nil
	case len(live) == 1:
		prop.result, prop.recs = domain.MatchResultMatch, pickRecs(live[0])
	case len(live) > 1:
		prop.result, prop.recs, prop.diff.Alternatives = domain.MatchResultAmbiguous, pickRecs(live[0]), len(live)-1
	default:
		prop.result, prop.recs = domain.MatchResultAmountDiff, pickRecs(nearest)
	}
	for _, r := range prop.recs {
		prop.diff.CandidatesSum += r.Supply
	}
	prop.diff.Diff, prop.diff.ReceiptCount = prop.diff.CandidatesSum-inv.SupplyTotal, len(prop.recs)
	months := map[string]bool{}
	for _, r := range prop.recs {
		months[r.ReceivedAt[:7]] = true
	}
	for m := range months {
		prop.diff.Months = append(prop.diff.Months, m)
	}
	sort.Strings(prop.diff.Months)
	if len(months) != 1 || !months[inv.IssueDate[:7]] {
		prop.warns = append(prop.warns, domain.MatchWarnDateGap)
	}
	if prop.diff.VerdictIDs, err = s.Match.VerdictIDs(ctx, tx, receiptIDs(prop.recs)); err != nil {
		return prop, false, err
	}
	return prop, false, nil
}

// proposeAll 은 대조 대상 세금계산서 전부의 제안을 만들거나 다시 계산한다(멱등). 호출한 트랜잭션 안에서만 쓴다.
func (s *TaxInvoiceService) proposeAll(ctx context.Context, tx contract.Tx) (domain.TaxProposeSummary, error) {
	var sum domain.TaxProposeSummary
	invs, err := s.Match.PendingInvoices(ctx, tx)
	if err != nil {
		return sum, err
	}
	for _, inv := range invs {
		sum.Examined++
		dismissed, err := s.Match.DismissedKeys(ctx, tx, inv.ID)
		if err != nil {
			return sum, err
		}
		prop, skip, err := s.evaluate(ctx, tx, inv, dismissed)
		if err != nil {
			return sum, err
		}
		pid, oldResult, oldKey, oldDiff, oldWarn, err := s.Match.ProposedFor(ctx, tx, inv.ID)
		if err != nil {
			return sum, err
		}
		if skip {
			sum.Skipped++
			continue
		}
		key := repository.SortedKey(receiptIDs(prop.recs))
		if pid != 0 {
			if oldResult == prop.result && oldKey == key && jsonEq(oldDiff, prop.diff) && jsonEq(oldWarn, prop.warns) {
				continue
			}
			if err := s.Match.UpdateProposed(ctx, tx, pid, prop.result, key, prop.diff, prop.warns, prop.recs); err != nil {
				return sum, err
			}
			sum.Updated++
			continue
		}
		if _, err := s.Match.Insert(ctx, tx, inv.ID, prop.result, key, prop.diff, prop.warns, prop.recs, s.now()); err != nil {
			return sum, err
		}
		sum.Created++
	}
	return sum, nil
}

// Propose 는 대조 제안을 지금 다시 만든다(taxinvoice:write). 새 수신 건이 생기면 동기화·수기 등록 뒤에도 자동으로 돈다.
func (s *TaxInvoiceService) Propose(ctx context.Context) (domain.TaxProposeSummary, error) {
	const action = "taxinvoice.match_propose"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxProposeSummary{}, err
	}
	var sum domain.TaxProposeSummary
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, 0, func(tx contract.Tx) error {
		var err error
		if sum, err = s.proposeAll(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, 0, audit.DiffItem{Key: "examined", New: fmt.Sprint(sum.Examined)}, audit.DiffItem{Key: "created", New: fmt.Sprint(sum.Created)})
	})
	return sum, err
}

// outMatch 는 응답용 보강(확정 가능 여부·일괄 확정 대상)을 붙인다.
func outMatch(m domain.TaxMatch) domain.TaxMatch {
	m.Confirmable = m.Status == domain.MatchProposed && (m.Result == domain.MatchResultMatch || m.Result == domain.MatchResultAmbiguous) && len(m.Receipts) > 0
	m.Bulk = m.Confirmable && m.Result == domain.MatchResultMatch && len(m.Warnings) == 0
	if m.Warnings == nil {
		m.Warnings = []string{}
	}
	return m
}

// ListMatches 는 대조 목록이다.
func (s *TaxInvoiceService) ListMatches(ctx context.Context, f domain.TaxMatchFilter) (domain.TaxMatchPage, error) {
	if _, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead); err != nil {
		return domain.TaxMatchPage{}, err
	}
	rows, total, err := s.Match.List(ctx, s.DB, f)
	if err != nil {
		return domain.TaxMatchPage{}, err
	}
	for i := range rows {
		rows[i] = outMatch(rows[i])
		s.fillJournalNote(ctx, s.DB, &rows[i])
	}
	return domain.TaxMatchPage{Rows: rows, Total: total}, nil
}

// GetMatch 는 대조 한 건이다.
func (s *TaxInvoiceService) GetMatch(ctx context.Context, id int64) (domain.TaxMatch, error) {
	if _, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead); err != nil {
		return domain.TaxMatch{}, err
	}
	m, err := s.Match.Get(ctx, s.DB, id)
	m = outMatch(m)
	if err == nil {
		s.fillJournalNote(ctx, s.DB, &m)
	}
	return m, err
}

func (s *TaxInvoiceService) confirmTx(ctx context.Context, tx contract.Tx, p contract.Principal, id int64, comment string) (domain.TaxMatch, error) {
	m, err := s.Match.Get(ctx, tx, id)
	if err != nil {
		return m, err
	}
	m = outMatch(m)
	if m.Status != domain.MatchProposed {
		return m, fmt.Errorf("%w: 제안 대기 상태의 대조만 확정할 수 있습니다", contract.ErrInvalidState)
	}
	if !m.Confirmable {
		return m, fmt.Errorf("%w: 공급가액이 입고 합과 정확히 맞는 대조만 확정할 수 있습니다(%s). 거래처·발주·입고를 확인하세요", contract.ErrInvalidState, domain.MatchResultLabels[m.Result])
	}
	// 확정 시점에 입고 합이 지금도 공급가액과 같은지 다시 본다.
	var sum int64
	journals := map[int64]int64{}
	for _, r := range m.Receipts {
		f, err := s.Ledger.ReceiptFacts(ctx, tx, r.ReceiptID)
		if err != nil {
			return m, err
		}
		sum += f.Supply
		if jid, _, found, err := s.Ledger.PurchaseJournalForReceipt(ctx, tx, r.ReceiptID); err != nil {
			return m, err
		} else if found {
			journals[r.ReceiptID] = jid
		}
	}
	if sum != m.Supply {
		return m, fmt.Errorf("%w: 입고 금액이 달라졌습니다. 대조를 다시 만드세요", contract.ErrInvalidState)
	}
	// 설정 taxinvoice.auto_journal 이 켜져 있으면 전표가 없는 입고마다 임시 매입 전표를 만든다(D7). 못 만든 사유는 감사 detail 에만 남긴다.
	created, skipped := 0, ""
	if s.autoJournalOn(ctx, tx) {
		for _, r := range m.Receipts {
			if _, has := journals[r.ReceiptID]; has {
				continue
			}
			jid, why, err := s.autoJournalForReceipt(ctx, tx, p, m.InvoiceNo, r.ReceiptID)
			if err != nil {
				return m, err
			}
			if jid != 0 {
				journals[r.ReceiptID] = jid
				created++
			} else if skipped == "" {
				skipped = why
			}
		}
	}
	if err := s.Match.SetConfirmed(ctx, tx, id, true, journals); err != nil {
		return m, err
	}
	if err := s.Match.Decide(ctx, tx, id, domain.MatchProposed, domain.MatchConfirmed, p.UserID, comment, s.now()); err != nil {
		return m, err
	}
	if created > 0 {
		if err := s.record(ctx, tx, p, "taxinvoice.match_journal", audit.TargetTaxInvoice, m.InvoiceID, audit.DiffItem{Key: "journals", New: fmt.Sprint(created)}); err != nil {
			return m, err
		}
	}
	if skipped != "" {
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "taxinvoice.match_journal", TargetType: audit.TargetTaxInvoice, TargetID: fmt.Sprint(m.InvoiceID), Result: audit.ResultError,
			Detail: "자동 매입 전표를 만들지 못함: " + skipped}); err != nil {
			return m, err
		}
	}
	if err := s.record(ctx, tx, p, "taxinvoice.match_confirm", audit.TargetTaxInvoice, m.InvoiceID,
		audit.DiffItem{Key: "receipts", New: fmt.Sprint(len(m.Receipts))}, audit.DiffItem{Key: "supply_total", New: labelWon(m.Supply)}, audit.DiffItem{Key: "result", New: domain.MatchResultLabels[m.Result]}); err != nil {
		return m, err
	}
	return m, nil
}

// Confirm 은 제안을 확정한다(taxinvoice:write). 입고들이 이 세금계산서에 연결되고 B32 부가세 집계가 이 연결을 "세금계산서 수취"로 본다.
func (s *TaxInvoiceService) Confirm(ctx context.Context, id int64, comment string) (domain.TaxMatch, error) {
	const action = "taxinvoice.match_confirm"
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxMatch{}, err
	}
	if utf8.RuneCountInString(comment) > 200 {
		return domain.TaxMatch{}, validation("의견은 200자 이내로 입력하세요.")
	}
	var out domain.TaxMatch
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		var err error
		if _, err = s.confirmTx(ctx, tx, p, id, strings.TrimSpace(comment)); err != nil {
			return err
		}
		out, err = s.Match.Get(ctx, tx, id)
		return err
	})
	out = outMatch(out)
	if err == nil {
		s.fillJournalNote(ctx, s.DB, &out)
	}
	return out, err
}

// ConfirmBulk 는 일치·경고 없음인 제안만 한꺼번에 확정한다. 그 밖의 건은 건너뛴다(자동 확정은 없고 사람이 이 단추를 누를 때만 한다).
func (s *TaxInvoiceService) ConfirmBulk(ctx context.Context, ids []int64) (domain.TaxBulkResult, error) {
	const action = "taxinvoice.match_confirm"
	var res domain.TaxBulkResult
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return res, err
	}
	if len(ids) == 0 || len(ids) > 200 {
		return res, validation("한 번에 1~200건까지 확정할 수 있습니다.")
	}
	for _, id := range ids {
		err := s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
			m, err := s.Match.Get(ctx, tx, id)
			if err != nil {
				return err
			}
			if !outMatch(m).Bulk {
				return errBulkSkip
			}
			_, err = s.confirmTx(ctx, tx, p, id, "")
			return err
		})
		switch {
		case err == nil:
			res.Confirmed++
		case err == errBulkSkip || isInvalidState(err) || isNotFound(err):
			res.Skipped++
		default:
			return res, err
		}
	}
	return res, nil
}

// Dismiss 는 제안을 기각한다(사유 필수). 같은 후보 조합은 다시 제안하지 않는다.
func (s *TaxInvoiceService) Dismiss(ctx context.Context, id int64, reason string) (domain.TaxMatch, error) {
	return s.decide(ctx, id, reason, "taxinvoice.match_dismiss", domain.MatchProposed, domain.MatchDismissed)
}

// Unlink 는 확정된 연결을 해제한다(사유 필수). 해제 뒤 다음 제안 때 새 제안이 만들어질 수 있다.
func (s *TaxInvoiceService) Unlink(ctx context.Context, id int64, reason string) (domain.TaxMatch, error) {
	return s.decide(ctx, id, reason, "taxinvoice.match_unlink", domain.MatchConfirmed, domain.MatchUnlinked)
}

func (s *TaxInvoiceService) decide(ctx context.Context, id int64, reason, action, from, to string) (domain.TaxMatch, error) {
	p, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, action, audit.TargetTaxInvoice)
	if err != nil {
		return domain.TaxMatch{}, err
	}
	r, err := reasonText(reason)
	if err != nil {
		return domain.TaxMatch{}, err
	}
	var out domain.TaxMatch
	err = s.write(ctx, p, action, audit.TargetTaxInvoice, id, func(tx contract.Tx) error {
		m, err := s.Match.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != from {
			return fmt.Errorf("%w: %s 상태의 대조는 이 작업을 할 수 없습니다", contract.ErrInvalidState, domain.MatchStatusLabels[m.Status])
		}
		if to == domain.MatchUnlinked {
			if err := s.Match.SetConfirmed(ctx, tx, id, false, nil); err != nil {
				return err
			}
		}
		if err := s.Match.Decide(ctx, tx, id, from, to, p.UserID, r, s.now()); err != nil {
			return err
		}
		if out, err = s.Match.Get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetTaxInvoice, m.InvoiceID, audit.DiffItem{Key: "receipts", New: fmt.Sprint(len(m.Receipts))}, audit.DiffItem{Key: "reason_len", New: fmt.Sprint(utf8.RuneCountInString(r))})
	})
	return outMatch(out), err
}

var errBulkSkip = errors.New("일괄 확정 대상이 아닙니다")

func isInvalidState(err error) bool { return errors.Is(err, contract.ErrInvalidState) }
func isNotFound(err error) bool     { return errors.Is(err, contract.ErrNotFound) }

// jsonEq 는 저장된 JSON 글자와 값을 JSON 으로 바꾼 것이 같은지다(다시 계산해도 같으면 제안을 고치지 않는다).
func jsonEq(stored string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var a, c any
	if json.Unmarshal([]byte(stored), &a) != nil || json.Unmarshal(b, &c) != nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(c)
	return string(x) == string(y)
}

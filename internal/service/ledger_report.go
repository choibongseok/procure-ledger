package service

import (
	"context"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 계정별 원장·시산표·매입 채무(B31 3.5·3.6). 조회는 ledger:read 이고 승인된 전표만 센다. 금액 열은 ledger:read 가 있어야 응답에 들어간다(권한이 없으면 서비스가 거부한다).

// LedgerReportService 는 domain.LedgerReportService 의 구현이다.
type LedgerReportService struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.LedgerReportService = (*LedgerReportService)(nil)

func NewLedgerReportService(b Base) *LedgerReportService { return &LedgerReportService{Base: b} }

// signed 는 차변 − 대변 값을 계정의 정상 방향 부호로 맞춘다(자산·비용은 차변이 +, 부채·자본·수익은 대변이 +).
func signed(kind string, debitMinusCredit int64) int64 {
	if kind == domain.AccountAsset || kind == domain.AccountExpense {
		return debitMinusCredit
	}
	return -debitMinusCredit
}

// isProfitLoss 는 수익·비용 계정인지다(회계연도마다 0 에서 시작해 보이는 계정).
func isProfitLoss(kind string) bool {
	return kind == domain.AccountRevenue || kind == domain.AccountExpense
}

func (b Base) kstDay() string { return b.now().In(contract.KST).Format("2006-01-02") }

// period 는 기간을 정한다: 비면 이번 달 1일~오늘, 종료일은 시작일 이후여야 한다.
func (b Base) period(from, to string) (string, string, error) {
	today := b.kstDay()
	if from == "" {
		from = today[:8] + "01"
	}
	if to == "" {
		to = today
	}
	if !validDate(from) || !validDate(to) {
		return "", "", validation("날짜는 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if to < from {
		return "", "", validation("종료일은 시작일 이후여야 합니다.")
	}
	return from, to, nil
}

// AccountLedger 는 계정별 원장이다. 맨 위 이월 잔액은 기간 시작 전 누계다.
func (s *LedgerReportService) AccountLedger(ctx context.Context, f domain.AccountLedgerFilter) (domain.AccountLedger, error) {
	var out domain.AccountLedger
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return out, err
	}
	if f.Account == "" {
		return out, validation("계정과목을 고르세요.")
	}
	from, to, err := s.period(f.From, f.To)
	if err != nil {
		return out, err
	}
	acc, found, err := s.Repo.AccountByCode(ctx, s.DB, f.Account)
	if err != nil {
		return out, err
	}
	if !found {
		return out, validation("알 수 없는 계정과목입니다(%s).", f.Account)
	}
	open, err := s.Repo.LedgerOpening(ctx, s.DB, acc.ID, from, f.VendorID)
	if err != nil {
		return out, err
	}
	if isProfitLoss(acc.Kind) { // 수익·비용은 회계연도 단위로 읽는다: 회계연도 시작 전 합계는 이월에서 뺀다(데이터는 그대로, 결산 전표 없음)
		start, err := s.fiscalStartMonth(ctx, s.DB)
		if err != nil {
			return out, err
		}
		fyFrom, _, _ := fiscalRange(from, start)
		before, err := s.Repo.LedgerOpening(ctx, s.DB, acc.ID, fyFrom, f.VendorID)
		if err != nil {
			return out, err
		}
		open -= before
	}
	lines, err := s.Repo.LedgerLines(ctx, s.DB, acc.ID, from, to, f.VendorID)
	if err != nil {
		return out, err
	}
	excluded, err := s.Repo.PendingJournalCount(ctx, s.DB, acc.ID, from, to)
	if err != nil {
		return out, err
	}
	out = domain.AccountLedger{Account: acc, From: from, To: to, Opening: signed(acc.Kind, open), ExcludedCount: excluded}
	running := open
	for _, l := range lines {
		r := domain.AccountLedgerRow{PostingDate: l.PostingDate, JournalNo: l.No, Memo: l.Memo, Counter: l.Counter}
		if l.Side == "D" {
			r.Debit = l.Amount
			running += l.Amount
			out.TotalDebit += l.Amount
		} else {
			r.Credit = l.Amount
			running -= l.Amount
			out.TotalCredit += l.Amount
		}
		r.Balance = signed(acc.Kind, running)
		out.Rows = append(out.Rows, r)
	}
	out.Closing = signed(acc.Kind, running)
	return out, nil
}

// TrialBalance 는 시산표다. 거래가 없는 계정은 기본으로 숨긴다(includeZero 로 포함).
// 차변·대변 합계가 다르면(정상에서는 불가능) Balanced 가 거짓이고 감사(ledger.imbalance)에 남긴다.
func (s *LedgerReportService) TrialBalance(ctx context.Context, from, to string, includeZero bool) (domain.TrialBalance, error) {
	var out domain.TrialBalance
	p, err := contract.Authorize(ctx, contract.PermLedgerRead)
	if err != nil {
		return out, err
	}
	start, err := s.fiscalStartMonth(ctx, s.DB)
	if err != nil {
		return out, err
	}
	if from == "" { // 시산표는 회계연도 단위로 보는 표라 기본 기간이 이번 회계연도 시작일~오늘이다
		from, _, _ = fiscalRange(s.kstDay(), start)
	}
	from, to, err = s.period(from, to)
	if err != nil {
		return out, err
	}
	raws, err := s.Repo.TrialRaws(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	// 수익·비용의 이월은 회계연도 시작 전 합계를 뺀 값이다(자산·부채·자본은 전체 누계). 차대 검증은 뺀 값이 아니라 원래 누계로 한다.
	fyFrom, _, _ := fiscalRange(from, start)
	prior, err := s.Repo.TrialRaws(ctx, s.DB, fyFrom, fyFrom)
	if err != nil {
		return out, err
	}
	priorOpen := map[string]int64{}
	for _, r := range prior {
		priorOpen[r.Code] = r.Opening
	}
	out = domain.TrialBalance{From: from, To: to}
	var openSum int64
	for _, r := range raws {
		openSum += r.Opening
		out.TotalDebit += r.Debit
		out.TotalCredit += r.Credit
		if isProfitLoss(r.Kind) {
			r.Opening -= priorOpen[r.Code]
		}
		closing := r.Opening + r.Debit - r.Credit
		if !includeZero && r.Opening == 0 && r.Debit == 0 && r.Credit == 0 && closing == 0 {
			continue
		}
		out.Rows = append(out.Rows, domain.TrialRow{Code: r.Code, Name: r.Name, Kind: r.Kind, Opening: signed(r.Kind, r.Opening), Debit: r.Debit, Credit: r.Credit, Closing: signed(r.Kind, closing)})
	}
	out.Balanced = out.TotalDebit == out.TotalCredit && openSum == 0
	if out.ExcludedCount, err = s.Repo.PendingJournalCount(ctx, s.DB, 0, from, to); err != nil {
		return out, err
	}
	if !out.Balanced {
		// 방어: 정상 경로에서는 일어나지 않는다. 기록 실패가 조회를 막지 않게 오류는 삼킨다.
		_ = s.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ledger.imbalance", TargetType: audit.TargetJournal, TargetID: "trial-balance", Result: audit.ResultSuccess, Detail: "시산표 차대 불일치",
				Diff: []audit.DiffItem{{Key: "debit_total", New: formatWon(out.TotalDebit)}, {Key: "credit_total", New: formatWon(out.TotalCredit)}}})
		})
	}
	return out, nil
}

// ---- 매입 채무 ----

// PayableService 는 domain.PayableService 의 구현이다.
type PayableService struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.PayableService = (*PayableService)(nil)

func NewPayableService(b Base) *PayableService { return &PayableService{Base: b} }

func (s *PayableService) asOfDay(asOf string) (string, error) {
	if asOf == "" {
		return s.kstDay(), nil
	}
	if !validDate(asOf) {
		return "", validation("기준일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	return asOf, nil
}

// ByVendor 는 거래처별 채무와 계정별 원장 외상매입금 잔액 검산이다.
func (s *PayableService) ByVendor(ctx context.Context, asOf string) (domain.PayableReport, error) {
	var out domain.PayableReport
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return out, err
	}
	day, err := s.asOfDay(asOf)
	if err != nil {
		return out, err
	}
	aggs, err := s.Repo.PayableByVendor(ctx, s.DB, day)
	if err != nil {
		return out, err
	}
	out.AsOf = day
	names := map[int64]string{}
	for _, a := range aggs {
		if a.LinkedEntity > 0 {
			if names, err = s.Repo.EntityNames(ctx, s.DB); err != nil {
				return out, err
			}
			break
		}
	}
	for _, a := range aggs {
		days, err := s.Repo.VendorPaymentDays(ctx, s.DB, a.VendorID)
		if err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, domain.PayableVendorRow{VendorID: a.VendorID, VendorName: a.VendorName, Count: a.Count, Outstanding: a.Outstanding, Reserved: a.Reserved,
			EarliestDue: a.EarliestDue, OverdueAmount: a.OverdueAmount, PaymentDays: days, Intercompany: intercompanyName(ctx, names, a.LinkedEntity)})
		out.Total += a.Outstanding
	}
	bal, err := s.Repo.AccountBalanceAsOf(ctx, s.DB, "2110", day)
	if err != nil {
		return out, err
	}
	out.LedgerBalance = -bal // 외상매입금은 대변이 정상 방향이다
	out.Matches = out.Total == out.LedgerBalance
	return out, nil
}

// Items 는 거래처의 미지급 채무 항목이다(만기 순, 경과일은 기준일 − 만기).
func (s *PayableService) Items(ctx context.Context, vendorID int64, asOf string) ([]domain.PayableItem, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return nil, err
	}
	day, err := s.asOfDay(asOf)
	if err != nil {
		return nil, err
	}
	if ok, err := s.Repo.VendorExists(ctx, s.DB, vendorID); err != nil {
		return nil, err
	} else if !ok {
		return nil, contract.ErrNotFound
	}
	items, err := s.Repo.PayableItems(ctx, s.DB, vendorID)
	if err != nil {
		return nil, err
	}
	base, _ := time.Parse("2006-01-02", day)
	for i := range items {
		if due, err := time.Parse("2006-01-02", items[i].DueDate); err == nil && base.After(due) {
			items[i].OverdueDays = int(base.Sub(due).Hours() / 24)
		}
	}
	return items, nil
}

// SetPaymentDays 는 거래처의 결제 조건(일)을 바꾼다. 이미 만들어진 채무 항목의 만기는 바뀌지 않는다(새 매입 전표부터 적용).
func (s *PayableService) SetPaymentDays(ctx context.Context, vendorID int64, days int) (int, error) {
	p, err := s.authorize(ctx, contract.PermAccountManage, "vendor.payment_days", audit.TargetVendor)
	if err != nil {
		return 0, err
	}
	if days < 0 || days > 365 { // 0일은 거래일 당일 만기다(DB 제약 0~365 와 같다)
		return 0, validation("결제 조건은 0일 이상 365일 이하로 입력하세요.")
	}
	err = s.write(ctx, p, "vendor.payment_days", audit.TargetVendor, vendorID, func(tx contract.Tx) error {
		old, err := s.Repo.SetVendorPaymentDays(ctx, tx, vendorID, days)
		if err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "vendor.payment_days", TargetType: audit.TargetVendor, TargetID: fmt.Sprint(vendorID), Result: audit.ResultSuccess, Detail: "거래처 결제 조건 수정",
			Diff: []audit.DiffItem{{Key: "payment_days", Old: fmt.Sprint(old), New: fmt.Sprint(days)}}})
	})
	return days, err
}

// intercompanyName 은 거래처의 연결 법인이 현재 법인과 다를 때 그 이름이다(자기 자신으로 연결된 거래처는 배지가 없다).
func intercompanyName(ctx context.Context, names map[int64]string, linked int64) string {
	if linked <= 0 || linked == contract.EntityFrom(ctx) {
		return ""
	}
	return names[linked]
}

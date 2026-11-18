package service

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 자금 계좌·자금 일보·입금 기록(B32 ②, 명세 3.2). 입출금 행은 추가 전용이고 정정은 반대 행이다. 계좌번호는 마스킹 값만 응답에 싣고 원문은 RevealAccount·RevealPayee 로만 나간다(감사).

// FundService 는 domain.FundService 의 구현이다.
type FundService struct {
	Base
	Repo   repository.SQLPayments
	Ledger repository.SQLLedger
}

var _ domain.FundService = (*FundService)(nil)

func NewFundService(b Base) *FundService { return &FundService{Base: b} }

var accountNoRe = regexp.MustCompile(`^[0-9][0-9-]{3,39}$`)

func (s *FundService) toAccount(ctx context.Context, q contract.Tx, r repository.BankRow, today string) (domain.BankAccount, error) {
	a := r.BankAccount
	a.AccountNo = maskAccount(r.Enc)
	bal, err := s.Repo.BankBalance(ctx, q, r.ID, today)
	a.Balance = bal
	return a, err
}

// Accounts 는 회사 계좌 목록이다(마스킹, 오늘 기말 잔액 포함).
func (s *FundService) Accounts(ctx context.Context, includeInactive bool) ([]domain.BankAccount, error) {
	if _, err := contract.Authorize(ctx, contract.PermFundRead); err != nil {
		return nil, err
	}
	rows, err := s.Repo.Banks(ctx, s.DB, includeInactive)
	if err != nil {
		return nil, err
	}
	out := make([]domain.BankAccount, 0, len(rows))
	today := s.kstDay()
	for _, r := range rows {
		a, err := s.toAccount(ctx, s.DB, r, today)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// SaveAccount 는 계좌를 추가(id 0)하거나 별칭·은행·계좌번호를 고친다. 기초 잔액·기준일·연결 계정은 새 계좌에서만 정하고 이후 바꿀 수 없다.
func (s *FundService) SaveAccount(ctx context.Context, id int64, in domain.BankAccountInput) (domain.BankAccount, error) {
	action := "bank_account.create"
	if id != 0 {
		action = "bank_account.update"
	}
	p, err := s.authorize(ctx, contract.PermFundManage, action, audit.TargetBankAccount)
	if err != nil {
		return domain.BankAccount{}, err
	}
	in.Alias, in.BankName, in.AccountNo = strings.TrimSpace(in.Alias), strings.TrimSpace(in.BankName), strings.TrimSpace(in.AccountNo)
	if c := utf8.RuneCountInString(in.Alias); c < 1 || c > 40 {
		return domain.BankAccount{}, validation("계좌 별칭은 1~40자로 입력하세요.")
	}
	if c := utf8.RuneCountInString(in.BankName); c < 1 || c > 40 {
		return domain.BankAccount{}, validation("은행 이름은 1~40자로 입력하세요.")
	}
	if in.AccountNo != "" && !accountNoRe.MatchString(in.AccountNo) {
		return domain.BankAccount{}, validation("계좌번호는 숫자와 하이픈(-)으로 4자 이상 입력하세요.")
	}
	var out domain.BankAccount
	err = s.write(ctx, p, action, audit.TargetBankAccount, id, func(tx contract.Tx) error {
		diff := []audit.DiffItem{{Key: "alias", New: in.Alias}, {Key: "bank_name", New: in.BankName}}
		if id == 0 {
			if in.AccountNo == "" {
				return validation("계좌번호를 입력하세요.")
			}
			code := in.LedgerCode
			if code == "" {
				code = "1020"
			}
			if code != "1020" && code != "1010" {
				return validation("연결 계정은 보통예금(1020)이나 현금(1010)만 고를 수 있습니다.")
			}
			acc, found, err := s.Ledger.AccountByCode(ctx, tx, code)
			if err != nil {
				return err
			}
			if !found || !acc.Active {
				return validation("연결 계정을 쓸 수 없습니다(%s).", code)
			}
			if !validDate(in.OpeningDate) {
				return validation("기초 기준일은 YYYY-MM-DD 형식으로 입력하세요.")
			}
			if in.OpeningBalance < 0 {
				return validation("기초 잔액은 0원 이상이어야 합니다.")
			}
			newID, err := s.Repo.InsertBank(ctx, tx, in, acc.ID, s.now())
			if err != nil {
				if dbutil.IsUnique(err) {
					return validation("같은 별칭의 계좌가 이미 있습니다(%s).", in.Alias)
				}
				return err
			}
			id = newID
			diff = append(diff, audit.DiffItem{Key: "opening_balance", New: formatWon(in.OpeningBalance)}, audit.DiffItem{Key: "opening_date", New: in.OpeningDate})
		} else {
			cur, err := s.Repo.Bank(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := s.Repo.UpdateBank(ctx, tx, id, in.Alias, in.BankName, in.AccountNo); err != nil {
				if dbutil.IsUnique(err) {
					return validation("같은 별칭의 계좌가 이미 있습니다(%s).", in.Alias)
				}
				return err
			}
			diff[0].Old, diff[1].Old = cur.Alias, cur.BankName
			if in.AccountNo != "" && in.AccountNo != cur.Enc {
				diff = append(diff, audit.DiffItem{Key: "account_changed", New: "계좌번호 변경됨"}) // 번호 자체는 감사에 넣지 않는다
			}
		}
		r, err := s.Repo.Bank(ctx, tx, id)
		if err != nil {
			return err
		}
		if out, err = s.toAccount(ctx, tx, r, s.kstDay()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetBankAccount, TargetID: in.Alias, Result: audit.ResultSuccess, Detail: "자금 계좌 저장", Diff: diff})
	})
	return out, err
}

// SetActive 는 계좌를 사용 중지하거나 다시 쓴다(삭제는 없다).
func (s *FundService) SetActive(ctx context.Context, id int64, active bool) (domain.BankAccount, error) {
	action := "bank_account.deactivate"
	if active {
		action = "bank_account.activate"
	}
	p, err := s.authorize(ctx, contract.PermFundManage, action, audit.TargetBankAccount)
	if err != nil {
		return domain.BankAccount{}, err
	}
	var out domain.BankAccount
	err = s.write(ctx, p, action, audit.TargetBankAccount, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Bank(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.SetBankActive(ctx, tx, id, active); err != nil {
			return err
		}
		cur.Active = active
		if out, err = s.toAccount(ctx, tx, cur, s.kstDay()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetBankAccount, TargetID: cur.Alias, Result: audit.ResultSuccess, Detail: "자금 계좌 사용 여부 변경",
			Diff: []audit.DiffItem{{Key: "active", Old: fmt.Sprint(!active), New: fmt.Sprint(active)}}})
	})
	return out, err
}

// Daily 는 자금 일보다: 일자·계좌별 기초·입금·출금·기말, 이월 잔액, 총계정원장 1020 검산.
func (s *FundService) Daily(ctx context.Context, f domain.FundDailyFilter) (domain.FundDaily, error) {
	var out domain.FundDaily
	if _, err := contract.Authorize(ctx, contract.PermFundRead); err != nil {
		return out, err
	}
	from, to, err := s.period(f.From, f.To)
	if err != nil {
		return out, err
	}
	ft, _ := time.Parse("2006-01-02", from)
	tt, _ := time.Parse("2006-01-02", to)
	if tt.Sub(ft) > 366*24*time.Hour {
		return out, validation("자금 일보는 한 번에 366일까지 볼 수 있습니다.")
	}
	banks, err := s.Repo.Banks(ctx, s.DB, true)
	if err != nil {
		return out, err
	}
	aliases := map[int64]string{}
	running := map[int64]int64{}
	var ids []int64
	for _, b := range banks {
		if f.BankAccountID != 0 && b.ID != f.BankAccountID {
			continue
		}
		aliases[b.ID] = b.Alias
		ids = append(ids, b.ID)
		o, err := s.Repo.OpeningBefore(ctx, s.DB, b.ID, from)
		if err != nil {
			return out, err
		}
		running[b.ID] = o
		out.Opening += o
	}
	if f.BankAccountID != 0 && len(ids) == 0 {
		return out, contract.ErrNotFound
	}
	raws, err := s.Repo.DailyRaws(ctx, s.DB, from, to, f.BankAccountID)
	if err != nil {
		return out, err
	}
	openIn := map[int64]map[string]int64{}
	for _, id := range ids {
		m, err := s.Repo.OpeningInRange(ctx, s.DB, id, from, to)
		if err != nil {
			return out, err
		}
		openIn[id] = m
	}
	sort.Slice(raws, func(i, j int) bool {
		if raws[i].Date != raws[j].Date {
			return raws[i].Date < raws[j].Date
		}
		return raws[i].BankID < raws[j].BankID
	})
	out.From, out.To = from, to
	for _, r := range raws {
		// 기초 기준일이 이 날짜 이전(같은 날 포함)인 계좌의 기초 잔액을 먼저 반영한다(기간 시작일 = 기초 기준일인 경계에서도 빠지지 않게).
		for d, v := range openIn[r.BankID] {
			if d <= r.Date {
				running[r.BankID] += v
				delete(openIn[r.BankID], d)
			}
		}
		open := running[r.BankID]
		row := domain.FundDailyRow{Date: r.Date, BankAccountID: r.BankID, BankAlias: aliases[r.BankID], Opening: open, Deposit: r.Deposit, Withdraw: r.Withdraw, PendingDeposit: r.PendingDeposit}
		row.Closing = open + r.Deposit - r.Withdraw
		running[r.BankID] = row.Closing
		out.TotalDeposit += r.Deposit
		out.TotalWithdraw += r.Withdraw
		out.PendingCount += boolToInt(r.PendingDeposit > 0)
		out.Rows = append(out.Rows, row)
	}
	// 기초 기준일이 기간 안이고 입출금이 없는 날의 기초도 기말에 반영한다.
	for _, id := range ids {
		for _, v := range openIn[id] {
			running[id] += v
		}
	}
	for _, id := range ids {
		out.Closing += running[id]
	}
	if out.Movements, err = s.Repo.MovementsInRange(ctx, s.DB, from, to, f.BankAccountID); err != nil {
		return out, err
	}
	// 검산: 모든 계좌가 1020 에 연결된 전체 보기에서 기말 합계 = 총계정원장 1020 잔액(승인된 전표).
	if f.BankAccountID == 0 {
		allLinked := true
		for _, b := range banks {
			if b.LedgerCode != "1020" {
				allLinked = false
			}
		}
		if allLinked {
			bal, err := s.Ledger.AccountBalanceAsOf(ctx, s.DB, "1020", to)
			if err != nil {
				return out, err
			}
			out.LedgerBalance = bal
			out.Matches = bal == out.Closing
		} else {
			out.Matches = true
		}
	} else {
		out.Matches = true
	}
	return out, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RecordDeposit 는 입금을 기록한다: 입금 행과 일반 입금 전표(임시, 차 계좌 연결 계정 / 대 상대 계정)를 한 트랜잭션에서 만든다. 전표가 승인되어야 잔액에 들어간다.
func (s *FundService) RecordDeposit(ctx context.Context, in domain.DepositInput) (domain.Journal, error) {
	p, err := s.authorize(ctx, contract.PermFundManage, "fund.deposit", audit.TargetBankAccount)
	if err != nil {
		return domain.Journal{}, err
	}
	if _, err := contract.Authorize(ctx, contract.PermLedgerWrite); err != nil {
		return domain.Journal{}, err // 입금 전표를 만드는 일은 재무(전표 쓰기)만 한다
	}
	if in.Amount < 1 || in.Amount > maxJournalAmount {
		return domain.Journal{}, validation("입금액은 1원 이상이어야 합니다.")
	}
	if !validDate(in.Date) {
		return domain.Journal{}, validation("입금일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if in.Date > s.kstDay() {
		return domain.Journal{}, validation("입금일은 오늘 이전이어야 합니다.")
	}
	memo := strings.TrimSpace(in.Memo)
	if c := utf8.RuneCountInString(memo); c < 1 || c > 200 {
		return domain.Journal{}, validation("적요는 1~200자로 입력하세요.")
	}
	var out domain.Journal
	err = s.write(ctx, p, "fund.deposit", audit.TargetBankAccount, in.BankAccountID, func(tx contract.Tx) error {
		bank, err := s.Repo.Bank(ctx, tx, in.BankAccountID)
		if err != nil {
			return validation("입금 계좌를 고르세요.")
		}
		if !bank.Active {
			return validation("사용 중지된 계좌입니다(%s).", bank.Alias)
		}
		code := in.CounterCode
		if code == "" {
			code = "4910"
		}
		counter, found, err := s.Ledger.AccountByCode(ctx, tx, code)
		if err != nil {
			return err
		}
		if !found || !counter.Active {
			return validation("상대 계정을 쓸 수 없습니다(%s).", code)
		}
		if counter.Code == bank.LedgerCode {
			return validation("상대 계정이 입금 계좌의 연결 계정과 같습니다.")
		}
		if in.VendorID != 0 {
			if ok, err := s.Ledger.VendorExists(ctx, tx, in.VendorID); err != nil {
				return err
			} else if !ok {
				return validation("거래처를 찾을 수 없습니다.")
			}
		}
		if counter.Code == "2110" && in.VendorID == 0 {
			return validation("외상매입금 줄은 거래처를 골라야 합니다.")
		}
		if err := s.assertMonthOpen(ctx, tx, in.Date); err != nil {
			return err
		}
		bankAcc, _, err := s.Ledger.AccountByCode(ctx, tx, bank.LedgerCode)
		if err != nil {
			return err
		}
		aj := autoJournal{Base: s.Base, Repo: s.Ledger}
		lines := []repository.LineInsert{{AccountID: bankAcc.ID, Side: "D", Amount: in.Amount}, {AccountID: counter.ID, Side: "C", Amount: in.Amount, VendorID: in.VendorID}}
		no, err := aj.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalGeneral, PostingDate: in.Date, TradeDate: in.Date, Memo: truncateRunes("입금: "+memo, 200), Total: in.Amount}, lines, domain.JournalEvidence{})
		if err != nil {
			return err
		}
		j, err := s.Ledger.Journal(ctx, tx, no)
		if err != nil {
			return err
		}
		if err := s.Repo.InsertMovement(ctx, tx, repository.MovementInsert{BankAccountID: bank.ID, Amount: in.Amount, Date: in.Date, Kind: domain.FundDeposit, Memo: truncateRunes(memo, 200),
			JournalID: j.ID, CounterAccountID: counter.ID, VendorID: in.VendorID, CreatedBy: p.UserID, Now: s.now()}); err != nil {
			return err
		}
		out = j
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "fund.deposit", TargetType: audit.TargetBankAccount, TargetID: bank.Alias, Result: audit.ResultSuccess, Detail: "입금 기록(임시 입금 전표 생성)",
			Diff: []audit.DiffItem{{Key: "paid_date", New: in.Date}, {Key: "amount", New: formatWon(in.Amount)}, {Key: "journal_no", New: no}}})
	})
	return out, err
}

// RevealAccount 는 회사 계좌번호 원문이다. 감사(account.reveal)를 같은 트랜잭션에서 남기고, 기록하지 못하면 원문을 주지 않는다.
func (s *FundService) RevealAccount(ctx context.Context, bankAccountID int64, screen string) (string, error) {
	p, err := s.authorize(ctx, contract.PermMaskBypass, "account.reveal", audit.TargetBankAccount)
	if err != nil {
		return "", err
	}
	var raw string
	err = s.write(ctx, p, "account.reveal", audit.TargetBankAccount, bankAccountID, func(tx contract.Tx) error {
		b, err := s.Repo.Bank(ctx, tx, bankAccountID)
		if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.reveal", TargetType: audit.TargetBankAccount, TargetID: b.Alias, Result: audit.ResultSuccess, Detail: "계좌번호 원문 보기",
			Diff: []audit.DiffItem{{Key: "screen", New: revealScreen(screen)}}}); err != nil {
			return err
		}
		if err := s.notePrivacy(ctx, tx, p, privacy.Access{Category: privacy.CatAccount, Screen: privacy.ScreenAccountReveal, Fields: []string{privacy.FieldAccountNo},
			Subjects: []privacy.Subject{{Type: privacy.SubjectCompany, ID: bankAccountID}}}); err != nil {
			return err
		}
		raw = b.Enc
		return nil
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// RevealPayee 는 지급 건의 받는 계좌 원문이다(지급 처리자용). 같은 감사를 남긴다.
func (s *FundService) RevealPayee(ctx context.Context, paymentNo string) (string, error) {
	p, err := s.authorize(ctx, contract.PermMaskBypass, "account.reveal", audit.TargetPayment)
	if err != nil {
		return "", err
	}
	var raw string
	err = s.write(ctx, p, "account.reveal", audit.TargetPayment, 0, func(tx contract.Tx) error {
		r, err := s.Repo.ByNo(ctx, tx, paymentNo)
		if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.reveal", TargetType: audit.TargetPayment, TargetID: r.No, Result: audit.ResultSuccess, Detail: "받는 계좌번호 원문 보기",
			Diff: []audit.DiffItem{{Key: "screen", New: "지급 처리"}}}); err != nil {
			return err
		}
		if err := s.notePrivacy(ctx, tx, p, privacy.Access{Category: privacy.CatAccount, Screen: privacy.ScreenPayeeReveal, Fields: []string{privacy.FieldAccountNo},
			Subjects: []privacy.Subject{{Type: privacy.SubjectPayment, ID: r.ID}}}); err != nil {
			return err
		}
		raw = r.PayeeEnc
		return nil
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

func revealScreen(s string) string {
	switch s {
	case "payment":
		return "지급 처리"
	}
	return "계좌 관리"
}

// notePrivacy 는 계좌 원문을 내보내기 전에 개인정보 조회 기록(B41)을 같은 트랜잭션에서 남긴다. 기록하지 못하면 오류라 원문을 주지 않는다.
func (s *FundService) notePrivacy(ctx context.Context, tx contract.Tx, p contract.Principal, a privacy.Access) error {
	if s.Privacy == nil {
		return nil
	}
	return s.Privacy.NoteTx(ctx, tx, p, a)
}

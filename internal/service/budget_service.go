package service

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 부서·월 예산(B25 ①, 3.1).
// 예산 판정의 정본은 judgeBudget 한 곳이다(작성 화면의 예산 확인·결재선 미리보기·상신이 같은 함수를 쓴다).
// 금액은 모두 공급가액(부가세 별도) 정수 원이다.

const (
	budgetNearPct   = 80
	maxBudgetAmount = 1_000_000_000_000 // 1조 원
	maxBudgetReason = 200
)

// budgetMonthOf 는 시각의 KST 월(YYYY-MM)이다. 상신한 날의 달이 예산 월이다(승인·발주가 다음 달이어도 옮기지 않는다).
func budgetMonthOf(t time.Time) string { return string(contract.DayOf(t))[:7] }

// budgetBadge 는 사용액(승인 + 진행 중) 비율의 배지다. 예산 0 원이면 사용이 있을 때만 초과다.
func budgetBadge(budget, spent int64) (badge string, pct int) {
	if budget <= 0 {
		if spent > 0 {
			return domain.BadgeOver, -1
		}
		return domain.BadgeOK, -1
	}
	pct = int(spent * 100 / budget)
	switch {
	case spent > budget:
		return domain.BadgeOver, pct
	case spent*100 >= budget*budgetNearPct:
		return domain.BadgeNear, pct
	}
	return domain.BadgeOK, pct
}

// judgeBudget 은 부서·월의 예산 확인이다. 예산이 등록되지 않았으면 확인하지 않는다(unregistered).
// 이번 요청 금액이 잔액(월 예산 − 사용액 − 진행 중)을 넘으면 초과다. prID 는 합계에서 뺀다(자기 자신).
func (b Base) judgeBudget(ctx context.Context, q contract.Tx, dept, amount, prID int64, month string) (domain.BudgetCheck, error) {
	repo := repository.SQLBudget{}
	out := domain.BudgetCheck{Month: month, DepartmentID: dept, Amount: amount, State: domain.BudgetUnregistered}
	if dept == 0 {
		return out, nil
	}
	name, err := repo.Department(ctx, q, dept)
	if err != nil {
		return out, err
	}
	out.DepartmentName = name
	_, budget, ok, err := repo.Amount(ctx, q, dept, month)
	if err != nil || !ok {
		return out, err
	}
	used, inProgress, err := repo.Sums(ctx, q, dept, month, prID)
	if err != nil {
		return out, err
	}
	out.Registered, out.Budget, out.Used, out.InProgress = true, budget, used, inProgress
	out.Balance = budget - used - inProgress
	out.After = out.Balance - amount
	out.State = domain.BudgetWithin
	if amount > out.Balance {
		out.State = domain.BudgetOver
		out.Over = amount - out.Balance
	}
	return out, nil
}

// judgeFor 는 사용자의 현재 부서로 judgeBudget 을 한다(미리보기·상신 사전 계산).
func (b Base) judgeFor(ctx context.Context, q contract.Tx, userID, amount, prID int64, now time.Time) (domain.BudgetCheck, error) {
	u, err := repository.SQLDelegations{}.User(ctx, q, userID)
	if err != nil {
		return domain.BudgetCheck{}, err
	}
	return b.judgeBudget(ctx, q, u.DepartmentID, amount, prID, budgetMonthOf(now))
}

func snapshotOf(c domain.BudgetCheck) domain.BudgetSnapshot {
	return domain.BudgetSnapshot{State: c.State, Month: c.Month, Budget: c.Budget, Used: c.Used, InProgress: c.InProgress, Amount: c.Amount, Over: c.Over}
}

// withFinanceAdded 는 결재선에 예산 초과로 강제로 더한 재무 단계가 있었는지를 스냅샷에 적는다.
func withFinanceAdded(s domain.BudgetSnapshot, line domain.LinePreview) domain.BudgetSnapshot {
	added := false
	for _, st := range line.Steps {
		if st.Forced {
			added = true
		}
	}
	s.FinanceAdded = &added
	return s
}

// BudgetService 는 domain.BudgetService 의 구현이다.
type BudgetService struct {
	Base
	Repo repository.SQLBudget
}

var _ domain.BudgetService = (*BudgetService)(nil)

func NewBudgetService(b Base) *BudgetService { return &BudgetService{Base: b} }

func parseBudgetMonth(v string, fallback time.Time) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return budgetMonthOf(fallback), nil
	}
	if _, err := time.Parse("2006-01", v); err != nil {
		return "", validation("적용 월은 연-월(예 2026-12) 형식으로 입력하세요")
	}
	return v, nil
}

// List 는 그 달에 예산이 등록된 부서의 예산 현황이다(월을 비우면 이번 달).
func (s *BudgetService) List(ctx context.Context, month string) ([]domain.BudgetRow, error) {
	if _, err := s.authorize(ctx, contract.PermBudgetRead, "budget.list", audit.TargetBudget); err != nil {
		return nil, err
	}
	m, err := parseBudgetMonth(month, s.now())
	if err != nil {
		return nil, err
	}
	rows, err := s.Repo.Rows(ctx, s.DB, m)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		r := &rows[i]
		r.Balance = r.Amount - r.Used - r.InProgress
		r.Badge, r.UsagePct = budgetBadge(r.Amount, r.Used+r.InProgress)
	}
	return rows, nil
}

// Revisions 는 그 달 예산의 수정 이력이다.
func (s *BudgetService) Revisions(ctx context.Context, month string) ([]domain.BudgetRevision, error) {
	if _, err := s.authorize(ctx, contract.PermBudgetRead, "budget.revisions", audit.TargetBudget); err != nil {
		return nil, err
	}
	m, err := parseBudgetMonth(month, s.now())
	if err != nil {
		return nil, err
	}
	return s.Repo.Revisions(ctx, s.DB, m)
}

// Set 은 부서·월 예산을 등록하거나 고친다(재무만). 이번 달 이후만, 사유 필수, 삭제는 없다(0 원으로 둔다). 이력과 감사를 같은 트랜잭션에 남긴다.
func (s *BudgetService) Set(ctx context.Context, in domain.BudgetInput) (domain.Budget, error) {
	p, err := s.authorize(ctx, contract.PermBudgetManage, "budget.set", audit.TargetBudget)
	if err != nil {
		return domain.Budget{}, err
	}
	month := strings.TrimSpace(in.Month)
	if _, perr := time.Parse("2006-01", month); perr != nil || len(month) != 7 {
		return domain.Budget{}, validation("적용 월을 연-월(예 2026-12) 형식으로 입력하세요")
	}
	if month < budgetMonthOf(s.now()) {
		return domain.Budget{}, validation("지난 달 예산은 고칠 수 없습니다. 이번 달 이후만 등록할 수 있습니다")
	}
	if in.Amount < 0 {
		return domain.Budget{}, validation("%s", msgBudgetAmount)
	}
	if in.Amount > maxBudgetAmount {
		return domain.Budget{}, validation("예산 금액이 너무 큽니다(최대 %s)", wonText(maxBudgetAmount))
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return domain.Budget{}, validation("예산을 바꾸는 사유를 입력하세요.")
	}
	if utf8.RuneCountInString(reason) > maxBudgetReason {
		return domain.Budget{}, validation("변경 사유는 %d자 이내로 입력해 주세요", maxBudgetReason)
	}
	var out domain.Budget
	err = s.write(ctx, p, "budget.set", audit.TargetBudget, 0, func(tx contract.Tx) error {
		// 상신의 예산 판정과 같은 키: 줄이기와 상신이 같은 부서·월을 동시에 만지면 순서대로 처리한다(PG).
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("budget:%d:%s", in.DepartmentID, month)); err != nil {
			return err
		}
		name, err := s.Repo.Department(ctx, tx, in.DepartmentID)
		if err != nil {
			return validation("부서를 찾을 수 없습니다")
		}
		// 이미 쓴 금액보다 적게 줄일 수 없다(같은 트랜잭션에서 합계를 다시 읽는다: 동시 상신과 경합해도 순서대로 판정된다). 올리기는 항상 된다.
		if _, cur, registered, err := s.Repo.Amount(ctx, tx, in.DepartmentID, month); err != nil {
			return err
		} else if registered && in.Amount < cur {
			used, inProgress, err := s.Repo.Sums(ctx, tx, in.DepartmentID, month, 0)
			if err != nil {
				return err
			}
			if spent := used + inProgress; in.Amount < spent {
				return validation("%s %s 예산은 사용액과 결재 진행 중 금액의 합(%s)보다 적게 줄일 수 없습니다. 결재 진행 중인 요청을 정리한 뒤 다시 시도하세요.", name, monthKorean(month), wonText(spent))
			}
		}
		now := s.now()
		id, old, err := s.Repo.Upsert(ctx, tx, in.DepartmentID, month, in.Amount, reason, p.UserID, now)
		if err != nil {
			return err
		}
		oldText := ""
		if old != nil {
			oldText = fmt.Sprint(*old)
		}
		out = domain.Budget{ID: id, DepartmentID: in.DepartmentID, DepartmentName: name, Month: month, Amount: in.Amount, UpdatedAt: now}
		return s.record(ctx, tx, p, "budget.set", audit.TargetBudget, id,
			audit.DiffItem{Key: "department", New: name},
			audit.DiffItem{Key: "month", New: month},
			audit.DiffItem{Key: "amount", Old: oldText, New: fmt.Sprint(in.Amount)},
			audit.DiffItem{Key: "reason", New: reason})
	})
	return out, err
}

// Check 는 작성 화면의 예산 확인이다. 본인 부서는 purchase:create 로, 다른 부서는 budget:read 가 있어야 본다.
// 부서가 없는 사용자는 본인 부서 확인이 "예산 미등록"으로 나온다.
func (s *BudgetService) Check(ctx context.Context, in domain.BudgetCheckInput) (domain.BudgetCheck, error) {
	// 본인 부서는 구매요청을 쓰는 역할(purchase:create)이나 예산 조회 역할(budget:read)이면 본다. 둘 다 없으면 거부하고 감사에 남긴다.
	p, err := contract.Authorize(ctx, contract.PermBudgetRead)
	if err != nil {
		if p, err = s.authorize(ctx, contract.PermPurchaseCreate, "budget.check", audit.TargetBudget); err != nil {
			return domain.BudgetCheck{}, err
		}
	}
	if in.Amount < 0 {
		return domain.BudgetCheck{}, validation("금액은 0원 이상이어야 합니다")
	}
	dept := in.DepartmentID
	if dept == 0 {
		dept = p.DepartmentID
	} else if dept != p.DepartmentID && !p.Can(contract.PermBudgetRead) {
		return domain.BudgetCheck{}, fmt.Errorf("%w: 다른 부서의 예산은 볼 수 없습니다", contract.ErrForbidden)
	}
	if in.PRID != 0 {
		// 자기 자신을 합계에서 빼는 값은 본인 요청에만 쓴다.
		rec, err := s.viewOwn(ctx, p, in.PRID)
		if err != nil {
			return domain.BudgetCheck{}, err
		}
		_ = rec
	}
	month, err := parseBudgetMonth(in.Month, s.now())
	if err != nil {
		return domain.BudgetCheck{}, err
	}
	return s.judgeBudget(ctx, s.DB, dept, in.Amount, in.PRID, month)
}

// viewOwn 은 본인이 올린 구매요청인지 확인한다(아니면 없는 것으로 본다).
func (s *BudgetService) viewOwn(ctx context.Context, p contract.Principal, id int64) (repository.PRRecord, error) {
	r, err := repository.SQLPurchases{}.GetPR(ctx, s.DB, id, repository.ScopeAll)
	if err != nil || r.RequesterID != p.UserID {
		return repository.PRRecord{}, contract.ErrNotFound
	}
	return r, nil
}

const (
	defaultDeliveryPlace = "본사 창고"
	maxDeliveryPlace     = 100
)

// normalizeDelivery 는 희망 납기(선택, YYYY-MM-DD, 오늘 이후)와 납품처(비면 기본값)를 검사한다.
func normalizeDelivery(due, place string, now time.Time) (string, string, error) {
	due, place = strings.TrimSpace(due), strings.TrimSpace(place)
	if due != "" {
		if _, err := time.Parse("2006-01-02", due); err != nil {
			return "", "", validation("희망 납기는 연-월-일(예 2026-12-18) 형식으로 입력하세요")
		}
		if due < string(contract.DayOf(now)) {
			return "", "", validation("희망 납기는 오늘이거나 그 뒤 날짜여야 합니다")
		}
	}
	if place == "" {
		place = defaultDeliveryPlace
	}
	if utf8.RuneCountInString(place) > maxDeliveryPlace {
		return "", "", validation("납품처는 %d자 이내로 입력해 주세요", maxDeliveryPlace)
	}
	return due, place, nil
}

// budgetCheckText 는 감사 기록에 남기는 예산 확인 결과 한 줄이다.
func budgetCheckText(c domain.BudgetCheck) string {
	switch c.State {
	case domain.BudgetOver:
		return fmt.Sprintf("%s 예산 초과(%s 초과)", c.Month, wonText(c.Over))
	case domain.BudgetWithin:
		return fmt.Sprintf("%s 예산 이내(요청 뒤 잔액 %s)", c.Month, wonText(c.After))
	}
	return c.Month + " 예산 미등록"
}

const msgBudgetAmount = "월 예산은 0 이상의 숫자로 입력하세요. 쉼표(,)는 넣어도 됩니다."

// monthKorean 은 "2026-11" 을 "2026년 11월" 로 쓴다.
func monthKorean(m string) string {
	if len(m) != 7 {
		return m
	}
	mo := m[5:]
	if mo[0] == '0' {
		mo = mo[1:]
	}
	return m[:4] + "년 " + mo + "월"
}

// wonText 는 금액을 "1,234원" 으로 쓴다.
func wonText(n int64) string { return moneyLabel(n) }

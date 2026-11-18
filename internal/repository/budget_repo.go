package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLBudget 은 부서·월 예산 저장소다(B25 ①). 집계는 purchase_requests 한 표에서 부서·예산 월·상태로 합한다.
type SQLBudget struct{}

// Department 는 부서 이름이다. 없으면 ErrNotFound.
func (SQLBudget) Department(ctx context.Context, q contract.Tx, id int64) (string, error) {
	var n string
	err := q.QueryRowContext(ctx, `SELECT name FROM departments WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.ErrNotFound
	}
	return n, err
}

// Amount 는 부서·월의 예산이다. 등록되지 않았으면 ok=false.
func (SQLBudget) Amount(ctx context.Context, q contract.Tx, dept int64, month string) (id, amount int64, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, amount FROM budgets WHERE `+contract.SQLEntity(ctx, "budgets")+` AND department_id = ? AND month = ?`, dept, month).Scan(&id, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	return id, amount, err == nil, err
}

// Sums 는 부서·예산 월의 승인 완료 사용액(발주가 만들어진 건)과 결재 진행 중(상신~결재중) 합계다. excludePR 은 합계에서 뺄 요청(0 이면 없음).
func (SQLBudget) Sums(ctx context.Context, q contract.Tx, dept int64, month string, excludePR int64) (used, inProgress int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN status = 'ordered' THEN total_amount ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status IN ('submitted','in_review') THEN total_amount ELSE 0 END),0)
		FROM purchase_requests WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND COALESCE(form_id,1) = 1 AND department_id = ? AND budget_month = ? AND id <> ?`, dept, month, excludePR).Scan(&used, &inProgress)
	return
}

// Rows 는 그 달에 예산이 등록된 부서의 예산 표다(부서 번호 순).
func (r SQLBudget) Rows(ctx context.Context, q contract.Tx, month string) ([]domain.BudgetRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.department_id, d.name, b.amount,
			COALESCE((SELECT SUM(total_amount) FROM purchase_requests p WHERE `+contract.SQLEntity(ctx, "p")+` AND COALESCE(p.form_id,1) = 1 AND p.department_id = b.department_id AND p.budget_month = b.month AND p.status = 'ordered'),0),
			COALESCE((SELECT SUM(total_amount) FROM purchase_requests p WHERE `+contract.SQLEntity(ctx, "p")+` AND COALESCE(p.form_id,1) = 1 AND p.department_id = b.department_id AND p.budget_month = b.month AND p.status IN ('submitted','in_review')),0)
		FROM budgets b JOIN departments d ON d.id = b.department_id WHERE `+contract.SQLEntity(ctx, "b")+` AND b.month = ? ORDER BY b.department_id`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BudgetRow
	for rows.Next() {
		x := domain.BudgetRow{Month: month}
		if err := rows.Scan(&x.DepartmentID, &x.DepartmentName, &x.Amount, &x.Used, &x.InProgress); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Upsert 는 예산을 등록하거나 고치고 이력 한 줄을 남긴다. 처음 등록이면 이전 금액이 없다.
func (SQLBudget) Upsert(ctx context.Context, q contract.Tx, dept int64, month string, amount int64, reason string, actor int64, now time.Time) (id int64, old *int64, err error) {
	var cur int64
	e := q.QueryRowContext(ctx, `SELECT id, amount FROM budgets WHERE `+contract.SQLEntity(ctx, "budgets")+` AND department_id = ? AND month = ?`, dept, month).Scan(&id, &cur)
	switch {
	case errors.Is(e, sql.ErrNoRows):
		newID, err := dbutil.InsertID(ctx, q, `INSERT INTO budgets (entity_id, department_id, month, amount, updated_by, updated_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?)`, dept, month, amount, actor, fmtTime(now))
		if err != nil {
			return 0, nil, mapErr(err)
		}
		id = newID
	case e != nil:
		return 0, nil, e
	default:
		old = &cur
		if _, err := q.ExecContext(ctx, `UPDATE budgets SET amount = ?, updated_by = ?, updated_at = ? WHERE `+contract.SQLEntity(ctx, "budgets")+` AND id = ?`, amount, actor, fmtTime(now), id); err != nil {
			return 0, nil, mapErr(err)
		}
	}
	var oldArg any
	if old != nil {
		oldArg = *old
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO budget_revisions (entity_id, budget_id, old_amount, new_amount, reason, actor_id, at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?)`, id, oldArg, amount, reason, actor, fmtTime(now)); err != nil {
		return 0, nil, mapErr(err)
	}
	return id, old, nil
}

// Revisions 는 그 달 예산의 수정 이력(최근 순)이다.
func (SQLBudget) Revisions(ctx context.Context, q contract.Tx, month string) ([]domain.BudgetRevision, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.name, b.month, r.old_amount, r.new_amount, r.reason, u.name, r.at
		FROM budget_revisions r JOIN budgets b ON b.id = r.budget_id JOIN departments d ON d.id = b.department_id JOIN users u ON u.id = r.actor_id
		WHERE `+contract.SQLEntity(ctx, "r")+` AND `+contract.SQLEntity(ctx, "b")+` AND b.month = ? ORDER BY r.id DESC LIMIT 100`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BudgetRevision
	for rows.Next() {
		var x domain.BudgetRevision
		var old sql.NullInt64
		var at string
		if err := rows.Scan(&x.DepartmentName, &x.Month, &old, &x.NewAmount, &x.Reason, &x.ActorName, &at); err != nil {
			return nil, err
		}
		if old.Valid {
			v := old.Int64
			x.OldAmount = &v
		}
		x.At = parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetBudgetSnapshot 은 상신 때 판정한 예산 월·상태·스냅샷을 요청에 저장한다.
func (SQLBudget) SetBudgetSnapshot(ctx context.Context, q contract.Tx, prID int64, s domain.BudgetSnapshot) error {
	raw, _ := json.Marshal(map[string]any{"month": s.Month, "budget": s.Budget, "used": s.Used, "inProgress": s.InProgress, "amount": s.Amount, "over": s.Over, "financeAdded": s.FinanceAdded})
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET budget_month = ?, budget_state = ?, budget_json = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, s.Month, s.State, string(raw), prID)
	return mapErr(err)
}

// SetDelivery 는 구매요청의 희망 납기(YYYY-MM-DD, 비면 없음)와 납품처를 저장한다.
func (SQLBudget) SetDelivery(ctx context.Context, q contract.Tx, prID int64, due, place string) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET due_date = ?, delivery_place = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, nullStr(due), place, prID)
	return mapErr(err)
}

// ParseBudgetSnapshot 은 저장된 스냅샷 JSON 을 읽는다(없거나 깨졌으면 nil).
func ParseBudgetSnapshot(state, raw string) *domain.BudgetSnapshot {
	if state == "" || state == domain.BudgetNone {
		return nil
	}
	s := &domain.BudgetSnapshot{State: state}
	if raw != "" {
		var m struct {
			Month        string `json:"month"`
			Budget       int64  `json:"budget"`
			Used         int64  `json:"used"`
			InProgress   int64  `json:"inProgress"`
			Amount       int64  `json:"amount"`
			Over         int64  `json:"over"`
			FinanceAdded *bool  `json:"financeAdded"`
		}
		if json.Unmarshal([]byte(raw), &m) == nil {
			s.Month, s.Budget, s.Used, s.InProgress, s.Amount, s.Over = m.Month, m.Budget, m.Used, m.InProgress, m.Amount, m.Over
			s.FinanceAdded = m.FinanceAdded
		}
	}
	return s
}

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 업무 보고서(B28 erp) 저장소. 새 표 없이 구매요청·발주·예산·결재 표를 읽는다. 열람 범위는 ViewScope.Cond 한 곳을 그대로 쓴다.

// SQLReport 는 보고서 조회다.
type SQLReport struct{}

// ReportFilter 는 보고서가 사용자가 고른 조건(부서)이다.
type ReportFilter struct {
	DepartmentID int64
}

// PurchaseAggRow 는 부서 × 예산 월 집계 한 줄이다. Budget 이 nil 이면 예산이 없다.
type PurchaseAggRow struct {
	DepartmentID   int64
	DepartmentName string
	Month          string
	Budget         *int64
	Orders         int
	Used           int64
	InProgress     int64
}

// PurchaseAgg 는 예산 월 범위의 부서·월별 사용액·진행 중 합계다. 구매 품의서만 센다(발주 생성=ordered, 진행 중=submitted·in_review).
func (SQLReport) PurchaseAgg(ctx context.Context, q contract.Tx, fromMonth, toMonth string, f ReportFilter, scope ViewScope) ([]PurchaseAggRow, error) {
	where := []string{"COALESCE(p.form_id,1) = 1", "p.status IN ('ordered','submitted','in_review')", "p.budget_month >= ?", "p.budget_month <= ?"}
	args := []any{fromMonth, toMonth}
	if f.DepartmentID != 0 {
		where = append(where, "p.department_id = ?") // 사용자가 고른 부서 조건(Filter.DepartmentID), 열람 범위가 아니다
		args = append(args, f.DepartmentID)
	}
	if !scope.All {
		cond, cargs := scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, `SELECT p.department_id, COALESCE(d.name,''), p.budget_month, b.amount,
			SUM(CASE WHEN p.status = 'ordered' THEN 1 ELSE 0 END),
			COALESCE(SUM(CASE WHEN p.status = 'ordered' THEN p.total_amount ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN p.status IN ('submitted','in_review') THEN p.total_amount ELSE 0 END),0)
		FROM purchase_requests p LEFT JOIN departments d ON d.id = p.department_id
		LEFT JOIN budgets b ON b.department_id = p.department_id AND b.month = p.budget_month
		WHERE `+contract.SQLEntity(ctx, "p")+` AND `+strings.Join(where, " AND ")+` GROUP BY p.department_id, d.name, p.budget_month, b.amount ORDER BY p.budget_month, p.department_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PurchaseAggRow
	for rows.Next() {
		var x PurchaseAggRow
		var budget *int64
		if err := rows.Scan(&x.DepartmentID, &x.DepartmentName, &x.Month, &budget, &x.Orders, &x.Used, &x.InProgress); err != nil {
			return nil, err
		}
		x.Budget = budget
		out = append(out, x)
	}
	return out, rows.Err()
}

// VendorOrderAgg 는 거래처별 발주 건수·금액이다(발주일 = 발주 생성일의 KST 날짜).
type VendorOrderAgg struct {
	VendorID   int64
	VendorName string
	Orders     int
	Amount     int64
}

// VendorOrders 는 기간 안에 발주일이 있는 발주를 거래처별로 합한다.
func (SQLReport) VendorOrders(ctx context.Context, q contract.Tx, from, to string, scope ViewScope) ([]VendorOrderAgg, error) {
	where := []string{"date(po.created_at, '+9 hours') >= ?", "date(po.created_at, '+9 hours') <= ?"}
	args := []any{from, to}
	if !scope.All {
		cond, cargs := scope.Cond()
		where = append(where, "po.pr_id IN (SELECT p.id FROM purchase_requests p WHERE p.entity_id = po.entity_id AND "+cond+")")
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(po.vendor_id,0), COALESCE(v.name,''), COUNT(*), COALESCE(SUM(po.total_amount),0)
		FROM purchase_orders po LEFT JOIN vendors v ON v.id = po.vendor_id WHERE `+contract.SQLEntity(ctx, "po")+` AND `+strings.Join(where, " AND ")+` GROUP BY po.vendor_id, v.name ORDER BY po.vendor_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VendorOrderAgg
	for rows.Next() {
		var x VendorOrderAgg
		if err := rows.Scan(&x.VendorID, &x.VendorName, &x.Orders, &x.Amount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// StepDecision 은 처리된 결재 단계 한 줄(소요 계산의 입력)이다.
type StepDecision struct {
	DocID     int64
	Round     int
	Stage     int
	Status    string // approved | rejected | agreed | objected
	Title     string
	StepKind  string
	DecidedAt time.Time
	FormCode  string
	FormName  string
}

// DecidedSteps 는 처리 날짜(KST)가 기간 안인 단계다. 문서의 열람 범위·부서·양식 조건이 걸린다.
func (SQLReport) DecidedSteps(ctx context.Context, q contract.Tx, from, to, formCode string, f ReportFilter, scope ViewScope) ([]StepDecision, error) {
	where := []string{"s.status IN ('approved','rejected','agreed','objected')", "s.decided_at IS NOT NULL",
		"date(s.decided_at, '+9 hours') >= ?", "date(s.decided_at, '+9 hours') <= ?"}
	args := []any{from, to}
	if formCode != "" {
		where = append(where, "fm.code = ?")
		args = append(args, formCode)
	}
	if f.DepartmentID != 0 {
		where = append(where, "p.department_id = ?") // 사용자가 고른 부서 조건(Filter.DepartmentID), 열람 범위가 아니다
		args = append(args, f.DepartmentID)
	}
	if !scope.Unrestricted() {
		cond, cargs := scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, `SELECT s.pr_id, s.round, s.stage, s.status, s.title, s.step_kind, s.decided_at, COALESCE(fm.code,'PUR'), COALESCE(fm.name,'구매 품의서')
		FROM approval_steps s JOIN purchase_requests p ON p.id = s.pr_id LEFT JOIN approval_forms fm ON fm.id = COALESCE(p.form_id,1)
		WHERE `+contract.SQLEntity(ctx, "p")+` AND `+strings.Join(where, " AND ")+` ORDER BY s.pr_id, s.round, s.stage, s.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepDecision
	for rows.Next() {
		var x StepDecision
		var at string
		if err := rows.Scan(&x.DocID, &x.Round, &x.Stage, &x.Status, &x.Title, &x.StepKind, &at, &x.FormCode, &x.FormName); err != nil {
			return nil, err
		}
		x.DecidedAt = parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// RoundTimes 는 문서 차수별 단계 시작 계산에 쓰는 원자료다: 상신 시각과 이미 처리된 단계(차수·단계·처리 시각)다.
type RoundTimes struct {
	Submit  map[[2]int64]time.Time // (문서, 차수) → 상신·재상신 시각
	Decided map[[2]int64][]DecidedStage
}

// DecidedStage 는 한 차수에서 처리된 단계의 단계 번호와 처리 시각이다.
type DecidedStage struct {
	Stage int
	At    time.Time
}

// RoundData 는 문서 목록(docIDs)의 차수별 상신 시각과 처리된 단계를 모은다.
func (SQLReport) RoundData(ctx context.Context, q contract.Tx, docIDs []int64) (RoundTimes, error) {
	out := RoundTimes{Submit: map[[2]int64]time.Time{}, Decided: map[[2]int64][]DecidedStage{}}
	for i := 0; i < len(docIDs); i += 400 {
		end := i + 400
		if end > len(docIDs) {
			end = len(docIDs)
		}
		chunk := docIDs[i:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for j, id := range chunk {
			args[j] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT pr_id, round, MIN(at) FROM approval_history WHERE action IN ('submit','resubmit') AND pr_id IN (`+ph+`) GROUP BY pr_id, round`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id int64
			var round int64
			var at string
			if err := rows.Scan(&id, &round, &at); err != nil {
				rows.Close()
				return out, err
			}
			out.Submit[[2]int64{id, round}] = parseTime(at)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
		rows, err = q.QueryContext(ctx, `SELECT pr_id, round, stage, decided_at FROM approval_steps WHERE decided_at IS NOT NULL AND status IN ('approved','rejected','agreed','objected') AND pr_id IN (`+ph+`)`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id, round int64
			var stage int
			var at string
			if err := rows.Scan(&id, &round, &stage, &at); err != nil {
				rows.Close()
				return out, err
			}
			k := [2]int64{id, round}
			out.Decided[k] = append(out.Decided[k], DecidedStage{Stage: stage, At: parseTime(at)})
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// CompletedDoc 는 결재가 끝난 문서의 차수와 마지막 처리 시각이다(전체 소요 계산용).
type CompletedDoc struct {
	DocID    int64
	Round    int
	DoneAt   time.Time
	FormCode string
	FormName string
}

// CompletedDocs 는 결재 완료(승인·발주완료) 문서 중 마지막 단계 처리일(KST)이 기간 안인 것이다.
func (SQLReport) CompletedDocs(ctx context.Context, q contract.Tx, from, to, formCode string, f ReportFilter, scope ViewScope) ([]CompletedDoc, error) {
	where := []string{"p.status IN ('approved','ordered')"}
	args := []any{}
	if formCode != "" {
		where = append(where, "fm.code = ?")
		args = append(args, formCode)
	}
	if f.DepartmentID != 0 {
		where = append(where, "p.department_id = ?") // 사용자가 고른 부서 조건(Filter.DepartmentID), 열람 범위가 아니다
		args = append(args, f.DepartmentID)
	}
	if !scope.Unrestricted() {
		cond, cargs := scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	args = append(args, from, to)
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.round, m.last_at, COALESCE(fm.code,'PUR'), COALESCE(fm.name,'구매 품의서')
		FROM purchase_requests p LEFT JOIN approval_forms fm ON fm.id = COALESCE(p.form_id,1)
		JOIN (SELECT pr_id, round, MAX(decided_at) AS last_at FROM approval_steps WHERE decided_at IS NOT NULL AND status IN ('approved','agreed') GROUP BY pr_id, round) m ON m.pr_id = p.id AND m.round = p.round
		WHERE `+contract.SQLEntity(ctx, "p")+` AND `+strings.Join(where, " AND ")+` AND date(m.last_at, '+9 hours') >= ? AND date(m.last_at, '+9 hours') <= ? ORDER BY p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompletedDoc
	for rows.Next() {
		var x CompletedDoc
		var at string
		if err := rows.Scan(&x.DocID, &x.Round, &at, &x.FormCode, &x.FormName); err != nil {
			return nil, err
		}
		x.DoneAt = parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// PendingStep 은 지금 진행 중인 문서의 현재(결재 차례) 단계 한 줄이다.
type PendingStep struct {
	DocID      int64
	DocNo      string
	Title      string
	FormName   string
	FormKind   string
	Round      int
	Stage      int
	StepTitle  string
	StepKind   string
	Approvers  []string
	NoApprover bool
}

// PendingSteps 는 진행 중(상신·결재 중) 문서의 결재 차례 단계다. 문서마다 가장 앞선 단계 번호의 차례 단계 하나를 돌려주고, 같은 단계의 병렬 결재자는 이름을 모은다.
func (SQLReport) PendingSteps(ctx context.Context, q contract.Tx, formCode string, f ReportFilter, scope ViewScope) ([]PendingStep, error) {
	where := []string{"p.status IN ('submitted','in_review')", "s.status = 'pending'", "s.round = p.round"}
	var args []any
	if formCode != "" {
		where = append(where, "fm.code = ?")
		args = append(args, formCode)
	}
	if f.DepartmentID != 0 {
		where = append(where, "p.department_id = ?") // 사용자가 고른 부서 조건(Filter.DepartmentID), 열람 범위가 아니다
		args = append(args, f.DepartmentID)
	}
	if !scope.Unrestricted() {
		cond, cargs := scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, `SELECT p.id, COALESCE(p.no,''), p.title, COALESCE(fm.name,'구매 품의서'), COALESCE(fm.kind,'purchase'), p.round, s.stage, s.title, s.step_kind, COALESCE(u.name,'')
		FROM approval_steps s JOIN purchase_requests p ON p.id = s.pr_id LEFT JOIN approval_forms fm ON fm.id = COALESCE(p.form_id,1)
		LEFT JOIN approval_step_current c ON c.pr_id = s.pr_id AND c.round = s.round AND c.seq = s.seq LEFT JOIN users u ON u.id = c.user_id
		WHERE `+contract.SQLEntity(ctx, "p")+` AND `+strings.Join(where, " AND ")+` ORDER BY p.id, s.stage, s.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDoc := map[int64]*PendingStep{}
	var order []int64
	for rows.Next() {
		var x PendingStep
		var name string
		if err := rows.Scan(&x.DocID, &x.DocNo, &x.Title, &x.FormName, &x.FormKind, &x.Round, &x.Stage, &x.StepTitle, &x.StepKind, &name); err != nil {
			return nil, err
		}
		cur, ok := byDoc[x.DocID]
		if !ok {
			cp := x
			byDoc[x.DocID] = &cp
			order = append(order, x.DocID)
			cur = &cp
		} else if x.Stage != cur.Stage {
			continue // 가장 앞선 단계만
		}
		if name != "" {
			cur.Approvers = append(cur.Approvers, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PendingStep, 0, len(order))
	for _, id := range order {
		p := byDoc[id]
		p.NoApprover = len(p.Approvers) == 0
		out = append(out, *p)
	}
	return out, nil
}

// Options 는 보고서 필터의 선택지다. 부서는 열람 범위 안의 문서가 있는 부서(전체 열람자는 모든 부서)이고, 양식은 사용 중이며 사람이 기안할 수 있는 양식이다
// (재고 조정 전표·월 마감 해제 요청서 같은 시스템 전용 양식은 구매 품의서만 남기고 뺀다).
func (SQLReport) Options(ctx context.Context, q contract.Tx, scope ViewScope, ownDept int64) (domain.ReportOptions, error) {
	var out domain.ReportOptions
	query := `SELECT d.id, d.name FROM departments d`
	var args []any
	if !scope.All {
		cond, cargs := scope.Cond()
		query += ` WHERE d.id = ? OR d.id IN (SELECT p.department_id FROM purchase_requests p WHERE ` + contract.SQLEntity(ctx, "p") + ` AND ` + cond + `)`
		args = append(args, ownDept)
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY d.name`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d domain.DepartmentRef
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			rows.Close()
			return out, err
		}
		out.Departments = append(out.Departments, d)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT f.code, f.name, COALESCE((SELECT v.definition FROM approval_form_versions v WHERE v.form_id = f.id AND v.status = 'published' ORDER BY v.version DESC LIMIT 1),'')
		FROM approval_forms f WHERE f.active = 1 ORDER BY f.name`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var f domain.FormRef
		var def string
		if err := rows.Scan(&f.Code, &f.Name, &def); err != nil {
			return out, err
		}
		var d domain.FormDefinition
		if f.Code != "PUR" && def != "" && json.Unmarshal([]byte(def), &d) == nil && d.System {
			continue
		}
		out.Forms = append(out.Forms, f)
	}
	return out, rows.Err()
}

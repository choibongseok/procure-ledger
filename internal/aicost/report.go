package aicost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Filter 는 전사 집계의 거름 조건이다(모두 선택).
type Filter struct {
	Month        string // YYYY-MM, 비면 이번 달
	EntityID     int64
	DepartmentID int64
	Purpose      string
	Provider     string
}

// Row 는 집계 한 줄(월 하나 안에서 법인·부서·용도·공급자·모델별)이다. 금액은 status='ok' 호출만 넣고 실패·차단은 건수만 센다.
type Row struct {
	EntityID       int64  `json:"entityId"`
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	Purpose        string `json:"purpose"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Calls          int64  `json:"calls"`
	Failed         int64  `json:"failed"`
	InputTokens    int64  `json:"inputTokens"`
	OutputTokens   int64  `json:"outputTokens"`
	CostMicroUSD   int64  `json:"costMicroUsd"`
	CostKRW        int64  `json:"costKrw"` // 월 환율이 있을 때만(없으면 0)
}

// DeptTotal 은 부서별 합계다.
type DeptTotal struct {
	EntityID       int64  `json:"entityId"`
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	Calls          int64  `json:"calls"`
	CostMicroUSD   int64  `json:"costMicroUsd"`
	CostKRW        int64  `json:"costKrw"`
}

// ProviderTotal 은 공급자별 합계와 실제 청구액(조정 입력) 비교다.
type ProviderTotal struct {
	Provider     string  `json:"provider"`
	Calls        int64   `json:"calls"`
	CostMicroUSD int64   `json:"costMicroUsd"`
	CostKRW      int64   `json:"costKrw"`
	ActualKRW    *int64  `json:"actualKrw,omitempty"`   // 입력한 실제 청구액(없으면 생략)
	AdjustRatio  float64 `json:"adjustRatio,omitempty"` // 청구액 ÷ 집계 원화(둘 다 있을 때)
	ZeroPrice    int64   `json:"zeroPriceCalls"`        // 금액이 0 인데 토큰이 있는 호출 수(단가 미설정 의심)
}

// Adjustment 는 입력된 실제 청구액이다(같은 월·공급자는 최신 한 건).
type Adjustment struct {
	ID         int64  `json:"id"`
	Month      string `json:"month"`
	Provider   string `json:"provider"`
	ActualKRW  int64  `json:"actualKrw"`
	Note       string `json:"note"`
	EnteredBy  int64  `json:"enteredBy"`
	EnteredAt  string `json:"enteredAt"`
	EnteredNam string `json:"enteredByName"`
}

// Report 는 전사 집계 한 달 결과다.
type Report struct {
	Month         string          `json:"month"`
	FxUsdKrwX1000 int64           `json:"fxUsdKrwX1000"` // 0 = 환율 미설정
	TotalMicro    int64           `json:"totalMicroUsd"`
	TotalKRW      int64           `json:"totalKrw"`
	Calls         int64           `json:"calls"`
	Failed        int64           `json:"failed"`
	Departments   []DeptTotal     `json:"departments"`
	Providers     []ProviderTotal `json:"providers"`
	Rows          []Row           `json:"rows"`
	Adjustments   []Adjustment    `json:"adjustments"`
	Warnings      []string        `json:"warnings"`
}

// monthSQL 은 월·범위 조건과 인자를 만든다. 법인 열 0(미상)은 기본 법인으로 본다(명세 14절).
func scopeCond(scope contract.EntityScope) string {
	if scope.All {
		return "1=1"
	}
	if len(scope.IDs) == 0 {
		return "1=0"
	}
	ids := make([]string, len(scope.IDs))
	for i, id := range scope.IDs {
		ids[i] = fmt.Sprint(id)
	}
	return "COALESCE(NULLIF(u.entity_id, 0), 1) IN (" + strings.Join(ids, ",") + ")"
}

const deptNameSQL = `COALESCE((SELECT s.name FROM org_unit_spans s WHERE s.unit_id = u.department_id ORDER BY s.valid_from DESC LIMIT 1),
	(SELECT d.name FROM departments d WHERE d.id = u.department_id), '')`

func deptLabel(id int64, name string) string {
	if id == 0 {
		return "(부서 미지정)"
	}
	if name == "" {
		return fmt.Sprintf("부서 %d", id)
	}
	return name
}

// aggregate 는 월 범위의 집계 줄을 읽는다(범위 조건은 호출자가 정한다).
func (s *Service) aggregate(ctx context.Context, month, cond string, args []any, fx int64) ([]Row, error) {
	from, to, err := monthRange(month)
	if err != nil {
		return nil, err
	}
	q := `SELECT COALESCE(NULLIF(u.entity_id, 0), 1), u.department_id, ` + deptNameSQL + `, u.purpose, u.provider, u.model,
		SUM(CASE WHEN u.status = 'ok' THEN 1 ELSE 0 END), SUM(CASE WHEN u.status <> 'ok' THEN 1 ELSE 0 END),
		COALESCE(SUM(CASE WHEN u.status = 'ok' THEN u.input_tokens ELSE 0 END), 0), COALESCE(SUM(CASE WHEN u.status = 'ok' THEN u.output_tokens ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN u.status = 'ok' THEN u.cost_micro_usd ELSE 0 END), 0)
		FROM llm_usage u WHERE u.at >= ? AND u.at < ? AND ` + cond + `
		GROUP BY COALESCE(NULLIF(u.entity_id, 0), 1), u.department_id, u.purpose, u.provider, u.model
		ORDER BY 1, 2, u.purpose, u.provider, u.model`
	rows, err := s.o.DB.QueryContext(ctx, q, append([]any{from, to}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.EntityID, &r.DepartmentID, &r.DepartmentName, &r.Purpose, &r.Provider, &r.Model, &r.Calls, &r.Failed, &r.InputTokens, &r.OutputTokens, &r.CostMicroUSD); err != nil {
			return nil, err
		}
		r.DepartmentName = deptLabel(r.DepartmentID, r.DepartmentName)
		r.CostKRW = microToKRW(r.CostMicroUSD, fx)
		out = append(out, r)
	}
	return out, rows.Err()
}

// fxFor 는 월 환율(x1000)이다. 없으면 0.
func (s *Service) fxFor(ctx context.Context, month string) (int64, error) {
	var fx int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT usd_krw_x1000 FROM ai_cost_fx WHERE month = ?), 0)`, month).Scan(&fx)
	return fx, err
}

// latestAdjustments 는 월의 공급자별 최신 청구액 입력이다.
func (s *Service) latestAdjustments(ctx context.Context, month string) ([]Adjustment, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT a.id, a.month, a.provider_code, a.actual_krw, a.note, a.entered_by, COALESCE(u.name, ''), a.entered_at
		FROM ai_cost_adjustments a LEFT JOIN users u ON u.id = a.entered_by
		WHERE a.month = ? AND a.id = (SELECT MAX(b.id) FROM ai_cost_adjustments b WHERE b.month = a.month AND b.provider_code = a.provider_code)
		ORDER BY a.provider_code`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Adjustment{}
	for rows.Next() {
		var a Adjustment
		if err := rows.Scan(&a.ID, &a.Month, &a.Provider, &a.ActualKRW, &a.Note, &a.EnteredBy, &a.EnteredNam, &a.EnteredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// zeroPriceCalls 는 토큰은 있는데 금액이 0 인 호출 수(공급자별)다.
func (s *Service) zeroPriceCalls(ctx context.Context, month, cond string, args []any) (map[string]int64, error) {
	from, to, err := monthRange(month)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT u.provider, COUNT(*) FROM llm_usage u WHERE u.at >= ? AND u.at < ? AND u.status = 'ok'
		AND u.cost_micro_usd = 0 AND u.input_tokens + u.output_tokens > 0 AND `+cond+` GROUP BY u.provider`, append([]any{from, to}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var n int64
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// Monthly 는 전사(법인 범위 안) 월 집계다(cost:read). 법인은 호출자의 읽기 범위와 현재 법인으로 좁혀진다.
func (s *Service) Monthly(ctx context.Context, f Filter) (Report, error) {
	scope, err := contract.ReadScope(ctx, contract.PermCostRead)
	if err != nil {
		return Report{}, err
	}
	month, err := s.normMonth(f.Month)
	if err != nil {
		return Report{}, err
	}
	cond, args := scopeCond(scope), []any{}
	if f.EntityID > 0 {
		cond += " AND COALESCE(NULLIF(u.entity_id, 0), 1) = ?"
		args = append(args, f.EntityID)
	}
	if f.DepartmentID > 0 {
		cond += " AND u.department_id = ?"
		args = append(args, f.DepartmentID)
	}
	if f.Purpose != "" {
		cond += " AND u.purpose = ?"
		args = append(args, f.Purpose)
	}
	if f.Provider != "" {
		cond += " AND u.provider = ?"
		args = append(args, f.Provider)
	}
	return s.build(ctx, month, cond, args, true)
}

func (s *Service) build(ctx context.Context, month, cond string, args []any, withAdj bool) (Report, error) {
	fx, err := s.fxFor(ctx, month)
	if err != nil {
		return Report{}, err
	}
	rows, err := s.aggregate(ctx, month, cond, args, fx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Month: month, FxUsdKrwX1000: fx, Rows: rows, Departments: []DeptTotal{}, Providers: []ProviderTotal{}, Adjustments: []Adjustment{}, Warnings: []string{}}
	type dk struct{ e, d int64 }
	depts := map[dk]*DeptTotal{}
	var deptOrder []dk
	provs := map[string]*ProviderTotal{}
	var provOrder []string
	for _, r := range rows {
		rep.Calls += r.Calls
		rep.Failed += r.Failed
		rep.TotalMicro += r.CostMicroUSD
		k := dk{r.EntityID, r.DepartmentID}
		dt := depts[k]
		if dt == nil {
			dt = &DeptTotal{EntityID: r.EntityID, DepartmentID: r.DepartmentID, DepartmentName: r.DepartmentName}
			depts[k] = dt
			deptOrder = append(deptOrder, k)
		}
		dt.Calls += r.Calls
		dt.CostMicroUSD += r.CostMicroUSD
		pt := provs[r.Provider]
		if pt == nil {
			pt = &ProviderTotal{Provider: r.Provider}
			provs[r.Provider] = pt
			provOrder = append(provOrder, r.Provider)
		}
		pt.Calls += r.Calls
		pt.CostMicroUSD += r.CostMicroUSD
	}
	rep.TotalKRW = microToKRW(rep.TotalMicro, fx)
	for _, k := range deptOrder {
		d := depts[k]
		d.CostKRW = microToKRW(d.CostMicroUSD, fx)
		rep.Departments = append(rep.Departments, *d)
	}
	sort.Slice(rep.Departments, func(i, j int) bool { return rep.Departments[i].CostMicroUSD > rep.Departments[j].CostMicroUSD })
	zero, err := s.zeroPriceCalls(ctx, month, cond, args)
	if err != nil {
		return Report{}, err
	}
	var adjs []Adjustment
	if withAdj {
		if adjs, err = s.latestAdjustments(ctx, month); err != nil {
			return Report{}, err
		}
		rep.Adjustments = adjs
	}
	actual := map[string]int64{}
	for _, a := range adjs {
		actual[a.Provider] = a.ActualKRW
	}
	var zeroTotal int64
	for _, name := range provOrder {
		p := provs[name]
		p.CostKRW = microToKRW(p.CostMicroUSD, fx)
		p.ZeroPrice = zero[name]
		zeroTotal += p.ZeroPrice
		if v, ok := actual[name]; ok {
			vv := v
			p.ActualKRW = &vv
			if p.CostKRW > 0 {
				p.AdjustRatio = float64(v) / float64(p.CostKRW)
			}
		}
		rep.Providers = append(rep.Providers, *p)
	}
	if fx == 0 && rep.TotalMicro > 0 {
		rep.Warnings = append(rep.Warnings, "이 달 환율이 설정되지 않아 원화 금액을 계산하지 않았습니다. 환율을 입력하세요.")
	}
	if zeroTotal > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("단가가 설정되지 않은 공급자의 호출 %d건은 금액이 0 입니다. 실제 청구액을 입력하면 보정됩니다.", zeroTotal))
	}
	for _, p := range rep.Providers {
		if p.AdjustRatio != 0 && (p.AdjustRatio > 2 || p.AdjustRatio < 0.5) {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("공급자 %s 의 입력 청구액이 집계의 2배(0.5배) 범위를 벗어났습니다. 확인하세요.", p.Provider))
		}
	}
	return rep, nil
}

// TrendPoint 는 월별 직접 비용 한 점이다.
type TrendPoint struct {
	Month        string `json:"month"`
	Calls        int64  `json:"calls"`
	CostMicroUSD int64  `json:"costMicroUsd"`
	CostKRW      int64  `json:"costKrw"`
}

// MyReport 는 부서장 화면용 우리 부서의 한 달 결과와 최근 12개월 추이다.
type MyReport struct {
	Month          string        `json:"month"`
	DepartmentID   int64         `json:"departmentId"`
	DepartmentName string        `json:"departmentName"`
	FxUsdKrwX1000  int64         `json:"fxUsdKrwX1000"`
	TotalMicro     int64         `json:"totalMicroUsd"`
	TotalKRW       int64         `json:"totalKrw"`
	Calls          int64         `json:"calls"`
	Forecast       *Forecast     `json:"forecast,omitempty"` // 이번 달만
	Rows           []Row         `json:"rows"`
	Trend          []TrendPoint  `json:"trend"`
	TopUsers       []UserTotal   `json:"topUsers"`
	Budget         *BudgetStatus `json:"budget,omitempty"` // 우리 부서 AI 비용 예산 대비(예산이 없으면 생략)
	Warnings       []string      `json:"warnings"`
}

// Forecast 는 이번 달 예상(오늘까지 누계 ÷ 경과일 × 월 일수)이다. "예상"이라고 표시하는 값이다.
type Forecast struct {
	ElapsedDays  int   `json:"elapsedDays"`
	DaysInMonth  int   `json:"daysInMonth"`
	CostMicroUSD int64 `json:"costMicroUsd"`
	CostKRW      int64 `json:"costKrw"`
}

// UserTotal 은 부서 안 사용자별 합계다(이름만, 프롬프트·응답은 어디에도 없다).
type UserTotal struct {
	Name         string `json:"name"`
	Calls        int64  `json:"calls"`
	CostMicroUSD int64  `json:"costMicroUsd"`
	CostKRW      int64  `json:"costKrw"`
}

// MyDepartment 는 호출자 본인 부서의 월 결과다(부서장 화면). 타 부서는 볼 수 없다(cost:read 가 있어도 이 경로는 본인 부서다. 전사는 Monthly).
// 조회는 감사에 남는다(ai_cost.view_dept: 대상=부서, 금액은 쓰지 않는다).
func (s *Service) MyDepartment(ctx context.Context, month string) (MyReport, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return MyReport{}, err
	}
	if p.DepartmentID <= 0 {
		return MyReport{}, fmt.Errorf("%w: 소속 부서가 없어 AI 비용을 볼 수 없습니다", contract.ErrNotFound)
	}
	month, err = s.normMonth(month)
	if err != nil {
		return MyReport{}, err
	}
	cond, args := "u.department_id = ?", []any{p.DepartmentID}
	rep, err := s.build(ctx, month, cond, args, false)
	if err != nil {
		return MyReport{}, err
	}
	out := MyReport{Month: month, DepartmentID: p.DepartmentID, FxUsdKrwX1000: rep.FxUsdKrwX1000, TotalMicro: rep.TotalMicro, TotalKRW: rep.TotalKRW, Calls: rep.Calls,
		Rows: rep.Rows, Warnings: rep.Warnings, Trend: []TrendPoint{}, TopUsers: []UserTotal{}}
	if len(rep.Departments) > 0 {
		out.DepartmentName = rep.Departments[0].DepartmentName
	}
	if out.DepartmentName == "" {
		name, err := s.deptName(ctx, p.DepartmentID)
		if err != nil {
			return MyReport{}, err
		}
		out.DepartmentName = name
	}
	for i := 11; i >= 0; i-- {
		m := prevMonth(month, i)
		fx, err := s.fxFor(ctx, m)
		if err != nil {
			return MyReport{}, err
		}
		rows, err := s.aggregate(ctx, m, cond, args, fx)
		if err != nil {
			return MyReport{}, err
		}
		pt := TrendPoint{Month: m}
		for _, r := range rows {
			pt.Calls += r.Calls
			pt.CostMicroUSD += r.CostMicroUSD
		}
		pt.CostKRW = microToKRW(pt.CostMicroUSD, fx)
		out.Trend = append(out.Trend, pt)
	}
	if month == s.currentMonth() {
		now := s.now().In(contract.KST)
		days := daysIn(month)
		elapsed := now.Day()
		if elapsed < 1 {
			elapsed = 1
		}
		f := &Forecast{ElapsedDays: elapsed, DaysInMonth: days}
		f.CostMicroUSD = out.TotalMicro * int64(days) / int64(elapsed)
		f.CostKRW = microToKRW(f.CostMicroUSD, rep.FxUsdKrwX1000)
		out.Forecast = f
	}
	from, to, _ := monthRange(month)
	urows, err := s.o.DB.QueryContext(ctx, `SELECT COALESCE(us.name, ''), COUNT(*), COALESCE(SUM(u.cost_micro_usd), 0) FROM llm_usage u LEFT JOIN users us ON us.id = u.user_id
		WHERE u.at >= ? AND u.at < ? AND u.status = 'ok' AND u.department_id = ? GROUP BY u.user_id, us.name ORDER BY 3 DESC, 2 DESC LIMIT 5`, from, to, p.DepartmentID)
	if err != nil {
		return MyReport{}, err
	}
	for urows.Next() {
		var u UserTotal
		if err := urows.Scan(&u.Name, &u.Calls, &u.CostMicroUSD); err != nil {
			urows.Close()
			return MyReport{}, err
		}
		u.CostKRW = microToKRW(u.CostMicroUSD, rep.FxUsdKrwX1000)
		out.TopUsers = append(out.TopUsers, u)
	}
	if err := urows.Close(); err != nil {
		return MyReport{}, err
	}
	if b, err := s.budgetFor(ctx, month, p.DepartmentID); err != nil {
		return MyReport{}, err
	} else {
		out.Budget = b
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.view_dept", TargetType: audit.TargetAICost, TargetID: fmt.Sprint(p.DepartmentID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: month}}})
	})
	if err != nil {
		return MyReport{}, err
	}
	return out, nil
}

// deptName 은 부서(조직 단위) 이름이다. 조직 단위의 가장 최근 구간 이름, 없으면 레거시 부서 표.
func (s *Service) deptName(ctx context.Context, id int64) (string, error) {
	var name string
	err := s.o.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT s.name FROM org_unit_spans s WHERE s.unit_id = ? ORDER BY s.valid_from DESC LIMIT 1),
		(SELECT d.name FROM departments d WHERE d.id = ?), '')`, id, id).Scan(&name)
	return deptLabel(id, name), err
}

func daysIn(month string) int {
	t, err := time.ParseInLocation("2006-01", month, contract.KST)
	if err != nil {
		return 30
	}
	return t.AddDate(0, 1, -1).Day()
}

package aicost

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ToolSummary 는 MCP 도구 get_ai_cost_summary 의 서비스다(명세 13절). 읽기 전용이고 숫자만 돌려준다(사용자·프롬프트·토큰 없음).
// 부서를 주지 않으면 본인 부서, 다른 부서는 cost:read 가 있어야 하며(법인 범위 안) 이름이 정확히 같은 부서만 찾는다.
// 도구 호출은 MCP 가드가 감사에 남기므로 여기서는 view_dept 를 따로 남기지 않는다.
func (s *Service) ToolSummary(ctx context.Context, month, department string) (map[string]any, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return nil, err
	}
	month, err = s.normMonth(month)
	if err != nil {
		return nil, err
	}
	dept, name := p.DepartmentID, ""
	department = strings.TrimSpace(department)
	if department != "" {
		var found int64
		rows, err := s.o.DB.QueryContext(ctx, `SELECT id, name FROM (
			SELECT s.unit_id AS id, s.name AS name FROM org_unit_spans s WHERE s.valid_from = (SELECT MAX(x.valid_from) FROM org_unit_spans x WHERE x.unit_id = s.unit_id)
			UNION ALL
			SELECT d.id, d.name FROM departments d WHERE NOT EXISTS (SELECT 1 FROM org_unit_spans x WHERE x.unit_id = d.id)) t WHERE name = ?`, department)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var n string
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			found = id
			name = n
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if found == 0 {
			return nil, fmt.Errorf("%w: 그 이름의 부서를 찾을 수 없습니다", contract.ErrNotFound)
		}
		if found != p.DepartmentID {
			scope, err := contract.ReadScope(ctx, contract.PermCostRead) // 다른 부서는 전사 권한(cost:read)과 법인 범위가 필요하다
			if err != nil || !scope.Has(s.unitEntity(ctx, s.today(), found)) {
				return nil, fmt.Errorf("%w: 다른 부서의 AI 비용은 볼 수 없습니다", contract.ErrForbidden)
			}
		}
		dept = found
	}
	if dept <= 0 {
		return nil, fmt.Errorf("%w: 소속 부서가 없어 AI 비용을 볼 수 없습니다", contract.ErrNotFound)
	}
	rep, err := s.build(ctx, month, "u.department_id = ?", []any{dept}, false)
	if err != nil {
		return nil, err
	}
	if name == "" {
		if name, err = s.deptName(ctx, dept); err != nil {
			return nil, err
		}
	}
	type bp struct {
		purpose string
		calls   int64
		micro   int64
		krw     int64
	}
	order := []string{}
	by := map[string]*bp{}
	for _, r := range rep.Rows {
		b := by[r.Purpose]
		if b == nil {
			b = &bp{purpose: r.Purpose}
			by[r.Purpose] = b
			order = append(order, r.Purpose)
		}
		b.calls += r.Calls
		b.micro += r.CostMicroUSD
		b.krw += r.CostKRW
	}
	purposes := []map[string]any{}
	for _, k := range order {
		b := by[k]
		purposes = append(purposes, map[string]any{"purpose": b.purpose, "calls": b.calls, "cost_usd_micro": b.micro, "cost_krw": b.krw})
	}
	return map[string]any{
		"department": name, "month": month, "calls": rep.Calls, "cost_usd_micro": rep.TotalMicro, "cost_krw": rep.TotalKRW,
		"fx_set": rep.FxUsdKrwX1000 > 0, "by_purpose": purposes,
	}, nil
}

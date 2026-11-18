package aicost

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// PreviewLine 은 부서별 배부 결과 한 줄이다.
type PreviewLine struct {
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	Headcount      int64  `json:"headcount"`
	Calls          int64  `json:"calls"`
	DirectKRW      int64  `json:"directKrw"`
	SharedKRW      int64  `json:"sharedKrw"`
	TotalKRW       int64  `json:"totalKrw"`
	DeltaKRW       *int64 `json:"deltaKrw,omitempty"` // 비교 버전 대비 증감(비교를 요청했을 때만)
}

// ProviderAdjust 는 실제 청구액으로 보정한 공급자다.
type ProviderAdjust struct {
	Provider  string `json:"provider"`
	ActualKRW int64  `json:"actualKrw"`
}

// Preview 는 월·법인 하나의 배부 미리보기다(저장하지 않는다). Blockers 가 있으면 3단계의 확정이 거부된다.
type Preview struct {
	Month          string `json:"month"`
	EntityID       int64  `json:"entityId"`
	RuleVersion    int    `json:"ruleVersion"`
	CompareVersion int    `json:"compareVersion,omitempty"`
	FxUsdKrwX1000  int64  `json:"fxUsdKrwX1000"`
	TotalKRW       int64  `json:"totalKrw"`
	DirectKRW      int64  `json:"directKrw"`
	SharedKRW      int64  `json:"sharedKrw"`
	// GroupShareKRW 는 법인 간 공용(그룹) 풀에서 이 법인이 받은 몫이다. 이 법인이 기본 법인이 아니면 지급 법인(기본 법인)을 상대로 한 "법인 간 거래 표시"이며
	// 상계·연결 조정은 하지 않는다(B44 D12). CounterpartyEntityID 는 그 상대 법인(없으면 0).
	GroupShareKRW        int64            `json:"groupShareKrw"`
	CounterpartyEntityID int64            `json:"counterpartyEntityId,omitempty"`
	Lines                []PreviewLine    `json:"lines"`
	Pools                []PoolResult     `json:"pools"`
	Adjusted             []ProviderAdjust `json:"adjusted"`
	Blockers             []string         `json:"blockers"`
	Warnings             []string         `json:"warnings"`
}

// PreviewInput 은 미리보기 요청이다. EntityID 0 은 현재 법인(없으면 기본 법인), Version 0 은 그 달에 적용되는 버전, Compare 는 비교할 다른 버전(선택)이다.
type PreviewInput struct {
	Month    string
	EntityID int64
	Version  int
	Compare  int
}

func lastDay(month string) string {
	t, err := time.ParseInLocation("2006-01", month, contract.KST)
	if err != nil {
		return month + "-28"
	}
	return t.AddDate(0, 1, -1).Format("2006-01-02")
}

// headcounts 는 날짜(월말) 기준 법인의 부서(주소속 조직 단위)별 인원이다.
func (s *Service) headcounts(ctx context.Context, day string, entity int64) (map[int64]int64, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT p.primary_unit_id, COUNT(*) FROM org_person_spans p
		JOIN org_unit_spans s ON s.unit_id = p.primary_unit_id AND s.valid_from <= ? AND (s.valid_to IS NULL OR s.valid_to > ?)
		WHERE p.left_flag = 0 AND p.valid_from <= ? AND (p.valid_to IS NULL OR p.valid_to > ?) AND s.entity_id = ?
		GROUP BY p.primary_unit_id`, day, day, day, day, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var d, n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, rows.Err()
}

// unitEntity 는 날짜 기준 조직 단위의 법인이다. 조직 단위가 없으면(레거시 부서) 기본 법인.
func (s *Service) unitEntity(ctx context.Context, day string, dept int64) int64 {
	var e int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT entity_id FROM org_unit_spans WHERE unit_id = ? AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?) ORDER BY valid_from DESC LIMIT 1`, dept, day, day).Scan(&e)
	if err != nil || e == 0 {
		return contract.DefaultEntityID
	}
	return e
}

// scopedEntity 는 요청한 법인이 호출자의 읽기 범위 안인지 확인하고 쓸 법인 번호를 돌려준다.
func (s *Service) scopedEntity(ctx context.Context, want int64) (int64, error) {
	scope, err := contract.ReadScope(ctx, contract.PermCostRead)
	if err != nil {
		return 0, err
	}
	e := want
	if e <= 0 {
		e = contract.WriteEntityID(ctx)
	}
	if !scope.Has(e) {
		return 0, fmt.Errorf("%w: 그 법인의 AI 비용은 볼 수 없습니다", contract.ErrForbidden)
	}
	return e, nil
}

// PreviewAllocation 은 월·법인의 배부 결과를 규칙 버전으로 계산해 돌려준다(cost:read). 저장하지 않고 요청마다 다시 계산한다.
func (s *Service) PreviewAllocation(ctx context.Context, in PreviewInput) (Preview, error) {
	entity, err := s.scopedEntity(ctx, in.EntityID)
	if err != nil {
		return Preview{}, err
	}
	month, err := s.normMonth(in.Month)
	if err != nil {
		return Preview{}, err
	}
	pv, byDept, err := s.computePreview(ctx, month, entity, in.Version)
	if err != nil {
		return Preview{}, err
	}
	if in.Compare > 0 && in.Compare != pv.RuleVersion {
		other, otherBy, err := s.computePreview(ctx, month, entity, in.Compare)
		if err != nil {
			return Preview{}, err
		}
		pv.CompareVersion = other.RuleVersion
		seen := map[int64]bool{}
		for i := range pv.Lines {
			l := &pv.Lines[i]
			seen[l.DepartmentID] = true
			d := l.TotalKRW - otherBy[l.DepartmentID]
			l.DeltaKRW = &d
		}
		for dep, krw := range otherBy { // 비교 버전에만 있는 부서(이 버전에서는 0)
			if !seen[dep] && krw != 0 {
				name, _ := s.deptName(ctx, dep)
				d := -krw
				pv.Lines = append(pv.Lines, PreviewLine{DepartmentID: dep, DepartmentName: name, DeltaKRW: &d})
			}
		}
		sort.Slice(pv.Lines, func(i, j int) bool { return pv.Lines[i].DepartmentID < pv.Lines[j].DepartmentID })
		_ = byDept
	}
	return pv, nil
}

type usageAgg struct {
	entity   int64
	dept     int64
	purpose  string
	provider string
	micro    int64
	calls    int64
}

func (s *Service) computePreview(ctx context.Context, month string, entity int64, version int) (Preview, map[int64]int64, error) {
	pv := Preview{Month: month, EntityID: entity, Lines: []PreviewLine{}, Pools: []PoolResult{}, Adjusted: []ProviderAdjust{}, Blockers: []string{}, Warnings: []string{}}
	rs, err := s.ruleSetFor(ctx, month, version)
	if err != nil {
		if version == 0 {
			pv.Blockers = append(pv.Blockers, "적용할 배부 규칙이 없습니다. 규칙 화면에서 기본 규칙을 만드세요.")
			return pv, map[int64]int64{}, nil
		}
		return Preview{}, nil, err
	}
	pv.RuleVersion = rs.Version
	fx, err := s.fxFor(ctx, month)
	if err != nil {
		return Preview{}, nil, err
	}
	pv.FxUsdKrwX1000 = fx
	if fx == 0 {
		pv.Blockers = append(pv.Blockers, "이 달 환율이 필요합니다. 설정에서 입력한 뒤 확정하세요.")
		return pv, map[int64]int64{}, nil
	}
	from, to, err := monthRange(month)
	if err != nil {
		return Preview{}, nil, err
	}
	// 법인 안의 직접 집계(법인 열 0 은 기본 법인) 와 설치 전체의 공급자별 합(청구액 보정의 기준).
	// 모든 법인의 집계를 한 번에 읽는다(그룹 풀은 설치 전체 금액을 법인 사이에 나눠야 한다). 이 법인의 줄만 법인 계산에 넣는다.
	rows, err := s.o.DB.QueryContext(ctx, `SELECT COALESCE(NULLIF(u.entity_id, 0), 1), u.department_id, u.purpose, u.provider, COALESCE(SUM(u.cost_micro_usd), 0), COUNT(*) FROM llm_usage u
		WHERE u.at >= ? AND u.at < ? AND u.status = 'ok'
		GROUP BY COALESCE(NULLIF(u.entity_id, 0), 1), u.department_id, u.purpose, u.provider ORDER BY 1, u.department_id, u.purpose, u.provider`, from, to)
	if err != nil {
		return Preview{}, nil, err
	}
	var aggs []usageAgg
	for rows.Next() {
		var a usageAgg
		if err := rows.Scan(&a.entity, &a.dept, &a.purpose, &a.provider, &a.micro, &a.calls); err != nil {
			rows.Close()
			return Preview{}, nil, err
		}
		aggs = append(aggs, a)
	}
	if err := rows.Close(); err != nil {
		return Preview{}, nil, err
	}
	instMicro := map[string]int64{}
	prow, err := s.o.DB.QueryContext(ctx, `SELECT provider, COALESCE(SUM(cost_micro_usd), 0) FROM llm_usage WHERE at >= ? AND at < ? AND status = 'ok' GROUP BY provider`, from, to)
	if err != nil {
		return Preview{}, nil, err
	}
	for prow.Next() {
		var p string
		var m int64
		if err := prow.Scan(&p, &m); err != nil {
			prow.Close()
			return Preview{}, nil, err
		}
		instMicro[p] = m
	}
	if err := prow.Close(); err != nil {
		return Preview{}, nil, err
	}
	adjs, err := s.latestAdjustments(ctx, month)
	if err != nil {
		return Preview{}, nil, err
	}
	actual := map[string]int64{}
	for _, a := range adjs {
		actual[a.Provider] = a.ActualKRW
	}
	fxRat := big.NewRat(fx, 1_000_000_000)
	crows := make([]calcRow, 0, len(aggs))
	var grows []groupRow
	calls := map[int64]int64{}
	adjusted := map[string]bool{}
	for _, a := range aggs {
		var amt *big.Rat
		if act, ok := actual[a.provider]; ok && instMicro[a.provider] > 0 {
			amt = new(big.Rat).Mul(big.NewRat(a.micro, 1), big.NewRat(act, instMicro[a.provider]))
			adjusted[a.provider] = true
		} else {
			amt = new(big.Rat).Mul(big.NewRat(a.micro, 1), fxRat)
		}
		row := calcRow{Dept: a.dept, Purpose: a.purpose, Provider: a.provider, Amount: amt}
		grows = append(grows, groupRow{Entity: a.entity, calcRow: row})
		if a.entity != entity {
			continue
		}
		calls[a.dept] += a.calls
		crows = append(crows, row)
	}
	for p := range adjusted {
		pv.Adjusted = append(pv.Adjusted, ProviderAdjust{Provider: p, ActualKRW: actual[p]})
	}
	sort.Slice(pv.Adjusted, func(i, j int) bool { return pv.Adjusted[i].Provider < pv.Adjusted[j].Provider })
	day := lastDay(month)
	hc, err := s.headcounts(ctx, day, entity)
	if err != nil {
		return Preview{}, nil, err
	}
	entityOf := map[int64]bool{}
	// 법인 간 공용(그룹) 풀: 법인이 둘 이상이면 설치 전체 금액을 법인 사이에 먼저 나누고 이 법인의 몫만 법인 안 풀로 넣는다. 법인이 하나뿐이면 법인 안 풀로 다룬다.
	pools := append([]PoolInput{}, rs.Pools...)
	entities, err := s.activeEntities(ctx)
	if err != nil {
		return Preview{}, nil, err
	}
	var injected []injectedPool
	var groupShare int64
	hasGroup := false
	for _, p := range pools {
		hasGroup = hasGroup || p.Scope == ScopeGroup
	}
	if hasGroup && len(entities) <= 1 {
		for i := range pools {
			if pools[i].Scope != ScopeGroup {
				continue
			}
			pools[i].Scope = ScopeEntity
			if pools[i].Driver == DriverFixed {
				pools[i].Driver, pools[i].TargetsAll, pools[i].Targets = DriverHeadcount, true, nil
			}
		}
		pv.Warnings = append(pv.Warnings, "법인이 하나뿐이라 법인 간 공용 풀을 법인 안 풀로 처리했습니다.")
	} else if hasGroup {
		ehc := map[int64]int64{}
		for _, e := range entities {
			m, err := s.headcounts(ctx, day, e)
			if err != nil {
				return Preview{}, nil, err
			}
			for _, c := range m {
				ehc[e] += c
			}
		}
		gr := splitGroupPools(groupInput{Rows: grows, Pools: pools, Entities: entities, EntityHC: ehc})
		pv.Blockers = append(pv.Blockers, gr.Blockers...)
		pv.Warnings = append(pv.Warnings, gr.Warnings...)
		for i, p := range pools {
			share := gr.Shares[i][entity]
			if p.Scope != ScopeGroup || share <= 0 {
				continue
			}
			local := p
			local.Name = p.Name
			local.Driver = DriverHeadcount
			if p.Driver == DriverUsage {
				local.Driver = DriverUsage
			}
			local.TargetsAll, local.Targets = true, nil
			injected = append(injected, injectedPool{Seq: i + 1, Pool: local, Amount: big.NewRat(share, 1)})
			groupShare += share
		}
	}
	pv.GroupShareKRW = groupShare
	if groupShare > 0 && entity != contract.DefaultEntityID {
		pv.CounterpartyEntityID = contract.DefaultEntityID
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("법인 간 거래 표시: 기본 법인이 대신 지급한 공용 비용 %s원이 포함되어 있습니다(표시만 하고 상계·연결 조정은 하지 않습니다).", thousandsKRW(groupShare)))
	}
	res := calculate(calcInput{Rows: crows, Pools: pools, Headcount: hc, Injected: injected, InEntity: func(d int64) bool {
		if v, ok := entityOf[d]; ok {
			return v
		}
		v := s.unitEntity(ctx, day, d) == entity
		entityOf[d] = v
		return v
	}})
	pv.TotalKRW, pv.DirectKRW, pv.SharedKRW = res.TotalKRW, res.DirectKRW, res.SharedKRW
	pv.Blockers = append(pv.Blockers, res.Blockers...)
	pv.Warnings = append(pv.Warnings, res.Warnings...)
	byDept := map[int64]int64{}
	names := map[int64]string{}
	nameOf := func(d int64) string {
		if n, ok := names[d]; ok {
			return n
		}
		n, _ := s.deptName(ctx, d)
		names[d] = n
		return n
	}
	for _, l := range res.Lines {
		byDept[l.Dept] = l.TotalKRW
		pv.Lines = append(pv.Lines, PreviewLine{DepartmentID: l.Dept, DepartmentName: nameOf(l.Dept), Headcount: hc[l.Dept], Calls: calls[l.Dept],
			DirectKRW: l.DirectKRW, SharedKRW: l.SharedKRW, TotalKRW: l.TotalKRW})
	}
	for i := range res.Pools {
		for j := range res.Pools[i].Shares {
			res.Pools[i].Shares[j].Name = nameOf(res.Pools[i].Shares[j].DepartmentID)
		}
	}
	pv.Pools = res.Pools
	return pv, byDept, nil
}

package aicost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 배부 규칙 세트(명세 7절). 풀은 공용 비용을 정의하고, 풀에 안 들어간 사용 행은 모두 직접 비용(요청 시점 부서)이다.
// 규칙 세트는 불변 버전이다(트리거). 새 버전은 적용 시작 월부터 쓰이고, 월의 배부는 시작 월이 그 달 이하인 가장 큰 버전을 쓴다.

const (
	DriverHeadcount = "headcount"
	DriverUsage     = "usage"
	DriverFixed     = "fixed"

	ScopeEntity = "entity" // 법인 안에서 부서에 나눈다(기본)
	ScopeGroup  = "group"  // 법인 간 공용: 설치 전체 금액을 먼저 법인 사이에 나누고, 각 법인 안에서 부서에 나눈다

	maxPools   = 10
	maxTargets = 200
)

// Selector 는 풀에 넣을 사용 행의 조건이다. 값이 있는 칸은 모두 맞아야 하고(그리고), 칸 안의 목록은 하나만 맞으면 된다.
type Selector struct {
	Purposes       []string `json:"purposes,omitempty"`
	Providers      []string `json:"providers,omitempty"`
	UnassignedDept bool     `json:"unassignedDept,omitempty"` // 부서 번호 0(배치·API·시스템 호출)
}

func (s Selector) empty() bool {
	return len(s.Purposes) == 0 && len(s.Providers) == 0 && !s.UnassignedDept
}

func (s Selector) matches(dept int64, purpose, provider string) bool {
	if s.UnassignedDept && dept != 0 {
		return false
	}
	if len(s.Purposes) > 0 && !contains(s.Purposes, purpose) {
		return false
	}
	if len(s.Providers) > 0 && !contains(s.Providers, provider) {
		return false
	}
	return true
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// PoolTarget 은 풀의 대상 부서다. WeightPct 는 driver 가 fixed 일 때만 쓴다.
type PoolTarget struct {
	DepartmentID int64 `json:"departmentId"`
	WeightPct    int   `json:"weightPct,omitempty"`
}

// PoolInput 은 풀 한 개의 입력이자 저장 모양이다.
type PoolInput struct {
	Name       string       `json:"name"`
	Selector   Selector     `json:"selector"`
	Driver     string       `json:"driver"`
	Scope      string       `json:"scope,omitempty"`      // entity(기본)|group. group 은 법인이 둘 이상일 때만 뜻이 있다
	TargetsAll bool         `json:"targetsAll,omitempty"` // 법인 안 풀: 그 법인의 모든 부서(그 달 인원이나 직접 비용이 있는 부서). 그룹 풀: 모든 법인
	Targets    []PoolTarget `json:"targets,omitempty"`    // 그룹 풀의 고정 비율은 DepartmentID 칸이 법인 번호다
}

// RuleSetInput 은 새 규칙 세트 버전의 입력이다.
type RuleSetInput struct {
	ValidFromMonth string      `json:"validFromMonth"`
	Note           string      `json:"note"`
	Pools          []PoolInput `json:"pools"`
}

// RuleSet 은 저장된 규칙 세트 한 버전이다.
type RuleSet struct {
	Version        int         `json:"version"`
	ValidFromMonth string      `json:"validFromMonth"`
	Note           string      `json:"note"`
	CreatedByName  string      `json:"createdByName"`
	CreatedAt      string      `json:"createdAt"`
	Pools          []PoolInput `json:"pools"`
	CreatedBy      int64       `json:"-"` // 규칙을 저장한 사람(권한 분리 판정용)
}

// RuleSetSummary 는 목록 한 줄이다.
type RuleSetSummary struct {
	Version        int    `json:"version"`
	ValidFromMonth string `json:"validFromMonth"`
	Note           string `json:"note"`
	CreatedByName  string `json:"createdByName"`
	CreatedAt      string `json:"createdAt"`
	PoolCount      int    `json:"poolCount"`
}

// SaveResult 는 저장 결과(번호와 경고)다.
type SaveResult struct {
	Version  int      `json:"version"`
	Warnings []string `json:"warnings"`
}

// DefaultRules 는 설치 때 "기본 규칙 만들기" 가 쓰는 규칙이다(명세 7.1): 평가 실행은 직접 비용 비율로, 부서 미지정 비용은 인원 비율로 전체 부서에 나눈다.
func DefaultRules() RuleSetInput {
	return RuleSetInput{ValidFromMonth: "2000-01", Note: "기본 규칙", Pools: []PoolInput{
		{Name: "평가·점검", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, TargetsAll: true},
		{Name: "부서 미지정", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true},
	}}
}

func (s *Service) validatePools(ctx context.Context, in RuleSetInput) ([]string, error) {
	if !reMonth.MatchString(in.ValidFromMonth) {
		return nil, valErr("적용 시작 월은 YYYY-MM 형식으로 입력하세요")
	}
	if n := len([]rune(strings.TrimSpace(in.Note))); n > 200 {
		return nil, valErr("메모는 200자 이내로 쓰세요")
	}
	if len(in.Pools) == 0 || len(in.Pools) > maxPools {
		return nil, valErr("풀은 1개 이상 %d개 이하여야 합니다", maxPools)
	}
	var warns []string
	for i, p := range in.Pools {
		label := fmt.Sprintf("풀 %d", i+1)
		if n := len([]rune(strings.TrimSpace(p.Name))); n < 1 || n > 40 {
			return nil, valErr("%s: 이름은 1~40자여야 합니다", label)
		}
		label = fmt.Sprintf("풀 '%s'", strings.TrimSpace(p.Name))
		if p.Selector.empty() {
			return nil, valErr("%s: 풀에 넣을 사용 조건(용도·공급자·부서 미지정)이 하나는 있어야 합니다", label)
		}
		for _, v := range append(append([]string{}, p.Selector.Purposes...), p.Selector.Providers...) {
			if n := len([]rune(v)); n < 1 || n > 60 {
				return nil, valErr("%s: 용도·공급자 이름이 올바르지 않습니다", label)
			}
		}
		switch p.Driver {
		case DriverHeadcount, DriverUsage, DriverFixed:
		default:
			return nil, valErr("%s: 배분 기준은 인원 비율·사용량 비율·고정 비율 중 하나여야 합니다", label)
		}
		switch p.Scope {
		case "", ScopeEntity:
		case ScopeGroup:
			if p.Driver == DriverFixed && p.TargetsAll {
				return nil, valErr("%s: 고정 비율은 대상 법인을 직접 지정해야 합니다", label)
			}
			if p.Driver != DriverFixed && !p.TargetsAll {
				return nil, valErr("%s: 법인 간 공용 풀은 전체 법인에 나누거나 고정 비율로 법인을 지정해야 합니다", label)
			}
		default:
			return nil, valErr("%s: 범위는 법인 안과 법인 간 공용 중 하나여야 합니다", label)
		}
		if p.TargetsAll && len(p.Targets) > 0 {
			return nil, valErr("%s: 전체 부서와 지정 부서를 함께 쓸 수 없습니다", label)
		}
		if !p.TargetsAll && len(p.Targets) == 0 {
			return nil, valErr("%s에 대상 부서가 없습니다.", label)
		}
		if p.Driver == DriverFixed && p.TargetsAll {
			return nil, valErr("%s: 고정 비율은 대상 부서를 직접 지정해야 합니다", label)
		}
		if len(p.Targets) > maxTargets {
			return nil, valErr("%s: 대상 부서가 너무 많습니다", label)
		}
		seen := map[int64]bool{}
		sum := 0
		for _, t := range p.Targets {
			if t.DepartmentID <= 0 || seen[t.DepartmentID] {
				return nil, valErr("%s: 대상 부서가 올바르지 않거나 겹칩니다", label)
			}
			seen[t.DepartmentID] = true
			if p.Scope == ScopeGroup {
				if !s.entityExists(ctx, t.DepartmentID) {
					return nil, valErr("%s: 없는 법인이 대상에 들어 있습니다", label)
				}
			} else if !s.deptExists(ctx, t.DepartmentID) {
				return nil, valErr("%s: 없는 부서가 대상에 들어 있습니다", label)
			}
			if p.Driver == DriverFixed {
				if t.WeightPct < 1 || t.WeightPct > 100 {
					return nil, valErr("%s: 고정 비율은 부서마다 1~100 이어야 합니다", label)
				}
				sum += t.WeightPct
			} else if t.WeightPct != 0 {
				return nil, valErr("%s: 비율은 고정 비율 풀에서만 입력합니다", label)
			}
		}
		if p.Driver == DriverFixed && sum != 100 {
			return nil, valErr("대상 비율의 합이 100 이 아닙니다.")
		}
	}
	// 겹침 경고: 같은 용도(또는 둘 다 용도 조건이 없는 공급자 조건)가 두 풀에 걸리면 앞의 풀이 이긴다.
	for i := range in.Pools {
		for j := i + 1; j < len(in.Pools); j++ {
			a, b := in.Pools[i].Selector, in.Pools[j].Selector
			if overlaps(a, b) {
				warns = append(warns, fmt.Sprintf("풀 '%s' 과 '%s' 의 사용 조건이 겹칩니다. 겹치는 호출은 앞의 풀에 들어갑니다.", strings.TrimSpace(in.Pools[i].Name), strings.TrimSpace(in.Pools[j].Name)))
			}
		}
	}
	return warns, nil
}

// overlaps 는 두 풀이 같은 용도나 같은 공급자를 명시적으로 함께 고르는지 본다(한쪽이 조건을 비워 둔 칸은 "전부"라 겹침으로 세지 않는다. 앞의 풀이 먼저 가져간다).
func overlaps(a, b Selector) bool {
	if len(a.Purposes) > 0 && len(b.Purposes) > 0 && intersects(a.Purposes, b.Purposes) {
		return true
	}
	return len(a.Providers) > 0 && len(b.Providers) > 0 && intersects(a.Providers, b.Providers)
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

// entityExists 는 사용 중인 법인이 있는지 본다.
func (s *Service) entityExists(ctx context.Context, id int64) bool {
	var n int
	_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE id = ? AND active = 1`, id).Scan(&n)
	return n > 0
}

// deptExists 는 부서(조직 단위 번호 또는 레거시 부서 번호)가 있는지 본다.
func (s *Service) deptExists(ctx context.Context, id int64) bool {
	var n int
	_ = s.o.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM org_unit_spans WHERE unit_id = ?) + (SELECT COUNT(*) FROM departments WHERE id = ?)`, id, id).Scan(&n)
	return n > 0
}

// SaveRuleSet 은 새 규칙 세트 버전을 만든다(cost:allocate). 버전은 1부터 이어서 오른다.
func (s *Service) SaveRuleSet(ctx context.Context, in RuleSetInput) (SaveResult, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return SaveResult{}, err
	}
	warns, err := s.validatePools(ctx, in)
	if err != nil {
		return SaveResult{}, err
	}
	var version int
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM ai_cost_rule_sets`).Scan(&version); err != nil {
			return err
		}
		var setID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO ai_cost_rule_sets (version, valid_from_month, note, created_by, created_at) VALUES (?,?,?,?,?) RETURNING id`,
			version, in.ValidFromMonth, strings.TrimSpace(in.Note), p.UserID, s.stamp()).Scan(&setID); err != nil {
			return err
		}
		for i, pl := range in.Pools {
			sel, _ := json.Marshal(pl.Selector)
			var poolID int64
			ta := 0
			if pl.TargetsAll {
				ta = 1
			}
			if err := tx.QueryRowContext(ctx, `INSERT INTO ai_cost_rule_pools (rule_set_id, seq, name, selector_json, driver, scope, targets_all) VALUES (?,?,?,?,?,?,?) RETURNING id`,
				setID, i+1, strings.TrimSpace(pl.Name), string(sel), pl.Driver, scopeOf(pl.Scope), ta).Scan(&poolID); err != nil {
				return err
			}
			for _, t := range pl.Targets {
				if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_rule_targets (pool_id, department_id, weight_pct) VALUES (?,?,?)`, poolID, t.DepartmentID, t.WeightPct); err != nil {
					return err
				}
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.ruleset_save", TargetType: audit.TargetAICost, TargetID: "rules/v" + strconv.Itoa(version), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "version", New: strconv.Itoa(version)}, {Key: "valid_from_month", New: in.ValidFromMonth}, {Key: "pool_count", New: strconv.Itoa(len(in.Pools))}}})
	})
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Version: version, Warnings: append([]string{}, warns...)}, nil
}

// CreateDefaultRules 는 규칙이 하나도 없을 때만 기본 규칙(v1)을 만든다(cost:allocate).
func (s *Service) CreateDefaultRules(ctx context.Context) (SaveResult, error) {
	if _, err := contract.Authorize(ctx, contract.PermCostAllocate); err != nil {
		return SaveResult{}, err
	}
	var n int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_rule_sets`).Scan(&n); err != nil {
		return SaveResult{}, err
	}
	if n > 0 {
		return SaveResult{}, fmt.Errorf("%w: 이미 배부 규칙이 있습니다.", contract.ErrInvalidState)
	}
	return s.SaveRuleSet(ctx, DefaultRules())
}

// ListRuleSets 는 규칙 세트 버전 목록(최신 먼저)이다(cost:read).
func (s *Service) ListRuleSets(ctx context.Context) ([]RuleSetSummary, error) {
	if _, err := contract.Authorize(ctx, contract.PermCostRead); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT r.version, r.valid_from_month, r.note, COALESCE(u.name, ''), r.created_at, (SELECT COUNT(*) FROM ai_cost_rule_pools p WHERE p.rule_set_id = r.id)
		FROM ai_cost_rule_sets r LEFT JOIN users u ON u.id = r.created_by ORDER BY r.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleSetSummary{}
	for rows.Next() {
		var r RuleSetSummary
		if err := rows.Scan(&r.Version, &r.ValidFromMonth, &r.Note, &r.CreatedByName, &r.CreatedAt, &r.PoolCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRuleSet 은 한 버전의 전체 내용이다(cost:read).
func (s *Service) GetRuleSet(ctx context.Context, version int) (RuleSet, error) {
	if _, err := contract.Authorize(ctx, contract.PermCostRead); err != nil {
		return RuleSet{}, err
	}
	return s.loadRuleSet(ctx, `r.version = ?`, version)
}

// ruleSetFor 는 월에 적용되는 규칙 세트(시작 월이 그 달 이하인 가장 큰 버전)다. 없으면 ErrNotFound.
func (s *Service) ruleSetFor(ctx context.Context, month string, version int) (RuleSet, error) {
	if version > 0 {
		return s.loadRuleSet(ctx, `r.version = ?`, version)
	}
	return s.loadRuleSet(ctx, `r.version = (SELECT MAX(version) FROM ai_cost_rule_sets WHERE valid_from_month <= ?)`, month)
}

func (s *Service) loadRuleSet(ctx context.Context, cond string, arg any) (RuleSet, error) {
	var rs RuleSet
	var id int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT r.id, r.version, r.valid_from_month, r.note, COALESCE(u.name, ''), r.created_at, r.created_by
		FROM ai_cost_rule_sets r LEFT JOIN users u ON u.id = r.created_by WHERE `+cond, arg).Scan(&id, &rs.Version, &rs.ValidFromMonth, &rs.Note, &rs.CreatedByName, &rs.CreatedAt, &rs.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return RuleSet{}, fmt.Errorf("%w: 적용할 배부 규칙이 없습니다", contract.ErrNotFound)
	}
	if err != nil {
		return RuleSet{}, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id, name, selector_json, driver, targets_all, scope FROM ai_cost_rule_pools WHERE rule_set_id = ? ORDER BY seq`, id)
	if err != nil {
		return RuleSet{}, err
	}
	type pr struct {
		id int64
		p  PoolInput
	}
	var pools []pr
	for rows.Next() {
		var x pr
		var sel string
		var ta int
		if err := rows.Scan(&x.id, &x.p.Name, &sel, &x.p.Driver, &ta, &x.p.Scope); err != nil {
			rows.Close()
			return RuleSet{}, err
		}
		x.p.TargetsAll = ta == 1
		if err := json.Unmarshal([]byte(sel), &x.p.Selector); err != nil {
			rows.Close()
			return RuleSet{}, err
		}
		pools = append(pools, x)
	}
	if err := rows.Close(); err != nil {
		return RuleSet{}, err
	}
	rs.Pools = []PoolInput{}
	for _, x := range pools {
		trows, err := s.o.DB.QueryContext(ctx, `SELECT department_id, weight_pct FROM ai_cost_rule_targets WHERE pool_id = ? ORDER BY department_id`, x.id)
		if err != nil {
			return RuleSet{}, err
		}
		for trows.Next() {
			var t PoolTarget
			if err := trows.Scan(&t.DepartmentID, &t.WeightPct); err != nil {
				trows.Close()
				return RuleSet{}, err
			}
			x.p.Targets = append(x.p.Targets, t)
		}
		if err := trows.Close(); err != nil {
			return RuleSet{}, err
		}
		sort.Slice(x.p.Targets, func(i, j int) bool { return x.p.Targets[i].DepartmentID < x.p.Targets[j].DepartmentID })
		rs.Pools = append(rs.Pools, x.p)
	}
	return rs, nil
}

// DeptOption 은 규칙 화면의 부서 선택 항목이다(현재 열려 있는 조직 단위).
type DeptOption struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	EntityID int64  `json:"entityId"`
}

// ListDepartments 는 호출자의 읽기 범위 안 법인의 열린 조직 단위 목록이다(cost:read). 규칙의 대상 부서를 고를 때 쓴다.
func (s *Service) ListDepartments(ctx context.Context) ([]DeptOption, error) {
	scope, err := contract.ReadScope(ctx, contract.PermCostRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT unit_id, name, entity_id FROM org_unit_spans WHERE valid_to IS NULL AND closed = 0 AND kind <> 'company' ORDER BY entity_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeptOption{}
	for rows.Next() {
		var d DeptOption
		if err := rows.Scan(&d.ID, &d.Name, &d.EntityID); err != nil {
			return nil, err
		}
		if scope.Has(d.EntityID) {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func scopeOf(s string) string {
	if s == ScopeGroup {
		return ScopeGroup
	}
	return ScopeEntity
}

// ActiveEntities 는 사용 중인 법인 번호(정렬)와 이름이다(cost:read 범위와 무관하게 설치의 법인 목록. 규칙 화면의 법인 선택에 쓴다).
func (s *Service) activeEntities(ctx context.Context) ([]int64, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id FROM entities WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EntityOption 은 규칙 화면의 법인 선택 항목이다.
type EntityOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ListEntities 는 호출자의 읽기 범위 안 사용 중인 법인이다(cost:read).
func (s *Service) ListEntities(ctx context.Context) ([]EntityOption, error) {
	scope, err := contract.ReadScope(ctx, contract.PermCostRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id, name FROM entities WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntityOption{}
	for rows.Next() {
		var e EntityOption
		if err := rows.Scan(&e.ID, &e.Name); err != nil {
			return nil, err
		}
		if scope.Has(e.ID) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

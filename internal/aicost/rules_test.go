package aicost_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// seedOrg 는 조직 단위 900(운영)·901(영업)과 월말 인원(운영 1명, 영업 2명)을 만든다. 구간 표는 조직 서비스만 쓸 수 있어 보호 행을 잠시 넣는다.
func (e *env) seedOrg() {
	e.t.Helper()
	ex := func(q string, a ...any) {
		e.t.Helper()
		if _, err := e.db.Exec(q, a...); err != nil {
			e.t.Fatalf("%v\n%s", err, q)
		}
	}
	ex(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`)
	for _, u := range []struct {
		id   int64
		code string
		name string
	}{{900, "OPS", "운영"}, {901, "SALES", "영업"}} {
		ex(`INSERT INTO org_units (id, code) VALUES (?, ?)`, u.id, u.code)
		ex(`INSERT INTO org_unit_spans (unit_id, valid_from, valid_to, code, name, kind, parent_id, sort_order, closed, head_user_id, head_acting, entity_id) VALUES (?, '2020-01-01', NULL, ?, ?, 'department', 0, 0, 0, 0, 0, 1)`, u.id, u.code, u.name)
	}
	for _, p := range []struct{ user, unit int64 }{{1, 900}, {2, 901}, {3, 901}} {
		ex(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (?, '2020-01-01', NULL, 0, 1, ?)`, p.user, p.unit)
	}
	ex(`DELETE FROM org_write_guard`)
}

func defaultInput(from string) aicost.RuleSetInput {
	in := aicost.DefaultRules()
	in.ValidFromMonth = from
	return in
}

func TestRuleSetValidation(t *testing.T) {
	e := newEnv(t)
	e.seedOrg()
	ok := func() aicost.RuleSetInput { return defaultInput("2026-11") }
	bad := map[string]func(*aicost.RuleSetInput){
		"시작 월 형식":   func(r *aicost.RuleSetInput) { r.ValidFromMonth = "2026-13" },
		"풀 없음":      func(r *aicost.RuleSetInput) { r.Pools = nil },
		"풀 이름 빔":    func(r *aicost.RuleSetInput) { r.Pools[0].Name = " " },
		"사용 조건 없음":  func(r *aicost.RuleSetInput) { r.Pools[0].Selector = aicost.Selector{} },
		"기준 이름 틀림":  func(r *aicost.RuleSetInput) { r.Pools[0].Driver = "equal" },
		"대상 없음":     func(r *aicost.RuleSetInput) { r.Pools[0].TargetsAll = false },
		"전체와 지정 함께": func(r *aicost.RuleSetInput) { r.Pools[0].Targets = []aicost.PoolTarget{{DepartmentID: 901}} },
		"없는 부서": func(r *aicost.RuleSetInput) {
			r.Pools[0].TargetsAll = false
			r.Pools[0].Targets = []aicost.PoolTarget{{DepartmentID: 4242}}
		},
		"고정은 전체 불가": func(r *aicost.RuleSetInput) { r.Pools[0].Driver = aicost.DriverFixed },
		"비율 합 99":   func(r *aicost.RuleSetInput) { fixedPool(r, 59, 40) },
		"비율 합 101":  func(r *aicost.RuleSetInput) { fixedPool(r, 61, 40) },
		"비율 0":      func(r *aicost.RuleSetInput) { fixedPool(r, 100, 0) },
		"인원 기준에 비율": func(r *aicost.RuleSetInput) {
			r.Pools[0].TargetsAll = false
			r.Pools[0].Targets = []aicost.PoolTarget{{DepartmentID: 901, WeightPct: 10}}
		},
	}
	for name, mut := range bad {
		in := ok()
		mut(&in)
		if _, err := e.svc.SaveRuleSet(finance, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부해야 한다: %v", name, err)
		}
	}
	// 비율 합이 100 이 아니면 문구가 명세와 같다.
	in := ok()
	fixedPool(&in, 59, 40)
	if _, err := e.svc.SaveRuleSet(finance, in); err == nil || !strings.Contains(err.Error(), "대상 비율의 합이 100 이 아닙니다.") {
		t.Fatalf("문구: %v", err)
	}
	in = ok()
	in.Pools[0].TargetsAll = false
	if _, err := e.svc.SaveRuleSet(finance, in); err == nil || !strings.Contains(err.Error(), "에 대상 부서가 없습니다.") {
		t.Fatalf("문구: %v", err)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_rule_sets`).Scan(&n)
	if n != 0 {
		t.Fatalf("거부된 저장이 남음: %d", n)
	}
}

func fixedPool(r *aicost.RuleSetInput, a, b int) {
	r.Pools[0] = aicost.PoolInput{Name: "고정", Selector: aicost.Selector{Purposes: []string{"eval.run"}}, Driver: aicost.DriverFixed,
		Targets: []aicost.PoolTarget{{DepartmentID: 900, WeightPct: a}, {DepartmentID: 901, WeightPct: b}}}
}

func TestRuleSetVersionsAreImmutableAndAudited(t *testing.T) {
	e := newEnv(t)
	e.seedOrg()
	if _, err := e.svc.SaveRuleSet(reader, defaultInput("2026-01")); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("cost:allocate 없이 저장: %v", err)
	}
	r1, err := e.svc.SaveRuleSet(finance, defaultInput("2026-01"))
	if err != nil || r1.Version != 1 {
		t.Fatalf("v1: %+v %v", r1, err)
	}
	in := defaultInput("2026-11")
	in.Pools = append(in.Pools, aicost.PoolInput{Name: "평가 중복", Selector: aicost.Selector{Purposes: []string{"eval.run"}}, Driver: aicost.DriverHeadcount, TargetsAll: true})
	in.Pools = append(in.Pools, aicost.PoolInput{Name: "지식 고정", Selector: aicost.Selector{Purposes: []string{"knowledge.answer"}}, Driver: aicost.DriverFixed,
		Targets: []aicost.PoolTarget{{DepartmentID: 900, WeightPct: 40}, {DepartmentID: 901, WeightPct: 60}}})
	r2, err := e.svc.SaveRuleSet(finance, in)
	if err != nil || r2.Version != 2 || len(r2.Warnings) != 1 || !strings.Contains(r2.Warnings[0], "겹칩니다") {
		t.Fatalf("v2: %+v %v", r2, err)
	}
	for _, q := range []string{`UPDATE ai_cost_rule_sets SET note = 'x'`, `DELETE FROM ai_cost_rule_sets`, `UPDATE ai_cost_rule_pools SET name = 'x'`, `DELETE FROM ai_cost_rule_pools`,
		`UPDATE ai_cost_rule_targets SET weight_pct = 1`, `DELETE FROM ai_cost_rule_targets`} {
		if _, err := e.db.Exec(q); err == nil {
			t.Fatalf("불변이어야 한다: %s", q)
		}
	}
	list, err := e.svc.ListRuleSets(reader)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[0].PoolCount != 4 || list[1].PoolCount != 2 {
		t.Fatalf("목록: %+v %v", list, err)
	}
	got, err := e.svc.GetRuleSet(reader, 2)
	if err != nil || got.Version != 2 || len(got.Pools) != 4 || got.Pools[0].Selector.Purposes[0] != "eval.run" {
		t.Fatalf("상세: %+v %v", got, err)
	}
	if _, err := e.svc.GetRuleSet(reader, 9); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 버전: %v", err)
	}
	if _, err := e.svc.ListRuleSets(manager); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("목록은 cost:read: %v", err)
	}
	if !e.rec.Has("ai_cost.ruleset_save", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("규칙 저장 감사가 없다")
	}
	for _, r := range e.rec.Records() {
		if r.Action == "ai_cost.ruleset_save" && (strings.Contains(r.Detail, "평가") || strings.Contains(r.Detail, "영업")) {
			t.Fatalf("감사에 풀 이름·부서가 있다: %+v", r)
		}
	}
	// 기본 규칙은 규칙이 없을 때만 만든다.
	e2 := newEnv(t)
	e2.seedOrg()
	if res, err := e2.svc.CreateDefaultRules(finance); err != nil || res.Version != 1 {
		t.Fatalf("기본 규칙: %+v %v", res, err)
	}
	if _, err := e2.svc.CreateDefaultRules(finance); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번째 기본 규칙: %v", err)
	}
}

func (e *env) seedUsage() {
	e.usage("2026-11-02T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 2_000_000)
	e.usage("2026-11-03T01:00:00Z", 2, 901, 1, "knowledge.answer", "anthropic-main", "ok", 10, 10, 1_000_000)
	e.usage("2026-11-04T01:00:00Z", 1, 900, 1, "eval.run", "anthropic-main", "ok", 10, 10, 500_000)
	e.usage("2026-11-06T01:00:00Z", 0, 0, 0, "mcp.select_tool", "anthropic-main", "ok", 10, 10, 300_000)
	e.usage("2026-11-07T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "error", 0, 0, 0)
}

func line(p aicost.Preview, dept int64) aicost.PreviewLine {
	for _, l := range p.Lines {
		if l.DepartmentID == dept {
			return l
		}
	}
	return aicost.PreviewLine{}
}

// 미리보기: 직접(요청 시점 부서)+공용(평가는 사용량 비율, 부서 미지정은 월말 인원 비율), 합계가 집계 총액과 같다.
func TestPreviewAllocation(t *testing.T) {
	e := newEnv(t)
	e.seedOrg()
	e.seedUsage()
	// 규칙·환율이 없으면 막힌다(저장하지 않는다).
	p, err := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if err != nil || len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "적용할 배부 규칙이 없습니다") {
		t.Fatalf("규칙 없음: %+v %v", p, err)
	}
	if _, err := e.svc.CreateDefaultRules(finance); err != nil {
		t.Fatal(err)
	}
	p, _ = e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "환율이 필요합니다") {
		t.Fatalf("환율 없음: %+v", p.Blockers)
	}
	if err := e.svc.SetFX(finance, "2026-11", 1_000_000); err != nil { // 1달러 = 1000원
		t.Fatal(err)
	}
	p, err = e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if err != nil || len(p.Blockers) != 0 {
		t.Fatalf("미리보기: %+v %v", p, err)
	}
	s, o := line(p, 901), line(p, 900)
	// 영업: 직접 3000(2000+1000) + 평가 500(사용량 비율: 영업만 직접 비용) + 부서 미지정 300 의 2/3 = 200. 운영: 부서 미지정 300 의 1/3 = 100.
	if s.DirectKRW != 3000 || s.SharedKRW != 700 || s.TotalKRW != 3700 || o.DirectKRW != 0 || o.SharedKRW != 100 || o.TotalKRW != 100 {
		t.Fatalf("영업 %+v 운영 %+v", s, o)
	}
	if p.TotalKRW != 3800 || p.DirectKRW != 3000 || p.SharedKRW != 800 || p.RuleVersion != 1 || p.EntityID != 1 || s.DepartmentName != "영업" || s.Headcount != 2 || o.Headcount != 1 || s.Calls != 2 {
		t.Fatalf("합계 %+v", p)
	}
	if len(p.Pools) != 2 || p.Pools[0].Name != "평가·점검" || p.Pools[0].AmountKRW != 500 || p.Pools[1].AmountKRW != 300 || len(p.Pools[1].Shares) != 2 || p.Pools[1].Shares[0].Name == "" {
		t.Fatalf("풀 %+v", p.Pools)
	}
	// 실제 청구액으로 보정하면 총액이 청구액이 되고 비율대로 나뉜다(4180 = 3800 × 1.1).
	if err := e.svc.AddAdjustment(finance, "2026-11", "anthropic-main", 4180, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if p.TotalKRW != 4180 || len(p.Adjusted) != 1 || p.Adjusted[0].ActualKRW != 4180 || line(p, 901).TotalKRW+line(p, 900).TotalKRW != 4180 {
		t.Fatalf("보정: %+v", p)
	}
	// 미리보기는 저장하지 않는다.
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_rule_sets`).Scan(&n)
	if n != 1 {
		t.Fatalf("규칙 세트 %d개", n)
	}
	// cost:read 가 없으면 볼 수 없다.
	if _, err := e.svc.PreviewAllocation(manager, aicost.PreviewInput{Month: "2026-11"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
}

func TestPreviewRuleVersionByMonthAndCompare(t *testing.T) {
	e := newEnv(t)
	e.seedOrg()
	e.seedUsage()
	if err := e.svc.SetFX(finance, "2026-11", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveRuleSet(finance, defaultInput("2026-01")); err != nil {
		t.Fatal(err)
	}
	// v2: 부서 미지정 비용을 운영 100% 로(고정 비율), 2026-11 부터.
	v2 := defaultInput("2026-11")
	v2.Pools[1] = aicost.PoolInput{Name: "부서 미지정", Selector: aicost.Selector{UnassignedDept: true}, Driver: aicost.DriverFixed, Targets: []aicost.PoolTarget{{DepartmentID: 900, WeightPct: 100}}}
	if r, err := e.svc.SaveRuleSet(finance, v2); err != nil || r.Version != 2 {
		t.Fatalf("v2: %+v %v", r, err)
	}
	cur, _ := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if cur.RuleVersion != 2 || line(cur, 900).SharedKRW != 300 {
		t.Fatalf("11월은 v2: %+v", cur)
	}
	old, _ := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11", Version: 1})
	if old.RuleVersion != 1 || line(old, 900).SharedKRW != 100 {
		t.Fatalf("v1 지정: %+v", old)
	}
	cmp, err := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11", Compare: 1})
	if err != nil || cmp.CompareVersion != 1 {
		t.Fatalf("비교: %+v %v", cmp, err)
	}
	d900, d901 := line(cmp, 900), line(cmp, 901)
	if d900.DeltaKRW == nil || *d900.DeltaKRW != 200 || d901.DeltaKRW == nil || *d901.DeltaKRW != -200 {
		t.Fatalf("증감: 운영 %+v 영업 %+v", d900, d901)
	}
	if _, err := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11", Version: 9}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 버전: %v", err)
	}
}

func TestPreviewEntityScopeAndMonthEndHeadcount(t *testing.T) {
	e := newEnv(t)
	e.seedOrg()
	e.seedUsage()
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetFX(finance, "2026-11", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateDefaultRules(finance); err != nil {
		t.Fatal(err)
	}
	// 법인 범위가 기본 법인뿐인 사람은 다른 법인 미리보기를 볼 수 없다.
	p := contract.Principal{UserID: 1, Role: contract.RoleFinance, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms: contract.NewPermSet(contract.PermCostRead), EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermCostRead: contract.NewEntitySet(1)}}
	scoped := contract.WithPrincipal(context.Background(), p)
	if _, err := e.svc.PreviewAllocation(scoped, aicost.PreviewInput{Month: "2026-11", EntityID: 2}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("범위 밖 법인: %v", err)
	}
	// 전체 범위는 두 번째 법인도 볼 수 있고, 그 법인에는 사용 기록이 없어 줄이 없다.
	other, err := e.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-11", EntityID: 2})
	if err != nil || other.EntityID != 2 || len(other.Lines) != 0 || other.TotalKRW != 0 {
		t.Fatalf("법인 2: %+v %v", other, err)
	}
	// 월말 인원: 11월 중 영업 1명이 퇴사(left_flag=1 구간이 11월 15일부터)하면 월말 인원이 1명으로 줄고, 10월 말은 2명이다.
	if _, err := e.db.Exec(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE org_person_spans SET valid_to = '2026-11-15' WHERE user_id = 3`,
		`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (3, '2026-11-15', NULL, 1, 1, 901)`,
		`DELETE FROM org_write_guard`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	nov, _ := e.svc.PreviewAllocation(finance, aicost.PreviewInput{Month: "2026-11"})
	if line(nov, 901).Headcount != 1 {
		t.Fatalf("11월 말 영업 인원 %d", line(nov, 901).Headcount)
	}
	if hc := line(nov, 901).SharedKRW; hc != 500+150 { // 평가 500 + 부서 미지정 300 의 1/2(운영 1명 : 영업 1명)
		t.Fatalf("월말 인원 기준 공용 %d", hc)
	}
}

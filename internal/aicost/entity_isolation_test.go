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

// seedTwoEntities 는 법인 2(계열사)와 그 부서 902(계열사팀, 월말 인원 1명)를 더한다. 기본 법인의 부서는 900(운영 1명)·901(영업 2명)이다.
func (r *runEnv) seedTwoEntities() {
	r.t.Helper()
	ex := func(q string, a ...any) {
		r.t.Helper()
		if _, err := r.db.Exec(q, a...); err != nil {
			r.t.Fatalf("%v\n%s", err, q)
		}
	}
	ex(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	ex(`INSERT INTO departments (id, name) VALUES (902, '계열사팀')`)
	ex(`INSERT INTO users (id, email, name, role, department_id) VALUES (5, 'abc@pl.test', '계열사직원', 'staff', 902)`)
	ex(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`)
	ex(`INSERT INTO org_units (id, code) VALUES (902, 'ABC-T')`)
	ex(`INSERT INTO org_unit_spans (unit_id, valid_from, valid_to, code, name, kind, parent_id, sort_order, closed, head_user_id, head_acting, entity_id) VALUES (902, '2020-01-01', NULL, 'ABC-T', '계열사팀', 'department', 0, 0, 0, 0, 0, 2)`)
	ex(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (5, '2020-01-01', NULL, 0, 1, 902)`)
	ex(`DELETE FROM org_write_guard`)
}

func (r *runEnv) seedTwoEntityUsage() {
	r.t.Helper()
	// 기본 법인: 영업 직접 2달러, 운영의 평가 실행 0.6달러. 계열사: 직접 1달러, 평가 실행 0.4달러. 설치 전체 평가 실행 = 1달러 = 1000원.
	r.usage("2026-10-02T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 2_000_000)
	r.usage("2026-10-04T01:00:00Z", 1, 900, 1, "eval.run", "anthropic-main", "ok", 10, 10, 600_000)
	r.usage("2026-10-03T01:00:00Z", 5, 902, 2, "docverify.extract", "anthropic-main", "ok", 10, 10, 1_000_000)
	r.usage("2026-10-05T01:00:00Z", 5, 902, 2, "eval.run", "anthropic-main", "ok", 10, 10, 400_000)
	if err := r.svc.SetFX(finance, "2026-10", 1_000_000); err != nil {
		r.t.Fatal(err)
	}
}

func groupRules(driver string) aicost.RuleSetInput {
	return aicost.RuleSetInput{ValidFromMonth: "2026-01", Note: "법인 간 평가 공용", Pools: []aicost.PoolInput{
		{Name: "평가·점검(법인 간)", Selector: aicost.Selector{Purposes: []string{"eval.run"}}, Driver: driver, Scope: aicost.ScopeGroup, TargetsAll: true},
		{Name: "부서 미지정", Selector: aicost.Selector{UnassignedDept: true}, Driver: aicost.DriverHeadcount, TargetsAll: true},
	}}
}

// D5: 법인 간 공용 풀은 설치 전체 평가 실행 비용(1,000원)을 법인 사이에 먼저 나누고(월말 인원 3:1 → 750/250), 각 법인 안에서 부서에 인원 비율로 나눈다.
// 기본 법인이 아닌 법인의 몫은 지급 법인(기본 법인)을 상대로 한 표시만 한다.
func TestGroupPoolSplitsAcrossEntitiesAndMarksCounterparty(t *testing.T) {
	r := newRunEnv(t)
	r.seedTwoEntities()
	r.seedTwoEntityUsage()
	if _, err := r.svc.SaveRuleSet(finance, groupRules(aicost.DriverHeadcount)); err != nil {
		t.Fatal(err)
	}
	e1, err := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 1})
	if err != nil || len(e1.Blockers) != 0 {
		t.Fatalf("법인 1: %+v %v", e1, err)
	}
	e2, err := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 2})
	if err != nil || len(e2.Blockers) != 0 {
		t.Fatalf("법인 2: %+v %v", e2, err)
	}
	// 법인 1: 직접 2000(영업) + 몫 750(운영 250·영업 500). 평가 실행 600 원은 법인 계산에서 빠진다.
	if e1.GroupShareKRW != 750 || e1.CounterpartyEntityID != 0 || e1.TotalKRW != 2750 || line(e1, 901).TotalKRW != 2500 || line(e1, 900).TotalKRW != 250 {
		t.Fatalf("법인 1: %+v", e1)
	}
	// 법인 2: 직접 1000 + 몫 250. 기본 법인이 대신 지급한 공용 비용 표시.
	if e2.GroupShareKRW != 250 || e2.CounterpartyEntityID != 1 || e2.TotalKRW != 1250 || line(e2, 902).TotalKRW != 1250 || line(e2, 902).Headcount != 1 {
		t.Fatalf("법인 2: %+v", e2)
	}
	found := false
	for _, w := range e2.Warnings {
		found = found || strings.Contains(w, "법인 간 거래 표시") && strings.Contains(w, "250")
	}
	if !found {
		t.Fatalf("법인 간 거래 안내가 없다: %v", e2.Warnings)
	}
	// 두 법인의 그룹 몫 합 = 설치 전체 평가 실행 비용.
	if e1.GroupShareKRW+e2.GroupShareKRW != 1000 {
		t.Fatal("법인 몫 합이 풀 금액과 다르다")
	}
	if len(e2.Pools) == 0 || !e2.Pools[0].Group || !strings.Contains(e2.Pools[0].Name, "법인 간") {
		t.Fatalf("풀 표시: %+v", e2.Pools)
	}
	// 법인 안의 부서에 나뉜 몫이 법인 안 부서만이다(다른 법인 부서가 없다).
	for _, l := range e2.Lines {
		if l.DepartmentID != 902 {
			t.Fatalf("법인 2 에 다른 법인 부서가 있다: %+v", l)
		}
	}
	// 사용량 기준: 법인의 직접 비용(2000 : 1000)으로 나눈다 → 666/334(소수부가 큰 쪽이 1원).
	if _, err := r.svc.SaveRuleSet(finance, groupRules(aicost.DriverUsage)); err != nil {
		t.Fatal(err)
	}
	u1, _ := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 1})
	u2, _ := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 2})
	if u1.GroupShareKRW != 667 || u2.GroupShareKRW != 333 {
		t.Fatalf("사용량 기준 몫 %d/%d", u1.GroupShareKRW, u2.GroupShareKRW)
	}
	// 확정: 법인 2 의 전표 메모에 법인 간 공용 비용이 적히고 실행에 몫·상대 법인이 남는다.
	if _, err := r.svc.SaveRuleSet(finance, groupRules(aicost.DriverHeadcount)); err != nil {
		t.Fatal(err)
	}
	run2, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10", EntityID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if run2.EntityID != 2 || run2.GroupShareKRW != 250 || run2.CounterpartyEntityID != 1 || run2.CounterpartyName != "기본 법인" && run2.CounterpartyName == "" {
		t.Fatalf("법인 2 실행: %+v", run2)
	}
	if n := len(r.j.created); n != 1 || !strings.Contains(r.j.created[0].Memo, "법인 간 공용 비용 250원 포함(지급 법인 1)") {
		t.Fatalf("전표 메모: %+v", r.j.created)
	}
	run1, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10", EntityID: 1})
	if err != nil || run1.CounterpartyEntityID != 0 || run1.GroupShareKRW != 750 || strings.Contains(r.j.created[1].Memo, "지급 법인") {
		t.Fatalf("법인 1 실행: %+v %v", run1, err)
	}
	if run1.TotalKRW+run2.TotalKRW != 4000 { // 직접 3000 + 평가 공용 1000
		t.Fatalf("두 법인 확정 합계 %d", run1.TotalKRW+run2.TotalKRW)
	}
}

func TestGroupPoolValidationAndSingleEntityFallback(t *testing.T) {
	r := newRunEnv(t)
	r.seedTwoEntities()
	ok := func() aicost.RuleSetInput { return groupRules(aicost.DriverHeadcount) }
	bad := map[string]func(*aicost.RuleSetInput){
		"그룹 풀은 전체 법인이거나 고정 비율": func(x *aicost.RuleSetInput) { x.Pools[0].TargetsAll = false },
		"고정 비율은 법인을 직접 지정":     func(x *aicost.RuleSetInput) { x.Pools[0].Driver = aicost.DriverFixed },
		"없는 법인":     func(x *aicost.RuleSetInput) { fixedGroup(x, 1, 60, 99, 40) },
		"비율 합 99":   func(x *aicost.RuleSetInput) { fixedGroup(x, 1, 60, 2, 39) },
		"알 수 없는 범위": func(x *aicost.RuleSetInput) { x.Pools[0].Scope = "world" },
	}
	for name, mut := range bad {
		in := ok()
		mut(&in)
		if _, err := r.svc.SaveRuleSet(finance, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부해야 한다: %v", name, err)
		}
	}
	good := ok()
	fixedGroup(&good, 1, 60, 2, 40)
	if res, err := r.svc.SaveRuleSet(finance, good); err != nil || res.Version != 1 {
		t.Fatalf("고정 비율 그룹 풀: %+v %v", res, err)
	}
	got, _ := r.svc.GetRuleSet(reader, 1)
	if got.Pools[0].Scope != aicost.ScopeGroup || got.Pools[1].Scope != aicost.ScopeEntity {
		t.Fatalf("범위가 저장·복원돼야 한다: %+v %+v", got.Pools[0].Scope, got.Pools[1].Scope)
	}
	// 고정 비율: 설치 전체 평가 1000원을 법인 1 에 60%, 법인 2 에 40%.
	r.seedTwoEntityUsage()
	p1, _ := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 1})
	p2, _ := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: 2})
	if p1.GroupShareKRW != 600 || p2.GroupShareKRW != 400 {
		t.Fatalf("고정 몫 %d/%d", p1.GroupShareKRW, p2.GroupShareKRW)
	}
	// 법인이 하나뿐인 설치: 그룹 풀을 법인 안 풀로 처리하고 알린다(고정 비율은 인원 비율로).
	one := newRunEnv(t)
	one.seedOctober()
	in := groupRules(aicost.DriverHeadcount)
	in.ValidFromMonth = "2026-11"
	if _, err := one.svc.SaveRuleSet(finance, in); err != nil {
		t.Fatal(err)
	}
	pv, err := one.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", Version: 2})
	if err != nil || len(pv.Blockers) != 0 || pv.GroupShareKRW != 0 {
		t.Fatalf("법인 하나: %+v %v", pv, err)
	}
	warned := false
	for _, w := range pv.Warnings {
		warned = warned || strings.Contains(w, "법인이 하나뿐이라")
	}
	if !warned || pv.TotalKRW != 3800 {
		t.Fatalf("법인 하나 안내·합계: %v %d", pv.Warnings, pv.TotalKRW)
	}
}

func fixedGroup(x *aicost.RuleSetInput, e1 int64, w1 int, e2 int64, w2 int) {
	x.Pools[0] = aicost.PoolInput{Name: "평가·점검(법인 간)", Selector: aicost.Selector{Purposes: []string{"eval.run"}}, Driver: aicost.DriverFixed, Scope: aicost.ScopeGroup,
		Targets: []aicost.PoolTarget{{DepartmentID: e1, WeightPct: w1}, {DepartmentID: e2, WeightPct: w2}}}
}

// AC-4911 법인 격리 표 시험: 법인 범위가 기본 법인뿐인 재무가 다른 법인(계열사)의 값을 읽거나 쓰려 하면 거부되고, 읽을 수 있는 응답에는 다른 법인 값이 없다.
func TestEntityIsolationTable(t *testing.T) {
	r := newRunEnv(t)
	r.seedTwoEntities()
	r.seedTwoEntityUsage()
	if _, err := r.svc.SaveRuleSet(finance, groupRules(aicost.DriverHeadcount)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10", EntityID: 2}); err != nil { // 계열사의 확정 실행이 이미 있다
		t.Fatal(err)
	}
	if err := r.svc.SetBudget(allocator, "2026-11", 902, 7777); err != nil {
		t.Fatal(err)
	}
	only1 := func(perms ...contract.Permission) context.Context {
		es := map[contract.Permission]contract.EntitySet{}
		for _, p := range perms {
			es[p] = contract.NewEntitySet(1)
		}
		return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleFinance, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
			Perms: contract.NewPermSet(perms...), EntityPerms: es})
	}
	fin1 := only1(contract.PermCostRead, contract.PermCostAllocate, contract.PermLedgerWrite, contract.PermReportRead)
	abc := int64(2)
	type op struct {
		name string
		run  func() error
	}
	denied := []op{
		{"미리보기(법인 2)", func() error {
			_, e := r.svc.PreviewAllocation(fin1, aicost.PreviewInput{Month: "2026-10", EntityID: abc})
			return e
		}},
		{"월 실행 조회(법인 2)", func() error { _, e := r.svc.GetRun(fin1, "2026-10", abc); return e }},
		{"확정(법인 2)", func() error {
			_, e := r.svc.ConfirmRun(fin1, aicost.ConfirmInput{Month: "2026-09", EntityID: abc})
			return e
		}},
		{"예산 목록(법인 2)", func() error { _, e := r.svc.ListBudgets(fin1, "2026-11", abc); return e }},
		{"예산 정하기(계열사팀)", func() error { return r.svc.SetBudget(fin1, "2026-11", 902, 1) }},
		{"CSV(법인 2)", func() error { _, _, e := r.svc.ExportCSV(fin1, "2026-10", abc); return e }},
		{"MCP 도구: 계열사팀 요약", func() error { _, e := r.svc.ToolSummary(fin1, "2026-10", "계열사팀"); return e }},
	}
	for _, o := range denied {
		if err := o.run(); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s: 거부해야 한다: %v", o.name, err)
		}
	}
	// 해제 요청·확인·전표 다시 만들기도 다른 법인의 실행이면 거부한다.
	run2, _ := r.svc.GetRun(reader, "2026-10", abc)
	if run2 == nil {
		t.Fatal("계열사 실행이 없다")
	}
	for name, fn := range map[string]func() error{
		"해제 요청": func() error { _, e := r.svc.RequestRelease(fin1, run2.ID, "x"); return e },
		"해제 확인": func() error { _, e := r.svc.ConfirmRelease(fin1, run2.ID); return e },
		"해제 반려": func() error { _, e := r.svc.RejectRelease(fin1, run2.ID); return e },
		"전표 다시": func() error { _, e := r.svc.RegenerateJournal(fin1, run2.ID); return e },
	} {
		if err := fn(); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s: 거부해야 한다: %v", name, err)
		}
	}
	// 읽을 수 있는 응답에는 다른 법인 값이 없다.
	rep, err := r.svc.Monthly(fin1, aicost.Filter{Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalMicro != 2_600_000 { // 기본 법인: 2.0 + 0.6
		t.Fatalf("집계에 다른 법인 값이 섞임: %d", rep.TotalMicro)
	}
	for _, d := range rep.Departments {
		if d.DepartmentID == 902 || d.EntityID != 1 {
			t.Fatalf("집계 부서: %+v", d)
		}
	}
	if f, _ := r.svc.Monthly(fin1, aicost.Filter{Month: "2026-10", EntityID: abc}); f.TotalMicro != 0 || len(f.Rows) != 0 {
		t.Fatalf("법인 거름으로도 다른 법인을 볼 수 없다: %+v", f)
	}
	pv, err := r.svc.PreviewAllocation(fin1, aicost.PreviewInput{Month: "2026-10", EntityID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range pv.Lines {
		if l.DepartmentID == 902 {
			t.Fatalf("미리보기에 다른 법인 부서: %+v", l)
		}
	}
	if pv.GroupShareKRW != 750 || pv.CounterpartyEntityID != 0 {
		t.Fatalf("법인 1 몫만 보여야 한다: %+v", pv)
	}
	deps, _ := r.svc.ListDepartments(fin1)
	for _, d := range deps {
		if d.EntityID != 1 || d.ID == 902 {
			t.Fatalf("부서 목록: %+v", d)
		}
	}
	ents, _ := r.svc.ListEntities(fin1)
	if len(ents) != 1 || ents[0].ID != 1 {
		t.Fatalf("법인 목록: %+v", ents)
	}
	bud, _ := r.svc.ListBudgets(fin1, "2026-11", 1)
	for _, b := range bud {
		if b.DepartmentID == 902 || b.EntityID != 1 {
			t.Fatalf("예산 목록: %+v", b)
		}
	}
	_, csvBody, err := r.svc.ExportCSV(fin1, "2026-10", 1)
	if err != nil || strings.Contains(string(csvBody), "계열사팀") {
		t.Fatalf("CSV 에 다른 법인 부서: %q %v", csvBody, err)
	}
	// 같은 사람이 법인 2 까지 범위가 있으면 볼 수 있다(전체 범위 재무).
	if p, err := r.svc.PreviewAllocation(reader, aicost.PreviewInput{Month: "2026-10", EntityID: abc}); err != nil || line(p, 902).DepartmentName != "계열사팀" {
		t.Fatalf("전체 범위: %+v %v", p, err)
	}
	// 자기 부서(기본 법인)의 MCP 요약은 된다.
	if _, err := r.svc.ToolSummary(fin1, "2026-10", ""); err != nil {
		t.Fatalf("본인 부서 요약: %v", err)
	}
	_ = audit.TargetAICost
}

func TestExpenseAccountSetting(t *testing.T) {
	r := newRunEnv(t)
	st, err := r.svc.GetSettings(reader)
	if err != nil || st.ExpenseAccount != "5290" || len(st.Options) < 3 {
		t.Fatalf("기본 설정: %+v %v", st, err)
	}
	for _, o := range st.Options {
		if !strings.HasPrefix(o.Code, "5") {
			t.Fatalf("비용 계정이 아닌 항목: %+v", o)
		}
	}
	if _, err := r.svc.GetSettings(manager); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("조회 권한: %v", err)
	}
	if err := r.svc.SetExpenseAccount(reader, "5260"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("변경 권한: %v", err)
	}
	for _, bad := range []string{"", "1010", "9999", "5290 "} {
		err := r.svc.SetExpenseAccount(allocator, bad)
		if bad == "5290 " {
			if !errors.Is(err, contract.ErrInvalidState) { // 공백은 지워 같은 값으로 본다
				t.Fatalf("같은 값: %v", err)
			}
			continue
		}
		if !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%q 는 거부해야 한다: %v", bad, err)
		}
	}
	if err := r.svc.SetExpenseAccount(allocator, "5260"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.svc.GetSettings(reader); got.ExpenseAccount != "5260" {
		t.Fatalf("변경이 반영되지 않음: %+v", got)
	}
	if !r.rec.Has("ai_cost.setting_set", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("설정 감사가 없다")
	}
	// 확정 전표는 바뀐 계정을 쓴다.
	r.seedOctober()
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); err != nil {
		t.Fatal(err)
	}
	if r.j.created[0].ExpenseCode != "5260" {
		t.Fatalf("전표 계정 %s", r.j.created[0].ExpenseCode)
	}
	r.rec.FailWith(errors.New("감사 실패"))
	if err := r.svc.SetExpenseAccount(allocator, "5290"); err == nil {
		t.Fatal("감사 실패는 변경 실패여야 한다")
	}
	r.rec.FailWith(nil)
	if got, _ := r.svc.GetSettings(reader); got.ExpenseAccount != "5260" {
		t.Fatalf("감사 실패 뒤 설정이 바뀜: %+v", got)
	}
}

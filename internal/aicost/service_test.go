package aicost_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t   *testing.T
	db  *sql.DB
	rec *fake.Recorder
	svc *aicost.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO departments (id, name) VALUES (900, '운영'), (901, '영업')`,
		`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'finance@pl.test', '재무', 'finance', 900), (2, 'mgr@pl.test', '영업팀장', 'manager', 901), (3, 'sales@pl.test', '영업사원', 'staff', 901)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, db: db, rec: fake.NewRecorder()}
	svc, err := aicost.New(aicost.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: e.rec,
		Now: func() time.Time { return time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC) }}) // KST 2026-11-14 12:00
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

func as(user, dept int64, perms ...contract.Permission) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: user, Role: contract.RoleFinance, DepartmentID: dept, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(perms...)})
}

var (
	finance = as(1, 900, contract.PermCostRead, contract.PermCostAllocate, contract.PermReportRead)
	reader  = as(1, 900, contract.PermCostRead)
	manager = as(2, 901, contract.PermReportRead)
	nobody  = as(3, 901)
)

func (e *env) usage(at string, user, dept, entity int64, purpose, provider, status string, in, out, cost int64) {
	e.t.Helper()
	if _, err := e.db.Exec(`INSERT INTO llm_usage (at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,0,?,?)`, at, user, dept, "staff", provider, "m1", purpose, in, out, cost, status, entity); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seed() {
	e.usage("2026-11-02T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 1000, 500, 2_000_000)
	e.usage("2026-11-03T01:00:00Z", 2, 901, 1, "knowledge.answer", "anthropic-main", "ok", 400, 100, 1_000_000)
	e.usage("2026-11-04T01:00:00Z", 1, 900, 1, "eval.run", "anthropic-main", "ok", 100, 100, 500_000)
	e.usage("2026-11-05T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "error", 0, 0, 0)
	e.usage("2026-11-05T02:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "blocked_quota", 0, 0, 0)
	// 법인 미상(0)은 기본 법인, 시스템 호출은 부서 0.
	e.usage("2026-11-06T01:00:00Z", 0, 0, 0, "mcp.select_tool", "mock", "ok", 50, 50, 0)
	// 월 경계(KST): 10월 31일 15:30 UTC = 11월 1일 00:30 KST 는 11월, 11월 30일 15:00 UTC = 12월 1일 00:00 KST 는 12월.
	e.usage("2026-10-31T15:30:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 300_000)
	e.usage("2026-11-30T15:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 7_000_000)
	e.usage("2026-10-31T14:59:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 9_000_000)
}

// AC-4901: 집계 금액은 status='ok' 합이고 실패·차단은 건수만 센다. KST 월 경계와 법인 미상(0)=기본 법인.
func TestMonthlyAggregatesOkCostAndKstMonthBoundary(t *testing.T) {
	e := newEnv(t)
	e.seed()
	rep, err := e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalMicro != 3_800_000 { // 2.0+1.0+0.5+0+0.3
		t.Fatalf("합계 %d (월 경계 포함 3,800,000 이어야 함): %+v", rep.TotalMicro, rep.Rows)
	}
	if rep.Calls != 5 || rep.Failed != 2 {
		t.Fatalf("건수 ok %d 실패 %d", rep.Calls, rep.Failed)
	}
	byDept := map[int64]int64{}
	for _, d := range rep.Departments {
		byDept[d.DepartmentID] = d.CostMicroUSD
		if d.EntityID != 1 {
			t.Fatalf("법인 미상(0)은 기본 법인으로 보여야 함: %+v", d)
		}
	}
	if byDept[901] != 3_300_000 || byDept[900] != 500_000 || byDept[0] != 0 {
		t.Fatalf("부서별 %v", byDept)
	}
	var dept0 string
	for _, d := range rep.Departments {
		if d.DepartmentID == 0 {
			dept0 = d.DepartmentName
		}
	}
	if dept0 != "(부서 미지정)" {
		t.Fatalf("부서 0 이름 %q", dept0)
	}
	dec, _ := e.svc.Monthly(finance, aicost.Filter{Month: "2026-12"})
	if dec.TotalMicro != 7_000_000 {
		t.Fatalf("12월 %d", dec.TotalMicro)
	}
	oct, _ := e.svc.Monthly(finance, aicost.Filter{Month: "2026-10"})
	if oct.TotalMicro != 9_000_000 {
		t.Fatalf("10월 %d", oct.TotalMicro)
	}
}

func TestMonthlyFiltersAndEntityScope(t *testing.T) {
	e := newEnv(t)
	e.seed()
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	e.usage("2026-11-07T01:00:00Z", 3, 901, 2, "docverify.extract", "anthropic-main", "ok", 1, 1, 4_000_000)
	// 법인 범위가 기본 법인뿐인 재무: 다른 법인 행이 안 보인다.
	p := contract.Principal{UserID: 1, Role: contract.RoleFinance, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms: contract.NewPermSet(contract.PermCostRead), EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermCostRead: contract.NewEntitySet(1)}}
	scoped := contract.WithPrincipal(context.Background(), p)
	rep, err := e.svc.Monthly(scoped, aicost.Filter{Month: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalMicro != 3_800_000 {
		t.Fatalf("법인 범위 밖 행이 섞임: %d", rep.TotalMicro)
	}
	all, _ := e.svc.Monthly(reader, aicost.Filter{Month: "2026-11"})
	if all.TotalMicro != 7_800_000 {
		t.Fatalf("전체 범위 %d", all.TotalMicro)
	}
	f, _ := e.svc.Monthly(reader, aicost.Filter{Month: "2026-11", EntityID: 2})
	if f.TotalMicro != 4_000_000 {
		t.Fatalf("법인 거름 %d", f.TotalMicro)
	}
	f, _ = e.svc.Monthly(reader, aicost.Filter{Month: "2026-11", Purpose: "eval.run"})
	if f.TotalMicro != 500_000 || len(f.Rows) != 1 {
		t.Fatalf("용도 거름 %+v", f)
	}
	f, _ = e.svc.Monthly(reader, aicost.Filter{Month: "2026-11", DepartmentID: 900, Provider: "anthropic-main"})
	if f.TotalMicro != 500_000 {
		t.Fatalf("부서·공급자 거름 %+v", f)
	}
}

func TestMonthlyRequiresCostRead(t *testing.T) {
	e := newEnv(t)
	for name, ctx := range map[string]context.Context{"manager": manager, "nobody": nobody} {
		if _, err := e.svc.Monthly(ctx, aicost.Filter{}); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.Monthly(reader, aicost.Filter{Month: "2026-13"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("월 형식: %v", err)
	}
}

// 환율: 입력 검증·반올림·감사·같은 값 거부. 환율이 없으면 원화 0 과 경고.
func TestFXAndKrwRounding(t *testing.T) {
	e := newEnv(t)
	e.seed()
	rep, _ := e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	if rep.FxUsdKrwX1000 != 0 || rep.TotalKRW != 0 || len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "환율") {
		t.Fatalf("환율 없음: %+v", rep.Warnings)
	}
	if err := e.svc.SetFX(reader, "2026-11", 1_350_500); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("cost:allocate 없이 환율: %v", err)
	}
	for _, bad := range []int64{0, 99_999, 10_000_001} {
		if err := e.svc.SetFX(finance, "2026-11", bad); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("범위 밖 환율 %d: %v", bad, err)
		}
	}
	if err := e.svc.SetFX(finance, "2026-12", 1_350_500); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("앞으로 올 달: %v", err)
	}
	if err := e.svc.SetFX(finance, "2026-11", 1_350_500); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetFX(finance, "2026-11", 1_350_500); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 값: %v", err)
	}
	rep, _ = e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	// 3.8 USD × 1350.5 = 5131.9 → 5132. 부서별 반올림 합과 총액 반올림이 다를 수 있어 총액은 총액으로 계산한다.
	if rep.TotalKRW != 5132 || rep.FxUsdKrwX1000 != 1_350_500 {
		t.Fatalf("원화 %d", rep.TotalKRW)
	}
	if err := e.svc.SetFX(finance, "2026-11", 1_400_000); err != nil {
		t.Fatal(err)
	}
	var last audit.Record
	for _, r := range e.rec.Records() {
		if r.Action == "ai_cost.fx_set" {
			last = r
		}
	}
	if last.TargetType != audit.TargetAICost || last.TargetID != "2026-11" || !strings.Contains(last.Detail, "1350500") && !diffHas(last, "usd_krw", "1350500", "1400000") {
		t.Fatalf("환율 감사: %+v", last)
	}
}

func diffHas(r audit.Record, key, old, nw string) bool {
	for _, d := range r.Diff {
		if d.Key == key && d.Old == old && d.New == nw {
			return true
		}
	}
	return false
}

// 청구액 입력: 추가 전용(수정·삭제 트리거), 같은 (월, 공급자)는 최신이 쓰이고 비율·경고가 계산된다.
func TestAdjustmentsAppendOnlyLatestWinsAndRatio(t *testing.T) {
	e := newEnv(t)
	e.seed()
	if err := e.svc.SetFX(finance, "2026-11", 1_000_000); err != nil { // 1달러 = 1000원
		t.Fatal(err)
	}
	if err := e.svc.AddAdjustment(reader, "2026-11", "anthropic-main", 1, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	for _, bad := range []struct {
		m, p string
		v    int64
	}{{"2026-13", "x", 1}, {"2026-11", "", 1}, {"2026-11", "x", -1}, {"2026-12", "x", 1}} {
		if err := e.svc.AddAdjustment(finance, bad.m, bad.p, bad.v, ""); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("검증 %+v: %v", bad, err)
		}
	}
	// 집계 3.8달러 = 3,800원. 첫 입력 4,000원 → 비율 1.05
	if err := e.svc.AddAdjustment(finance, "2026-11", "anthropic-main", 4000, "10월 청구서"); err != nil {
		t.Fatal(err)
	}
	rep, _ := e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	if len(rep.Adjustments) != 1 || rep.Adjustments[0].ActualKRW != 4000 || rep.Adjustments[0].EnteredNam != "재무" {
		t.Fatalf("입력: %+v", rep.Adjustments)
	}
	var ratio float64
	for _, p := range rep.Providers {
		if p.Provider == "anthropic-main" {
			ratio = p.AdjustRatio
		}
	}
	if ratio < 1.05 || ratio > 1.0527 {
		t.Fatalf("비율 %v", ratio)
	}
	// 다시 입력하면 최신이 쓰이고 이전 값이 감사에 남는다. 3배 넘는 값은 경고.
	if err := e.svc.AddAdjustment(finance, "2026-11", "anthropic-main", 12000, ""); err != nil {
		t.Fatal(err)
	}
	rep, _ = e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	if len(rep.Adjustments) != 1 || rep.Adjustments[0].ActualKRW != 12000 {
		t.Fatalf("최신: %+v", rep.Adjustments)
	}
	warned := false
	for _, w := range rep.Warnings {
		warned = warned || strings.Contains(w, "범위를 벗어났습니다")
	}
	if !warned {
		t.Fatalf("경고 없음: %v", rep.Warnings)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_adjustments WHERE month = '2026-11'`).Scan(&n)
	if n != 2 {
		t.Fatalf("이력 %d행", n)
	}
	if _, err := e.db.Exec(`UPDATE ai_cost_adjustments SET actual_krw = 1`); err == nil {
		t.Fatal("청구액 수정이 막혀야 한다")
	}
	if _, err := e.db.Exec(`DELETE FROM ai_cost_adjustments`); err == nil {
		t.Fatal("청구액 삭제가 막혀야 한다")
	}
	var prev audit.Record
	for _, r := range e.rec.Records() {
		if r.Action == "ai_cost.adjust_set" {
			prev = r
		}
	}
	if !diffHas(prev, "actual_krw", "4000", "12000") {
		t.Fatalf("이전 값 감사: %+v", prev)
	}
}

func TestZeroPriceWarning(t *testing.T) {
	e := newEnv(t)
	e.usage("2026-11-02T01:00:00Z", 3, 901, 1, "docverify.extract", "mock", "ok", 100, 100, 0)
	e.usage("2026-11-02T02:00:00Z", 3, 901, 1, "docverify.extract", "mock", "ok", 100, 100, 0)
	rep, _ := e.svc.Monthly(finance, aicost.Filter{Month: "2026-11"})
	found := false
	for _, w := range rep.Warnings {
		found = found || strings.Contains(w, "호출 2건은 금액이 0")
	}
	if !found || rep.Providers[0].ZeroPrice != 2 {
		t.Fatalf("단가 0 경고: %v %+v", rep.Warnings, rep.Providers)
	}
}

// 부서장 화면: 본인 부서만(타 부서 사용은 안 보임), 12개월 추이, 이번 달 예상, 조회 감사.
func TestMyDepartmentOnlyOwnAndAudited(t *testing.T) {
	e := newEnv(t)
	e.seed()
	if err := e.svc.SetFX(finance, "2026-11", 1_000_000); err != nil {
		t.Fatal(err)
	}
	my, err := e.svc.MyDepartment(manager, "")
	if err != nil {
		t.Fatal(err)
	}
	if my.DepartmentID != 901 || my.DepartmentName != "영업" || my.Month != "2026-11" {
		t.Fatalf("%+v", my)
	}
	if my.TotalMicro != 3_300_000 || my.TotalKRW != 3300 { // 영업(901): 2.0+1.0+0.3
		t.Fatalf("합계 %d/%d", my.TotalMicro, my.TotalKRW)
	}
	for _, r := range my.Rows {
		if r.DepartmentID != 901 {
			t.Fatalf("타 부서 행: %+v", r)
		}
	}
	if len(my.Trend) != 12 || my.Trend[11].Month != "2026-11" || my.Trend[0].Month != "2025-12" || my.Trend[10].CostMicroUSD != 9_000_000 {
		t.Fatalf("추이 %+v", my.Trend)
	}
	// 이번 달 예상: 14일 경과, 30일 → 3,300,000 × 30 / 14
	if my.Forecast == nil || my.Forecast.ElapsedDays != 14 || my.Forecast.DaysInMonth != 30 || my.Forecast.CostMicroUSD != 3_300_000*30/14 {
		t.Fatalf("예상 %+v", my.Forecast)
	}
	if len(my.TopUsers) == 0 || my.TopUsers[0].Name != "영업사원" {
		t.Fatalf("상위 사용자 %+v", my.TopUsers)
	}
	past, _ := e.svc.MyDepartment(manager, "2026-10")
	if past.Forecast != nil {
		t.Fatal("지난 달에는 예상이 없다")
	}
	if !e.rec.Has("ai_cost.view_dept", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("부서 조회 감사가 없다")
	}
	for _, r := range e.rec.Records() {
		if r.Action == "ai_cost.view_dept" && (strings.Contains(r.Detail, "영업") || strings.Contains(r.Detail, "3300")) {
			t.Fatalf("감사에 금액·이름이 있다: %+v", r)
		}
	}
	if _, err := e.svc.MyDepartment(nobody, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("report:read 없음: %v", err)
	}
	noDept := as(9, 0, contract.PermReportRead)
	if _, err := e.svc.MyDepartment(noDept, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("부서 없음: %v", err)
	}
}

// 감사 기록이 실패하면 쓰기가 롤백된다.
func TestWriteRollsBackWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	e.rec.FailWith(errors.New("감사 실패"))
	if err := e.svc.SetFX(finance, "2026-11", 1_350_500); err == nil {
		t.Fatal("감사 실패는 요청 실패여야 한다")
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_fx`).Scan(&n)
	if n != 0 {
		t.Fatal("환율이 남았다")
	}
	if err := e.svc.AddAdjustment(finance, "2026-11", "x", 5, ""); err == nil {
		t.Fatal("감사 실패는 요청 실패여야 한다")
	}
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_adjustments`).Scan(&n)
	if n != 0 {
		t.Fatal("청구액이 남았다")
	}
}

// 마이그레이션 0096: 기존 행의 법인은 부서(조직 단위)의 법인으로 채운다.
func TestMicroToKRWBigNumbers(t *testing.T) {
	// 1조 마이크로달러(= 100만 달러) × 환율 1350.5 가 넘치지 않는다.
	if got := aicost.MicroToKRWForTest(1_000_000_000_000, 1_350_500); got != 1_350_500_000 {
		t.Fatalf("%d", got)
	}
	if aicost.MicroToKRWForTest(0, 1_350_500) != 0 || aicost.MicroToKRWForTest(5, 0) != 0 {
		t.Fatal("0 처리")
	}
}

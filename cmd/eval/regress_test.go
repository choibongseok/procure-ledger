package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/migrations"
)

// passing 은 합격 기준을 넘기는 보고서이고, failOne 이 있으면 그 케이스 하나만 실패한다(필수 케이스는 아님).
func passing(failOne string, extra ...CaseResult) Report {
	rep := Report{}
	for i := 0; i < 40; i++ {
		id := "d" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		rep.Results = append(rep.Results, CaseResult{Suite: "docs", ID: id, Pass: id != failOne})
	}
	for i := 0; i < 90; i++ { // 도구 선택 케이스 수 하한(evals.MinToolCases)을 넘긴다
		id := "t" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		rep.Results = append(rep.Results, CaseResult{Suite: "tools", ID: id, Pass: id != failOne})
	}
	for i := 0; i < 12; i++ {
		id := "p" + string(rune('a'+i))
		rep.Results = append(rep.Results, CaseResult{Suite: "policy", ID: id, Pass: id != failOne})
	}
	for i := 0; i < 12; i++ {
		id := "q" + string(rune('a'+i))
		rep.Results = append(rep.Results, CaseResult{Suite: "quotes", ID: id, Pass: id != failOne})
	}
	for i := 0; i < 10; i++ {
		id := "k" + string(rune('a'+i))
		rep.Results = append(rep.Results, CaseResult{Suite: "knowledge", ID: id, Pass: id != failOne})
	}
	rep.Results = append(rep.Results, extra...)
	return rep
}

func writeBase(t *testing.T, rep Report) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := writeBaseline(path, "mock", rep); err != nil {
		t.Fatal(err)
	}
	return path
}

func fin(t *testing.T, rep Report, o finishOptions) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	o.Provider = "mock"
	code := finish(rep, o, &out, &errw)
	return code, out.String() + errw.String()
}

// 기준선에서 통과하던 케이스가 실패하면 합합격률이 기준을 넘어도 종료 코드가 0 이 아니다.
func TestRegressionOfPreviouslyPassingCaseFails(t *testing.T) {
	path := writeBase(t, passing(""))
	code, out := fin(t, passing("daa"), finishOptions{BaselinePath: path})
	if code != 1 {
		t.Fatalf("회귀는 종료 코드 1 이어야 함: %d\n%s", code, out)
	}
	if !strings.Contains(out, "docs/daa") || !strings.Contains(out, "회귀 감지") {
		t.Fatalf("새로 실패한 케이스를 표시해야 함:\n%s", out)
	}
}

// 기준선에 없던 새 케이스가 실패해도 실패다.
func TestNewFailingCaseNotInBaselineFails(t *testing.T) {
	path := writeBase(t, passing(""))
	rep := passing("", CaseResult{Suite: "docs", ID: "brand-new", Pass: false})
	if code, out := fin(t, rep, finishOptions{BaselinePath: path}); code != 1 || !strings.Contains(out, "docs/brand-new") {
		t.Fatalf("새 케이스 실패: %d\n%s", code, out)
	}
	// 새 케이스가 통과하면 문제없다.
	rep = passing("", CaseResult{Suite: "docs", ID: "brand-new", Pass: true})
	if code, out := fin(t, rep, finishOptions{BaselinePath: path}); code != 0 {
		t.Fatalf("새 케이스 통과는 정상: %d\n%s", code, out)
	}
}

// 기준선에서 이미 실패하던 케이스가 계속 실패하는 것은 회귀가 아니고, 새로 통과한 케이스는 보고한다.
func TestKnownFailureIsNotRegressionAndNewPassIsReported(t *testing.T) {
	path := writeBase(t, passing("dab"))
	code, out := fin(t, passing("dab"), finishOptions{BaselinePath: path})
	if code != 0 {
		t.Fatalf("알려진 실패는 회귀가 아님: %d\n%s", code, out)
	}
	code, out = fin(t, passing(""), finishOptions{BaselinePath: path})
	if code != 0 || !strings.Contains(out, "새로 통과한 케이스: docs/dab") {
		t.Fatalf("새로 통과한 케이스 보고: %d\n%s", code, out)
	}
}

// 기준선에서 사라진 케이스는 경고만 하고 실패로 세지 않는다. --only 로 안 돌린 구분은 사라짐이 아니다.
func TestRemovedCaseWarnsOnly(t *testing.T) {
	path := writeBase(t, passing("", CaseResult{Suite: "docs", ID: "gone", Pass: true}))
	code, out := fin(t, passing(""), finishOptions{BaselinePath: path})
	if code != 0 || !strings.Contains(out, "경고") || !strings.Contains(out, "docs/gone") {
		t.Fatalf("사라진 케이스는 경고: %d\n%s", code, out)
	}
	only := Report{only: "tools"}
	for _, c := range passing("").Results {
		if c.Suite == "tools" {
			only.Results = append(only.Results, c)
		}
	}
	if _, out := fin(t, only, finishOptions{BaselinePath: path, Only: "tools"}); strings.Contains(out, "docs/gone") {
		t.Fatalf("돌리지 않은 구분은 경고하지 않음:\n%s", out)
	}
}

// 기본 실행은 비교만 한다. 갱신 플래그가 있을 때만 파일이 바뀐다.
func TestBaselineChangesOnlyWithUpdateFlag(t *testing.T) {
	path := writeBase(t, passing(""))
	before, _ := os.ReadFile(path)
	rep := passing("", CaseResult{Suite: "docs", ID: "added", Pass: true})
	if code, _ := fin(t, rep, finishOptions{BaselinePath: path}); code != 0 {
		t.Fatal("비교 통과")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("기본 실행이 기준선 파일을 바꿨다")
	}
	if code, out := fin(t, rep, finishOptions{BaselinePath: path, Update: true}); code != 0 || !strings.Contains(out, "기준선을 갱신했습니다") {
		t.Fatalf("갱신: %d\n%s", code, out)
	}
	b, err := evals.ReadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range b.Cases {
		found = found || c.Key() == "docs/added"
	}
	if !found || len(b.Cases) != len(rep.Results) {
		t.Fatalf("갱신된 기준선에 이번 결과가 담겨야 함: %d건", len(b.Cases))
	}
}

// 회귀가 있는 결과를 갱신 플래그로 받아들이면 새 기준이 되고, 합격하지 못한 결과는 기준선이 되지 못한다.
func TestUpdateAcceptsRegressionButRefusesFailingRun(t *testing.T) {
	path := writeBase(t, passing(""))
	if code, out := fin(t, passing("daa"), finishOptions{BaselinePath: path, Update: true}); code != 0 {
		t.Fatalf("합격한 결과의 갱신은 받아들임: %d\n%s", code, out)
	}
	if code, out := fin(t, passing("daa"), finishOptions{BaselinePath: path}); code != 0 {
		t.Fatalf("갱신 뒤에는 같은 결과가 회귀가 아님: %d\n%s", code, out)
	}
	bad := Report{}
	for i := 0; i < 5; i++ {
		bad.Results = append(bad.Results, CaseResult{Suite: "docs", ID: "x", Pass: false})
	}
	before, _ := os.ReadFile(path)
	if code, _ := fin(t, bad, finishOptions{BaselinePath: path, Update: true}); code != 1 {
		t.Fatalf("불합격 결과는 갱신 거부: %d", code)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("불합격 결과가 기준선에 저장됨")
	}
	if code, _ := fin(t, passing(""), finishOptions{BaselinePath: path, Update: true, Only: "docs"}); code != 2 {
		t.Fatalf("--only 와 갱신은 함께 쓸 수 없음: %d", code)
	}
}

// 저장소에 커밋된 기준선은 현재 골든셋 전체를 담고 있고 mock 실행이 그 기준선과 맞는다.
func TestCommittedBaselineMatchesGoldenSet(t *testing.T) {
	base, err := evals.LoadBaseline()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), Config{Provider: llmgw.MockProvider{}, Model: "mock-1", PolicyPattern: "mock*"})
	if err != nil {
		t.Fatal(err)
	}
	d := evals.Compare(base, rep.States(), rep.suites())
	if len(d.NewFailures)+len(d.NewPasses)+len(d.Removed) != 0 || len(base.Cases) != len(rep.Results) {
		t.Fatalf("커밋된 기준선이 골든셋과 다름(갱신: go run ./cmd/eval --update-baseline): %+v 기준선 %d건 실행 %d건", d, len(base.Cases), len(rep.Results))
	}
}

// 기본 실행(플래그 없음)은 DB 도 저장소도 건드리지 않는다. --db 를 줄 때만 이력과 감사 로그가 남는다.
func TestRunHistorySavedOnlyWithDBFlag(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "erp.db")
	if code, out := fin(t, passing(""), finishOptions{BaselinePath: writeBase(t, passing(""))}); code != 0 {
		t.Fatal(out)
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("--db 없이 DB 파일이 생겼다")
	}
	path := writeBase(t, passing(""))
	if code, out := fin(t, passing("daa"), finishOptions{BaselinePath: path, DBPath: dbPath}); code != 1 || !strings.Contains(out, "실행 이력을 저장했습니다") {
		t.Fatalf("회귀 실행도 이력은 남긴다: %d\n%s", code, out)
	}
	db, err := dbutil.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	st := &llmgw.Store{DB: db}
	admin := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelWeb})
	runs, err := st.ListEvalRuns(admin, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("이력 1건: %v %v", runs, err)
	}
	r := runs[0]
	if r.Provider != "mock" || r.Total != 164 || r.Passed != 163 || r.PassRatePct != 99 || r.GatePassed || len(r.NewFailures) != 1 || r.NewFailures[0] != "docs/daa" || len(r.Cases) != 164 {
		t.Fatalf("저장된 요약: %+v", r)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'eval.run' AND target_type = 'eval_run'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("감사 로그 1건: %d %v", n, err)
	}
}

// 이력 조회는 eval:read(관리자·감사)만 가능하고 거부된다.
func TestListEvalRunsPermission(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	st := &llmgw.Store{DB: db}
	for role, want := range map[contract.Role]bool{contract.RoleAdmin: true, contract.RoleAuditor: true, contract.RoleFinance: false, contract.RoleManager: false, contract.RoleStaff: false} {
		ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: role, Channel: contract.ChannelWeb})
		_, err := st.ListEvalRuns(ctx, 5)
		if (err == nil) != want {
			t.Errorf("%s: 허용=%v 인데 오류=%v", role, want, err)
		}
	}
	_ = fake.NewRecorder()
	_ = audit.TargetEval
}

package aicost_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// fakeJournals 는 회계 서비스의 배부 전표 입구를 흉내 낸다(실제 전표 표는 cmd/server 실스택 시험이 다룬다).
type fakeJournals struct {
	status  map[string]string
	created []domain.AICostJournalInput
	failErr error
	seq     int
}

func newFakeJournals() *fakeJournals { return &fakeJournals{status: map[string]string{}} }

func (f *fakeJournals) CreateAICostDraft(_ context.Context, _ contract.Tx, in domain.AICostJournalInput) (string, error) {
	if f.failErr != nil {
		return "", f.failErr
	}
	f.seq++
	no := fmt.Sprintf("JV-AIC-%d", f.seq)
	f.status[no] = domain.JournalDraft
	f.created = append(f.created, in)
	return no, nil
}
func (f *fakeJournals) AICostJournalStatus(_ context.Context, _ contract.Tx, no string) (string, error) {
	st, ok := f.status[no]
	if !ok {
		return "", contract.ErrNotFound
	}
	return st, nil
}
func (f *fakeJournals) CancelAICostDraft(_ context.Context, _ contract.Tx, no, _ string) error {
	switch f.status[no] {
	case domain.JournalApproved:
		return fmt.Errorf("%w: 승인된 전표가 연결되어 있어 해제할 수 없습니다. 먼저 전표를 역분개하세요.", contract.ErrInvalidState)
	case domain.JournalRequested:
		return fmt.Errorf("%w: 전표가 승인 요청 중입니다. 먼저 전표의 승인 요청을 회수하세요.", contract.ErrInvalidState)
	}
	f.status[no] = domain.JournalCanceled
	return nil
}

// fakeSoD 는 규칙 하나를 차단 모드로 흉내 낸다(같은 사람일 때만).
type fakeSoD struct {
	block   map[string]bool
	checked []sod.Check
	blocked []sod.Check
}

func (f *fakeSoD) Evaluate(_ context.Context, _ contract.Tx, c sod.Check) (sod.Decision, error) {
	f.checked = append(f.checked, c)
	if c.Other == 0 || c.Other != c.Actor.UserID {
		return sod.Decision{Mode: sod.ModeOff}, nil
	}
	if f.block[c.Rule] {
		return sod.Decision{Mode: sod.ModeBlock, Violation: true}, &sod.BlockedError{Check: c}
	}
	return sod.Decision{Mode: sod.ModeWarn, Violation: true}, nil
}
func (f *fakeSoD) RecordBlocked(_ context.Context, c sod.Check) { f.blocked = append(f.blocked, c) }

type runEnv struct {
	*env
	j   *fakeJournals
	sod *fakeSoD
	now time.Time
}

func newRunEnv(t *testing.T) *runEnv {
	t.Helper()
	e := newEnv(t)
	e.seedOrg()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (4, 'fin2@pl.test', '재무2', 'finance', 900)`); err != nil {
		t.Fatal(err)
	}
	r := &runEnv{env: e, j: newFakeJournals(), sod: &fakeSoD{block: map[string]bool{}}, now: time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)}
	r.svc = r.mk()
	return r
}

func (r *runEnv) mk() *aicost.Service {
	svc, err := aicost.New(aicost.Options{DB: r.db, Txs: contract.SQLTxManager{DB: r.db}, Audit: r.rec, Journals: r.j, SoD: r.sod, Now: func() time.Time { return r.now }})
	if err != nil {
		r.t.Fatal(err)
	}
	return svc
}

func (r *runEnv) seedOctober() {
	r.usage("2026-10-02T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 2_000_000)
	r.usage("2026-10-03T01:00:00Z", 2, 901, 1, "knowledge.answer", "anthropic-main", "ok", 10, 10, 1_000_000)
	r.usage("2026-10-04T01:00:00Z", 1, 900, 1, "eval.run", "anthropic-main", "ok", 10, 10, 500_000)
	r.usage("2026-10-06T01:00:00Z", 0, 0, 0, "mcp.select_tool", "anthropic-main", "ok", 10, 10, 300_000)
	if err := r.svc.SetFX(finance, "2026-10", 1_000_000); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.svc.CreateDefaultRules(finance); err != nil {
		r.t.Fatal(err)
	}
}

var (
	finance2  = as(4, 900, contract.PermCostRead, contract.PermCostAllocate, contract.PermReportRead, contract.PermLedgerWrite)
	allocator = as(1, 900, contract.PermCostRead, contract.PermCostAllocate, contract.PermReportRead, contract.PermLedgerWrite)
)

// AC-4905·4906: 확정은 실행·부서별 줄·전표 초안·감사를 함께 쓴다. 확정한 달은 환율·청구액·재확정이 거부된다.
func TestConfirmRunWritesRunLinesJournalAndLocksMonth(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	run, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "confirmed" || run.RuleVersion != 1 || run.TotalKRW != 3800 || run.DirectKRW != 3000 || run.SharedKRW != 800 || run.JournalNo != "JV-AIC-1" || run.JournalStatus != "draft" || run.EntityID != 1 || run.ConfirmedByName != "재무" {
		t.Fatalf("실행: %+v", run)
	}
	if len(run.Lines) != 2 || run.Lines[1].DepartmentName != "영업" || run.Lines[1].TotalKRW != 3700 || run.Lines[0].TotalKRW != 100 || len(run.Pools) != 2 {
		t.Fatalf("줄: %+v", run.Lines)
	}
	// 전표 입력: 전기일은 그 달 말일, 부서별 줄 합이 총액, 규칙 버전이 메모에 있다.
	if len(r.j.created) != 1 {
		t.Fatalf("전표 %d건", len(r.j.created))
	}
	in := r.j.created[0]
	var sum int64
	for _, l := range in.Lines {
		sum += l.Amount
		if l.DepartmentID == 0 || !strings.Contains(l.Memo, "AI 비용") {
			t.Fatalf("줄: %+v", l)
		}
	}
	if in.PostingDate != "2026-10-31" || in.TradeDate != "2026-11-14" || sum != 3800 || in.RunID != run.ID || in.ExpenseCode != "5290" || !strings.Contains(in.Memo, "규칙 v1") {
		t.Fatalf("전표 입력: %+v", in)
	}
	if !r.rec.Has("ai_cost.run_confirm", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("확정 감사가 없다")
	}
	// 같은 달 다시 확정은 409, 확정한 달은 환율·청구액·재계산이 막힌다.
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이미 확정된 달입니다") {
		t.Fatalf("재확정: %v", err)
	}
	if err := r.svc.SetFX(finance, "2026-10", 1_100_000); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "확정된 달은 다시 계산할 수 없습니다") {
		t.Fatalf("환율 잠금: %v", err)
	}
	if err := r.svc.AddAdjustment(finance, "2026-10", "anthropic-main", 5000, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("청구액 잠금: %v", err)
	}
	// 서비스를 우회해도 DB 가 막는다.
	for _, q := range []string{`UPDATE ai_cost_fx SET usd_krw_x1000 = 1200000 WHERE month = '2026-10'`, `INSERT INTO ai_cost_adjustments (month, provider_code, actual_krw, entered_by, entered_at) VALUES ('2026-10', 'x', 1, 1, 't')`,
		`UPDATE ai_cost_run_lines SET total_krw = 1`, `DELETE FROM ai_cost_run_lines`, `UPDATE ai_cost_runs SET total_krw = 1`, `DELETE FROM ai_cost_runs`} {
		if _, err := r.db.Exec(q); err == nil {
			t.Fatalf("막혀야 한다: %s", q)
		}
	}
	// GetRun: 확정 실행·전표 상태가 보인다. 다른 달은 nil.
	got, err := r.svc.GetRun(reader, "2026-10", 0)
	if err != nil || got == nil || got.ID != run.ID || got.JournalStatus != "draft" {
		t.Fatalf("GetRun: %+v %v", got, err)
	}
	if none, err := r.svc.GetRun(reader, "2026-09", 0); err != nil || none != nil {
		t.Fatalf("없는 달: %+v %v", none, err)
	}
	// 확정한 달의 미리보기 호출은 여전히 된다(표시용). 11월(이번 달)은 확정할 수 없다.
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-11"}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "아직 끝나지 않은 달") {
		t.Fatalf("이번 달: %v", err)
	}
}

func TestConfirmRunGuards(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	if _, err := r.svc.ConfirmRun(reader, aicost.ConfirmInput{Month: "2026-10"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("cost:allocate 없음: %v", err)
	}
	noLedger := as(1, 900, contract.PermCostRead, contract.PermCostAllocate)
	if _, err := r.svc.ConfirmRun(noLedger, aicost.ConfirmInput{Month: "2026-10"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("전표 입력 권한 없음: %v", err)
	}
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-09"}); !errors.Is(err, contract.ErrInvalidState) { // 9월: 환율 없음
		t.Fatalf("환율 없음: %v", err)
	}
	// 전표를 못 만들면(마감된 달 등) 확정 전체가 취소된다.
	r.j.failErr = fmt.Errorf("%w: 이 달 장부가 마감되어 전표를 만들 수 없습니다. 장부 마감을 해제한 뒤 다시 확정하세요.", contract.ErrInvalidState)
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "장부가 마감되어") {
		t.Fatalf("전표 실패: %v", err)
	}
	var n int
	_ = r.db.QueryRow(`SELECT (SELECT COUNT(*) FROM ai_cost_runs) + (SELECT COUNT(*) FROM ai_cost_run_lines)`).Scan(&n)
	if n != 0 {
		t.Fatalf("전표 실패 뒤 실행이 남음: %d", n)
	}
	r.j.failErr = nil
	// 감사 기록이 실패해도 모두 취소된다.
	r.rec.FailWith(errors.New("감사 실패"))
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); err == nil {
		t.Fatal("감사 실패는 확정 실패여야 한다")
	}
	_ = r.db.QueryRow(`SELECT (SELECT COUNT(*) FROM ai_cost_runs) + (SELECT COUNT(*) FROM ai_cost_run_lines)`).Scan(&n)
	if n != 0 {
		t.Fatalf("감사 실패 뒤 실행이 남음: %d", n)
	}
	r.rec.FailWith(nil)
	// 법인 범위 밖.
	scoped := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleFinance, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms:       contract.NewPermSet(contract.PermCostRead, contract.PermCostAllocate, contract.PermLedgerWrite),
		EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermCostRead: contract.NewEntitySet(1), contract.PermCostAllocate: contract.NewEntitySet(1)}})
	if _, err := r.svc.ConfirmRun(scoped, aicost.ConfirmInput{Month: "2026-10", EntityID: 2}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("법인 범위 밖: %v", err)
	}
	// 정상 확정은 그대로 된다.
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); err != nil {
		t.Fatal(err)
	}
}

// 합계가 0 인 달은 전표 없이 실행만 확정한다. 규칙 작성자가 확정하면 규칙 모드대로(경고 통과/차단).
func TestConfirmRunEmptyMonthAndSoD(t *testing.T) {
	r := newRunEnv(t)
	if err := r.svc.SetFX(finance, "2026-09", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CreateDefaultRules(finance); err != nil { // 규칙 작성자 = 사용자 1
		t.Fatal(err)
	}
	run, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-09"})
	if err != nil || run.TotalKRW != 0 || run.JournalNo != "" || len(r.j.created) != 0 {
		t.Fatalf("빈 달: %+v %v", run, err)
	}
	found := false
	for _, c := range r.sod.checked {
		found = found || (c.Rule == sod.RuleAICostRuleConfirm && c.Other == 1 && c.Actor.UserID == 1)
	}
	if !found {
		t.Fatalf("규칙 작성자 판정이 없다: %+v", r.sod.checked)
	}
	// 차단 모드: 같은 사람이면 확정이 막히고 롤백 밖에 위반이 기록된다. 다른 사람은 통과.
	r.sod.block[sod.RuleAICostRuleConfirm] = true
	if err := r.svc.SetFX(finance, "2026-08", 1_000_000); err != nil {
		t.Fatal(err)
	}
	var be *sod.BlockedError
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-08"}); !errors.As(err, &be) || len(r.sod.blocked) != 1 {
		t.Fatalf("차단: %v %+v", err, r.sod.blocked)
	}
	if got, _ := r.svc.GetRun(reader, "2026-08", 0); got != nil {
		t.Fatal("차단된 확정이 남음")
	}
	if _, err := r.svc.ConfirmRun(finance2, aicost.ConfirmInput{Month: "2026-08"}); err != nil {
		t.Fatalf("다른 사람 확정: %v", err)
	}
}

// 해제: 요청 → 다른 재무가 확인해야 풀리고 임시 전표가 취소된다. 요청자 본인 확인·승인된 전표·승인 요청 중 전표는 거부한다.
func TestReleaseRequestConfirmFlow(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	run, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RequestRelease(reader, run.ID, "사유"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	if _, err := r.svc.RequestRelease(allocator, run.ID, " "); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 필수: %v", err)
	}
	if _, err := r.svc.ConfirmRelease(finance2, run.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "확인할 해제 요청이 없습니다") {
		t.Fatalf("요청 없이 확인: %v", err)
	}
	// 전표가 승인 요청 중이면 요청도 막힌다.
	r.j.status[run.JournalNo] = domain.JournalRequested
	if _, err := r.svc.RequestRelease(allocator, run.ID, "틀림"); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "승인 요청을 회수") {
		t.Fatalf("승인 요청 중: %v", err)
	}
	r.j.status[run.JournalNo] = domain.JournalApproved
	if _, err := r.svc.RequestRelease(allocator, run.ID, "틀림"); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "먼저 전표를 역분개하세요") {
		t.Fatalf("승인됨: %v", err)
	}
	r.j.status[run.JournalNo] = domain.JournalDraft
	got, err := r.svc.RequestRelease(allocator, run.ID, "규칙을 잘못 골랐습니다")
	if err != nil || got.Release == nil || got.Release.Status != "pending" || got.Release.RequestedByName != "재무" {
		t.Fatalf("요청: %+v %v", got.Release, err)
	}
	if _, err := r.svc.RequestRelease(finance2, run.ID, "또"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("중복 요청: %v", err)
	}
	// 요청자 본인은 확인할 수 없다.
	if _, err := r.svc.ConfirmRelease(allocator, run.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "요청한 사람이 확인할 수 없습니다") {
		t.Fatalf("본인 확인: %v", err)
	}
	// DB 도 같은 규칙을 지킨다.
	if _, err := r.db.Exec(`UPDATE ai_cost_release_requests SET status = 'confirmed', confirmed_by = requested_by`); err == nil {
		t.Fatal("요청자=확인자가 DB 에서 막혀야 한다")
	}
	// 전표가 승인 요청 중이면 확인도 막히고 요청은 그대로 남는다.
	r.j.status[run.JournalNo] = domain.JournalRequested
	if _, err := r.svc.ConfirmRelease(finance2, run.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("확인 때 승인 요청 중: %v", err)
	}
	r.j.status[run.JournalNo] = domain.JournalDraft
	out, err := r.svc.ConfirmRelease(finance2, run.ID)
	if err != nil || out.Status != "released" || out.ReleasedAt == "" || out.Release == nil || out.Release.Status != "confirmed" || out.JournalStatus != "canceled" {
		t.Fatalf("해제: %+v %v", out, err)
	}
	if !r.rec.Has("ai_cost.run_release_request", audit.TargetAICost, audit.ResultSuccess) || !r.rec.Has("ai_cost.run_release", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("해제 감사가 없다")
	}
	// 해제된 달은 환율을 다시 바꾸고 다시 확정할 수 있다(새 실행).
	if err := r.svc.SetFX(finance, "2026-10", 1_100_000); err != nil {
		t.Fatalf("해제 뒤 환율: %v", err)
	}
	again, err := r.svc.ConfirmRun(finance2, aicost.ConfirmInput{Month: "2026-10"})
	if err != nil || again.ID == run.ID || again.FxUsdKrwX1000 != 1_100_000 || again.JournalNo != "JV-AIC-2" {
		t.Fatalf("재확정: %+v %v", again, err)
	}
	if cur, _ := r.svc.GetRun(reader, "2026-10", 0); cur == nil || cur.ID != again.ID {
		t.Fatalf("GetRun 은 확정 실행을 우선한다: %+v", cur)
	}
	if _, err := r.svc.RequestRelease(allocator, run.ID, "해제된 실행"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("해제된 실행에 요청: %v", err)
	}
}

func TestReleaseRejectCancelAndExpire(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	run, _ := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"})
	if _, err := r.svc.RequestRelease(allocator, run.ID, "첫 요청"); err != nil {
		t.Fatal(err)
	}
	// 다른 사람이 반려하면 rejected, 확정은 그대로.
	out, err := r.svc.RejectRelease(finance2, run.ID)
	if err != nil || out.Status != "confirmed" || out.Release.Status != "rejected" {
		t.Fatalf("반려: %+v %v", out, err)
	}
	// 요청자 본인이 접으면 canceled.
	if _, err := r.svc.RequestRelease(allocator, run.ID, "두 번째"); err != nil {
		t.Fatal(err)
	}
	out, err = r.svc.RejectRelease(allocator, run.ID)
	if err != nil || out.Release.Status != "canceled" {
		t.Fatalf("취소: %+v %v", out, err)
	}
	if _, err := r.svc.RejectRelease(allocator, run.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("요청 없이 반려: %v", err)
	}
	// 7일이 지나면 확인할 수 없고 만료로 보이며, 새 요청이 낡은 요청을 닫는다.
	if _, err := r.svc.RequestRelease(allocator, run.ID, "세 번째"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(8 * 24 * time.Hour)
	late := r.mk()
	view, _ := late.GetRun(reader, "2026-10", 0)
	if view.Release == nil || view.Release.Status != "expired" {
		t.Fatalf("만료 표시: %+v", view.Release)
	}
	if _, err := late.ConfirmRelease(finance2, run.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "7일이 지나 만료") {
		t.Fatalf("만료 확인: %v", err)
	}
	if _, err := late.RequestRelease(allocator, run.ID, "네 번째"); err != nil {
		t.Fatalf("만료 뒤 새 요청: %v", err)
	}
}

func TestRegenerateJournal(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	run, _ := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"})
	if _, err := r.svc.RegenerateJournal(allocator, run.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이미 전표가 있습니다") {
		t.Fatalf("전표가 살아 있음: %v", err)
	}
	r.j.status[run.JournalNo] = domain.JournalCanceled
	if _, err := r.svc.RegenerateJournal(reader, run.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	out, err := r.svc.RegenerateJournal(allocator, run.ID)
	if err != nil || out.JournalNo != "JV-AIC-2" || out.JournalStatus != "draft" {
		t.Fatalf("다시 만들기: %+v %v", out, err)
	}
	if len(r.j.created) != 2 || r.j.created[1].RunID != run.ID {
		t.Fatalf("전표 입력: %+v", r.j.created)
	}
	var sum int64
	for _, l := range r.j.created[1].Lines {
		sum += l.Amount
	}
	if sum != 3800 || !r.rec.Has("ai_cost.journal_regenerate", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatalf("합계 %d", sum)
	}
}

func TestBudgetsMyReportAndLimitNotices(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	if _, err := r.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (1, 'cost:allocate', 1, 1), (4, 'cost:allocate', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	// 영업(901)의 팀장은 사용자 2(조직장으로 등록).
	if _, err := r.db.Exec(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE org_unit_spans SET head_user_id = 2 WHERE unit_id = 901`); err != nil {
		t.Fatal(err)
	}
	_, _ = r.db.Exec(`DELETE FROM org_write_guard`)
	if err := r.svc.SetBudget(reader, "2026-11", 901, 1000); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	for _, bad := range []struct {
		m string
		d int64
		v int64
	}{{"2026-13", 901, 1}, {"2026-11", 4242, 1}, {"2026-11", 901, -1}} {
		if err := r.svc.SetBudget(allocator, bad.m, bad.d, bad.v); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("검증 %+v: %v", bad, err)
		}
	}
	if err := r.svc.SetBudget(allocator, "2026-11", 901, 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetBudget(allocator, "2026-11", 901, 1000); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 값: %v", err)
	}
	if err := r.svc.SetBudget(allocator, "2026-11", 901, 2000); err != nil {
		t.Fatal(err)
	}
	var revs int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_budget_revisions WHERE department_id = 901 AND month = '2026-11'`).Scan(&revs)
	if revs != 2 {
		t.Fatalf("이력 %d건", revs)
	}
	if _, err := r.db.Exec(`DELETE FROM ai_cost_budgets`); err == nil {
		t.Fatal("예산 삭제가 막혀야 한다")
	}
	if err := r.svc.SetBudget(allocator, "2026-11", 901, 1000); err != nil { // 1000원으로 낮춤
		t.Fatal(err)
	}
	// 이번 달(11월) 영업 직접 비용: 환율은 11월이 없어 10월 값(1000원/달러)으로 어림. 0.79달러 = 790원 → 79%.
	r.usage("2026-11-05T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 790_000)
	if n, err := r.svc.CheckLimits(context.Background()); err != nil || n != 0 {
		t.Fatalf("79%% 는 알리지 않는다: %d %v", n, err)
	}
	my, err := r.svc.MyDepartment(manager, "")
	if err != nil || my.Budget == nil || my.Budget.AmountKRW != 1000 || my.Budget.UsedKRW != 790 || my.Budget.Pct != 79 || !my.Budget.FxEstimated {
		t.Fatalf("예산 대비: %+v %v", my.Budget, err)
	}
	rows, err := r.svc.ListBudgets(reader, "2026-11", 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("예산 목록: %+v %v", rows, err)
	}
	for _, b := range rows {
		if b.DepartmentID == 901 && (b.AmountKRW == nil || *b.AmountKRW != 1000 || b.UsedKRW != 790) {
			t.Fatalf("영업 줄: %+v", b)
		}
		if b.DepartmentID == 900 && b.AmountKRW != nil {
			t.Fatalf("예산 없는 부서: %+v", b)
		}
	}
	r.usage("2026-11-06T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 20_000) // 810원 = 81%
	n, err := r.svc.CheckLimits(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("80%% 알림: %d %v", n, err)
	}
	var notes int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'aicost_limit'`).Scan(&notes)
	if notes != 3 { // 부서장 1 + 재무 2
		t.Fatalf("알림 %d건(부서장 1 + 재무 2 여야 함)", notes)
	}
	var title string
	_ = r.db.QueryRow(`SELECT title FROM notifications WHERE kind = 'aicost_limit' AND user_id = 2`).Scan(&title)
	if !strings.Contains(title, "80%") {
		t.Fatalf("부서장 알림: %q", title)
	}
	// 같은 문턱은 다시 알리지 않는다(재시도·재기동).
	if n, err := r.svc.CheckLimits(context.Background()); err != nil || n != 0 {
		t.Fatalf("중복 알림: %d %v", n, err)
	}
	r.usage("2026-11-07T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 250_000) // 1060원 = 106%
	if n, err := r.svc.CheckLimits(context.Background()); err != nil || n != 1 {
		t.Fatalf("100%% 알림: %d %v", n, err)
	}
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'aicost_limit'`).Scan(&notes)
	if notes != 6 {
		t.Fatalf("알림 %d건", notes)
	}
}

// 환율이 하나도 없으면 원화로 비교할 수 없어 알리지 않는다.
func TestCheckLimitsSkipsWithoutFx(t *testing.T) {
	r := newRunEnv(t)
	if err := r.svc.SetBudget(allocator, "2026-11", 901, 100); err != nil {
		t.Fatal(err)
	}
	r.usage("2026-11-05T01:00:00Z", 3, 901, 1, "docverify.extract", "anthropic-main", "ok", 10, 10, 5_000_000)
	if n, err := r.svc.CheckLimits(context.Background()); err != nil || n != 0 {
		t.Fatalf("환율 없음: %d %v", n, err)
	}
	my, err := r.svc.MyDepartment(manager, "")
	if err != nil || my.Budget == nil || my.Budget.UsedKRW != 0 {
		t.Fatalf("환율 없는 예산 대비: %+v %v", my.Budget, err)
	}
}

func TestExportCSV(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober()
	name, body, err := r.svc.ExportCSV(reader, "2026-10", 0)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if name != "ai-cost-2026-10.csv" || !strings.HasPrefix(text, "\xEF\xBB\xBF월,법인,부서,") || !strings.Contains(text, "미리보기") || !strings.Contains(text, ",영업,2,2,3000,700,3700,") {
		t.Fatalf("미리보기 CSV: %q %q", name, text)
	}
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"}); err != nil {
		t.Fatal(err)
	}
	_, body, err = r.svc.ExportCSV(reader, "2026-10", 0)
	if err != nil || !strings.Contains(string(body), "확정") || strings.Contains(string(body), "미리보기") {
		t.Fatalf("확정 CSV: %q %v", body, err)
	}
	if strings.Contains(string(body), "영업사원") || strings.Contains(string(body), "@") {
		t.Fatalf("CSV 에 사용자 이름이 있다: %q", body)
	}
	if !r.rec.Has("ai_cost.export", audit.TargetAICost, audit.ResultSuccess) {
		t.Fatal("내려받기 감사가 없다")
	}
	if _, _, err := r.svc.ExportCSV(manager, "2026-10", 0); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	// 환율이 없는 달은 막는 문제가 그대로 오류로 나온다.
	if _, _, err := r.svc.ExportCSV(reader, "2026-07", 0); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("환율 없는 달: %v", err)
	}
}

func TestRuleEngineRulesExist(t *testing.T) {
	for code, def := range map[string]string{sod.RuleAICostRuleConfirm: sod.ModeWarn, sod.RuleAICostConfirmPost: sod.ModeBlock} {
		rule, ok := sod.RuleByCode(code)
		if !ok || rule.Kind != sod.KindTxn || rule.Default != def || rule.Locked {
			t.Fatalf("규칙 %s: %+v %v", code, rule, ok)
		}
	}
	_ = fake.NewRecorder
}

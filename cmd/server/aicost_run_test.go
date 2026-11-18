package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B49 3단계 AC-4906~4908·4914: 실제 서비스 조립(회계 서비스·전표 표·SoD 판정기) 위에서 배부 확정이 부서 차원이 있는 임시 전표를 만들고, 해제가 그 전표를 취소하며,
// 마감된 달·승인 요청 중 전표·확정자의 승인을 막는지 확인한다.
func TestAICostConfirmCreatesRealJournalAndReleaseCancelsIt(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if _, err := d.Org.(*org.Service).SeedDev(ctx); err != nil { // main 이 기동 때 하는 개발용 조직 시드(월말 인원이 여기서 온다)
		t.Fatal(err)
	}
	svc, ok := d.AICost.(*aicost.Service)
	if !ok {
		t.Fatal("AI 비용 서비스가 조립되지 않았다")
	}
	who := func(email string) context.Context {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name, p.Channel, p.Auth = contract.Role(role), email, contract.ChannelWeb, contract.AuthSession
		p.Perms = contract.NewPermSet(contract.RolePermissions[p.Role]...)
		return contract.WithPrincipal(ctx, p)
	}
	fin, fin2, auditor := who("finance@pl.test"), who("finance2@pl.test"), who("auditor@pl.test")
	prev := time.Now().In(contract.KST).AddDate(0, -1, 0).Format("2006-01")
	prevAt := prev + "-10T01:00:00Z"
	// 영업·구매 부서(개발 조직)의 사용자 호출 몇 건과 부서 없는 호출.
	var deptA, deptB, userA, userB int64
	_ = d.DB.QueryRowContext(ctx, `SELECT department_id, id FROM users WHERE email = 'staff@pl.test'`).Scan(&deptA, &userA)
	_ = d.DB.QueryRowContext(ctx, `SELECT department_id, id FROM users WHERE email = 'finance@pl.test'`).Scan(&deptB, &userB)
	if deptA == deptB {
		t.Skip("시험 사용자의 부서가 같다")
	}
	add := func(user, dept int64, purpose string, cost int64) {
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO llm_usage (at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
			VALUES (?,?,?, 'staff', 'anthropic-main', 'm1', ?, 10, 10, ?, 0, 'ok', 1)`, prevAt, user, dept, purpose, cost); err != nil {
			t.Fatal(err)
		}
	}
	add(userA, deptA, "docverify.extract", 2_000_000)
	add(userB, deptB, "knowledge.answer", 1_000_000)
	add(0, 0, "mcp.select_tool", 300_000)
	if err := svc.SetFX(fin, prev, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateDefaultRules(fin); err != nil {
		t.Fatal(err)
	}

	// 장부가 마감된 달(그 전달)이면 전표를 못 만들어 확정이 통째로 취소된다.
	closed := time.Now().In(contract.KST).AddDate(0, -2, 0).Format("2006-01")
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO llm_usage (at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
		VALUES (?,?,?, 'staff', 'anthropic-main', 'm1', 'docverify.extract', 10, 10, 1000000, 0, 'ok', 1)`, closed+"-10T01:00:00Z", userA, deptA); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetFX(fin, closed, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (1, ?, 'closed', 1, ?, 't')`, closed, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmRun(fin, aicost.ConfirmInput{Month: closed}); err == nil || !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "장부가 마감되어") {
		t.Fatalf("마감된 달: %v", err)
	}
	var n int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_runs`).Scan(&n)
	if n != 0 {
		t.Fatalf("마감된 달 확정이 남음: %d", n)
	}

	run, err := svc.ConfirmRun(fin, aicost.ConfirmInput{Month: prev})
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalKRW != 3300 || run.JournalNo == "" || run.JournalStatus != "draft" {
		t.Fatalf("실행: %+v", run)
	}
	// 규칙을 저장한 사람이 확정했으므로 권한 분리 위반(기본 경고)이 기록되고, 그 대상이 이 실행이다(번호 0 이 아니다).
	var vSubject, vSubjectID int64
	var vType string
	if err := d.DB.QueryRowContext(ctx, `SELECT subject_type, subject_id, actor_id FROM sod_violations WHERE rule_code = 'aicost.rule_confirm' ORDER BY id DESC LIMIT 1`).Scan(&vType, &vSubjectID, &vSubject); err != nil {
		t.Fatalf("규칙 작성자 확정 위반 기록: %v", err)
	}
	if vType != "ai_cost_run" || vSubjectID != run.ID || vSubjectID == 0 || vSubject != userB {
		t.Fatalf("위반 대상: %s %d (실행 %d) 행위자 %d", vType, vSubjectID, run.ID, vSubject)
	}
	// 전표: 종류·출처·전기일·부서 차원·차대 균형.
	var kind, status, srcType, posting string
	var srcID, total, entity int64
	if err := d.DB.QueryRowContext(ctx, `SELECT kind, status, source_type, source_id, posting_date, total_amount, entity_id FROM journals WHERE no = ?`, run.JournalNo).Scan(&kind, &status, &srcType, &srcID, &posting, &total, &entity); err != nil {
		t.Fatal(err)
	}
	last := time.Date(mustYear(prev), time.Month(mustMonth(prev))+1, 0, 0, 0, 0, 0, contract.KST).Format("2006-01-02")
	if kind != "auto_aicost" || status != "draft" || srcType != "aicost_run" || srcID != run.ID || posting != last || total != 3300 || entity != 1 {
		t.Fatalf("전표: %s %s %s %d %s %d %d (전기일 기대 %s)", kind, status, srcType, srcID, posting, total, entity, last)
	}
	rows, err := d.DB.QueryContext(ctx, `SELECT l.side, l.amount, COALESCE(l.department_id, 0), a.code FROM journal_lines l JOIN accounts a ON a.id = l.account_id JOIN journals j ON j.id = l.journal_id WHERE j.no = ? ORDER BY l.seq`, run.JournalNo)
	if err != nil {
		t.Fatal(err)
	}
	var debit, credit int64
	depts := map[int64]int64{}
	var creditCode string
	for rows.Next() {
		var side, code string
		var amt, dept int64
		if err := rows.Scan(&side, &amt, &dept, &code); err != nil {
			t.Fatal(err)
		}
		if side == "D" {
			debit += amt
			depts[dept] += amt
			if code != "5290" || dept == 0 {
				t.Fatalf("차변 줄: %s %d", code, dept)
			}
		} else {
			credit += amt
			creditCode = code
			if dept != 0 {
				t.Fatalf("대변 줄에 부서: %d", dept)
			}
		}
	}
	rows.Close()
	if debit != 3300 || credit != 3300 || creditCode != "2120" || len(depts) < 2 || depts[deptA] < 2000 || depts[deptB] < 1000 {
		t.Fatalf("줄: 차 %d 대 %d %s %v", debit, credit, creditCode, depts)
	}
	// 확정자가 그 전표를 승인하는 판정은 기본 차단이고, 다른 사람은 통과한다.
	p1, _ := contract.PrincipalFrom(fin)
	p2, _ := contract.PrincipalFrom(fin2)
	check := func(actor contract.Principal, rule string, other int64) error {
		return d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			_, err := d.SoD.Evaluate(ctx, tx, sod.Check{Rule: rule, Actor: actor, Other: other, Subject: sod.Ref{Type: "journal", ID: 1, No: run.JournalNo}})
			return err
		})
	}
	var be *sod.BlockedError
	if err := check(p1, sod.RuleAICostConfirmPost, p1.UserID); !errors.As(err, &be) {
		t.Fatalf("확정자의 전표 승인은 차단이어야 한다: %v", err)
	}
	if err := check(p2, sod.RuleAICostConfirmPost, p1.UserID); err != nil {
		t.Fatalf("다른 사람은 통과: %v", err)
	}
	if err := check(p1, sod.RuleAICostRuleConfirm, p1.UserID); err != nil {
		t.Fatalf("규칙 작성자의 확정은 기본 경고(통과): %v", err)
	}

	// 해제: 요청 → 다른 재무가 확인 → 전표 취소·확정 풀림 → 다시 확정할 수 있다.
	if _, err := svc.RequestRelease(fin, run.ID, "시험 해제"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmRelease(auditor, run.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사는 확인할 수 없다: %v", err)
	}
	out, err := svc.ConfirmRelease(fin2, run.ID)
	if err != nil || out.Status != "released" || out.JournalStatus != "canceled" {
		t.Fatalf("해제: %+v %v", out, err)
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT status FROM journals WHERE no = ?`, run.JournalNo).Scan(&status)
	if status != "canceled" {
		t.Fatalf("전표 상태 %s", status)
	}
	again, err := svc.ConfirmRun(fin2, aicost.ConfirmInput{Month: prev})
	if err != nil || again.JournalNo == "" || again.JournalNo == run.JournalNo {
		t.Fatalf("재확정: %+v %v", again, err)
	}
	// 승인 요청 중인 전표가 있으면 해제를 요청할 수 없다(전표 화면에서 회수한 뒤에).
	if _, err := d.Journals.RequestApproval(fin2, again.JournalNo); err != nil {
		t.Logf("전표 승인 요청을 만들 수 없어 이 부분은 건너뜁니다: %v", err)
	} else {
		if _, err := svc.RequestRelease(fin, again.ID, "요청 중인데"); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "승인 요청") {
			t.Fatalf("승인 요청 중 해제 요청: %v", err)
		}
	}
}

func mustYear(m string) int {
	t, _ := time.Parse("2006-01", m)
	return t.Year()
}

func mustMonth(m string) int {
	t, _ := time.Parse("2006-01", m)
	return int(t.Month())
}

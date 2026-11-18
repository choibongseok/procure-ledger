package evalset_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/evalset"
)

// 판정 후속 1: 끝난 실행의 문항 결과는 DB 에서도 고치거나 지울 수 없다. 진행 중 실행은 넣고 지울 수 있다(실패 정리).
func TestRunItemsAreImmutableOnceRunFinished(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"가", "가", "contains"})
	var runID int64
	if err := e.db.QueryRow(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status) VALUES ('x','p',1,0,0,90,0,(SELECT id FROM eval_sets WHERE code='faq'),1,'running') RETURNING id`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO eval_run_items (run_id, item_seq, pass, score) VALUES (?, 1, 0, 0)`, runID); err != nil {
		t.Fatalf("진행 중 실행에 INSERT: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE eval_run_items SET pass = 1 WHERE run_id = ?`, runID); err != nil {
		t.Fatalf("진행 중 실행은 고칠 수 있음: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE eval_runs SET status = 'done' WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE eval_run_items SET pass = 1 WHERE run_id = ?`, runID); err == nil || !strings.Contains(err.Error(), "고칠 수 없습니다") {
		t.Fatalf("끝난 실행의 UPDATE: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM eval_run_items WHERE run_id = ?`, runID); err == nil || !strings.Contains(err.Error(), "고칠 수 없습니다") {
		t.Fatalf("끝난 실행의 DELETE: %v", err)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM eval_run_items WHERE run_id = ?`, runID).Scan(&n)
	if n != 1 {
		t.Fatalf("문항 결과 %d건", n)
	}
	// 실패한 실행도 끝난 것이다.
	var failedID int64
	_ = e.db.QueryRow(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status) VALUES ('x','p',1,0,0,90,0,(SELECT id FROM eval_sets WHERE code='faq'),1,'running') RETURNING id`).Scan(&failedID)
	_, _ = e.db.Exec(`INSERT INTO eval_run_items (run_id, item_seq, pass, score) VALUES (?, 1, 0, 0)`, failedID)
	if _, err := e.db.Exec(`UPDATE eval_runs SET status = 'failed', fail_reason = '중단' WHERE id = ?`, failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`DELETE FROM eval_run_items WHERE run_id = ?`, failedID); err == nil {
		t.Fatal("실패한 실행의 문항 결과 삭제가 통과함")
	}
}

// 판정 후속 2: llm 채점 문항은 기대 답이 비어도 된다(루브릭의 필수·금지 줄로 채점). 그 밖의 채점은 기대 답이 필요하다. CSV 도 같다.
func TestLLMItemAllowsEmptyExpected(t *testing.T) {
	e := newEnv(t)
	e.newSet("faq", evalset.PurposeText)
	r, err := e.svc.SaveItem(admin, "faq", evalset.ItemInput{Question: "환불 규정은?", Grader: evalset.GraderLLM, Rubric: "필수: 환불, 7일\n금지: 불가"})
	if err != nil || r.Item.Expected != "" || r.Item.Grader != "llm" {
		t.Fatalf("llm 빈 기대 답: %+v %v", r.Item, err)
	}
	if _, err := e.svc.SaveItem(admin, "faq", evalset.ItemInput{Question: "q", Grader: evalset.GraderExact}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "기대 답") {
		t.Fatalf("exact 빈 기대 답: %v", err)
	}
	if _, err := e.svc.SaveItem(admin, "faq", evalset.ItemInput{Question: "q", Grader: evalset.GraderLLM}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "필수") {
		t.Fatalf("llm 은 루브릭 줄이 필요: %v", err)
	}
	csv := "문항,기대,채점,허용오차,치명,태그,맥락,기준\n연차는?,,LLM,,,,,\"필수: 15일\"\n배송은?,,정확,,,,,\n"
	if _, err := e.svc.ImportCSV(admin, "faq", []byte(csv)); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "3행") {
		t.Fatalf("csv: 정확 일치의 빈 기대 답은 3행 오류여야 함: %v", err)
	}
	ok := "문항,기대,채점,허용오차,치명,태그,맥락,기준\n연차는?,,LLM,,,,,\"필수: 15일\"\n"
	if res, err := e.svc.ImportCSV(admin, "faq", []byte(ok)); err != nil || res.Imported != 1 {
		t.Fatalf("csv llm 빈 기대 답: %+v %v", res, err)
	}
	// 게시·내보내기·왕복.
	d, err := e.svc.Publish(admin, "faq", "")
	if err != nil || len(d.Items) != 2 {
		t.Fatalf("게시: %+v %v", d, err)
	}
	out, _, err := e.svc.ExportCSV(admin, "faq", 0)
	if err != nil || !strings.Contains(string(out), "연차는?") {
		t.Fatalf("내보내기: %s %v", out, err)
	}
	// 채점: 기대 답과 무관하게 루브릭으로 채점한다.
	if g := evalset.Grade(d.Items[0], "곧 처리됩니다"); g.Pass {
		t.Fatalf("필수 낱말이 없는데 통과: %+v", g)
	}
	if g := evalset.Grade(d.Items[0], "환불은 7일 안에 가능합니다"); !g.Pass {
		t.Fatalf("루브릭 통과가 실패: %+v", g)
	}
}

// 판정 후속 3: 직전 게시본과 문항이 같은 초안은 게시되지 않는다(409 상당). 바뀌면 게시된다.
func TestPublishRejectsUnchangedDraft(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"가", "가", "contains"})
	if _, err := e.svc.NewDraft(admin, "faq"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Publish(admin, "faq", ""); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "직전 게시본과 문항이 같습니다") {
		t.Fatalf("변경 없는 게시: %v", err)
	}
	var versions int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM eval_set_versions`).Scan(&versions)
	if versions != 2 {
		t.Fatalf("버전 %d개(게시되지 않은 초안이 남아 있어야 함)", versions)
	}
	e.text("faq", "나", "나", "contains")
	d, err := e.svc.Publish(admin, "faq", "")
	if err != nil || d.CurrentVersion != 2 {
		t.Fatalf("바뀐 초안 게시: %+v %v", d.SetView, err)
	}
	// 문항을 지웠다가 같은 문항을 되살려 직전과 같아져도 같은 해시라 거부된다.
	if _, err := e.svc.NewDraft(admin, "faq"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Publish(admin, "faq", ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번째 변경 없는 게시: %v", err)
	}
}

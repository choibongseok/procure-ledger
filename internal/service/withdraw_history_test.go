package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// stalledSubmitted 는 영업1팀 직원의 건이 상신된 뒤 배정자(영업1팀 팀장)가 퇴사해 멈춘 건이다.
func stalledSubmitted(t *testing.T, e *env) domain.PurchaseRequest {
	t.Helper()
	pr := e.submitted(t, uStaff2, 500_000)
	e.deactivate(t, uLead2)
	return pr
}

// 회수는 감사 로그뿐 아니라 결재 이력에도 한 줄 남고, 기존 이력은 그대로다.
func TestWithdrawLeavesHistoryRow(t *testing.T) {
	e := newEnv(t)
	pr := stalledSubmitted(t, e)
	before := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, pr.ID)
	if _, err := e.purchase.Withdraw(as(uStaff2), pr.ID); err != nil {
		t.Fatalf("회수: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, pr.ID); n != before+1 {
		t.Fatalf("이력 %d → %d, 한 줄 늘어야 함", before, n)
	}
	hist, err := e.approval.History(as(uStaff2), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := hist[len(hist)-1]
	if last.Action != "withdraw" || last.ActorID != uStaff2 {
		t.Fatalf("회수 이력: %+v", last)
	}
	if hist[0].Action != domain.ActionSubmit {
		t.Fatalf("기존 이력이 바뀜: %+v", hist[0])
	}
	if _, ok := lastRecord(e, "purchase_request.withdraw"); !ok {
		t.Fatal("회수 감사 기록 없음")
	}
	// 다시 상신하면 이력이 이어진다(회수 → 재상신). 퇴사한 팀장은 건너뛰고 대표까지 올라간다.
	if _, err := e.purchase.Submit(as(uStaff2), pr.ID); err != nil {
		t.Fatalf("재상신: %v", err)
	}
}

// 감사 기록이 실패하면 회수와 이력 행이 함께 롤백된다.
func TestWithdrawAuditFailureRollsBackHistory(t *testing.T) {
	e := newEnv(t)
	pr := stalledSubmitted(t, e)
	before := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, pr.ID)
	e.rec.FailWith(errBoom)
	_, err := e.purchase.Withdraw(as(uStaff2), pr.ID)
	is(t, err, contract.ErrAuditFailed)
	e.rec.FailWith(nil)
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, pr.ID); n != before {
		t.Fatalf("이력이 남음: %d → %d", before, n)
	}
	got, _ := e.purchase.Get(as(uStaff2), pr.ID)
	if got.Status != domain.PRSubmitted {
		t.Fatalf("회수가 롤백되지 않음: %s", got.Status)
	}
}

// 회수 이력 행도 수정·삭제할 수 없고, 요청자가 아닌 사람 명의의 회수 행은 넣을 수 없다.
func TestWithdrawHistoryImmutable(t *testing.T) {
	e := newEnv(t)
	pr := stalledSubmitted(t, e)
	if _, err := e.purchase.Withdraw(as(uStaff2), pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE approval_history SET comment='변조' WHERE pr_id=? AND action='withdraw'`, pr.ID); err == nil {
		t.Fatal("회수 이력 수정이 허용됨")
	}
	if _, err := e.db.Exec(`DELETE FROM approval_history WHERE pr_id=? AND action='withdraw'`, pr.ID); err == nil {
		t.Fatal("회수 이력 삭제가 허용됨")
	}
	if _, err := e.db.Exec(`INSERT INTO approval_history (pr_id, round, seq, actor_id, action, at) VALUES (?,1,0,?, 'withdraw', 'x')`, pr.ID, uHead); err == nil {
		t.Fatal("요청자가 아닌 사람의 회수 행이 허용됨")
	}
}

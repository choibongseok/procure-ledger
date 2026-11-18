package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func (e *env) stalled(t *testing.T, by, prID int64) bool {
	t.Helper()
	pr, err := e.purchase.Get(as(by), prID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.purchase.StalledIDs(as(by), []domain.PurchaseRequest{pr})
	if err != nil {
		t.Fatal(err)
	}
	return m[prID]
}

// 대신 결재를 포함해 한 사람이 같은 차수의 두 단계를 결재할 수 없다. 거부는 감사에 남고 이력은 늘지 않는다.
// 수임자(부서장)가 뒤 단계(부서장)의 유일한 결재자이면 앞 단계 대신 결재를 결재 시점에 거부한다(B9). 멈추지 않으므로 회수는 필요 없다.
func TestDelegateCannotDecideEarlierStepWhenSoleLaterApprover(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead, uHead)
	pr := e.submitted(t, uStaff, 20_000_000) // 팀장 → 부서장 → 재무
	deny := func(ctx context.Context) {
		t.Helper()
		before := count(t, e.db, `SELECT COUNT(*) FROM approval_history`)
		_, err := e.approval.Decide(ctx, pr.ID, domain.Decision{Approve: true})
		is(t, err, contract.ErrForbidden)
		if !strings.Contains(err.Error(), "결재가 멈춥니다") {
			t.Fatalf("한국어 안내 아님: %v", err)
		}
		if count(t, e.db, `SELECT COUNT(*) FROM approval_history`) != before {
			t.Fatal("거부됐는데 이력이 남음")
		}
		if !e.rec.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) {
			t.Fatal("거부 감사 기록 없음")
		}
		if st, _ := e.purchase.Get(as(uStaff), pr.ID); st.Steps[0].Status != domain.StepPending {
			t.Fatalf("단계가 바뀜: %+v", st.Steps)
		}
	}
	deny(as(uHead))
	// MCP 채널로 들어와도 같은 서비스가 막는다.
	mcp := users[uHead]
	mcp.Channel = contract.ChannelMCP
	deny(contract.WithPrincipal(context.Background(), mcp))
	if e.stalled(t, uStaff, pr.ID) {
		t.Fatal("멈추지 않아야 함")
	}
	// 1단계 배정자(팀장)가 직접 결재하면 부서장이 2단계를 결재한다.
	approve(t, e, uLead, pr.ID)
	approve(t, e, uHead, pr.ID)
	approve(t, e, uFinance, pr.ID)
}

// 위임 해제는 이제 건을 멈추게 하지 않는다: 직책 단계는 배정된 결재자가 직접 결재할 수 있어 위임에 기대는 건이 없다(명세 3.6).
// 결재 단계를 맡은 사람이 위임을 해제해도 거부되지 않고, 해제 뒤에는 수임자가 결재할 수 없다.
func TestRevokeDoesNotStrandBecauseAssigneeDecidesDirectly(t *testing.T) {
	e := newEnv(t)
	e.addSalesHead(t)
	d := e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 20_000_000) // 영업1팀 팀장 → 영업부서장 → 재무
	if err := e.delegation.Revoke(as(uLead2), d.ID); err != nil {
		t.Fatalf("해제: %v", err)
	}
	if !e.rec.Has("delegation.revoke", audit.TargetDelegation, audit.ResultSuccess) {
		t.Fatal("해제 감사 기록 없음")
	}
	_, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)
	if e.stalled(t, uStaff2, pr.ID) {
		t.Fatal("배정자가 있으니 멈춘 건이 아님")
	}
	approve(t, e, uLead2, pr.ID)
}

// 반려 뒤 재상신된 차수에서는 이전 차수에서 결재한 사람이 다시 결재할 수 있다.
func TestRejectedResubmitAllowsDecidingAgain(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 20_000_000)
	approve(t, e, uLead, pr.ID)
	approve(t, e, uHead, pr.ID)
	if _, err := e.approval.Decide(as(uFinance), pr.ID, domain.Decision{Comment: "증빙 부족"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	approve(t, e, uLead, pr.ID)
	approve(t, e, uHead, pr.ID)
}

// 한 사람이 같은 결재선의 두 단계에 배정되지 않는다: 겸직 조직장이라 앞 단계와 같은 사람이면 위로 올린다(명세 3.1).
func TestSubmitNeverAssignsOnePersonTwice(t *testing.T) {
	e := newEnv(t)
	// 구매부서장(uHead)이 구매1팀 팀장을 겸임한다. 구매1팀 직원 요청의 팀장·부서장 단계 후보가 모두 uHead 이다.
	e.org.AddConcurrent(uHead, unitBuy1)
	e.org.SetHead(unitBuy1, uHead)
	pr := e.submitted(t, uStaff, 2_000_000)
	names := e.lineNames(t, uStaff, pr.ID)
	if len(names) != 2 || names[0] != "부서장" || names[1] != "대표" {
		t.Fatalf("같은 사람은 위로 올라가야 함: %v", names)
	}
	if !strings.Contains(pr.Steps[1].RaisedNote, "앞 단계 결재자와 같은 분") {
		t.Fatalf("올림 사유: %q", pr.Steps[1].RaisedNote)
	}
}

// 해제된 위임으로는 진행 중 건을 더 이상 볼 수 없다(이력·감사에만 남는다).
func TestViewEndsWithRevoke(t *testing.T) {
	e := newEnv(t)
	e.addSalesHead(t)
	d := e.mustDelegate(t, uLead2, uLead)
	pr := e.submitted(t, uStaff2, 2_000_000) // 영업1팀 팀장 → 영업부서장
	approve(t, e, uLead, pr.ID)              // 대신 결재
	if _, err := e.purchase.Get(as(uLead), pr.ID); err != nil {
		t.Fatalf("위임이 유효한 동안은 열람: %v", err)
	}
	if err := e.delegation.Revoke(as(uLead2), d.ID); err != nil {
		t.Fatalf("해제(이 건은 부서장 단계가 남아 있음): %v", err)
	}
	if _, err := e.purchase.Get(as(uLead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("해제 뒤에도 열람됨: %v", err)
	}
	if _, err := e.approval.History(as(uLead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("해제 뒤에도 이력 열람됨: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE on_behalf_of = ?`, uLead2); n != 1 {
		t.Fatalf("대신 결재 이력은 남아야 함: %d", n)
	}
}

// 기간이 끝난 위임으로도 열람되지 않는다.
func TestViewEndsWhenDelegationExpires(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead2, uLead)
	pr := e.submitted(t, uStaff2, 500_000)
	if _, err := e.purchase.Get(as(uLead), pr.ID); err != nil {
		t.Fatalf("유효한 동안 열람: %v", err)
	}
	e.now = e.now.Add(48 * time.Hour)
	if _, err := e.purchase.Get(as(uLead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("만료 뒤 열람됨: %v", err)
	}
}

// 배정자가 퇴사하면 결재자 없음이 되고 요청자만 회수할 수 있다. 위임은 그 건을 풀어 주지 못한다.
func TestStalledWhenAssigneeLeavesAndWithdrawFlow(t *testing.T) {
	e := newEnv(t)
	d := e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 500_000)
	if e.stalled(t, uStaff2, pr.ID) {
		t.Fatal("배정자가 재직 중이면 멈춘 건이 아님")
	}
	// 결재할 수 있는 사람이 있으면 회수할 수 없다. 요청자 본인만 회수한다.
	_, err := e.purchase.Withdraw(as(uStaff2), pr.ID)
	is(t, err, contract.ErrInvalidState)
	_, err = e.purchase.Withdraw(as(uLead2), pr.ID)
	is(t, err, contract.ErrForbidden)

	e.deactivate(t, uLead2)
	if !e.stalled(t, uStaff2, pr.ID) {
		t.Fatal("배정자 퇴사 뒤 결재자 없음으로 표시돼야 함(위임자가 퇴사하면 위임도 효력이 없다)")
	}
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 0 {
		t.Fatalf("퇴사한 위임자의 수임자 결재함: %+v", p)
	}
	got, err := e.purchase.Withdraw(as(uStaff2), pr.ID)
	if err != nil || got.Status != domain.PRDraft {
		t.Fatalf("회수: %+v %v", got, err)
	}
	_ = d
	// 재상신하면 결재선을 새로 만든다: 퇴사한 팀장은 건너뛰고 영업팀 부서장이 공석이라 대표까지 올라간다.
	re, err := e.purchase.Submit(as(uStaff2), pr.ID)
	if err != nil {
		t.Fatalf("재상신: %v", err)
	}
	if names := e.lineNames(t, uStaff2, re.ID); len(names) != 1 || names[0] != "대표" {
		t.Fatalf("새 결재선: %v", names)
	}
	approve(t, e, uCEO, pr.ID)
}

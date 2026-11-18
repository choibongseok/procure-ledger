package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func tabIDs(t *testing.T, e *env, by int64, tab string) []int64 {
	t.Helper()
	list, err := e.approval.ListInbox(as(by), tab)
	if err != nil {
		t.Fatalf("결재함 %s(%d): %v", tab, by, err)
	}
	ids := []int64{}
	for _, pr := range list {
		ids = append(ids, pr.ID)
	}
	return ids
}

func wantInbox(t *testing.T, e *env, by int64, tab string, want ...int64) {
	t.Helper()
	got := tabIDs(t, e, by, tab)
	if len(got) != len(want) {
		t.Fatalf("사용자 %d %s 탭: %v, 기대 %v", by, tab, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("사용자 %d %s 탭: %v, 기대 %v", by, tab, got, want)
		}
	}
}

// 결재함 탭: 대기=지금 내 차례, 진행=결재선에 있고 안 끝난 건(내 차례 아님·이미 결재한 건 포함), 완료=내가 결재한 끝난 건.
func TestInboxTabsFollowTheApprovalLine(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 20_000_000) // 팀장 → 부서장 → 재무
	id := pr.ID

	wantInbox(t, e, uLead, "pending", id)
	wantInbox(t, e, uLead, "progress")
	wantInbox(t, e, uLead, "done")
	// 아직 차례가 아닌 부서장·재무는 진행에 보인다.
	wantInbox(t, e, uHead, "pending")
	wantInbox(t, e, uHead, "progress", id)
	wantInbox(t, e, uFinance, "progress", id)
	// 요청자 본인은 어느 탭에도 없다.
	for _, tab := range []string{"pending", "progress", "done"} {
		wantInbox(t, e, uStaff, tab)
	}

	approve(t, e, uLead, id)
	wantInbox(t, e, uLead, "pending")
	wantInbox(t, e, uLead, "progress", id) // 이미 결재했지만 건이 아직 안 끝났다
	wantInbox(t, e, uLead, "done")
	wantInbox(t, e, uHead, "pending", id)
	wantInbox(t, e, uHead, "progress")

	approve(t, e, uHead, id)
	approve(t, e, uFinance, id)
	for _, uid := range []int64{uLead, uHead, uFinance} {
		wantInbox(t, e, uid, "pending")
		wantInbox(t, e, uid, "progress")
		wantInbox(t, e, uid, "done", id)
	}
}

// 반려된 건도 반려한 사람의 완료 탭에 남고, 아직 결재하지 않은 뒤 단계 결재자의 진행 탭에서는 사라진다.
func TestInboxTabsRejectedGoesToDone(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 3_000_000)
	approve(t, e, uLead, pr.ID)
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false, Comment: "단가 재확인"}); err != nil {
		t.Fatal(err)
	}
	wantInbox(t, e, uHead, "done", pr.ID)
	wantInbox(t, e, uLead, "done", pr.ID)
	wantInbox(t, e, uHead, "progress")
	wantInbox(t, e, uFinance, "progress")
}

// 다른 부서 직원·결재선에 없는 결재자·감사·관리자는 어느 탭에서도 이 건을 보지 못한다(진행 중이든 끝난 뒤든).
func TestInboxTabsHideUnrelatedUsers(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 20_000_000) // 구매팀 요청: 팀장 → 부서장 → 재무
	unrelated := []int64{uStaff2, uLead2, uLead3, uAuditor, uAdmin}
	check := func(stage string) {
		for _, uid := range unrelated {
			for _, tab := range []string{"pending", "progress", "done"} {
				if got := tabIDs(t, e, uid, tab); len(got) != 0 {
					t.Fatalf("%s: 무관한 사용자 %d 의 %s 탭에 건이 보임: %v", stage, uid, tab, got)
				}
			}
		}
		// 요청자 본인은 어느 탭에도 없다(진행 탭 포함).
		for _, tab := range []string{"pending", "progress", "done"} {
			if got := tabIDs(t, e, uStaff, tab); len(got) != 0 {
				t.Fatalf("%s: 요청자 본인의 %s 탭에 건이 보임: %v", stage, tab, got)
			}
		}
	}
	check("상신 직후")
	approve(t, e, uLead, pr.ID)
	check("1단계 승인 뒤")
	approve(t, e, uHead, pr.ID)
	approve(t, e, uFinance, pr.ID)
	check("결재 완료 뒤")
}

func TestInboxTabsRejectUnknownTabAndNoPrincipal(t *testing.T) {
	e := newEnv(t)
	if _, err := e.approval.ListInbox(as(uLead), "all"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 탭: %v", err)
	}
	if _, err := e.approval.ListInbox(contractNoUser(), "progress"); err == nil {
		t.Fatal("미인증이 결재함을 봄")
	}
}

// 결재 요청·반려·완료 알림이 각각 생긴다(README 의 앱 안 알림 문장). 알림 제목은 업무 말이다.
func TestApprovalNotificationsPendingRejectedDone(t *testing.T) {
	e := newEnv(t)
	// 상신 → 첫 결재자에게 결재 요청 알림
	pr := e.submitted(t, uStaff, 3_000_000)
	notes, _ := e.notification.List(as(uLead), domain.NotificationFilter{})
	if !hasKind(notes, "approval_pending") {
		t.Fatalf("상신 때 결재 요청 알림이 없음: %+v", notes)
	}
	// 승인 → 다음 결재자에게 결재 요청 알림
	approve(t, e, uLead, pr.ID)
	if notes, _ = e.notification.List(as(uHead), domain.NotificationFilter{}); !hasKind(notes, "approval_pending") {
		t.Fatalf("다음 결재자에게 결재 요청 알림이 없음: %+v", notes)
	}
	// 반려 → 요청자에게 반려 알림(사유 포함)
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false, Comment: "단가 재확인"}); err != nil {
		t.Fatal(err)
	}
	notes, _ = e.notification.List(as(uStaff), domain.NotificationFilter{})
	if !hasKind(notes, "approval_rejected") {
		t.Fatalf("반려 알림이 없음: %+v", notes)
	}
	for _, n := range notes {
		if n.Kind == "approval_rejected" && n.Title != "문서가 반려되었습니다" {
			t.Fatalf("반려 알림 제목: %q", n.Title)
		}
	}
	// 마지막 승인 → 요청자에게 결재 완료 알림
	pr2 := e.submitted(t, uStaff, 100_000)
	approve(t, e, uLead, pr2.ID)
	notes, _ = e.notification.List(as(uStaff), domain.NotificationFilter{})
	if !hasKind(notes, "approval_done") {
		t.Fatalf("결재 완료 알림이 없음: %+v", notes)
	}
}

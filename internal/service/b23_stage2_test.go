package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) stage2() (*service.InboxService, *service.ReceiptService, *service.ApprovalService) {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewInboxService(b), service.NewReceiptService(b), service.NewApprovalService(b)
}

func inboxIDs(t *testing.T, in *service.InboxService, who int64, box domain.InboxBox) map[int64]domain.InboxItem {
	t.Helper()
	list, err := in.List(as(who), box, domain.InboxFilter{})
	if err != nil {
		t.Fatalf("%s %d: %v", box, who, err)
	}
	m := map[int64]domain.InboxItem{}
	for _, it := range list {
		m[it.ID] = it
	}
	return m
}

// 대표 흐름(AC-B23-2): 상신 → 결재 대기함·진행함 → 읽음 → 결재 완료 → 완료함·참조함·수신함 → 접수·담당자 지정·처리 완료 → 기안자 알림.
func TestStage2InboxAndReceiptFlow(t *testing.T) {
	e := newEnv(t)
	in, rc, _ := e.stage2()
	pr := e.genSubmitted(t, uStaff, "수신 흐름", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2},
		domain.LineEdit{Role: "receive", TargetType: "unit", TargetID: unitSales1})
	if got := inboxIDs(t, in, uStaff, domain.BoxDrafted); got[pr.ID].ID == 0 {
		t.Fatal("기안함에 내 문서가 없음")
	}
	pend := inboxIDs(t, in, uLead, domain.BoxPending)
	if it := pend[pr.ID]; it.ID == 0 || !it.Unread {
		t.Fatalf("결재 대기함에 안 읽은 문서가 있어야 함: %+v", it)
	}
	if got := inboxIDs(t, in, uStaff2, domain.BoxCC); got[pr.ID].ID == 0 {
		t.Fatal("참조함에 문서가 없음")
	}
	if got := inboxIDs(t, in, uCEO, domain.BoxPending); len(got) != 0 {
		t.Fatalf("관계 없는 사람의 결재 대기함: %v", got)
	}
	if err := in.MarkRead(as(uLead), pr.ID); err != nil {
		t.Fatal(err)
	}
	if it := inboxIDs(t, in, uLead, domain.BoxPending)[pr.ID]; it.Unread {
		t.Fatal("읽음 처리 뒤에도 안 읽음")
	}
	if err := in.MarkRead(as(uLead3), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("열람 범위 밖 읽음 처리는 404: %v", err)
	}
	// 결재선 끝까지 승인: 수신 건이 생기고 수신처 조직장에게 알림.
	decideAs(t, e, uLead, pr.ID, true, "")
	if got := inboxIDs(t, in, uHead, domain.BoxPending); got[pr.ID].ID == 0 {
		t.Fatal("다음 결재자의 대기함에 문서가 없음")
	}
	if got := inboxIDs(t, in, uLead, domain.BoxInProgress); got[pr.ID].ID == 0 {
		t.Fatal("결재한 사람의 진행함에 문서가 없음")
	}
	decideAs(t, e, uHead, pr.ID, true, "")
	if got := inboxIDs(t, in, uLead, domain.BoxDone); got[pr.ID].ID == 0 {
		t.Fatal("완료함에 문서가 없음")
	}
	recv := inboxIDs(t, in, uLead2, domain.BoxReceive)
	it := recv[pr.ID]
	if it.ID == 0 || it.ReceiptStatus != "pending" || it.ReceiptID == 0 {
		t.Fatalf("수신함: %+v", it)
	}
	if got := inboxIDs(t, in, uStaff, domain.BoxReceive); len(got) != 0 {
		t.Fatal("접수 권한이 없는 사람의 수신함")
	}
	if notes, _ := e.notification.List(as(uLead2), domain.NotificationFilter{}); !hasKind(notes, "receipt_arrived") {
		t.Fatalf("수신 도착 알림 없음: %+v", notes)
	}
	// 권한: 수신처 소속이 아닌 사람은 접수할 수 없다.
	if _, err := rc.Accept(as(uStaff), it.ReceiptID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("접수 권한 없음 거부: %v", err)
	}
	got, err := rc.Accept(as(uLead2), it.ReceiptID)
	if err != nil || got.Status != "received" {
		t.Fatalf("접수: %+v %v", got, err)
	}
	if _, err := rc.Accept(as(uLead2), it.ReceiptID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이중 접수: %v", err)
	}
	if _, err := rc.Assign(as(uLead2), it.ReceiptID, uCEO); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("다른 조직 구성원 지정 거부: %v", err)
	}
	got, err = rc.Assign(as(uLead2), it.ReceiptID, uStaff2)
	if err != nil || got.Status != "assigned" || got.AssigneeID != uStaff2 {
		t.Fatalf("지정: %+v %v", got, err)
	}
	got, err = rc.Complete(as(uStaff2), it.ReceiptID, "처리함")
	if err != nil || got.Status != "done" {
		t.Fatalf("지정된 담당자의 처리 완료: %+v %v", got, err)
	}
	if notes, _ := e.notification.List(as(uStaff), domain.NotificationFilter{}); !hasKind(notes, "receipt_done") {
		t.Fatalf("처리 완료 알림 없음: %+v", notes)
	}
	if _, err := rc.Return(as(uLead2), it.ReceiptID, "사유"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("끝난 건 반송 거부: %v", err)
	}
	list, _ := rc.List(as(uStaff), domain.DocReceiptFilter{DocID: pr.ID})
	if len(list) != 1 || list[0].Status != "done" {
		t.Fatalf("문서의 수신 건: %+v", list)
	}
}

func hasKind(notes []domain.Notification, kind string) bool {
	for _, n := range notes {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// 수신 반송은 사유가 필수이고 기안자에게 알린다. 수신 담당자는 조직장만 지정한다.
func TestStage2ReturnAndReceivers(t *testing.T) {
	e := newEnv(t)
	in, rc, _ := e.stage2()
	if err := rc.AddReceiver(as(uStaff), unitSales1, uStaff2); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("조직장이 아니면 담당자 지정 불가: %v", err)
	}
	if err := rc.AddReceiver(as(uLead2), unitSales1, uStaff2); err != nil {
		t.Fatal(err)
	}
	if err := rc.AddReceiver(as(uLead2), unitSales1, uStaff2); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("중복 지정: %v", err)
	}
	if err := rc.AddReceiver(as(uLead2), unitSales1, uCEO); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("다른 조직 구성원: %v", err)
	}
	pr := e.genSubmitted(t, uStaff, "반송", domain.LineEdit{Role: "receive", TargetType: "unit", TargetID: unitSales1})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	it := inboxIDs(t, in, uStaff2, domain.BoxReceive)[pr.ID] // 수신 담당자도 접수할 수 있다
	if it.ReceiptID == 0 {
		t.Fatal("수신 담당자의 수신함이 비어 있음")
	}
	if _, err := rc.Return(as(uStaff2), it.ReceiptID, "  "); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 반송: %v", err)
	}
	got, err := rc.Return(as(uStaff2), it.ReceiptID, "관할이 아님")
	if err != nil || got.Status != "returned" || got.Reason != "관할이 아님" {
		t.Fatalf("반송: %+v %v", got, err)
	}
	if notes, _ := e.notification.List(as(uStaff), domain.NotificationFilter{}); !hasKind(notes, "receipt_returned") {
		t.Fatalf("반송 알림 없음: %+v", notes)
	}
	if err := rc.RemoveReceiver(as(uLead2), unitSales1, uStaff2); err != nil {
		t.Fatal(err)
	}
	if got := inboxIDs(t, in, uStaff2, domain.BoxReceive); len(got) != 0 {
		t.Fatalf("담당자에서 빠진 뒤에도 수신함이 보임: %v", got)
	}
}

// 상신 취소(D9): 아무도 처리하지 않은 건만 기안자가 임시저장으로 되돌린다. 알림·감사·이력이 남는다.
func TestStage2CancelSubmit(t *testing.T) {
	e := newEnv(t)
	in, _, _ := e.stage2()
	docs := e.docs()
	pr := e.genSubmitted(t, uStaff, "취소")
	if _, err := docs.CancelSubmit(as(uLead), pr.ID, "x"); !errors.Is(err, contract.ErrNotFound) && !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("기안자가 아닌 사람의 취소: %v", err)
	}
	got, err := docs.CancelSubmit(as(uStaff), pr.ID, "내용 수정")
	if err != nil || got.Status != domain.PRDraft {
		t.Fatalf("상신 취소: %+v %v", got, err)
	}
	if !e.rec.Has("approval_doc.cancel_submit", "approval_doc", "success") {
		t.Fatal("상신 취소 감사 없음")
	}
	if notes, _ := e.notification.List(as(uLead), domain.NotificationFilter{}); !hasKind(notes, "submission_cancelled") {
		t.Fatalf("취소 알림 없음: %+v", notes)
	}
	if got := inboxIDs(t, in, uLead, domain.BoxPending); got[pr.ID].ID != 0 {
		t.Fatal("취소된 문서가 결재 대기함에 남음")
	}
	// 다시 상신해서 한 명이라도 처리하면 취소할 수 없다.
	if _, err := docs.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, pr.ID, true, "")
	if _, err := docs.CancelSubmit(as(uStaff), pr.ID, "또"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("처리된 건의 취소 거부: %v", err)
	}
	if _, err := docs.CancelSubmit(as(uStaff), pr.ID, ""); err == nil {
		t.Fatal("사유 없이 취소됨")
	}
	// 구매 품의서도 같다.
	p2 := e.submitted(t, uStaff, 500_000)
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	if c, err := service.NewPurchaseService(b).CancelSubmit(as(uStaff), p2.ID, "금액 수정"); err != nil || c.Status != domain.PRDraft {
		t.Fatalf("구매 품의서 상신 취소: %+v %v", c, err)
	}
}

// 일괄 승인: 건마다 처리하고 실패는 건별로 돌려준다. 일괄 반려는 없다.
func TestStage2BatchApprove(t *testing.T) {
	e := newEnv(t)
	_, _, ap := e.stage2()
	a := e.genSubmitted(t, uStaff, "일괄 1")
	b := e.genSubmitted(t, uStaff, "일괄 2")
	c := e.genSubmitted(t, uStaff2, "다른 부서")
	items := []domain.BatchItem{{DocID: a.ID}, {DocID: b.ID}, {DocID: c.ID}, {DocID: a.ID}, {DocID: 9999}}
	res, err := ap.DecideBatch(as(uLead), items, "일괄 승인")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 { // 같은 번호는 한 번만
		t.Fatalf("결과 %d건: %+v", len(res), res)
	}
	if !res[0].OK || !res[1].OK || res[2].OK || res[3].OK {
		t.Fatalf("건별 결과: %+v", res)
	}
	for _, id := range []int64{a.ID, b.ID} {
		got, _ := e.docs().Get(as(uStaff), id)
		if got.Steps[0].Status != domain.StepApproved || got.Steps[0].Comment != "일괄 승인" {
			t.Fatalf("일괄 승인 반영 안 됨: %+v", got.Steps[0])
		}
	}
	if !e.rec.Has("approval.batch", "approval", "success") {
		t.Fatal("일괄 승인 요약 감사 없음")
	}
	if _, err := ap.DecideBatch(as(uStaff), items[:1], ""); !errors.Is(err, contract.ErrForbidden) && !errors.Is(err, contract.ErrNotFound) {
		// 직원도 합의 권한은 있으나 처리할 차례가 아니므로 건별 실패로 돌아온다.
		r, _ := ap.DecideBatch(as(uStaff), items[:1], "")
		if len(r) != 1 || r[0].OK {
			t.Fatalf("차례 아닌 사람의 일괄 승인: %v %+v", err, r)
		}
	}
	if _, err := ap.DecideBatch(as(uLead), nil, ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 선택: %v", err)
	}
	many := make([]domain.BatchItem, 51)
	for i := range many {
		many[i].DocID = int64(i + 1)
	}
	if _, err := ap.DecideBatch(as(uLead), many, ""); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "50건") {
		t.Fatalf("50건 초과: %v", err)
	}
}

// 긴급 표시는 양식이 허용할 때만 저장되고 문서함 목록에 나온다.
func TestStage2UrgentShownInInbox(t *testing.T) {
	e := newEnv(t)
	in, _, _ := e.stage2()
	d, err := e.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "급함", Fields: map[string]any{"content": "본문"}, Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.docs().Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	if it := inboxIDs(t, in, uLead, domain.BoxPending)[d.ID]; !it.Urgent {
		t.Fatalf("긴급 표시가 목록에 없음: %+v", it)
	}
}

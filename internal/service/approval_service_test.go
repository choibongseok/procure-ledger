package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func approve(t *testing.T, e *env, by, prID int64) domain.DecisionResult {
	t.Helper()
	res, err := e.approval.Decide(as(by), prID, domain.Decision{Approve: true})
	if err != nil {
		t.Fatalf("승인(%d): %v", by, err)
	}
	return res
}

func kinds(pr domain.PurchaseRequest) []domain.StepKind {
	var out []domain.StepKind
	for _, s := range pr.Steps {
		out = append(out, s.Kind)
	}
	return out
}

func TestApprovalLineByAmount(t *testing.T) {
	cases := []struct {
		amount int64
		want   []domain.StepKind
	}{
		{999_999, []domain.StepKind{domain.StepTeamLead}},
		{1_000_000, []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead}},
		{9_999_999, []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead}},
		{10_000_000, []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead, domain.StepFinance}},
	}
	for _, c := range cases {
		e := newEnv(t)
		pr := e.submitted(t, uStaff, c.amount)
		got := kinds(pr)
		if len(got) != len(c.want) {
			t.Fatalf("%d원: 결재선 %v, 기대 %v", c.amount, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%d원: 결재선 %v, 기대 %v", c.amount, got, c.want)
			}
		}
		if pr.Status != domain.PRSubmitted || pr.Steps[0].Status != domain.StepPending {
			t.Fatalf("상신 직후: %s %v", pr.Status, pr.Steps)
		}
	}
}

func TestSingleStepApprovalCreatesPO(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	res := approve(t, e, uLead, pr.ID)
	if res.Request.Status != domain.PROrdered || res.PurchaseOrder == nil {
		t.Fatalf("발주완료·PO 기대: %+v", res)
	}
	po := res.PurchaseOrder
	if po.PRID != pr.ID || po.TotalAmount != 500_000 || len(po.Lines) != 1 || po.Status != domain.POOpen || res.Request.PurchaseOrderID != po.ID {
		t.Fatalf("PO 내용: %+v", po)
	}
	for _, a := range []struct {
		action string
		tt     audit.TargetType
	}{
		{"purchase_request.create", audit.TargetPurchaseRequest}, {"purchase_request.submit", audit.TargetPurchaseRequest},
		{"approval.approve", audit.TargetApproval}, {"purchase_order.create", audit.TargetPurchaseOrder},
	} {
		if !e.rec.Has(a.action, a.tt, audit.ResultSuccess) {
			t.Errorf("감사 기록 없음: %s", a.action)
		}
	}
	hist, err := e.approval.History(as(uStaff), pr.ID)
	if err != nil || len(hist) != 2 || hist[0].Action != domain.ActionSubmit || hist[1].Action != domain.ActionApprove || hist[1].ActorID != uLead {
		t.Fatalf("이력: %+v %v", hist, err)
	}
	if hist[0].ActorName == "" || hist[1].ActorName == "" {
		t.Fatalf("이력에 처리자 이름이 없음: %+v", hist)
	}
}

func TestMultiStepApprovalInOrder(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 20_000_000)

	// 순서를 건너뛴 결재는 거부된다.
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("부서장이 팀장 차례에 결재함")
	} else {
		is(t, err, contract.ErrForbidden)
	}
	if _, err := e.approval.Decide(as(uFinance), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("재무가 팀장 차례에 결재함")
	}

	res := approve(t, e, uLead, pr.ID)
	if res.Request.Status != domain.PRInReview || res.PurchaseOrder != nil {
		t.Fatalf("1단계 후: %s", res.Request.Status)
	}
	if res.Request.Steps[1].Status != domain.StepPending {
		t.Fatalf("다음 단계가 열려야 함: %+v", res.Request.Steps)
	}
	// 같은 단계를 다시 결재할 수 없다(팀장 차례는 끝났다).
	if _, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("이중 결재")
	}
	if res = approve(t, e, uHead, pr.ID); res.Request.Status != domain.PRInReview {
		t.Fatalf("2단계 후: %s", res.Request.Status)
	}
	if res = approve(t, e, uFinance, pr.ID); res.Request.Status != domain.PROrdered || res.PurchaseOrder == nil {
		t.Fatalf("3단계 후: %+v", res)
	}
	// 발주완료 건은 더 결재할 수 없다.
	_, err := e.approval.Decide(as(uFinance2), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrInvalidState)
}

func TestRejectNeedsReasonAndResubmit(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 3_000_000)
	approve(t, e, uLead, pr.ID)

	_, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false, Comment: "  "})
	is(t, err, contract.ErrValidation)
	if got, _ := e.purchase.Get(as(uStaff), pr.ID); got.Status != domain.PRInReview {
		t.Fatalf("사유 없는 반려가 상태를 바꿈: %s", got.Status)
	}

	res, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false, Comment: "단가 재확인"})
	if err != nil || res.Request.Status != domain.PRRejected || res.Request.RejectReason != "단가 재확인" || res.PurchaseOrder != nil {
		t.Fatalf("반려: %+v %v", res, err)
	}
	// 반려 건은 더 결재할 수 없다.
	_, err = e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrInvalidState)

	// 수정 후 재상신: 새 차수 결재선이 처음부터 다시 돈다.
	it, _ := e.inventory.SearchItems(as(uStaff), "", 1)
	if _, err = e.purchase.UpdateDraft(as(uStaff), pr.ID, domain.DraftInput{
		VendorID: res.Request.VendorID, Title: "재작성", Lines: []domain.PRLineInput{{ItemID: it[0].ID, Quantity: 2, UnitPrice: 400_000}},
	}); err != nil {
		t.Fatal(err)
	}
	re, err := e.purchase.Submit(as(uStaff), pr.ID)
	if err != nil || re.Status != domain.PRSubmitted || re.RejectReason != "" || len(re.Steps) != 1 || re.Steps[0].Kind != domain.StepTeamLead {
		t.Fatalf("재상신: %+v %v", re, err)
	}
	if res = approve(t, e, uLead, pr.ID); res.Request.Status != domain.PROrdered {
		t.Fatalf("재상신 후 승인: %s", res.Request.Status)
	}
	hist, _ := e.approval.History(as(uAuditor), pr.ID)
	var acts []domain.ApprovalAction
	for _, h := range hist {
		acts = append(acts, h.Action)
	}
	want := []domain.ApprovalAction{domain.ActionSubmit, domain.ActionApprove, domain.ActionReject, domain.ActionResubmit, domain.ActionApprove}
	if len(acts) != len(want) {
		t.Fatalf("이력 %v, 기대 %v", acts, want)
	}
	for i := range want {
		if acts[i] != want[i] {
			t.Fatalf("이력 %v, 기대 %v", acts, want)
		}
	}
	if hist[2].Comment != "단가 재확인" {
		t.Fatalf("반려 사유가 이력에 남아야 함: %+v", hist[2])
	}
}

func TestSelfApprovalForbidden(t *testing.T) {
	e := newEnv(t)
	// 팀장이 직접 올린 요청: 팀장 단계가 자기 차례여도 본인은 결재할 수 없다.
	pr := e.submitted(t, uLead, 100_000)
	before := e.rec.Count()
	_, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrSelfApproval)
	if !e.rec.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) || e.rec.Count() != before+1 {
		t.Fatal("자기 결재 시도가 거부 기록으로 남아야 함")
	}
	if got, _ := e.purchase.Get(as(uLead), pr.ID); got.Status != domain.PRSubmitted {
		t.Fatalf("상태가 바뀜: %s", got.Status)
	}
	// 반려도 같다.
	_, err = e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: false, Comment: "x"})
	is(t, err, contract.ErrSelfApproval)
	// 본인 요청은 내 결재함에도 나오지 않는다.
	pend, err := e.approval.ListPending(as(uLead))
	if err != nil || len(pend) != 0 {
		t.Fatalf("본인 요청이 결재함에 있음: %v %v", pend, err)
	}
	// 다른 팀장은 못 하지만(없으므로) 부서장은 차례가 아니라 거부 — 서비스 검사를 뚫어도 DB 트리거가 막는다.
	if _, err := e.db.Exec(`INSERT INTO approval_history (pr_id, round, seq, actor_id, action, at) VALUES (?,1,1,?, 'approve', 'x')`, pr.ID, uLead); err == nil {
		t.Fatal("DB 트리거가 자기 결재를 막지 못함")
	}
}

func TestApprovalRequiresPermissionAndIsAudited(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 100_000)
	for _, id := range []int64{uStaff, uAuditor, uAdmin} {
		before := e.rec.Count()
		_, err := e.approval.Decide(as(id), pr.ID, domain.Decision{Approve: true})
		is(t, err, contract.ErrForbidden)
		if e.rec.Count() != before+1 || !e.rec.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) {
			t.Fatalf("사용자 %d 거부 기록 없음", id)
		}
	}
	_, err := e.approval.Decide(contractNoUser(), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrUnauthenticated)
	if got, _ := e.purchase.Get(as(uStaff), pr.ID); got.Status != domain.PRSubmitted {
		t.Fatal("권한 없는 호출이 상태를 바꿈")
	}
}

func TestDepartmentIsolation(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 100_000) // 구매팀 요청

	// 타 부서 직원: 조회·이력·목록에서 존재가 보이지 않는다.
	_, err := e.purchase.Get(as(uStaff2), pr.ID)
	is(t, err, contract.ErrNotFound)
	_, err = e.approval.History(as(uStaff2), pr.ID)
	is(t, err, contract.ErrNotFound)
	list, err := e.purchase.List(as(uStaff2), domain.PRFilter{})
	if err != nil || len(list) != 0 {
		t.Fatalf("타 부서 목록 노출: %v %v", list, err)
	}
	// 필터로 타 부서를 지정해도 소용없다(서비스 범위 + 저장소 조건절).
	list, _ = e.purchase.List(as(uStaff2), domain.PRFilter{DepartmentID: deptBuy})
	if len(list) != 0 {
		t.Fatal("부서 필터로 격리를 우회함")
	}
	// 타 부서 팀장은 결재도 못 한다.
	_, err = e.approval.Decide(as(uLead2), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)
	pend, _ := e.approval.ListPending(as(uLead2))
	if len(pend) != 0 {
		t.Fatal("타 부서 결재함 노출")
	}
	// 타 부서 직원은 수정·상신도 못 한다.
	_, err = e.purchase.Submit(as(uStaff2), pr.ID)
	is(t, err, contract.ErrNotFound)
	// 같은 부서 다른 사람: 보이지만 수정·상신은 요청자만.
	_, err = e.purchase.Submit(as(uLead), pr.ID)
	is(t, err, contract.ErrForbidden)

	// 감사·재무는 전 부서를 본다.
	for _, id := range []int64{uAuditor, uFinance, uAdmin} {
		if got, err := e.purchase.Get(as(id), pr.ID); err != nil || got.ID != pr.ID {
			t.Fatalf("사용자 %d 조회 실패: %v", id, err)
		}
	}
	// 같은 부서 팀장 결재함에는 있다.
	if pend, _ = e.approval.ListPending(as(uLead)); len(pend) != 1 {
		t.Fatalf("팀장 결재함 %d건", len(pend))
	}
	if pend, _ = e.approval.ListPending(as(uHead)); len(pend) != 0 {
		t.Fatal("부서장 차례가 아닌데 결재함에 있음")
	}
}

func TestApprovalHistoryIsImmutable(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 100_000)
	approve(t, e, uLead, pr.ID)
	if _, err := e.db.Exec(`UPDATE approval_history SET comment='변조' WHERE pr_id=?`, pr.ID); err == nil {
		t.Fatal("결재 이력 수정이 가능함")
	}
	if _, err := e.db.Exec(`DELETE FROM approval_history WHERE pr_id=?`, pr.ID); err == nil {
		t.Fatal("결재 이력 삭제가 가능함")
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id=?`, pr.ID); n != 2 {
		t.Fatalf("이력 %d건", n)
	}
}

func TestAuditFailureRollsBackPurchase(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "AF-1", 0, false)
	v := e.vendor(t)
	in := domain.DraftInput{VendorID: v.ID, Title: "t", Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 1, UnitPrice: 100}}}

	e.rec.FailWith(errBoom)
	_, err := e.purchase.CreateDraft(as(uStaff), in)
	is(t, err, contract.ErrAuditFailed)
	if n := count(t, e.db, `SELECT COUNT(*) FROM purchase_requests`); n != 0 {
		t.Fatalf("감사 실패인데 PR %d건이 남음", n)
	}
	e.rec.FailWith(nil)

	pr, err := e.purchase.CreateDraft(as(uStaff), in)
	if err != nil {
		t.Fatal(err)
	}
	e.rec.FailWith(errBoom)
	_, err = e.purchase.Submit(as(uStaff), pr.ID)
	is(t, err, contract.ErrAuditFailed)
	if got, _ := e.purchase.Get(as(uStaff), pr.ID); got.Status != domain.PRDraft || len(got.Steps) != 0 {
		t.Fatalf("상신이 롤백되지 않음: %s %v", got.Status, got.Steps)
	}
	e.rec.FailWith(nil)
	if _, err = e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}

	e.rec.FailWith(errBoom)
	_, err = e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrAuditFailed)
	got, _ := e.purchase.Get(as(uStaff), pr.ID)
	if got.Status != domain.PRSubmitted || got.PurchaseOrderID != 0 || got.Steps[0].Status != domain.StepPending {
		t.Fatalf("결재가 롤백되지 않음: %+v", got)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM purchase_orders`) + count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE action='approve'`); n != 0 {
		t.Fatalf("PO·이력이 남음: %d", n)
	}
}

func TestDraftEditRules(t *testing.T) {
	e := newEnv(t)
	pr := e.draft(t, uStaff, 1000)
	it, _ := e.inventory.SearchItems(as(uStaff), "", 1)
	in := domain.DraftInput{VendorID: pr.VendorID, Title: "수정", Lines: []domain.PRLineInput{{ItemID: it[0].ID, Quantity: 3, UnitPrice: 2000}}}

	got, err := e.purchase.UpdateDraft(as(uStaff), pr.ID, in)
	if err != nil || got.TotalAmount != 6000 || got.Lines[0].Amount != 6000 || got.Title != "수정" {
		t.Fatalf("수정: %+v %v", got, err)
	}
	for name, bad := range map[string]domain.DraftInput{
		"제목 없음":  {VendorID: pr.VendorID, Lines: in.Lines},
		"줄 없음":   {VendorID: pr.VendorID, Title: "x"},
		"수량 0":   {VendorID: pr.VendorID, Title: "x", Lines: []domain.PRLineInput{{ItemID: it[0].ID, Quantity: 0, UnitPrice: 1}}},
		"없는 품목":  {VendorID: pr.VendorID, Title: "x", Lines: []domain.PRLineInput{{ItemID: 9999, Quantity: 1, UnitPrice: 1}}},
		"없는 거래처": {VendorID: 9999, Title: "x", Lines: in.Lines},
		"음수 단가":  {VendorID: pr.VendorID, Title: "x", Lines: []domain.PRLineInput{{ItemID: it[0].ID, Quantity: 1, UnitPrice: -1}}},
	} {
		if _, err := e.purchase.UpdateDraft(as(uStaff), pr.ID, bad); !isErr(err, contract.ErrValidation) {
			t.Errorf("%s: 입력 오류 기대, %v", name, err)
		}
	}
	// 거래처 없이는 상신할 수 없다.
	novendor, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{Title: "거래처 없음", Lines: in.Lines})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.purchase.Submit(as(uStaff), novendor.ID)
	is(t, err, contract.ErrValidation)

	// 상신 뒤에는 수정·재상신 불가.
	if _, err = e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.purchase.UpdateDraft(as(uStaff), pr.ID, in)
	is(t, err, contract.ErrInvalidState)
	_, err = e.purchase.Submit(as(uStaff), pr.ID)
	is(t, err, contract.ErrInvalidState)

	// admin·auditor 는 작성할 수 없다.
	for _, id := range []int64{uAdmin, uAuditor} {
		_, err = e.purchase.CreateDraft(as(id), in)
		is(t, err, contract.ErrForbidden)
	}
	if !e.rec.Has("purchase_request.create", audit.TargetPurchaseRequest, audit.ResultDenied) {
		t.Fatal("작성 거부 기록 없음")
	}
}

// 요청자 외에 해당 단계를 맡을 사람이 없으면 상신을 거부한다(결재가 조용히 멈추는 건을 만들지 않는다).
// 결재선을 만들 수 없으면 단계별 사유와 함께 400 으로 거부되고, 상태·단계는 그대로이며 거부가 감사 로그에 남는다(AC-13·13b).
func TestSubmitRejectedWhenLineCannotBeBuilt(t *testing.T) {
	e := newEnv(t)
	// 대표가 공석이면 영업1팀 팀장(uLead2)의 요청은 위로 올라가도 결재자를 찾지 못한다.
	e.org.SetHead(unitCompany, 0)
	pr := e.draft(t, uLead2, 100_000)
	_, err := e.purchase.Submit(as(uLead2), pr.ID)
	is(t, err, contract.ErrValidation)
	if !strings.Contains(err.Error(), "1단계(팀장): 위로 올라가도 맡을 결재자를 찾지 못했습니다") {
		t.Fatalf("한국어 사유가 단계를 알려야 함: %v", err)
	}
	if got, _ := e.purchase.Get(as(uLead2), pr.ID); got.Status != domain.PRDraft || len(got.Steps) != 0 || got.LineAsOf != "" {
		t.Fatalf("거부된 상신이 상태를 바꿈: %s", got.Status)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_lines`); n != 0 {
		t.Fatalf("거부된 상신이 스냅샷을 남김: %d", n)
	}
	if !e.rec.Has("purchase_request.submit", audit.TargetPurchaseRequest, audit.ResultDenied) {
		t.Fatal("결재선 불가 거부가 감사 로그에 남아야 함")
	}
	// 직원 요청은 팀장(uLead)이 있어 상신된다. 재무 담당이 모두 없으면 고액 건만 거부된다.
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE role = 'finance'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), e.draft(t, uStaff, 500_000).ID); err != nil {
		t.Fatalf("소액 상신: %v", err)
	}
	big := e.draft(t, uStaff, 20_000_000)
	_, err = e.purchase.Submit(as(uStaff), big.ID)
	is(t, err, contract.ErrValidation)
	if !strings.Contains(err.Error(), "재무 단계") {
		t.Fatalf("재무 단계 사유: %v", err)
	}
}

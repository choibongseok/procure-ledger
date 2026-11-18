package service_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func (e *env) purchaseDraftWithEdits(t *testing.T, by, amount int64, edits ...domain.LineEdit) domain.PurchaseRequest {
	t.Helper()
	skuSeq++
	it := e.item(t, fmt.Sprintf("SKU-%d", skuSeq), 0, false)
	v := e.vendor(t)
	pr, err := e.purchase.CreateDraft(as(by), domain.DraftInput{VendorID: v.ID, Title: "참조 시험", Reason: "x",
		Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 1, UnitPrice: amount}}, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

// AC-104 구매 품의서에 참조·수신을 더하면 상신 뒤 참조 대상이 열람 조건 함수 한 곳에서 건을 보고, 수신처가 응답에 나온다. 합의·결재 편집은 거부된다.
func TestPurchaseCCAndReceiveEdits(t *testing.T) {
	e := newEnv(t)
	d := e.purchaseDraftWithEdits(t, uStaff, 500_000,
		domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2}, domain.LineEdit{Role: "receive", TargetType: "unit", TargetID: unitSales})
	// 임시저장 중에는 참조 대상에게 보이지 않는다.
	if _, err := e.purchase.Get(as(uStaff2), d.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("임시저장 건이 참조 대상에게 보임: %v", err)
	}
	if got, _ := e.purchase.Get(as(uStaff), d.ID); len(got.DraftEdits) != 2 || got.DraftEdits[0].Name == "" {
		t.Fatalf("저장해 둔 편집이 응답에 없음: %+v", got.DraftEdits)
	}
	pr, err := e.purchase.Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Recipients) != 2 {
		t.Fatalf("참조·수신 저장: %+v", pr.Recipients)
	}
	got, err := e.purchase.Get(as(uStaff2), d.ID) // 다른 부서 직원이 참조로 열람
	if err != nil || got.ID != d.ID {
		t.Fatalf("참조 대상 열람: %v", err)
	}
	// 참조가 아닌 다른 부서 사용자는 여전히 못 본다(감사자 제외).
	other := e.submitted(t, uStaff, 500_000)
	if _, err := e.purchase.Get(as(uStaff2), other.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("참조가 아닌 건이 보임: %v", err)
	}
	// 구매 품의서는 참조·수신만 더할 수 있다.
	bad := e.purchaseDraftWithEdits(t, uStaff, 500_000, domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance})
	if _, err := e.purchase.Submit(as(uStaff), bad.ID); err == nil || !strings.Contains(err.Error(), "구매 품의서는 참조·수신만 더할 수 있습니다") {
		t.Fatalf("합의 편집이 통과함: %v", err)
	}
}

// AC-132 공석인 조직을 합의 대상으로 고르면 올리지 않고 결재선 불가 사유를 낸다.
func TestAgreeTargetUnitWithoutHeadIsRejected(t *testing.T) {
	e := newEnv(t)
	pv, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "COOP", Fields: map[string]any{"target_unit": float64(unitSales)}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pv.Problems {
		found = found || strings.Contains(p, "합의 대상 조직 영업팀의 조직장이 없어 합의자를 정할 수 없습니다.")
	}
	seen := map[string]bool{}
	for _, p := range pv.Problems {
		if seen[p] {
			t.Fatalf("같은 문제 문구가 두 번 나옴: %v", pv.Problems)
		}
		seen[p] = true
	}
	if pv.Complete || !found {
		t.Fatalf("공석 합의 대상: complete=%v %v", pv.Complete, pv.Problems)
	}
	// 협조 요청 부서를 고르지 않으면 어떤 칸을 채워야 하는지 알린다.
	pv, _ = e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "COOP"})
	if pv.Complete || !strings.Contains(strings.Join(pv.Problems, " "), "「협조 요청 부서」를 선택해야 결재선을 만들 수 있습니다.") {
		t.Fatalf("부서 미선택: %v", pv.Problems)
	}
}

// AC-135·136 합의 단계도 admin 이 재지정할 수 있고(퇴사·권한 상실), 결재자 소멸 목록에 합의 단계가 나온다. 병렬 합의 중 한 명이 퇴사해도 풀 수 있다.
func TestAgreeStepReassignAndOrphans(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "합의 재지정", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance, Group: 1},
		domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3, Group: 1})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	e.deactivate(t, uLead3)
	// 소멸 목록: 퇴사한 합의자 한 행, 합의 역할 표시.
	orphans, err := e.liner().ListOrphanedSteps(as(uAdmin), "")
	if err != nil {
		t.Fatal(err)
	}
	var o *domain.OrphanStep
	for i := range orphans {
		if orphans[i].PRID == pr.ID {
			o = &orphans[i]
		}
	}
	if o == nil || o.Role != "agree" || o.Title != "합의" || o.Reason != "퇴사" || !o.Reassignable || o.ApproverID != uLead3 {
		t.Fatalf("소멸 목록: %+v", orphans)
	}
	// 후보: 합의 권한이 있는 재직자(직원 포함), 퇴사자·요청자·이미 처리 중인 사람은 없다.
	cands, err := e.reassigner().ReassignCandidates(as(uAdmin), pr.ID, o.Seq, "")
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]bool{}
	for _, c := range cands {
		names[c.UserID] = true
	}
	if names[uStaff] || names[uLead3] || names[uFinance] || !names[uStaff2] {
		t.Fatalf("합의 후보: %+v", cands)
	}
	// 재지정 뒤 새 합의자가 동의하면 문서가 끝난다.
	if _, err := e.reassigner().ReassignStep(as(uAdmin), pr.ID, o.Seq, domain.ReassignInput{UserID: uStaff2, Cause: "leave", Reason: "퇴사"}); err != nil {
		t.Fatalf("합의 재지정: %v", err)
	}
	decideAs(t, e, uFinance, pr.ID, true, "")
	res := decideAs(t, e, uStaff2, pr.ID, true, "")
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("상태 %s", res.Request.Status)
	}
	// 층 요건이 없으므로 결재 권한이 없는 직원이 새 합의자가 될 수 있었다. 결재 단계에는 여전히 직원을 지정할 수 없다.
	pr2 := e.genSubmitted(t, uStaff, "결재 재지정")
	if _, err := e.reassigner().ReassignStep(as(uAdmin), pr2.ID, 1, domain.ReassignInput{UserID: uStaff2, Cause: "other", Reason: "x", ConfirmActive: true}); err == nil {
		t.Fatal("결재 단계에 직원이 지정됨")
	}
}

// AC-138 재상신: 저장해 둔 편집에서 자격을 잃은 사람은 응답(편집기)에서 빠지고 이유가 안내되며, 그대로 상신하면 무엇을 고칠지 문구로 알려 준다.
func TestResubmitPrunesIneligibleEdits(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "재상신 편집", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance},
		domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	decideAs(t, e, uLead, pr.ID, false, "보완")
	e.deactivate(t, uFinance)
	got, err := e.docs().Get(as(uStaff), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DraftEdits) != 1 || got.DraftEdits[0].TargetID != uStaff2 || got.DraftEdits[0].Name != "영업직원" {
		t.Fatalf("편집기에 남는 편집: %+v", got.DraftEdits)
	}
	if len(got.EditNotes) != 1 || !strings.Contains(got.EditNotes[0], "재무님은 퇴사했거나 재직 중이 아니어서 결재선에서 뺐습니다.") {
		t.Fatalf("안내: %v", got.EditNotes)
	}
	// 고치지 않고 다시 상신하면 사유와 조치를 알려 준다.
	_, err = e.docs().Submit(as(uStaff), pr.ID)
	if err == nil || !strings.Contains(err.Error(), "재직 중이 아니어서 결재선에 넣을 수 없습니다. 결재선 편집에서 이 사람을 빼거나 다른 사람으로 바꾸세요.") {
		t.Fatalf("재상신 문구: %v", err)
	}
	// 편집기가 준 편집(자격 상실자 제외)으로 저장하면 재상신된다.
	if _, err := e.docs().UpdateDraft(as(uStaff), pr.ID, domain.DocDraftInput{FormCode: "GEN", Title: "재상신 편집", Fields: map[string]any{"content": "수정"}, Edits: got.DraftEdits}); err != nil {
		t.Fatal(err)
	}
	again, err := e.docs().Submit(as(uStaff), pr.ID)
	if err != nil || again.Round != 2 || len(again.Recipients) != 1 {
		t.Fatalf("재상신: %+v %v", again, err)
	}
}

// free 양식: 필수가 아닌 기본 단계를 기안자가 뺄 수 있고 필수 단계는 뺄 수 없다. add_only 양식은 뺄 수 없다. 더한 줄은 편집 순서대로 단계가 된다.
func TestFreeFormRemovesOptionalDefaultsAndKeepsEditOrder(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "GEN")
	def := v.Definition
	def.Line.Steps = append(def.Line.Steps,
		domain.StepSpec{Role: "agree", Source: "fixed_user", UserID: uFinance, Required: false},
		domain.StepSpec{Role: "agree", Source: "fixed_user", UserID: uLead3, Required: true})
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	preview := func(edits ...domain.LineEdit) domain.LinePreview {
		pv, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "GEN", Edits: edits})
		if err != nil {
			t.Fatal(err)
		}
		return pv
	}
	base := preview()
	if !base.Complete || len(base.Steps) != 4 || base.Steps[2].DefIndex != 3 || base.Steps[2].Required || !base.Steps[3].Required {
		t.Fatalf("기본: %+v %v", base.Steps, base.Problems)
	}
	// 선택 단계(기본 3번)는 뺄 수 있다.
	pv := preview(domain.LineEdit{Role: "remove", TargetType: "default", TargetID: 3})
	if !pv.Complete || len(pv.Steps) != 3 || pv.Steps[2].ApproverID != uLead3 {
		t.Fatalf("선택 단계 빼기: %+v %v", pv.Steps, pv.Problems)
	}
	// 필수 단계(기본 4번)는 뺄 수 없다.
	pv = preview(domain.LineEdit{Role: "remove", TargetType: "default", TargetID: 4})
	if pv.Complete || !strings.Contains(strings.Join(pv.Problems, " "), "필수 단계는 뺄 수 없습니다.") {
		t.Fatalf("필수 단계 빼기: %v", pv.Problems)
	}
	// 더한 줄은 편집 순서대로 단계가 되고, 순서를 바꾸면 단계 순서도 바뀐다.
	a := domain.LineEdit{Role: "approve", TargetType: "user", TargetID: uCEO}
	b := domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance2}
	pv = preview(domain.LineEdit{Role: "remove", TargetType: "default", TargetID: 3}, a, b)
	pv2 := preview(domain.LineEdit{Role: "remove", TargetType: "default", TargetID: 3}, b, a)
	if !pv.Complete || !pv2.Complete || pv.Steps[3].ApproverID != uCEO || pv.Steps[4].ApproverID != uFinance2 || pv2.Steps[3].ApproverID != uFinance2 || pv2.Steps[4].ApproverID != uCEO {
		t.Fatalf("순서: %+v / %+v", pv.Steps, pv2.Steps)
	}
	// add_only 양식(EXP)에서는 기본 단계를 뺄 수 없다.
	bad, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "EXP", Amount: 100_000, Edits: []domain.LineEdit{{Role: "remove", TargetType: "default", TargetID: 1}}})
	if err != nil || bad.Complete || !strings.Contains(strings.Join(bad.Problems, " "), "기본 결재선의 단계를 뺄 수 없습니다.") {
		t.Fatalf("add_only 빼기: %+v %v", bad.Problems, err)
	}
}

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

func (e *env) authority() *service.AuthorityService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewAuthorityService(b)
}

func (e *env) expense(t *testing.T, by int64, total int64) domain.PurchaseRequest {
	t.Helper()
	att, docs := e.attach()
	ev, err := att.Upload(as(by), "영수증.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	d, err := docs.CreateDraft(as(by), domain.DocDraftInput{FormCode: "EXP", Title: "지출", Fields: map[string]any{
		"spend_date": "2026-11-10", "spend_kind": "출장", "payee": "김가나", "items": []any{map[string]any{"name": "항목", "amount": total}},
		"total": total, "evidence": []any{float64(ev.ID)}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// AC-300 전결: 지출 결의서 1천만~3천만 원 구간은 부서장이 최종 결재자라 재무 단계가 전결로 건너뛰어지고, 부서장 승인으로 바로 결재 완료된다.
func TestAuthorityFlowSkipsFinance(t *testing.T) {
	e := newEnv(t)
	_, docs := e.attach()
	d := e.expense(t, uStaff, 15_000_000)
	pv, err := docs.PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "EXP", Amount: 15_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Authority == nil || pv.Authority.FinalOrder != 2 || len(pv.Authority.SkippedOrders) != 1 || pv.Authority.SkippedOrders[0] != 3 || !pv.Steps[2].Skipped || pv.Steps[2].SkipNote != "전결" {
		t.Fatalf("미리보기 전결: %+v %+v", pv.Authority, pv.Steps)
	}
	pr, err := docs.Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stepStatuses(pr); len(got) != 3 || got[2] != "approve:skipped" {
		t.Fatalf("상신 직후 단계: %v", got)
	}
	if pr.Authority == nil || pr.Authority.Slug != "approval-authority" || pr.Authority.ClauseNo != "제2조" {
		t.Fatalf("스냅샷의 근거 조항: %+v", pr.Authority)
	}
	decideAs(t, e, uLead, d.ID, true, "")
	res := decideAs(t, e, uHead, d.ID, true, "")
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("부서장 승인으로 바로 결재 완료돼야 함: %s", res.Request.Status)
	}
	got, _ := docs.Get(as(uStaff), d.ID)
	if !got.Steps[1].FinalAuthority || got.Steps[2].Status != domain.StepSkipped || got.Steps[2].SkipReason != "전결" || got.Steps[0].FinalAuthority {
		t.Fatalf("결재란 표시: %+v", got.Steps)
	}
	if pend, _ := e.approval.ListPending(as(uFinance)); len(pend) != 0 {
		t.Fatalf("전결로 건너뛴 재무의 결재함에 문서가 보임: %v", pend)
	}
}

// 규정 구간 밖이면 전결이 없고, 규정이 나중에 바뀌어도 이미 상신한 문서는 스냅샷대로 진행한다.
func TestAuthorityBoundaryAndSnapshot(t *testing.T) {
	e := newEnv(t)
	_, docs := e.attach()
	for _, total := range []int64{9_999_999, 30_000_000} {
		d := e.expense(t, uStaff, total)
		pr, err := docs.Submit(as(uStaff), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if pr.Authority != nil {
			t.Fatalf("%d원은 규정 구간 밖: %+v", total, pr.Authority)
		}
		for _, s := range pr.Steps {
			if s.Status == domain.StepSkipped {
				t.Fatalf("건너뛴 단계가 있음: %v", stepStatuses(pr))
			}
		}
	}
	d := e.expense(t, uStaff, 10_000_000) // 경계값은 구간에 포함
	pr, err := docs.Submit(as(uStaff), d.ID)
	if err != nil || pr.Authority == nil {
		t.Fatalf("경계값 1천만 원: %+v %v", pr.Authority, err)
	}
	// 규정을 지워도 이 문서는 그대로 전결로 끝난다.
	a := e.authority()
	rules, _ := a.List(as(uAdmin))
	for _, r := range rules {
		if err := a.Delete(as(uAdmin), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	decideAs(t, e, uLead, d.ID, true, "")
	if res := decideAs(t, e, uHead, d.ID, true, ""); res.Request.Status != domain.PRApproved {
		t.Fatalf("스냅샷대로 전결 완료돼야 함: %s", res.Request.Status)
	}
	d2 := e.expense(t, uStaff, 15_000_000)
	if pr2, _ := docs.Submit(as(uStaff), d2.ID); pr2.Authority != nil {
		t.Fatal("삭제한 규정이 새 문서에 적용됨")
	}
}

// 규정 표 관리: admin 만, 겹침·금액 필드 없는 양식·잘못된 값을 거부하고 감사 기록이 남는다(AC-301 일부).
func TestAuthorityRuleAdmin(t *testing.T) {
	e := newEnv(t)
	a := e.authority()
	if _, err := a.List(as(uStaff)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 조회: %v", err)
	}
	rules, err := a.List(as(uAdmin))
	if err != nil || len(rules) != 1 || rules[0].FormCode != "EXP" || rules[0].FinalLevel != 2 {
		t.Fatalf("시드: %+v %v", rules, err)
	}
	in := domain.AuthorityRuleInput{FormCode: "EXP", MinAmount: 20_000_000, MaxAmount: 50_000_000, FinalLevel: 3, Active: true}
	if _, err := a.Save(as(uAdmin), in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "겹칩니다") {
		t.Fatalf("겹침 거부: %v", err)
	}
	in.MinAmount = 30_000_000
	made, err := a.Save(as(uAdmin), in)
	if err != nil || made.ID == 0 || made.FinalLevel != 3 {
		t.Fatalf("저장: %+v %v", made, err)
	}
	bad := []domain.AuthorityRuleInput{
		{FormCode: "GEN", MinAmount: 0, FinalLevel: 1, Active: true},
		{FormCode: "ZZZ", MinAmount: 0, FinalLevel: 1, Active: true},
		{FormCode: "EXP", MinAmount: 5, MaxAmount: 5, FinalLevel: 1, Active: true},
		{FormCode: "EXP", MinAmount: 100_000_000, FinalLevel: 9, Active: true},
		{FormCode: "EXP", MinAmount: 100_000_000, FinalLevel: 2, PolicySlug: "approval-authority", Active: true},
	}
	for i, b := range bad {
		if _, err := a.Save(as(uAdmin), b); !errors.Is(err, contract.ErrValidation) && !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("잘못된 입력 %d 이 통과: %v", i, err)
		}
	}
	// 사용하지 않는 규정은 겹쳐도 되고, 수정은 자기 자신과 겹침 검사를 하지 않는다.
	made.FinalLevel = 4
	if _, err := a.Save(as(uAdmin), domain.AuthorityRuleInput{ID: made.ID, FormCode: "EXP", MinAmount: 30_000_000, MaxAmount: 50_000_000, FinalLevel: 4, Active: true}); err != nil {
		t.Fatalf("자기 자신 수정: %v", err)
	}
	for _, act := range []string{"authority_rule.create", "authority_rule.update"} {
		if !e.rec.Has(act, "authority_rule", "success") {
			t.Fatalf("감사 %s 없음", act)
		}
	}
	e.rec.FailWith(errBoom)
	if err := a.Delete(as(uAdmin), made.ID); !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("감사 실패면 삭제도 취소: %v", err)
	}
	e.rec.FailWith(nil)
	if rules, _ := a.List(as(uAdmin)); len(rules) != 2 {
		t.Fatalf("감사 실패인데 규정이 지워짐: %d", len(rules))
	}
}

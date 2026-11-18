package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

type fakeClauses map[string]bool

func (f fakeClauses) ClauseBy(_ context.Context, slug, no string) (domain.PolicyHit, error) {
	if f[slug+"/"+no] {
		return domain.PolicyHit{DocSlug: slug, ClauseNo: no}, nil
	}
	return domain.PolicyHit{}, contract.ErrNotFound
}

// 상신 취소 가능 여부는 서비스가 요청자 본인에게만 주고, 처리 이력이 생기면 false 가 된다(화면은 이 값으로 버튼을 보인다).
func TestCanCancelSubmitFlag(t *testing.T) {
	e := newEnv(t)
	docs := e.docs()
	pr := e.genSubmitted(t, uStaff, "취소 가능 표시")
	got, err := docs.Get(as(uStaff), pr.ID)
	if err != nil || !got.CanCancelSubmit {
		t.Fatalf("처리 전에는 취소 가능: %+v %v", got.CanCancelSubmit, err)
	}
	if other, _ := docs.Get(as(uLead), pr.ID); other.CanCancelSubmit {
		t.Fatal("요청자가 아닌 사람에게 취소 가능 표시가 나옴")
	}
	decideAs(t, e, uLead, pr.ID, true, "")
	if got, _ = docs.Get(as(uStaff), pr.ID); got.CanCancelSubmit {
		t.Fatal("처리 이력이 있는데 취소 가능으로 표시됨")
	}
	p := e.submitted(t, uStaff, 500_000)
	pp, err := e.purchase.Get(as(uStaff), p.ID)
	if err != nil || !pp.CanCancelSubmit {
		t.Fatalf("구매 품의서도 같다: %+v %v", pp.CanCancelSubmit, err)
	}
}

// 전결 규정 저장 때 근거 조항이 실제 규정 문서에 있는지 확인한다(없으면 400).
func TestAuthorityRuleChecksClauseExists(t *testing.T) {
	e := newEnv(t)
	a := e.authority()
	a.Clauses = func() domain.ClauseReader { return fakeClauses{"approval-authority/제2조": true} }
	in := domain.AuthorityRuleInput{FormCode: "EXP", MinAmount: 30_000_000, MaxAmount: 50_000_000, FinalLevel: 3, PolicySlug: "approval-authority", ClauseNo: "제9조", Active: true}
	if _, err := a.Save(as(uAdmin), in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "근거 조항을 찾을 수 없습니다") {
		t.Fatalf("없는 조항: %v", err)
	}
	in.ClauseNo = "제2조"
	if _, err := a.Save(as(uAdmin), in); err != nil {
		t.Fatalf("있는 조항: %v", err)
	}
	// 규정 검색이 연결되지 않았으면(nil) 확인을 건너뛴다.
	a.Clauses = func() domain.ClauseReader { return nil }
	in.MinAmount, in.MaxAmount, in.ClauseNo = 50_000_000, 60_000_000, "제9조"
	if _, err := a.Save(as(uAdmin), in); err != nil {
		t.Fatalf("규정 검색 없음: %v", err)
	}
}

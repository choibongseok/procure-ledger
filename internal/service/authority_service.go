package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// AuthorityService 는 전결 규정 표 관리다(B23 단계 3, 권한 authority:manage). 변경은 감사 기록이고 같은 트랜잭션이다.
type AuthorityService struct {
	Base
	Rules repository.SQLAuthority
	Forms repository.SQLForms
	// PolicyDocs 는 규정 문서 목록(slug·한국어 제목)을 돌려준다. 규정 검색 서비스가 없으면 nil 이다(화면은 제목 없이 조항 번호만 보인다).
	PolicyDocs func() []domain.PolicyRef
	// Clauses 는 근거 조항이 실제 규정 문서에 있는지 확인하는 규정 검색 서비스다(규정 검색이 아직 연결되지 않았으면 nil 을 돌려주고 확인을 건너뛴다).
	Clauses func() domain.ClauseReader
}

var _ domain.AuthorityService = (*AuthorityService)(nil)

// NewAuthorityService 는 SQLite 저장소로 전결 규정 서비스를 만든다.
func NewAuthorityService(b Base) *AuthorityService { return &AuthorityService{Base: b} }

func (s *AuthorityService) List(ctx context.Context) ([]domain.AuthorityRule, error) {
	if _, err := contract.Authorize(ctx, contract.PermAuthorityManage); err != nil {
		return nil, err
	}
	out, err := s.Rules.List(ctx, s.DB)
	if out == nil {
		out = []domain.AuthorityRule{}
	}
	titles := s.policyTitles()
	for i := range out {
		out[i].PolicyTitle = titles[out[i].PolicySlug]
	}
	return out, err
}

func (s *AuthorityService) policyTitles() map[string]string {
	m := map[string]string{}
	if s.PolicyDocs != nil {
		for _, d := range s.PolicyDocs() {
			m[d.Slug] = d.Title
		}
	}
	return m
}

// Policies 는 근거로 고를 수 있는 규정 문서 목록이다.
func (s *AuthorityService) Policies(ctx context.Context) ([]domain.PolicyRef, error) {
	if _, err := contract.Authorize(ctx, contract.PermAuthorityManage); err != nil {
		return nil, err
	}
	out := []domain.PolicyRef{}
	if s.PolicyDocs != nil {
		out = append(out, s.PolicyDocs()...)
	}
	return out, nil
}

func amountText(n int64) string {
	if n <= 0 {
		return "상한 없음"
	}
	return moneyLabel(n)
}

// validate 는 입력과 기존 규정의 겹침을 검사하고 양식 번호(전체 양식이면 0)를 돌려준다.
func (s *AuthorityService) validate(ctx context.Context, in domain.AuthorityRuleInput) (int64, error) {
	if in.FinalLevel < 1 || in.FinalLevel > 4 {
		return 0, validation("최종 결재자 직책을 팀장·부서장·본부장·대표 중에서 고르세요")
	}
	if in.MinAmount < 0 {
		return 0, validation("최소 금액은 0원 이상이어야 합니다")
	}
	if in.MaxAmount != 0 && in.MaxAmount <= in.MinAmount {
		return 0, validation("최대 금액(미만)은 최소 금액보다 커야 합니다")
	}
	in.PolicySlug, in.ClauseNo = strings.TrimSpace(in.PolicySlug), strings.TrimSpace(in.ClauseNo)
	if (in.PolicySlug == "") != (in.ClauseNo == "") {
		return 0, validation("근거 규정 문서와 조항 번호는 함께 입력하세요")
	}
	if len(in.PolicySlug) > 100 || len(in.ClauseNo) > 40 {
		return 0, validation("근거 규정이 너무 깁니다")
	}
	var formID int64
	if in.FormCode != "" {
		id, _, err := s.Rules.FormIDByCode(ctx, s.DB, in.FormCode)
		if errors.Is(err, contract.ErrNotFound) {
			return 0, validation("알 수 없는 양식입니다")
		}
		if err != nil {
			return 0, err
		}
		formID = id
		// 금액 필드가 없는 양식에는 전결 규정을 둘 수 없다(E19, 게시 검증과 같은 문구). 구매 품의서도 금액 필드가 지정돼 있어야 한다.
		if !s.formHasAmountField(ctx, id) {
			return 0, validation("이 양식에는 금액 필드가 없어 전결 규정을 둘 수 없습니다")
		}
	}
	return formID, nil
}

func (s *AuthorityService) formHasAmountField(ctx context.Context, formID int64) bool {
	v, err := s.Forms.LatestPublished(ctx, s.DB, formID)
	return err == nil && v.Definition.Settings.AmountField != ""
}

// overlap 은 같은 양식 범위의 사용 중 규정과 금액 구간이 겹치면 그 규정을 돌려준다.
func (s *AuthorityService) overlap(ctx context.Context, in domain.AuthorityRuleInput) (*domain.AuthorityRule, error) {
	if !in.Active {
		return nil, nil
	}
	all, err := s.Rules.List(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.ID == in.ID || !r.Active || r.FormCode != in.FormCode {
			continue
		}
		// [min, max) 구간 겹침: 하나의 시작이 다른 것의 끝보다 앞이어야 한다(상한 없음은 무한대).
		aEnd, bEnd := in.MaxAmount, r.MaxAmount
		if (aEnd == 0 || r.MinAmount < aEnd) && (bEnd == 0 || in.MinAmount < bEnd) {
			rr := r
			return &rr, nil
		}
	}
	return nil, nil
}

// Save 는 규정을 만들거나 고친다. 같은 양식 범위에서 금액 구간이 겹치면 거부한다.
func (s *AuthorityService) Save(ctx context.Context, in domain.AuthorityRuleInput) (domain.AuthorityRule, error) {
	action := "authority_rule.create"
	if in.ID != 0 {
		action = "authority_rule.update"
	}
	p, err := s.authorize(ctx, contract.PermAuthorityManage, action, audit.TargetAuthorityRule)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	in.PolicySlug, in.ClauseNo = strings.TrimSpace(in.PolicySlug), strings.TrimSpace(in.ClauseNo)
	formID, err := s.validate(ctx, in)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	if in.PolicySlug != "" && s.Clauses != nil {
		if rd := s.Clauses(); rd != nil {
			if _, cerr := rd.ClauseBy(ctx, in.PolicySlug, in.ClauseNo); errors.Is(cerr, contract.ErrNotFound) {
				return domain.AuthorityRule{}, validation("근거 조항을 찾을 수 없습니다. 규정 문서와 조항 번호를 확인하세요")
			} else if cerr != nil {
				return domain.AuthorityRule{}, cerr
			}
		}
	}
	if o, err := s.overlap(ctx, in); err != nil {
		return domain.AuthorityRule{}, err
	} else if o != nil {
		return domain.AuthorityRule{}, validation("같은 양식의 다른 규정(%s 이상 %s)과 금액 구간이 겹칩니다", moneyLabel(o.MinAmount), amountText(o.MaxAmount))
	}
	var before domain.AuthorityRule
	if in.ID != 0 {
		if before, err = s.Rules.Get(ctx, s.DB, in.ID); err != nil {
			return domain.AuthorityRule{}, err
		}
	}
	var out domain.AuthorityRule
	err = s.write(ctx, p, action, audit.TargetAuthorityRule, in.ID, func(tx contract.Tx) error {
		id := in.ID
		if in.ID == 0 {
			if id, err = s.Rules.Insert(ctx, tx, formID, in, s.now()); err != nil {
				return err
			}
		} else if err := s.Rules.Update(ctx, tx, formID, in, s.now()); err != nil {
			return err
		}
		out, err = s.Rules.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		diff := []audit.DiffItem{
			{Key: "form", Old: ruleScope(before, in.ID != 0), New: ruleScope(out, true)},
			{Key: "amount_range", Old: ruleRange(before, in.ID != 0), New: ruleRange(out, true)},
			{Key: "final_level", Old: levelOld(before, in.ID != 0), New: levelTitle(out.FinalLevel)},
			{Key: "clause", Old: clauseOld(before, in.ID != 0), New: clauseText(out)},
			{Key: "active", Old: activeOld(before, in.ID != 0), New: fmt.Sprint(out.Active)},
		}
		return s.record(ctx, tx, p, action, audit.TargetAuthorityRule, id, diff...)
	})
	return out, err
}

func ruleScope(r domain.AuthorityRule, ok bool) string {
	if !ok {
		return ""
	}
	if r.FormCode == "" {
		return "모든 양식"
	}
	return r.FormName
}

func ruleRange(r domain.AuthorityRule, ok bool) string {
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s 이상 %s", moneyLabel(r.MinAmount), amountText(r.MaxAmount))
}

func levelOld(r domain.AuthorityRule, ok bool) string {
	if !ok {
		return ""
	}
	return levelTitle(r.FinalLevel)
}

func clauseText(r domain.AuthorityRule) string {
	if r.PolicySlug == "" {
		return ""
	}
	return r.PolicySlug + " " + r.ClauseNo
}

func clauseOld(r domain.AuthorityRule, ok bool) string {
	if !ok {
		return ""
	}
	return clauseText(r)
}

func activeOld(r domain.AuthorityRule, ok bool) string {
	if !ok {
		return ""
	}
	return fmt.Sprint(r.Active)
}

// Delete 는 규정을 지운다. 진행 중 문서는 상신 때 저장한 스냅샷이라 영향이 없다.
func (s *AuthorityService) Delete(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermAuthorityManage, "authority_rule.delete", audit.TargetAuthorityRule)
	if err != nil {
		return err
	}
	before, err := s.Rules.Get(ctx, s.DB, id)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "authority_rule.delete", audit.TargetAuthorityRule, id, func(tx contract.Tx) error {
		if err := s.Rules.Delete(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "authority_rule.delete", audit.TargetAuthorityRule, id,
			audit.DiffItem{Key: "form", Old: ruleScope(before, true)}, audit.DiffItem{Key: "amount_range", Old: ruleRange(before, true)},
			audit.DiffItem{Key: "final_level", Old: levelTitle(before.FinalLevel)}, audit.DiffItem{Key: "clause", Old: clauseText(before)})
	})
}

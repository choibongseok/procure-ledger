package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// DocViewService 는 사내 지식 검색(B47)이 결재 문서 열람 판정을 빌려 쓰는 한 곳이다(명세 6절). 관계 판정(결재선·참조·수신·부서·조직장·위임·특권)을
// 이 서비스가 따로 구현하지 않고 ViewScope.Cond 를 그대로 쓴다. 호출자는 knowledge 서비스이고, 부작용(읽음 표시·열람 감사)은 없다.
type DocViewService struct {
	Base
	Repo      repository.SQLKnowledge
	Purchases repository.SQLPurchases
}

func NewDocViewService(b Base) *DocViewService { return &DocViewService{Base: b} }

// Openable 은 후보 문서 중 호출자가 지금 열 수 있는 것이다(완료·비폐기·현재 법인·양식 검색 포함·ViewScope).
func (s *DocViewService) Openable(ctx context.Context, ids []int64) (map[int64]bool, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.Repo.OpenableDocIDs(ctx, s.DB, ids, scope)
}

// Recheck 는 결과로 돌려주기 직전의 두 번째 확인이다: 문서 단건을 열람 범위로 다시 읽어 완료·비폐기·검색 포함인지 본다(Openable 과 독립된 읽기 경로).
func (s *DocViewService) Recheck(ctx context.Context, id int64) (bool, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return false, contract.ErrUnauthenticated
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return false, err
	}
	rec, err := s.Purchases.GetPR(ctx, s.DB, id, scope)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if rec.DisposedAt != "" || (rec.Status != domain.PRApproved && rec.Status != domain.PROrdered) {
		return false, nil
	}
	return true, nil
}

// RelationOpenable 은 Openable 과 같은 판정에서 특권 열람(document:read_all·purchase:read_all)만 뺀 것이다. 관계(요청자·결재선·참조·수신·부서·조직장)로 열 수 있는 문서다.
// 특권으로만 열린 결과의 건수를 감사에 남길 때 쓴다(명세 6절 7번, D9).
func (s *DocViewService) RelationOpenable(ctx context.Context, ids []int64) (map[int64]bool, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}
	rp := p
	rp.Perms = contract.NewPermSet()
	for _, perm := range contract.AllPermissions {
		if perm != contract.PermDocumentReadAll && perm != contract.PermPurchaseReadAll && p.Can(perm) {
			rp.Perms[perm] = struct{}{}
		}
	}
	scope, err := s.viewScope(ctx, rp)
	if err != nil {
		return nil, err
	}
	return s.Repo.OpenableDocIDs(ctx, s.DB, ids, scope)
}

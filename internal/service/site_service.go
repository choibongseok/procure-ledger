package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 현재 법인의 활성 사업장 목록(B44 3단계). 전표 입력·창고·부가세 화면의 선택기가 쓴다. 이름과 부가세 방식만 담아 로그인한 사람이면 읽을 수 있다.
// "전체" 모드나 현재 법인이 없는 호출은 기본 법인이 아니라 빈 목록이다(사업장은 법인 하나에 속한 값이다).

type SiteService struct {
	Base
	Repo repository.SQLSites
}

var _ domain.SiteService = (*SiteService)(nil)

func NewSiteService(b Base) *SiteService { return &SiteService{Base: b} }

func (s *SiteService) List(ctx context.Context) ([]domain.SiteInfo, error) {
	if _, ok := contract.PrincipalFrom(ctx); !ok {
		return nil, contract.ErrUnauthenticated
	}
	if a, has := contract.ActiveEntityFrom(ctx); has && (a.All || a.ID == 0) {
		return []domain.SiteInfo{}, nil
	}
	out, err := s.Repo.Sites(ctx, s.DB, contract.WriteEntityID(ctx), true)
	if out == nil {
		out = []domain.SiteInfo{}
	}
	return out, err
}

package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// ClauseService 는 전결 근거 조항 원문을 읽는다(B23 단계 3). 규정 검색 서비스(B12, ai)의 ClauseBy 를 그대로 쓰고,
// 열람 범위는 그 서비스가 호출자 기준으로 판정한다. 규정 서비스가 없거나 조항이 없으면 ErrNotFound 다.
type ClauseService struct{ Reader domain.ClauseReader }

// Clause 는 slug 와 조항 번호로 근거 조항을 찾는다.
func (c ClauseService) Clause(ctx context.Context, slug, no string) (domain.PolicyHit, error) {
	if _, err := contract.Authorize(ctx, contract.PermDraftRead); err != nil {
		return domain.PolicyHit{}, err
	}
	slug, no = strings.TrimSpace(slug), strings.TrimSpace(no)
	if slug == "" || no == "" {
		return domain.PolicyHit{}, validation("규정 문서와 조항 번호를 지정하세요")
	}
	if c.Reader == nil {
		return domain.PolicyHit{}, fmt.Errorf("%w: 근거 조항을 볼 수 없습니다", contract.ErrNotFound)
	}
	return c.Reader.ClauseBy(ctx, slug, no)
}

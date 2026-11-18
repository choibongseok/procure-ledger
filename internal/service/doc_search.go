package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// DocSearchService 는 domain.DocSearchService 의 구현이다(B29 ②). 열람 조건은 ViewScope 한 곳이고 검색은 그 위에서 일어난다.
type DocSearchService struct {
	Base
	Repo repository.SQLPurchases
}

var _ domain.DocSearchService = (*DocSearchService)(nil)

func NewDocSearchService(b Base) *DocSearchService { return &DocSearchService{Base: b} }

const docSearchPage = 50

// Search 는 조건에 맞고 호출자가 볼 수 있는 문서를 최근 변경 순으로 돌려준다.
func (s *DocSearchService) Search(ctx context.Context, f domain.DocSearchFilter) (domain.DocSearchResult, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftRead)
	if err != nil {
		return domain.DocSearchResult{}, err
	}
	term := repository.NormalizeSearch(f.Query)
	requester := strings.TrimSpace(f.Requester)
	if term == "" && f.From == "" && f.To == "" && f.FormCode == "" && len(f.Statuses) == 0 && requester == "" {
		return domain.DocSearchResult{}, validation("검색어나 조건을 하나 이상 입력하세요.")
	}
	if term != "" && utf8.RuneCountInString(term) < 2 {
		return domain.DocSearchResult{}, validation("검색어는 2자 이상 입력하세요.")
	}
	for _, st := range f.Statuses {
		switch st {
		case "draft", "in_review", "done", "rejected", "disposed":
		default:
			return domain.DocSearchResult{}, validation("알 수 없는 상태입니다. 임시저장·결재 중·완료·반려·폐기됨 중에서 고르세요.")
		}
	}
	for _, d := range []string{f.From, f.To} {
		if d == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return domain.DocSearchResult{}, validation("날짜는 YYYY-MM-DD 형식으로 입력하세요.")
		}
	}
	if f.From != "" && f.To != "" && f.To < f.From {
		return domain.DocSearchResult{}, validation("종료일은 시작일 이후여야 합니다.")
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.DocSearchResult{}, err
	}
	rows, total, err := s.Repo.SearchDocs(ctx, s.DB, repository.SearchQuery{Term: term, From: f.From, To: f.To, FormCode: f.FormCode, Statuses: f.Statuses,
		Requester: requester, Limit: docSearchPage, Offset: f.Offset, UserID: p.UserID}, scope)
	if err != nil {
		return domain.DocSearchResult{}, err
	}
	for i := range rows {
		rows[i].Matched = matchedPlace(term, rows[i])
	}
	return domain.DocSearchResult{Rows: rows, Total: total}, nil
}

// matchedPlace 는 검색어가 일치한 곳의 이름이다(제목·번호·기안자, 그 밖에는 본문). 검색어가 없으면 빈 값.
func matchedPlace(term string, r domain.DocSearchRow) string {
	if term == "" {
		return ""
	}
	switch {
	case strings.Contains(repository.NormalizeSearch(r.Title), term):
		return "제목"
	case strings.Contains(repository.NormalizeSearch(r.No), term):
		return "번호"
	case strings.Contains(repository.NormalizeSearch(r.RequesterName), term):
		return "기안자"
	}
	return "본문"
}

// BackfillSearchText 는 검색 텍스트가 빈 기존 문서를 채운다(기동 때 한 번, 멱등).
func BackfillSearchText(ctx context.Context, b Base) error {
	return b.Txs.WithTx(ctx, func(tx contract.Tx) error {
		_, err := repository.BackfillSearchText(ctx, tx)
		return err
	})
}

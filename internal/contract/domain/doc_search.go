package domain

import (
	"context"
	"time"
)

// B29 ②: 문서 통합 검색( 4절). 결과는 호출자가 열람할 수 있는 문서뿐이다(권한 조건이 쿼리 WHERE 의 맨 앞).

// DocSearchFilter 는 검색 조건이다. 최소 하나는 있어야 하고 검색어는 2자 이상이다. Statuses 는 draft | in_review | done | rejected | disposed.
type DocSearchFilter struct {
	Query     string
	From, To  string // 기안일(KST) YYYY-MM-DD
	FormCode  string
	Statuses  []string
	Requester string
	Offset    int
}

// DocSearchRow 는 결과 한 줄이다. Matched 는 일치한 곳의 이름(제목·번호·기안자·본문)이다.
type DocSearchRow struct {
	ID            int64
	No            string
	Title         string
	FormCode      string
	FormName      string
	FormKind      string // purchase | general
	RequesterName string
	UnitPath      string
	Status        PRStatus
	Disposed      bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Matched       string
}

// DocSearchResult 는 한 쪽(50건)과 전체 건수다.
type DocSearchResult struct {
	Rows  []DocSearchRow
	Total int
}

// DocSearchService 는 문서 통합 검색이다(draft:read, 읽기 전용).
type DocSearchService interface {
	Search(ctx context.Context, f DocSearchFilter) (DocSearchResult, error)
}

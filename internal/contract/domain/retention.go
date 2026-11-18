package domain

import "context"

// 보존 연한 만료 문서 폐기(B29 ③, 6절).
// 기본은 아무것도 지우지 않는다. 서버는 만료를 목록으로만 알리고, 폐기는 관리자가 명시적으로 승인할 때만 일어난다.

// RetentionCandidate 는 폐기 후보(또는 폐기 보류) 문서 한 건이다.
type RetentionCandidate struct {
	ID             int64
	No             string
	Title          string
	FormCode       string
	FormName       string
	RequesterName  string
	CompletedAt    string // 결재 완료 일시(KST, YYYY-MM-DD)
	RetentionUntil string // 보존 기한(YYYY-MM-DD, 이 날까지 보존)
	DaysOver       int    // 기한을 넘긴 일수(기한 다음 날이 1)
	Held           bool
	HoldReason     string
}

// DisposeInput 은 폐기 승인 입력이다. IDs 는 한 번에 최대 MaxDisposeBatch 건이다.
type DisposeInput struct {
	IDs    []int64
	Reason string
}

// MaxDisposeBatch 는 한 번에 폐기할 수 있는 최대 문서 수다.
const MaxDisposeBatch = 100

// RetentionService 는 보존 기한이 지난 문서의 목록과 폐기다.
type RetentionService interface {
	// Candidates 는 폐기 후보 목록이다(retention:read). 보존 기한이 오래 지난 순이다. limit·offset 은 쪽 나누기이고 total 은 전체 건수다.
	Candidates(ctx context.Context, limit, offset int) (rows []RetentionCandidate, total int, err error)
	// Holds 는 폐기 보류 중인 문서 목록이다(retention:read).
	Holds(ctx context.Context) ([]RetentionCandidate, error)
	// ExpiredCount 는 폐기 후보 건수다(관리자 홈 카드, retention:read).
	ExpiredCount(ctx context.Context) (int, error)
	// Dispose 는 문서를 폐기한다(retention:dispose). 요청 전체가 한 트랜잭션이고 하나라도 후보가 아니면 아무것도 지우지 않는다.
	Dispose(ctx context.Context, in DisposeInput) (int, error)
	// Hold·ClearHold 는 폐기 보류를 걸고 푼다(retention:dispose). 사유는 필수다.
	Hold(ctx context.Context, id int64, reason string) error
	ClearHold(ctx context.Context, id int64, reason string) error
}

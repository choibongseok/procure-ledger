package domain

import (
	"context"
	"time"
)

// B23 단계 2: 문서함(7종)·읽음/안읽음·수신 처리·일괄 승인·상신 취소. 기존 시그니처는 바꾸지 않고 더하기만 한다.

// InboxBox 는 문서함 종류다. 한 문서가 여러 함에 동시에 보일 수 있다(조회 전용 목록, 저장되는 것은 읽음 상태뿐).
type InboxBox string

const (
	BoxDrafted    InboxBox = "drafted"     // 기안함: 내가 기안한 임시저장·상신·결재중
	BoxPending    InboxBox = "pending"     // 결재 대기함: 지금 내 차례(결재·합의, 위임분 포함)
	BoxInProgress InboxBox = "in_progress" // 결재 진행함: 결재선에 있고 진행 중이며 지금 내 차례가 아님
	BoxDone       InboxBox = "done"        // 완료함: 관여했고 결재 완료
	BoxCC         InboxBox = "cc"          // 참조함
	BoxReceive    InboxBox = "receive"     // 수신함: 내가 접수 권한이 있는 수신처의 수신 건
	BoxRejected   InboxBox = "rejected"    // 반려함
)

// InboxBoxes 는 문서함 전체(화면 탭 순서)다.
var InboxBoxes = []InboxBox{BoxDrafted, BoxPending, BoxInProgress, BoxDone, BoxCC, BoxReceive, BoxRejected}

// InboxItem 은 문서함 목록의 한 줄이다. 수신함이면 수신 건 정보가 채워진다.
type InboxItem struct {
	ID            int64
	No            string
	Title         string
	FormCode      string
	FormName      string
	FormKind      string // purchase | general
	RequesterName string
	UnitPath      string
	Status        PRStatus
	Urgent        bool
	Unread        bool
	Disposed      bool // 보존 만료로 폐기된 문서(B29): 제목·본문이 없는 껍데기다
	UpdatedAt     time.Time
	Amount        int64
	ReceiptID     int64  // 수신함에서만
	ReceiptStatus string // pending | received | assigned | done | returned
	ReceiptUnit   string
}

// InboxFilter 는 문서함 목록 조건이다. Tab 은 수신함의 상태 탭(waiting 접수 대기·received 접수·done 처리 완료·returned 반송)이고 비면 전부다.
type InboxFilter struct {
	Tab    string
	Limit  int // 기본 50, 최대 100
	Offset int
}

// InboxCount 는 문서함 한 곳의 건수와 안 읽은 건수다.
type InboxCount struct {
	Total  int
	Unread int
}

// InboxService 는 문서함 조회와 읽음 처리다(권한 inbox:read). 목록의 열람 조건은 구매요청·문서와 같은 ViewScope 한 곳을 쓴다.
type InboxService interface {
	List(ctx context.Context, box InboxBox, f InboxFilter) ([]InboxItem, error)
	Counts(ctx context.Context) (map[InboxBox]InboxCount, error)
	MarkRead(ctx context.Context, docID int64) error // 열람 범위 안의 문서만, 밖이면 ErrNotFound
}

// DocReceipt 는 양식 문서의 수신 건이다(입고 Receipt 와 다르다)(결재 완료 때 수신처마다 생긴다).
type DocReceipt struct {
	ID            int64
	DocID         int64
	DocNo         string
	DocTitle      string
	FormName      string
	RequesterID   int64
	RequesterName string
	UnitID        int64
	UnitName      string
	Status        string // pending | received | assigned | done | returned
	CreatedAt     time.Time
	ReceivedBy    string
	ReceivedAt    *time.Time
	AssigneeID    int64
	AssigneeName  string
	AssignedAt    *time.Time
	DoneBy        string
	DoneAt        *time.Time
	ReturnedBy    string
	ReturnedAt    *time.Time
	Comment       string
	Reason        string
	CanHandle     bool // 호출자가 이 수신 건을 처리(접수·지정·완료·반송)할 수 있는가(수신처 조직장·수신 담당자·지정된 담당자). 화면은 이 값으로 버튼을 보인다.
}

// DocReceiptFilter 는 수신 건 조회 조건이다. DocID 가 있으면 그 문서의 수신 건(문서 상세용), 아니면 내가 접수 권한이 있는 수신처의 건이다.
type DocReceiptFilter struct {
	DocID int64
	Tab   string
	Limit int
}

// UnitReceiver 는 수신 담당자 한 명이다.
type UnitReceiver struct {
	UserID int64
	Name   string
}

// DocReceiptService 는 수신 처리와 수신 담당자 관리다(권한 receipt:handle). 접수 권한은 수신처 조직장(직무대행 포함)과 그 조직의 수신 담당자다.
type DocReceiptService interface {
	List(ctx context.Context, f DocReceiptFilter) ([]DocReceipt, error)
	Accept(ctx context.Context, receiptID int64) (DocReceipt, error)
	Assign(ctx context.Context, receiptID, userID int64) (DocReceipt, error)
	Complete(ctx context.Context, receiptID int64, comment string) (DocReceipt, error)
	Return(ctx context.Context, receiptID int64, reason string) (DocReceipt, error)
	Receivers(ctx context.Context, unitID int64) ([]UnitReceiver, error)
	AddReceiver(ctx context.Context, unitID, userID int64) error    // 그 조직의 조직장만
	RemoveReceiver(ctx context.Context, unitID, userID int64) error // 그 조직의 조직장만
}

// BatchItem 은 일괄 승인의 한 건이다.
type BatchItem struct {
	DocID int64
}

// BatchResult 는 일괄 승인 한 건의 결과다. 한 건의 실패가 다른 건을 막지 않는다.
type BatchResult struct {
	DocID   int64
	OK      bool
	Message string   // 실패 사유(한국어) 또는 성공 안내
	Status  PRStatus // 처리 뒤 문서 상태(성공할 때)
}

// Package changes 는 변경 피드(B42, 8절)다.(기록)·ai(조회 API).
//
// 업무 쓰기(품목·거래처·발주·입고·전표·채무)는 **같은 트랜잭션에서** Note 로 한 줄씩 남긴다. 롤백되면 줄도 없다.
// 줄에는 값이 없다(자원·번호·동작·상태·시각·바꾼 경로). 소비자는 값을 API 로 읽는다. 증분 조회와 웹훅이 같은 줄을 읽는다.
//
// 번호(seq) 순서 = 커밋 순서다. PG 의 번호는 받은 순서와 커밋 순서가 다를 수 있어(번호를 먼저 받고 늦게 커밋하는 트랜잭션이 있으면
// 소비자가 더 큰 번호를 먼저 읽고 지나가 그 줄을 영영 놓친다) Note 는 피드 전용 advisory lock 을 잡은 **뒤에** 줄을 넣고, 잠금은 트랜잭션이 끝날 때까지 쥔다.
// 그래서 어떤 트랜잭션이든 자기보다 작은 번호의 줄이 아직 커밋 전이라 보이지 않는 일이 없다. SQLite 는 연결 하나가 쓰기를 직렬화한다.
package changes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 자원 종류.
const (
	ResItem          = "item"
	ResVendor        = "vendor"
	ResPurchaseOrder = "purchase_order"
	ResReceipt       = "receipt"
	ResJournal       = "journal"
	ResPayable       = "payable"
	ResTaxInvoice    = "tax_invoice" // B43 3단계: 매출·매입 세금계산서(생성·상태 변경)
)

// Resources 는 추적하는 자원 전체다(웹훅 구독·API 범위가 이 목록으로 검증한다).
var Resources = []string{ResItem, ResVendor, ResPurchaseOrder, ResReceipt, ResJournal, ResPayable, ResTaxInvoice}

// 동작.
const (
	OpCreated       = "created"
	OpUpdated       = "updated"
	OpStatusChanged = "status_changed"
	OpCanceled      = "canceled"
)

const tsLayout = "2006-01-02T15:04:05.000000000Z"

func validResource(r string) bool {
	for _, x := range Resources {
		if x == r {
			return true
		}
	}
	return false
}

func validOp(op string) bool {
	return op == OpCreated || op == OpUpdated || op == OpStatusChanged || op == OpCanceled
}

// Note 는 변경 한 건을 피드에 남긴다. 호출한 트랜잭션(q)에서만 쓰고, 실패하면 호출자는 그 오류로 트랜잭션을 끝내야 한다.
// 바꾼 경로(actor_channel)는 context 의 Principal 채널이다(없으면 web). status 는 발주·전표 같은 상태가 있는 자원의 새 상태 코드, 없으면 "".
func Note(ctx context.Context, q contract.Tx, resource string, id int64, op, status string) error {
	return NoteIn(ctx, q, contract.EntityFrom(ctx), resource, id, op, status)
}

// NoteIn 은 Note 와 같되 레코드의 법인을 명시한다(B44). 업무 저장소는 자기가 아는 레코드의 법인을 넘긴다. Note 는 요청의 현재 법인(없으면 기본 법인)을 쓴다.
func NoteIn(ctx context.Context, q contract.Tx, entityID int64, resource string, id int64, op, status string) error {
	if entityID <= 0 {
		entityID = contract.DefaultEntityID
	}
	if !validResource(resource) || !validOp(op) || id <= 0 {
		return fmt.Errorf("changes: 알 수 없는 변경 기록(%q %d %q)", resource, id, op)
	}
	if err := dbutil.AcquireTxLock(ctx, q, dbutil.Active(), dbutil.LockChangeFeed); err != nil {
		return err
	}
	ch := string(contract.ChannelWeb)
	if p, ok := contract.PrincipalFrom(ctx); ok && p.Channel != "" {
		ch = string(p.Channel)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO change_events (at, resource, object_id, op, status, actor_channel, entity_id) VALUES (?,?,?,?,?,?,?)`,
		time.Now().UTC().Format(tsLayout), resource, id, op, status, ch, entityID)
	return err
}

// Event 는 피드 한 줄이다.
type Event struct {
	Seq          int64  `json:"seq"`
	At           string `json:"at"`
	Resource     string `json:"resource"`
	ObjectID     int64  `json:"id"`
	Op           string `json:"op"`
	Status       string `json:"status,omitempty"`
	ActorChannel string `json:"actor_channel"`
	// EntityID 는 변경이 일어난 법인이다(공개 응답에는 싣지 않는다: 웹훅 발송 판정과 토큰 범위 거름이 쓴다).
	EntityID int64 `json:"-"`
}

// 커서: "c1.<seq>". 클라이언트는 그대로 돌려보낸다(형식은 나중에 바꿀 수 있게 불투명하게 둔다).
const cursorPrefix = "c1."

// EncodeCursor 는 번호를 커서 문자열로 만든다.
func EncodeCursor(seq int64) string { return cursorPrefix + strconv.FormatInt(seq, 10) }

// ErrBadCursor 는 형식이 틀리거나 아직 없는 번호(미래)의 커서다(400).
var ErrBadCursor = errors.New("커서가 올바르지 않습니다")

// ErrCursorExpired 는 보존 정리로 사라진 번호를 가리키는 커서다(410). 소비자는 전체 적재부터 다시 한다.
var ErrCursorExpired = errors.New("커서가 만료되었습니다. 전체를 다시 읽어 주세요")

// DecodeCursor 는 커서 문자열을 번호로 푼다. 빈 문자열은 0(처음부터)이 아니라 오류다(호출자가 정한다).
func DecodeCursor(c string) (int64, error) {
	if !strings.HasPrefix(c, cursorPrefix) {
		return 0, ErrBadCursor
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(c, cursorPrefix), 10, 64)
	if err != nil || n < 0 {
		return 0, ErrBadCursor
	}
	return n, nil
}

// Head 는 현재 가장 큰 번호다(줄이 없으면 0). 최초 적재는 시작하기 전에 이 값을 받아 둔다.
func Head(ctx context.Context, q contract.Tx) (int64, error) {
	var n sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(seq) FROM change_events`).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// purgedThrough 는 보존 정리가 지운 마지막 번호다(app_settings changes.purged_through, 없으면 0).
func purgedThrough(ctx context.Context, q contract.Tx) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'changes.purged_through'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n, nil
}

// After 는 번호가 after 보다 큰 줄을 번호 순으로 최대 limit 개 돌려준다. resources 가 비어 있지 않으면 그 자원의 줄만이다.
// 두 번째 값은 limit 개를 넘는 줄이 더 있는지, 세 번째 값은 읽기 시작할 때의 가장 큰 번호(head)다: 더 없으면(more=false) 그 번호까지 모두 훑은 것이라
// 필터로 걸러져 줄이 없어도 소비자는 커서를 head 로 올려도 된다(번호 순서 = 커밋 순서라 head 이하의 줄은 이미 보인다).
// 커서가 정리로 사라진 구간이면 ErrCursorExpired, 현재 가장 큰 번호보다 크면 ErrBadCursor 다.
func After(ctx context.Context, q contract.Tx, after int64, limit int, resources []string) ([]Event, bool, int64, error) {
	return AfterIn(ctx, q, after, limit, resources, contract.EntityScope{All: true})
}

// AfterIn 은 After 와 같되 법인 범위로 거른다(B44: 토큰이 한 법인에 묶여 있으면 그 법인의 변경만 보인다).
func AfterIn(ctx context.Context, q contract.Tx, after int64, limit int, resources []string, scope contract.EntityScope) ([]Event, bool, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	head, err := Head(ctx, q)
	if err != nil {
		return nil, false, 0, err
	}
	if after > head {
		return nil, false, 0, ErrBadCursor
	}
	if purged, err := purgedThrough(ctx, q); err != nil {
		return nil, false, 0, err
	} else if after < purged {
		return nil, false, 0, ErrCursorExpired
	}
	query := `SELECT seq, at, resource, object_id, op, status, actor_channel, entity_id FROM change_events WHERE seq > ?`
	args := []any{after}
	if len(resources) > 0 {
		marks := make([]string, len(resources))
		for i, r := range resources {
			if !validResource(r) {
				return nil, false, 0, fmt.Errorf("%w: 알 수 없는 자원 %q", contract.ErrValidation, r)
			}
			marks[i] = "?"
			args = append(args, r)
		}
		query += ` AND resource IN (` + strings.Join(marks, ",") + `)`
	}
	if cond, cargs := scope.Cond(""); cond != "" {
		query += ` AND ` + cond
		args = append(args, cargs...)
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, 0, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.At, &e.Resource, &e.ObjectID, &e.Op, &e.Status, &e.ActorChannel, &e.EntityID); err != nil {
			return nil, false, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, head, nil
}

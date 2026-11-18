package sod

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

const atLayout = "2006-01-02T15:04:05.000000000Z"

// 같은 규칙의 위반 알림은 한 시간에 이 건수까지 개별로 보내고, 넘으면 한 번만 묶음 알림을 보낸다(D12).
const notifyBurst = 10

// Options 는 Engine 구성이다.
type Options struct {
	DB      *sql.DB
	Txs     contract.TxManager
	Audit   audit.Recorder
	Deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error // 메일·웹훅 발송 건(B36). 비면 앱 안 알림만
	Now     func() time.Time
}

// Engine 은 판정·기록·규칙 모드·예외를 맡는 구현이다(Evaluator 구현).
type Engine struct {
	db      *sql.DB
	txs     contract.TxManager
	audit   audit.Recorder
	deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
	now     func() time.Time
}

var _ Evaluator = (*Engine)(nil)

// New 는 Engine 을 만든다.
func New(o Options) (*Engine, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil {
		return nil, errors.New("sod: DB·Txs·Audit 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Engine{db: o.DB, txs: o.Txs, audit: o.Audit, deliver: o.Deliver, now: o.Now}, nil
}

func (e *Engine) stamp() string { return e.now().UTC().Format(atLayout) }

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ModeOf 는 규칙의 현재 모드다. 행이 없으면 코드의 초기 모드이고, 잠긴 규칙은 항상 초기 모드다.
func ModeOf(ctx context.Context, q querier, r Rule) string {
	if r.Locked {
		return r.Default
	}
	var m string
	if err := q.QueryRowContext(ctx, `SELECT mode FROM sod_rule_settings WHERE rule_code = ?`, r.Code).Scan(&m); err != nil {
		return r.Default
	}
	return m
}

// Evaluate 는 Evaluator 를 구현한다.
func (e *Engine) Evaluate(ctx context.Context, tx contract.Tx, c Check) (Decision, error) {
	r, ok := RuleByCode(c.Rule)
	if !ok || r.Kind != KindTxn {
		return Decision{}, fmt.Errorf("sod: 알 수 없는 거래 규칙 %q", c.Rule)
	}
	if c.Other == 0 || c.Other != c.Actor.UserID {
		return Decision{Mode: ModeOff}, nil // 서로 다른 사람이 했다
	}
	mode := ModeOf(ctx, tx, r)
	switch mode {
	case ModeOff:
		return Decision{Mode: ModeOff}, nil
	case ModeWarn:
		if err := e.writeViolation(ctx, tx, c, r, "allowed", ModeWarn, 0); err != nil {
			return Decision{}, err
		}
		return Decision{Mode: ModeWarn, Violation: true}, nil
	}
	// block: 유효한 예외(승인됨·만료 전·1회)가 있으면 소진하고 통과한다. 잠긴 규칙은 예외를 만들 수 없으니 여기서도 찾지 않는다.
	if !r.Locked {
		exID, err := e.consumeException(ctx, tx, c)
		if err != nil {
			return Decision{}, err
		}
		if exID != 0 {
			if err := e.writeViolation(ctx, tx, c, r, "excepted", ModeBlock, exID); err != nil {
				return Decision{}, err
			}
			return Decision{Mode: ModeBlock, Violation: true, Excepted: true}, nil
		}
	}
	return Decision{Mode: ModeBlock, Violation: true}, &BlockedError{Check: c}
}

// consumeException 은 이 사람·이 거래·이 규칙의 승인된 예외 하나를 사용 처리하고 id 를 돌려준다(없으면 0).
func (e *Engine) consumeException(ctx context.Context, tx contract.Tx, c Check) (int64, error) {
	now := e.stamp()
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM sod_exceptions WHERE rule_code = ? AND subject_type = ? AND subject_id = ? AND user_id = ? AND status = 'active' AND expires_at > ? ORDER BY id LIMIT 1`,
		c.Rule, c.Subject.Type, c.Subject.ID, c.Actor.UserID, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE sod_exceptions SET status = 'used', used_at = ? WHERE id = ? AND status = 'active'`, now, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, nil // 동시에 다른 거래가 소진했다
	}
	if err := e.audit.Record(ctx, tx, audit.Entry{Actor: c.Actor, Action: "sod.exception_used", TargetType: audit.TargetSoDException, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{{Key: "sod_rule", New: c.Rule}, {Key: "sod_subject_type", New: c.Subject.Type}, {Key: "sod_subject_id", New: fmt.Sprint(c.Subject.ID)}}}); err != nil {
		return 0, err
	}
	return id, nil
}

func subjectNote(r Rule, result string, c Check) string {
	switch result {
	case "blocked":
		return fmt.Sprintf("%s 시도를 차단했습니다(%s 와 %s 를 같은 사람이 함)", r.Title, r.StepA, r.StepB)
	case "excepted":
		return fmt.Sprintf("%s: 승인된 예외로 통과", r.Title)
	}
	return fmt.Sprintf("%s: 같은 사람이 %s 와 %s 를 모두 함(경고)", r.Title, r.StepA, r.StepB)
}

// writeViolation 은 위반 행·감사·알림을 호출자의 트랜잭션에 쓴다.
func (e *Engine) writeViolation(ctx context.Context, tx contract.Tx, c Check, r Rule, result, mode string, exceptionID int64) error {
	now := e.stamp()
	var ex any
	if exceptionID != 0 {
		ex = exceptionID
	}
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO sod_violations (at, rule_code, result, mode, kind, subject_type, subject_id, subject_no, actor_id, other_id, exception_id, note) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		now, r.Code, result, mode, r.Kind, c.Subject.Type, c.Subject.ID, c.Subject.No, c.Actor.UserID, c.Other, ex, subjectNote(r, result, c))
	if err != nil {
		return err
	}
	action, res := "sod.violation", audit.ResultSuccess
	if result == "blocked" {
		action, res = "sod.blocked", audit.ResultDenied
	}
	if err := e.audit.Record(ctx, tx, audit.Entry{Actor: c.Actor, Action: action, TargetType: audit.TargetSoDViolation, TargetID: fmt.Sprint(id), Result: res,
		Diff:   []audit.DiffItem{{Key: "sod_rule", New: r.Code}, {Key: "sod_subject_type", New: c.Subject.Type}, {Key: "sod_subject_id", New: fmt.Sprint(c.Subject.ID)}, {Key: "sod_result", New: result}},
		Detail: r.Title}); err != nil {
		return err
	}
	return e.notifyViolation(ctx, tx, r, id, now)
}

// notifyViolation 은 sod:read 권한자에게 앱 안 알림을 보낸다. 같은 규칙이 한 시간에 10건을 넘으면 한 번만 묶음으로 알리고 이후는 보내지 않는다.
func (e *Engine) notifyViolation(ctx context.Context, tx contract.Tx, r Rule, violationID int64, now string) error {
	since := e.now().UTC().Add(-time.Hour).Format(atLayout)
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = ? AND at >= ?`, r.Code, since).Scan(&n); err != nil {
		return err
	}
	if n > notifyBurst+1 {
		return nil
	}
	title, body := "권한 분리 위반: "+r.Title, "권한 분리 규칙 위반이 기록되었습니다. 위반 목록에서 확인하세요."
	if n == notifyBurst+1 {
		title, body = "권한 분리 위반 다수: "+r.Title, fmt.Sprintf("같은 규칙의 위반이 한 시간에 %d건을 넘었습니다. 이후 알림은 묶어서 생략합니다. 위반 목록에서 확인하세요.", notifyBurst)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE e.permission = ? AND u.active = 1 ORDER BY e.user_id`, string(contract.PermSoDRead))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	at := e.now()
	for _, uid := range ids {
		nid, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: uid, Kind: "sod_violation", Title: title, Body: body, Link: "/admin/sod/violations", RefType: "sod_violation", RefID: violationID, Now: at})
		if err != nil {
			return err
		}
		if e.deliver != nil {
			if derr := e.deliver(ctx, tx, nid, uid, "sod_violation", at); derr != nil {
				log.Printf("sod: 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", nid, derr)
			}
		}
	}
	return nil
}

// RecordBlocked 는 차단된 시도를 별도 트랜잭션에 남긴다. 실패는 로그만 남긴다(이미 거래는 거부됐다).
func (e *Engine) RecordBlocked(ctx context.Context, c Check) {
	r, ok := RuleByCode(c.Rule)
	if !ok {
		return
	}
	err := e.txs.WithTx(ctx, func(tx contract.Tx) error { return e.writeViolation(ctx, tx, c, r, "blocked", ModeBlock, 0) })
	if err != nil {
		log.Printf("sod: 차단 시도 기록 실패(규칙 %s): %v", c.Rule, err)
	}
}

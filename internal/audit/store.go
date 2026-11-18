// Package audit 는 해시 체인 감사 로그 구현이다.
//
// 기록(Record)은 호출자의 트랜잭션 안에서 직전 레코드를 읽어 이어 붙인다. 같은 트랜잭션이므로
// 비즈니스 쓰기와 감사 기록은 함께 커밋되거나 함께 롤백된다. 기록이 실패하면 오류(ErrAuditFailed)를
// 돌려주고, 서비스는 그 오류로 WithTx 를 끝내 요청 전체를 취소한다.
// 직렬화: SQLite 는 단일 쓰기 연결에 기대고, PG 는 record 첫머리의 트랜잭션 수준 advisory lock(B37 S1)으로 한다.
//
// 한계: 체인만으로는 중간 변조·삭제·끼워 넣기를 잡는다. 꼬리(마지막 N건) 삭제와 체인 전체 재계산은
// 외부 앵커(anchor.go)와 대조해야 잡히고, 그 한계는 anchor.go 머리말에 적혀 있다.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

const timeLayout = time.RFC3339Nano

// Store 는 audit.Recorder 와 audit.Reader 를 구현한다.
type Store struct {
	db      *sql.DB
	dialect dbutil.Dialect
	noLock  bool // 시험 전용(export_test.go): 체인 잠금을 건너뛰어 "틀린 구현"이 시험에 걸리는지 본다
	now     func() time.Time
	anchor  *Anchor // 외부 앵커(B4). nil 이면 외부 대조를 하지 않는다
	// hasEntity 는 audit_logs 에 entity_id 열(B44 0074)이 있다고 한 번 확인되면 참이다. 열은 없어지지 않으므로 참만 캐시한다.
	hasEntity atomic.Bool
}

// SetAnchor 는 Verify 가 대조할 외부 앵커를 연결한다(NewAnchor 가 부른다).
func (s *Store) SetAnchor(a *Anchor) { s.anchor = a }

// New 는 db 위의 Store 를 만든다.
func New(db *sql.DB) *Store { return &Store{db: db, dialect: dbutil.DialectOf(db), now: time.Now} }

var (
	_ audit.Recorder = (*Store)(nil)
	_ audit.Reader   = (*Store)(nil)
	_ audit.Scanner  = (*Store)(nil)
)

// Record 는 tx 안에서 레코드를 이어 붙인다.
func (s *Store) Record(ctx context.Context, tx contract.Tx, e audit.Entry) error {
	if err := s.record(ctx, tx, e); err != nil {
		return fmt.Errorf("%w: %v", contract.ErrAuditFailed, err)
	}
	return nil
}

func (s *Store) record(ctx context.Context, tx contract.Tx, e audit.Entry) error {
	if tx == nil {
		return errors.New("트랜잭션이 없습니다")
	}
	if err := audit.ValidateEntry(e); err != nil {
		return err
	}

	// PG 는 연결이 여럿이라 두 트랜잭션이 같은 마지막 행을 읽고 같은 seq 를 붙이려 할 수 있다(B37 S1).
	// 마지막 행을 읽기 전에 트랜잭션 수준 advisory lock 을 잡아 체인에 붙이는 구간을 직렬화한다(커밋·롤백에 풀린다).
	// SQLite 는 연결 하나가 쓰기를 직렬화하므로 아무 일도 하지 않는다. PK(seq)·hash UNIQUE 가 두 번째 안전망이다.
	if !s.noLock {
		if err := dbutil.AcquireTxLock(ctx, tx, s.dialect, dbutil.LockAuditChain); err != nil {
			return err
		}
	}
	// 이관 전 스키마(entity_id 열 없음)에서도 기록할 수 있어야 한다(마이그레이션 중간 상태를 시험하는 경로). 그때는 법인 값을 남기지 않는다.
	_, hasEntity, err := s.colsFor(ctx, tx)
	if err != nil {
		return err
	}
	var prevSeq int64
	var prevHash string
	err = tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&prevSeq, &prevHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rec := audit.Record{
		Seq:        prevSeq + 1,
		At:         s.now().UTC(),
		ActorID:    e.Actor.UserID,
		ActorRole:  e.Actor.Role,
		Channel:    e.Actor.Channel,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		Result:     e.Result,
		Diff:       e.Diff,
		Detail:     e.Detail,
		PrevHash:   prevHash,
		EntityID:   audit.EntityOf(ctx, e),
	}
	if !hasEntity {
		rec.EntityID = 0
	}
	if rec.Diff == nil {
		rec.Diff = []audit.DiffItem{}
	}
	rec.Hash = audit.HashRecord(rec)
	diff, err := json.Marshal(rec.Diff)
	if err != nil {
		return err
	}
	if !hasEntity {
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs
		(seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			rec.Seq, rec.At.Format(timeLayout), rec.ActorID, string(rec.ActorRole), string(rec.Channel), rec.Action,
			string(rec.TargetType), rec.TargetID, string(rec.Result), string(diff), rec.Detail, rec.PrevHash, rec.Hash)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs
		(seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash, entity_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Seq, rec.At.Format(timeLayout), rec.ActorID, string(rec.ActorRole), string(rec.Channel), rec.Action,
		string(rec.TargetType), rec.TargetID, string(rec.Result), string(diff), rec.Detail, rec.PrevHash, rec.Hash, rec.EntityID)
	return err
}

const baseCols = `seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash`

// colsFor 는 읽을 열 목록이다. 이관 전 스키마(entity_id 열 없음)의 DB 는 `0 AS entity_id` 로 읽는다: 백업 세트의 감사 체인 검증(verify-backup·복구 점검)이
// 이관 전 마이그레이션 상태의 DB 를 그대로 열기 때문이다(B44 명세 13.1.1).
func (s *Store) colsFor(ctx context.Context, q contract.Tx) (string, bool, error) {
	if s.hasEntity.Load() {
		return baseCols + ", entity_id", true, nil
	}
	var n int
	var err error
	if s.dialect.Kind() == dbutil.KindPostgres {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'audit_logs' AND column_name = 'entity_id'`).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('audit_logs') WHERE name = 'entity_id'`).Scan(&n)
	}
	if err != nil {
		return "", false, err
	}
	if n > 0 {
		s.hasEntity.Store(true)
		return baseCols + ", entity_id", true, nil
	}
	return baseCols + ", 0 AS entity_id", false, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanRecord(sc scanner) (audit.Record, error) {
	var r audit.Record
	var at, role, channel, tt, result, diff string
	if err := sc.Scan(&r.Seq, &at, &r.ActorID, &role, &channel, &r.Action, &tt, &r.TargetID, &result, &diff, &r.Detail, &r.PrevHash, &r.Hash, &r.EntityID); err != nil {
		return r, err
	}
	r.ActorRole, r.Channel, r.TargetType, r.Result = contract.Role(role), contract.Channel(channel), audit.TargetType(tt), audit.Result(result)
	t, err := time.Parse(timeLayout, at)
	if err != nil {
		return r, fmt.Errorf("시각 형식: %w", err)
	}
	r.At = t.UTC()
	if err := json.Unmarshal([]byte(diff), &r.Diff); err != nil {
		return r, fmt.Errorf("diff 형식: %w", err)
	}
	return r, nil
}

// List 는 조건에 맞는 기록을 돌려준다. Limit 기본 100, 최대 500.
func (s *Store) List(ctx context.Context, f audit.Filter) ([]audit.Record, error) {
	cols, _, err := s.colsFor(ctx, s.db)
	if err != nil {
		return nil, err
	}
	q, args := buildQuery(f, cols)
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []audit.Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Scan 은 조건에 맞는 기록을 한도 없이 순서대로 fn 에 넘긴다(읽기 전용, 감사 레코드를 만들지 않는다).
// 사용량 집계처럼 기간 전체를 훑는 조회용이다. fn 이 오류를 돌려주면 거기서 멈춘다.
// Filter.Limit 가 0 보다 크면 그 건수까지만 읽는다.
func (s *Store) Scan(ctx context.Context, f audit.Filter, fn func(audit.Record) error) error {
	cols, _, err := s.colsFor(ctx, s.db)
	if err != nil {
		return err
	}
	q, args := buildQuery(f, cols)
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func buildQuery(f audit.Filter, cols string) (string, []any) {
	var where []string
	var args []any
	add := func(cond string, v any) { where = append(where, cond); args = append(args, v) }
	if f.ActorID != 0 {
		add("actor_id = ?", f.ActorID)
	}
	if len(f.ActorIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.ActorIDs)), ",")
		where = append(where, "actor_id IN ("+marks+")")
		for _, id := range f.ActorIDs {
			args = append(args, id)
		}
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.TargetType != "" {
		add("target_type = ?", string(f.TargetType))
	}
	if f.TargetID != "" {
		add("target_id = ?", f.TargetID)
	}
	if f.Result != "" {
		add("result = ?", string(f.Result))
	}
	if f.Channel != "" {
		add("channel = ?", string(f.Channel))
	}
	if !f.From.IsZero() {
		add("at >= ?", f.From.UTC().Format(timeLayout))
	}
	if !f.To.IsZero() {
		add("at < ?", f.To.UTC().Format(timeLayout))
	}
	if f.AfterSeq > 0 {
		if f.Desc {
			add("seq < ?", f.AfterSeq)
		} else {
			add("seq > ?", f.AfterSeq)
		}
	}
	// 법인 범위(B44): 범위 안의 법인 사건과 법인과 무관한 사건(entity_id = 0)만 본다. 그룹 범위(All)는 거르지 않는다.
	if f.Entities != nil && !f.Entities.All {
		cond, a := f.Entities.Cond("")
		where = append(where, "("+cond+" OR entity_id = 0)")
		args = append(args, a...)
	}
	q := "SELECT " + cols + " FROM audit_logs"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Desc {
		q += " ORDER BY seq DESC"
	} else {
		q += " ORDER BY seq ASC"
	}
	return q, args
}

const anchorDisabledMsg = "외부 앵커가 설정되지 않아 외부 대조를 하지 않았습니다. 마지막 기록 삭제나 체인 전체 재계산은 이 검증으로 잡을 수 없습니다."

// Verify 는 체인 검증(verifyChain)에 외부 앵커 대조를 더한다. 앵커가 꺼져 있으면 Anchor.State 가
// disabled 이고 메시지가 그 사실을 알린다(조용히 통과로 보이지 않는다). 앵커 대조가 실패하면 OK 는 false 다.
func (s *Store) Verify(ctx context.Context) (audit.VerifyResult, error) {
	res, err := s.verifyChain(ctx)
	if err != nil {
		return res, err
	}
	res.ChainOK = res.OK
	if s.anchor == nil {
		res.Anchor = audit.AnchorStatus{State: audit.AnchorDisabled, Message: anchorDisabledMsg}
		return res, nil
	}
	if res.Anchor, err = s.anchor.Status(ctx); err != nil {
		return res, err
	}
	if res.Anchor.State.Failed() {
		res.OK = false
	}
	return res, nil
}

// verifyChain 은 처음부터 체인을 따라가며 시퀀스 연속성·직전 해시·내용 해시를 확인한다.
// 어긋나는 첫 지점의 Seq 와 이유를 돌려준다.
func (s *Store) verifyChain(ctx context.Context) (audit.VerifyResult, error) {
	return s.verifyChainOn(ctx, s.db)
}

// VerifyChainIn 은 주어진 질의 대상(보통 아직 커밋하지 않은 트랜잭션)에서 해시 체인만 검증한다(외부 앵커 대조 없음).
// SQLite→PG 이관 도구가 커밋 전에 대상 체인을 확인하려고 쓴다.
func (s *Store) VerifyChainIn(ctx context.Context, q contract.Tx) (audit.VerifyResult, error) {
	res, err := s.verifyChainOn(ctx, q)
	res.ChainOK = res.OK
	return res, err
}

func (s *Store) verifyChainOn(ctx context.Context, q contract.Tx) (audit.VerifyResult, error) {
	res := audit.VerifyResult{VerifiedAt: time.Now().UTC()}
	cols, hasEntity, err := s.colsFor(ctx, q)
	if err != nil {
		return res, err
	}
	// 이관 경계(B44 13.1.1): 이관 때 기록한 머리 seq 이하의 행은 법인 값이 해시에 없으므로 0 이어야 한다. 아니면 DB 를 직접 고친 것이다.
	epoch := int64(-1)
	if hasEntity {
		var v string
		switch err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'audit.entity_epoch_seq'`).Scan(&v); {
		case err == nil:
			if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil {
				epoch = n
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return res, err
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT `+cols+` FROM audit_logs ORDER BY seq ASC`)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	var prevHash string
	var prevSeq int64
	for rows.Next() {
		r, err := scanRecord(rows)
		res.Checked++
		if err != nil { // 형식이 깨진 행도 변조로 본다.
			res.BrokenSeq, res.BrokenWhy = r.Seq, "행 형식이 깨졌습니다: "+err.Error()
			return res, nil
		}
		why := ""
		switch {
		case r.Seq != prevSeq+1:
			why = fmt.Sprintf("시퀀스가 이어지지 않습니다(이전 %d, 현재 %d): 행이 삭제되었거나 끼워 넣어졌습니다", prevSeq, r.Seq)
		case r.PrevHash != prevHash:
			why = "직전 해시가 이전 레코드와 다릅니다"
		case epoch >= 0 && r.Seq <= epoch && r.EntityID != 0:
			// 법인 값이 0 이 아니면 해시 입력이 달라져 어차피 아래 해시 검사에도 걸린다. 여기서 먼저 걸러 원인을 구체적으로 알린다.
			why = "다법인 이관 전 기록의 법인 값이 바뀌었습니다(변조)"
		case audit.HashRecord(r) != r.Hash:
			why = "레코드 내용이 해시와 다릅니다(변조)"
		}
		if why != "" {
			res.BrokenSeq, res.BrokenWhy = r.Seq, why
			return res, nil
		}
		prevHash, prevSeq = r.Hash, r.Seq
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.OK = true
	res.HeadHash = prevHash
	return res, nil
}

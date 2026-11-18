// Package fake 는 계약 인터페이스의 가짜 구현이다.
// erp·ai 의 테스트는 platform 구현(SQLite·OIDC) 없이 이 패키지만으로 돌아야 한다.
// 가짜는 실제 구현과 같은 규칙(권한 검사, 감사 기록, 기록 실패 시 요청 실패)을 따른다.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Recorder 는 audit.Recorder·audit.Reader 의 메모리 구현이다. 실제와 같은 해시 체인을 만든다.
// 테스트는 Has/Records 로 "이 호출이 감사 기록을 남겼는가"를 검사하고,
// FailWith 로 "기록 실패 시 요청이 실패하는가"를 검사한다.
type Recorder struct {
	mu      sync.Mutex
	records []audit.Record
	failErr error
	now     func() time.Time
}

// NewRecorder 는 빈 기록기를 만든다.
func NewRecorder() *Recorder { return &Recorder{now: time.Now} }

// FailWith 는 이후 Record 호출이 err 로 실패하게 한다. nil 이면 해제.
func (r *Recorder) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failErr = err
}

// Record 는 audit.Recorder 를 구현한다. tx 는 무시한다.
func (r *Recorder) Record(ctx context.Context, _ contract.Tx, e audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failErr != nil {
		return fmt.Errorf("%w: %v", contract.ErrAuditFailed, r.failErr)
	}
	// 실제 기록기와 같은 입력 검증. 실제에서 거부될 기록은 가짜에서도 ErrAuditFailed 다.
	if err := audit.ValidateEntry(e); err != nil {
		return fmt.Errorf("%w: %v", contract.ErrAuditFailed, err)
	}
	rec := audit.Record{
		Seq:        int64(len(r.records) + 1),
		At:         r.now().UTC(),
		ActorID:    e.Actor.UserID,
		ActorRole:  e.Actor.Role,
		Channel:    e.Actor.Channel,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		Result:     e.Result,
		Diff:       e.Diff,
		Detail:     e.Detail,
		EntityID:   audit.EntityOf(ctx, e),
	}
	if n := len(r.records); n > 0 {
		rec.PrevHash = r.records[n-1].Hash
	}
	rec.Hash = HashRecord(rec)
	r.records = append(r.records, rec)
	return nil
}

// HashRecord 는 실제 기록기와 같은 체인 해시다(contract/audit.HashRecord).
func HashRecord(rec audit.Record) string { return audit.HashRecord(rec) }

// Records 는 기록 사본이다.
func (r *Recorder) Records() []audit.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Record(nil), r.records...)
}

// Has 는 조건에 맞는 기록이 있는지 본다. 빈 문자열 필드는 무시한다.
func (r *Recorder) Has(action string, tt audit.TargetType, result audit.Result) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if action != "" && rec.Action != action {
			continue
		}
		if tt != "" && rec.TargetType != tt {
			continue
		}
		if result != "" && rec.Result != result {
			continue
		}
		return true
	}
	return false
}

// Count 는 기록 수다.
func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// List 는 audit.Reader 를 구현한다(필터는 Action·TargetType·ActorID·Limit 만 지원).
func (r *Recorder) List(_ context.Context, f audit.Filter) ([]audit.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Record
	for _, rec := range r.records {
		if f.Action != "" && rec.Action != f.Action {
			continue
		}
		if f.TargetType != "" && rec.TargetType != f.TargetType {
			continue
		}
		if f.ActorID != 0 && rec.ActorID != f.ActorID {
			continue
		}
		if rec.Seq <= f.AfterSeq {
			continue
		}
		out = append(out, rec)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// Verify 는 audit.Reader 를 구현한다. 처음 어긋난 Seq 를 돌려준다.
func (r *Recorder) Verify(_ context.Context) (audit.VerifyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 가짜는 외부 앵커가 없다(Anchor.State = disabled).
	off := audit.AnchorStatus{State: audit.AnchorDisabled}
	res := audit.VerifyResult{OK: true, ChainOK: true, VerifiedAt: time.Now().UTC(), Anchor: off}
	prev := ""
	for _, rec := range r.records {
		res.Checked++
		if rec.PrevHash != prev {
			return audit.VerifyResult{Checked: res.Checked, BrokenSeq: rec.Seq, BrokenWhy: "직전 해시 불일치", VerifiedAt: res.VerifiedAt, Anchor: off}, nil
		}
		if HashRecord(rec) != rec.Hash {
			return audit.VerifyResult{Checked: res.Checked, BrokenSeq: rec.Seq, BrokenWhy: "내용 해시 불일치", VerifiedAt: res.VerifiedAt, Anchor: off}, nil
		}
		prev = rec.Hash
	}
	res.HeadHash = prev
	return res, nil
}

// Tamper 는 테스트용으로 한 행의 Detail 을 바꾼다(체인 검증이 그 지점을 짚는지 확인).
func (r *Recorder) Tamper(seq int64, detail string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.records {
		if r.records[i].Seq == seq {
			r.records[i].Detail = detail
			return nil
		}
	}
	return errors.New("해당 seq 가 없습니다")
}

// TxManager 는 contract.TxManager 의 가짜다. fn 에 SQL 이 불가능한 Tx 를 넘긴다.
// 가짜 서비스는 SQL 을 쓰지 않으므로 호출 수만 센다.
type TxManager struct {
	mu    sync.Mutex
	Calls int
}

type noSQLTx struct{ contract.Tx }

// WithTx 는 contract.TxManager 를 구현한다.
func (m *TxManager) WithTx(_ context.Context, fn func(tx contract.Tx) error) error {
	m.mu.Lock()
	m.Calls++
	m.mu.Unlock()
	return fn(noSQLTx{})
}

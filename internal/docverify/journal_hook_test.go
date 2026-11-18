package docverify_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B31: 판정 확정이 전표 훅을 같은 트랜잭션 안에서 한 번 부른다. 가짜 훅은 호출 인자와 트랜잭션 안에서 읽은 확정 상태를 기록한다.
type fakeHook struct {
	calls   []domain.Verdict
	inTxOK  []bool // 호출 시점에 같은 트랜잭션에서 확정 갱신이 보였는가
	outcome domain.JournalOutcome
	err     error
	db      *sql.DB
}

func (h *fakeHook) OnVerdictConfirmed(ctx context.Context, tx contract.Tx, v domain.Verdict) (domain.JournalOutcome, error) {
	h.calls = append(h.calls, v)
	var by sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT confirmed_by FROM document_verdicts WHERE id = ?`, v.ID).Scan(&by)
	h.inTxOK = append(h.inTxOK, by.Valid && by.Int64 != 0)
	return h.outcome, h.err
}

func confirmedBy(t *testing.T, e *env, id int64) (int64, string) {
	t.Helper()
	var by sql.NullInt64
	var out sql.NullString
	if err := e.db.QueryRow(`SELECT confirmed_by, confirmed_outcome FROM document_verdicts WHERE id = ?`, id).Scan(&by, &out); err != nil {
		t.Fatal(err)
	}
	return by.Int64, out.String
}

func TestConfirmCallsJournalHookInsideTheConfirmTransaction(t *testing.T) {
	e := newEnv(t)
	h := &fakeHook{outcome: domain.JournalOutcome{Created: true, JournalNo: "JV-2026-00001"}}
	e.svc.JournalHook = h
	v := e.verify(t, staff, boltSpec(), goodDoc)
	got, err := e.svc.Confirm(ctxAs(finance), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || !h.inTxOK[0] {
		t.Fatalf("훅은 확정 트랜잭션 안에서 한 번 불려야 함: calls=%d inTx=%v", len(h.calls), h.inTxOK)
	}
	c := h.calls[0]
	if c.ID != v.ID || c.ConfirmedBy != finance.UserID || c.ConfirmedOutcome != domain.OutcomeMatch || c.POID != v.POID || c.ReceiptID != v.ReceiptID || c.DocumentID != v.DocumentID {
		t.Fatalf("훅에 넘긴 판정: %+v", c)
	}
	// 확정 응답 형식은 훅이 있든 없든 같다(훅 결과는 응답에 싣지 않는다).
	e2 := newEnv(t)
	v2 := e2.verify(t, staff, boltSpec(), goodDoc)
	plain, err := e2.svc.Confirm(ctxAs(finance), v2.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch})
	if err != nil {
		t.Fatal(err)
	}
	norm := func(x domain.Verdict) domain.Verdict {
		x.ID, x.DocumentID, x.POID, x.ReceiptID = 0, 0, 0, 0
		x.CreatedAt = time.Time{}
		return x
	}
	g, p := norm(got), norm(plain)
	if !reflect.DeepEqual(g, p) {
		t.Fatalf("확정 응답이 훅 유무에 따라 다름:\n훅 %+v\n무 %+v", g, p)
	}
}

func TestConfirmWithQueuedOrAttachedOutcomeStillConfirms(t *testing.T) {
	for name, out := range map[string]domain.JournalOutcome{
		"전표 대기": {Queued: true, Reason: "규칙 계정 사용 불가: 상품"},
		"증빙 연결": {Attached: true},
	} {
		e := newEnv(t)
		e.svc.JournalHook = &fakeHook{outcome: out}
		v := e.verify(t, staff, boltSpec(), goodDoc)
		if _, err := e.svc.Confirm(ctxAs(finance), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
			t.Fatalf("%s: 확정은 성공해야 함: %v", name, err)
		}
		if by, oc := confirmedBy(t, e, v.ID); by != finance.UserID || oc != "match" {
			t.Fatalf("%s: 확정이 저장되어야 함: %d %s", name, by, oc)
		}
	}
}

func TestConfirmIsCancelledWhenHookFails(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("전표 대기 행을 쓰지 못함")
	e.svc.JournalHook = &fakeHook{err: boom}
	v := e.verify(t, staff, boltSpec(), goodDoc)
	if _, err := e.svc.Confirm(ctxAs(finance), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err == nil || !errors.Is(err, boom) {
		t.Fatalf("훅 오류는 확정 오류로 올라와야 함: %v", err)
	}
	// 확정 갱신이 취소된다(진짜 감사 기록기는 같은 트랜잭션이라 함께 취소된다. 가짜 기록기는 트랜잭션 밖이라 건수는 보지 않는다).
	if by, oc := confirmedBy(t, e, v.ID); by != 0 || oc != "" {
		t.Fatalf("확정이 취소되어야 함: by=%d out=%q", by, oc)
	}
	// 훅이 정상으로 돌아오면 다시 확정할 수 있다.
	e.svc.JournalHook = &fakeHook{}
	if _, err := e.svc.Confirm(ctxAs(finance), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
		t.Fatalf("재확정: %v", err)
	}
}

func TestConfirmNonMatchAlsoReachesHookAndNilHookIsNotCalled(t *testing.T) {
	// 일치가 아닌 확정(불일치·보류)도 훅에 넘어간다(훅이 전표 대기 행을 만든다). 판정은 훅이 하지 않고 확정 값만 전달한다.
	e := newEnv(t)
	h := &fakeHook{outcome: domain.JournalOutcome{Queued: true, Reason: "불일치 확정"}}
	e.svc.JournalHook = h
	v := e.verify(t, staff, boltSpec(), goodDoc)
	if _, err := e.svc.Confirm(ctxAs(finance), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMismatch, Comment: "거래처와 금액 협의 필요"}); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0].ConfirmedOutcome != domain.OutcomeMismatch {
		t.Fatalf("불일치 확정도 훅에 전달: %+v", h.calls)
	}
	// 훅이 없으면(nil) 아무것도 부르지 않고 기존과 같이 동작한다. 거부된 확정(권한·사유 없음·이미 확정)은 훅을 부르지 않는다.
	e2 := newEnv(t)
	h2 := &fakeHook{}
	e2.svc.JournalHook = h2
	v2 := e2.verify(t, staff, boltSpec(), goodDoc)
	if _, err := e2.svc.Confirm(ctxAs(staff), v2.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 확정 거부: %v", err)
	}
	if _, err := e2.svc.Confirm(ctxAs(finance), v2.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.svc.Confirm(ctxAs(finance), v2.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 확정: %v", err)
	}
	if len(h2.calls) != 1 {
		t.Fatalf("성공한 확정 한 번에만 훅이 불려야 함: %d", len(h2.calls))
	}
	e3 := newEnv(t) // JournalHook nil
	v3 := e3.verify(t, staff, boltSpec(), goodDoc)
	if _, err := e3.svc.Confirm(ctxAs(finance), v3.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
		t.Fatalf("훅 없는 확정: %v", err)
	}
}

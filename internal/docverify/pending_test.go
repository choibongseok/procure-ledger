package docverify_test

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 확정 대기 요약: 재무만 조회하고, 확정된 판정은 빠지며, 원문은 싣지 않는다.
func TestPendingConfirmationsCountsOnlyUnconfirmedForFinance(t *testing.T) {
	e := newEnv(t)
	v1 := e.verify(t, staff, boltSpec(), goodDoc)
	v2 := e.verify(t, staff, boltSpec(), goodDoc)
	if _, err := e.svc.Confirm(ctxAs(finance), v1.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
		t.Fatal(err)
	}
	total, list, err := e.svc.PendingConfirmations(ctxAs(finance), 5)
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != v2.ID {
		t.Fatalf("확정 대기 1건이어야 함: %d %+v %v", total, list, err)
	}
	if list[0].SourceText != "" {
		t.Fatal("요약에는 원문을 싣지 않는다")
	}
	if _, _, err := e.svc.PendingConfirmations(ctxAs(staff), 5); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무가 아니면 거부: %v", err)
	}
}

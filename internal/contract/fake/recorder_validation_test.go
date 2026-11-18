package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

// 가짜 기록기는 실제 기록기와 같은 입력 검증(audit.ValidateEntry)을 한다.
func TestFakeRecorderRejectsWhatRealRecorderRejects(t *testing.T) {
	ok := audit.Entry{
		Actor:  contract.Principal{UserID: 1, Role: contract.RoleStaff, Channel: contract.ChannelWeb},
		Action: "item.create", TargetType: audit.TargetItem, TargetID: "1", Result: audit.ResultSuccess,
	}
	bad := map[string]func(e *audit.Entry){
		"빈 TargetID":  func(e *audit.Entry) { e.TargetID = "" },
		"빈 Action":    func(e *audit.Entry) { e.Action = "" },
		"모르는 대상 종류":   func(e *audit.Entry) { e.TargetType = "nope" },
		"모르는 결과":      func(e *audit.Entry) { e.Result = "maybe" },
		"빈 Channel":   func(e *audit.Entry) { e.Actor.Channel = "" },
		"모르는 Channel": func(e *audit.Entry) { e.Actor.Channel = "fax" },
	}
	if err := fake.NewRecorder().Record(context.Background(), nil, ok); err != nil {
		t.Fatalf("정상 기록: %v", err)
	}
	for name, mut := range bad {
		r := fake.NewRecorder()
		e := ok
		mut(&e)
		if err := r.Record(context.Background(), nil, e); !errors.Is(err, contract.ErrAuditFailed) {
			t.Errorf("%s: ErrAuditFailed 를 기대했으나 %v", name, err)
		}
		if r.Count() != 0 {
			t.Errorf("%s: 거부된 기록이 남음", name)
		}
	}
}

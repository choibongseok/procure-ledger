package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// 실제 조립(SQLite 기록기)에서 권한 거부가 감사 로그에 남는다. 가짜 기록기는 통과시키지만 실제 기록기가
// 거부하는 입력(빈 대상 ID 등)으로 거부 기록이 조용히 사라지는 사고를 영역별로 하나씩 잡는다.
func TestDeniedAuditRecordedInRealAssembly(t *testing.T) {
	d, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	as := func(role contract.Role) context.Context {
		return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 77, Name: "x", Role: role, DepartmentID: 1, Channel: contract.ChannelWeb})
	}
	count := func(action string) int {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND result = 'denied'`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	cases := []struct {
		track, action string
		call          func() error
	}{
		{"t2 결재", "approval.decide", func() error {
			_, err := d.Approval.Decide(as(contract.RoleStaff), 1, domain.Decision{Approve: true})
			return err
		}},
		{"t2 구매요청", "purchase_request.create", func() error {
			_, err := d.Purchase.CreateDraft(as(contract.RoleAuditor), domain.DraftInput{Title: "x"})
			return err
		}},
		{"t3 증빙 판정", "document.verify", func() error {
			_, err := d.Documents.Verify(as(contract.RoleAuditor), domain.VerifyInput{POID: 1, Text: "x"})
			return err
		}},
	}
	for _, c := range cases {
		before := count(c.action)
		if err := c.call(); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s: 거부를 기대했으나 %v", c.track, err)
			continue
		}
		if count(c.action) != before+1 {
			t.Errorf("%s: 실제 조립에서 %q 거부 기록이 남지 않음", c.track, c.action)
		}
	}
}

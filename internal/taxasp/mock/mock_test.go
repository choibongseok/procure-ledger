package mock_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/taxasp"
	"github.com/choibongseok/procure-ledger/internal/taxasp/mock"
	"github.com/choibongseok/procure-ledger/internal/taxasp/taxasptest"
)

// 모의 구현이 적합성 키트를 모든 시나리오에서 통과한다(permanent 는 발행이 늘 반려되므로 키트의 성공 경로를 건너뛰는 별도 시험이 있다).
func TestMockPassesConformance(t *testing.T) {
	for _, mode := range []string{"none", "transient", "delayed"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			st := mock.NewStore()
			taxasptest.Conformance(t, taxasptest.Options{
				New:          func(*testing.T) taxasp.Provider { return mock.Factory(st)() },
				Config:       taxasp.Config{Provider: "mock", MockMode: mode, APIKey: "k-secret-do-not-leak"},
				SeedReceived: func(items []taxasp.Received) { st.Seed(items...) },
			})
		})
	}
}

func TestMockApprovalNumberIsDeterministic24Digits(t *testing.T) {
	run := func() string {
		p := mock.Factory(mock.NewStore())()
		_ = p.Configure(context.Background(), taxasp.Config{MockMode: "none"})
		inv := taxasp.Invoice{No: "TI-000001", TaxType: "taxable", IssueDate: "2026-11-09", Supplier: taxasp.Party{BizNo: "1234567890"}, Buyer: taxasp.Party{BizNo: "2208162517"}, SupplyTotal: 100, TaxTotal: 10, Total: 110}
		r, err := p.Issue(context.Background(), taxasp.IssueRequest{IdempotencyKey: "k", Invoice: inv})
		if err != nil {
			t.Fatal(err)
		}
		s, err := p.Status(context.Background(), r.Ref)
		if err != nil {
			t.Fatal(err)
		}
		return s.ApprovalNo
	}
	a, b := run(), run()
	if a != b || len(a) != 24 || !strings.HasPrefix(a, "20261109") || strings.Trim(a, "0123456789") != "" {
		t.Fatalf("결정적인 24자리 숫자: %q %q", a, b)
	}
}

func TestMockScenarios(t *testing.T) {
	ctx := context.Background()
	inv := taxasp.Invoice{No: "TI-000002", TaxType: "taxable", IssueDate: "2026-11-09", Supplier: taxasp.Party{BizNo: "1234567890"}, Buyer: taxasp.Party{BizNo: "2208162517"}, SupplyTotal: 100, TaxTotal: 10, Total: 110}
	make1 := func(mode string) (taxasp.Provider, *mock.Store) {
		st := mock.NewStore()
		p := mock.Factory(st)()
		if err := p.Configure(ctx, taxasp.Config{MockMode: mode}); err != nil {
			t.Fatal(err)
		}
		return p, st
	}
	// permanent: 늘 영구 오류.
	p, _ := make1("permanent")
	if _, err := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "k", Invoice: inv}); err == nil || taxasp.AsError(err).Retryable || taxasp.AsError(err).Code != "rejected_validation" {
		t.Fatalf("permanent: %v", err)
	}
	// transient: 처음 두 번은 일시 오류, 세 번째에 성공, 이후 같은 참조.
	p, _ = make1("transient")
	for i := 0; i < 2; i++ {
		if _, err := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "k", Invoice: inv}); err == nil || !taxasp.AsError(err).Retryable {
			t.Fatalf("transient %d: %v", i, err)
		}
	}
	r, err := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "k", Invoice: inv})
	if err != nil || r.Ref == "" {
		t.Fatalf("transient 성공: %v", err)
	}
	// delayed: 처음 두 번의 조회는 pending.
	p, _ = make1("delayed")
	r, _ = p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "k", Invoice: inv})
	for i := 0; i < 2; i++ {
		if s, _ := p.Status(ctx, r.Ref); s.Kind != taxasp.StatusPending {
			t.Fatalf("delayed %d: %+v", i, s)
		}
	}
	if s, _ := p.Status(ctx, r.Ref); s.Kind != taxasp.StatusAccepted {
		t.Fatalf("delayed 수신: %+v", s)
	}
	// conflict: 두 번째 조회부터 같은 승인번호가 다른 금액으로 온다.
	p, st := make1("conflict")
	st.Seed(taxasp.Received{ApprovalNo: strings.Repeat("C", 24), Invoice: taxasp.Invoice{IssueDate: "2026-11-05", SupplyTotal: 100, Total: 110, TaxTotal: 10}})
	first, _ := p.ListReceived(ctx, taxasp.ListQuery{From: "2026-11-01", To: "2026-11-30"})
	second, _ := p.ListReceived(ctx, taxasp.ListQuery{From: "2026-11-01", To: "2026-11-30"})
	if len(first.Items) != 1 || len(second.Items) != 1 || first.Items[0].Invoice.SupplyTotal == second.Items[0].Invoice.SupplyTotal {
		t.Fatalf("conflict: %+v %+v", first, second)
	}
	// 모르는 시나리오·형식이 틀린 요청.
	if err := mock.Factory(mock.NewStore())().Configure(ctx, taxasp.Config{MockMode: "bogus"}); err == nil {
		t.Fatal("알 수 없는 시나리오는 설정 오류")
	}
}

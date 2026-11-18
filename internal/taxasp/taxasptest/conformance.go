// Package taxasptest 는 대행사(ASP) 어댑터 적합성 시험 키트다(B43 D2, 명세 8.1).
// 어떤 어댑터든 통과해야 하는 최소 공통 계약을 고정한다. 모의 구현이 이 키트를 통과하는 것이 이 저장소의 시험이고, 회사가 만든 어댑터는 자기 저장소의 시험에서
// Conformance 를 불러 같은 계약을 확인한다.
package taxasptest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/taxasp"
)

// Options 는 키트 설정이다.
type Options struct {
	// New 는 설정 전의 어댑터를 만든다.
	New func(t *testing.T) taxasp.Provider
	// Config 는 어댑터에 건넬 설정이다. APIKey 는 오류·문장에 새면 안 되는 값으로 채운다.
	Config taxasp.Config
	// SeedReceived 는 수신 목록에 나올 가상 수신 건을 어댑터 쪽에 심는다(ListReceived 를 지원하는 어댑터만).
	SeedReceived func(items []taxasp.Received)
}

func sampleInvoice(no string) taxasp.Invoice {
	return taxasp.Invoice{No: no, TaxType: "taxable", IssueDate: "2026-11-09", ChargeType: "claim",
		Supplier:    taxasp.Party{BizNo: "1234567890", Name: "공급자 주식회사", Rep: "홍"},
		Buyer:       taxasp.Party{BizNo: "2208162517", Name: "받는자 상사", Rep: "김"},
		Lines:       []taxasp.Line{{Name: "복사용지", Qty: 10, UnitPrice: 10000, Supply: 100000, Tax: 10000}},
		SupplyTotal: 100000, TaxTotal: 10000, Total: 110000}
}

// Conformance 는 어댑터가 지켜야 할 계약을 시험한다.
func Conformance(t *testing.T, o Options) {
	t.Helper()
	ctx := context.Background()
	newP := func(t *testing.T) taxasp.Provider {
		t.Helper()
		p := o.New(t)
		if err := p.Configure(ctx, o.Config); err != nil {
			t.Fatalf("설정: %v", err)
		}
		return p
	}

	t.Run("이름과 지원 범위", func(t *testing.T) {
		p := o.New(t)
		if strings.TrimSpace(p.Name()) == "" {
			t.Fatal("Name 이 비었다")
		}
		_ = p.Capabilities()
	})

	t.Run("발행은 멱등이다", func(t *testing.T) {
		p := newP(t)
		var a, b taxasp.IssueResult
		var err error
		// 일시 오류가 나는 어댑터는 재시도해서 성공할 때까지 부른다(같은 멱등 키).
		call := func(key string) taxasp.IssueResult {
			for i := 0; i < 5; i++ {
				r, e := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: key, Invoice: sampleInvoice("TI-000001")})
				if e == nil {
					return r
				}
				if ae := taxasp.AsError(e); !ae.Retryable {
					t.Fatalf("영구 오류: %v", e)
				}
				err = e
			}
			t.Fatalf("재시도해도 성공하지 못함: %v", err)
			return taxasp.IssueResult{}
		}
		a = call("key-1")
		b = call("key-1")
		if a.Ref == "" || a.Ref != b.Ref {
			t.Fatalf("같은 멱등 키는 같은 참조여야 한다: %q %q", a.Ref, b.Ref)
		}
		c := call("key-2")
		if c.Ref == a.Ref {
			t.Fatal("다른 멱등 키는 다른 참조여야 한다")
		}
	})

	t.Run("접수 결과는 승인번호 24자리이고 건마다 유일하다", func(t *testing.T) {
		p := newP(t)
		if !p.Capabilities().StatusPoll {
			t.Skip("접수 결과 조회를 지원하지 않는다")
		}
		seen := map[string]bool{}
		for i, no := range []string{"TI-000010", "TI-000011"} {
			var ref string
			for try := 0; try < 5 && ref == ""; try++ {
				if r, err := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "k" + no, Invoice: sampleInvoice(no)}); err == nil {
					ref = r.Ref
				}
			}
			if ref == "" {
				t.Fatalf("%d번째 발행 실패", i)
			}
			var res taxasp.StatusResult
			for poll := 0; poll < 10; poll++ {
				var err error
				if res, err = p.Status(ctx, ref); err != nil {
					t.Fatalf("상태 조회: %v", err)
				}
				if res.Kind != taxasp.StatusPending {
					break
				}
			}
			if res.Kind != taxasp.StatusAccepted || len(res.ApprovalNo) != 24 || seen[res.ApprovalNo] {
				t.Fatalf("국세청 수신 결과: %+v (중복 %v)", res, seen[res.ApprovalNo])
			}
			seen[res.ApprovalNo] = true
		}
		if _, err := p.Status(ctx, "NO-SUCH-REF"); err == nil {
			t.Fatal("모르는 참조의 조회는 오류여야 한다")
		} else if ae := taxasp.AsError(err); ae.Code == "" {
			t.Fatalf("분류된 오류여야 한다: %v", err)
		}
	})

	t.Run("검증 오류는 영구 오류이고 키가 오류 문장에 없다", func(t *testing.T) {
		p := newP(t)
		bad := sampleInvoice("TI-000020")
		bad.Buyer.BizNo = "12" // 형식 오류
		_, err := p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "kbad", Invoice: bad})
		if err == nil {
			t.Fatal("형식이 틀린 요청은 거부되어야 한다")
		}
		ae := taxasp.AsError(err)
		if ae.Retryable || ae.Code == "" {
			t.Fatalf("영구 오류로 분류되어야 한다: %+v", ae)
		}
		if o.Config.APIKey != "" && (strings.Contains(err.Error(), o.Config.APIKey) || strings.Contains(ae.Error(), o.Config.APIKey)) {
			t.Fatal("오류 문장에 API 키가 있다")
		}
	})

	t.Run("취소된 컨텍스트를 존중한다", func(t *testing.T) {
		p := newP(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := p.Issue(cctx, taxasp.IssueRequest{IdempotencyKey: "kc", Invoice: sampleInvoice("TI-000030")}); err == nil {
			t.Fatal("취소된 컨텍스트에서 성공하면 안 된다")
		}
	})

	t.Run("수신 목록: 쪽 나누기와 겹친 기간 재조회", func(t *testing.T) {
		p := newP(t)
		if !p.Capabilities().ListReceived || o.SeedReceived == nil {
			t.Skip("수신 목록을 지원하지 않는다")
		}
		var seed []taxasp.Received
		for i, d := range []string{"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05"} {
			inv := sampleInvoice("X")
			inv.IssueDate = d
			seed = append(seed, taxasp.Received{ApprovalNo: strings.Repeat(string(rune('A'+i)), 24), Invoice: inv})
		}
		o.SeedReceived(seed)
		collect := func(from, to string) map[string]bool {
			got := map[string]bool{}
			q := taxasp.ListQuery{From: from, To: to, Limit: 2}
			for page := 0; page < 10; page++ {
				res, err := p.ListReceived(ctx, q)
				if err != nil {
					t.Fatalf("수신 목록: %v", err)
				}
				for _, it := range res.Items {
					if got[it.ApprovalNo] {
						t.Fatalf("한 번의 순회에서 같은 승인번호가 두 번 나왔다: %s", it.ApprovalNo)
					}
					got[it.ApprovalNo] = true
					if it.Invoice.IssueDate < from || it.Invoice.IssueDate > to {
						t.Fatalf("기간 밖의 건: %s", it.Invoice.IssueDate)
					}
				}
				if !res.HasMore {
					return got
				}
				q.Cursor = res.NextCursor
			}
			t.Fatal("쪽이 끝나지 않는다")
			return nil
		}
		all := collect("2026-11-01", "2026-11-05")
		if len(all) != 5 {
			t.Fatalf("5건을 모두 받아야 한다: %d", len(all))
		}
		overlap := collect("2026-11-03", "2026-11-05") // 겹친 기간 재조회: 같은 승인번호가 같은 값으로 다시 온다
		for ap := range overlap {
			if !all[ap] {
				t.Fatalf("겹친 기간에서 처음 보는 승인번호: %s", ap)
			}
		}
		if len(overlap) != 3 {
			t.Fatalf("겹친 기간 3건: %d", len(overlap))
		}
		if empty := collect("2027-01-01", "2027-01-31"); len(empty) != 0 {
			t.Fatal("빈 기간은 빈 목록이어야 한다")
		}
	})

	t.Run("호출이 오래 걸리지 않는다", func(t *testing.T) {
		p := newP(t)
		start := time.Now()
		_, _ = p.Issue(ctx, taxasp.IssueRequest{IdempotencyKey: "kt", Invoice: sampleInvoice("TI-000040")})
		if time.Since(start) > 5*time.Second {
			t.Fatal("발행 호출이 5초를 넘었다")
		}
	})
}

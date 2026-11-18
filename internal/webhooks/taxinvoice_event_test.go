package webhooks_test

import (
	"encoding/json"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
)

// B43: 세금계산서 상태 변경은 웹훅 이벤트로 나가고 본문의 api_url 이 읽기 API 의 한 건 조회(?id=)를 가리킨다.
func TestTaxInvoiceEventBodyPointsAtReadAPI(t *testing.T) {
	e := newEnv(t)
	e.sub("회계 연동", func(ci *webhooks.CreateInput) {
		ci.Events = []string{"tax_invoice.status_changed", "tax_invoice.status_changed:accepted"}
	})
	e.note(changes.ResTaxInvoice, 7, changes.OpStatusChanged, "accepted")
	if n := e.tick(); n != 1 {
		t.Fatalf("이벤트 하나가 나가야 한다: %d", n)
	}
	rs := e.recv.all()
	if len(rs) != 1 || rs[0].hdr.Get("X-PL-Event-Type") != "tax_invoice.status_changed" {
		t.Fatalf("요청: %+v", rs)
	}
	var m map[string]any
	if err := json.Unmarshal(rs[0].body, &m); err != nil {
		t.Fatal(err)
	}
	if m["resource"] != "tax_invoice" || m["status"] != "accepted" || m["api_url"] != "https://erp.example/api/v1/tax-invoices?id=7" {
		t.Fatalf("본문: %s", rs[0].body)
	}
}

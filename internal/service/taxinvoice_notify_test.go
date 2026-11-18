package service_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// AC-4317: 전송 실패는 발행 권한자에게 같은 트랜잭션에서 알림이 가고, 본문에 번호·금액·상대방·사업자번호·사유 코드가 없다.
func TestTaxInvoiceFailureNotifiesIssuersWithoutContent(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	ctx := context.Background()
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (?, 'taxinvoice:issue', 1, 1)`, uFinance); err != nil {
		t.Fatal(err)
	}
	v := e.taxDraft(t, 123_456)
	if _, err := e.tax.Issue(as(uFinance2), v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	e.tax.ClaimDue(ctx, e.now, 10)
	if err := e.tax.Fail(ctx, v.ID, "rejected_validation", 422); err != nil {
		t.Fatal(err)
	}
	var kind, title, body, link string
	var uid int64
	if err := e.db.QueryRow(`SELECT user_id, kind, title, body, link FROM notifications WHERE kind = 'tax_invoice_failed'`).Scan(&uid, &kind, &title, &body, &link); err != nil {
		t.Fatalf("알림이 없음: %v", err)
	}
	if uid != uFinance || link != "/finance/tax-invoices" {
		t.Fatalf("받는 사람·링크: %d %s", uid, link)
	}
	for _, bad := range []string{v.No, "123456", "123,456", "rejected_validation", "한빛", "220-81"} {
		if strings.Contains(title+body, bad) {
			t.Fatalf("알림에 %q: %s %s", bad, title, body)
		}
	}
}

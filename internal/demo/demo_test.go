package demo_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/demo"
)

func build(t *testing.T) (deps.Deps, func()) {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	d, closeDB, err := bootstrap.Build(context.Background(), config.Load())
	if err != nil {
		t.Fatal(err)
	}
	return d, closeDB
}

func scalar(t *testing.T, d deps.Deps, q string, args ...any) string {
	t.Helper()
	var v any
	if err := d.DB.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}

func counts(t *testing.T, d deps.Deps) string {
	var parts []string
	for _, tbl := range []string{"users", "vendors", "items", "purchase_requests", "purchase_orders", "receipts", "stock_ledger", "issue_slips", "journals", "journal_lines", "payables", "audit_logs", "org_units"} {
		parts = append(parts, tbl+"="+scalar(t, d, `SELECT COUNT(*) FROM `+tbl))
	}
	return strings.Join(parts, " ")
}

// 데모 데이터: 서비스 경로로 만들어져 감사 체인·원장·전표 차대·채무가 정합하고, 데모 사용자는 로그인 자격이 없으며, 결정적이고, 한 번만 들어간다.
func TestSeedBuildsConsistentDemoSet(t *testing.T) {
	d, closeDB := build(t)
	defer closeDB()
	ctx := context.Background()
	if err := demo.Seed(ctx, d); err != nil {
		t.Fatalf("데모 데이터: %v", err)
	}
	// 감사 체인.
	res, err := d.AuditReader.Verify(ctx)
	if err != nil || !res.ChainOK || res.Checked < 20 {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
	// 전표: 차대 합이 같고 승인됐으며 채무가 생겼다.
	if got := scalar(t, d, `SELECT COUNT(*) FROM (SELECT journal_id FROM journal_lines GROUP BY journal_id HAVING SUM(CASE side WHEN 'D' THEN amount ELSE -amount END) <> 0) x`); got != "0" {
		t.Errorf("차대 합이 어긋난 전표 %s건", got)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM journals WHERE status = 'approved'`); got == "0" {
		t.Error("승인된 전표가 없음")
	}
	if got := scalar(t, d, `SELECT COALESCE(SUM(amount), 0) FROM payables`); got != "3000000" {
		t.Errorf("채무 합 %s(기대 3000000)", got)
	}
	// 재고 = 원장 합: 모니터 10 입고 − 2 출고 = 8, 복사용지 30, 토너 5(로트).
	for sku, want := range map[string]string{"DEMO-MON27": "8", "DEMO-PAPER": "30", "DEMO-TONER": "5"} {
		if got := scalar(t, d, `SELECT COALESCE(SUM(l.qty_delta), 0) FROM stock_ledger l JOIN items i ON i.id = l.item_id WHERE i.sku = ?`, sku); got != want {
			t.Errorf("%s 재고 %s(기대 %s)", sku, got, want)
		}
	}
	if scalar(t, d, `SELECT COALESCE(SUM(on_hand), 0) FROM v_wh_lot_stock`) != scalar(t, d, `SELECT COALESCE(SUM(qty_delta), 0) FROM stock_ledger`) {
		t.Error("재고 합계와 원장 합이 다름")
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM v_wh_lot_stock WHERE on_hand < 0`); got != "0" {
		t.Errorf("음수 재고 %s줄", got)
	}
	// 구매 흐름 상태: 발주 3건(입고 완료 2·부분 입고 1), 결재 중 1, 반려 1, 임시저장 1.
	for status, want := range map[string]string{"ordered": "3", "in_review": "1", "rejected": "1", "draft": "1"} {
		if got := scalar(t, d, `SELECT COUNT(*) FROM purchase_requests WHERE status = ?`, status); got != want {
			t.Errorf("구매요청 상태 %s: %s건(기대 %s)", status, got, want)
		}
	}
	// 데모 사용자: 로그인 자격 없음(비밀번호·외부 연결 없음), 개발 시드와 섞이지 않음.
	if got := scalar(t, d, `SELECT COUNT(*) FROM user_credentials`); got != "0" {
		t.Errorf("비밀번호 자격이 %s건 만들어짐", got)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM user_identities`); got != "0" {
		t.Errorf("외부 연결이 %s건 만들어짐", got)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM users WHERE email NOT LIKE '%@demo.invalid'`); got != "0" {
		t.Errorf("데모가 아닌 이메일 사용자 %s명", got)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM users WHERE email LIKE '%@pl.test'`); got != "0" {
		t.Errorf("개발 시드 사용자(@pl.test) %s명", got)
	}
	// 한 번만: 두 번째는 거부한다.
	if err := demo.Seed(ctx, d); err == nil {
		t.Error("데모 데이터가 두 번 들어감")
	}
}

// 같은 입력(같은 시계)이면 같은 데이터: 두 개의 새 DB 에 넣은 결과의 표별 행 수가 같다.
func TestSeedIsDeterministic(t *testing.T) {
	var got []string
	for i := 0; i < 2; i++ {
		d, closeDB := build(t)
		if err := demo.Seed(context.Background(), d); err != nil {
			closeDB()
			t.Fatalf("데모 데이터 %d: %v", i, err)
		}
		got = append(got, counts(t, d))
		closeDB()
	}
	if got[0] != got[1] {
		t.Fatalf("결정적이지 않음:\n%s\n%s", got[0], got[1])
	}
}

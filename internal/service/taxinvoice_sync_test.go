package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/taxasp"
	"github.com/choibongseok/procure-ledger/internal/taxasp/mock"
)

// B43 2단계(ai): 수신 동기화 작업이 실제 서비스(반영·충돌 기록·상태·감사) 위에서 모의 대행사와 도는지 본다.

const syncBuyerBiz = "1234567890" // taxCompany 의 회사 사업자번호

// aspOwner 는 대행사 연동을 켠 사람(관리자)을 심는다: tax_asp_settings.updated_by. 자동 실행의 작성자 칸이 된다.
func (e *env) aspOwner(t *testing.T, userID int64) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO tax_asp_settings (id, provider, secret_sealed, active, updated_by, updated_at) VALUES (1, 'mock', '', 1, ?, '2026-11-01T00:00:00Z')
		ON CONFLICT (id) DO UPDATE SET updated_by = excluded.updated_by`, userID); err != nil {
		t.Fatal(err)
	}
}

func (e *env) syncer(mode string, st *mock.Store) *taxasp.Syncer {
	reg := taxasp.NewRegistry()
	reg.Register("mock", mock.Factory(st))
	src := taxasp.Static{Cfg: taxasp.Config{Provider: "mock", MockMode: mode}, On: true}
	return &taxasp.Syncer{Store: e.tax, Settings: src, Registry: reg, Now: func() time.Time { return e.now }}
}

var syncSeq int

func (e *env) recv(daysAgo int, supplierBiz string, supply int64) taxasp.Received {
	syncSeq++
	d := e.now.In(contract.KST).AddDate(0, 0, -daysAgo).Format("2006-01-02")
	return taxasp.Received{ApprovalNo: fmt.Sprintf("%s%08d%08d", d[:4]+d[5:7]+d[8:], 11110000+syncSeq, syncSeq), Invoice: taxasp.Invoice{TaxType: "taxable", IssueDate: d,
		Supplier: taxasp.Party{BizNo: supplierBiz, Name: "수신상사", Rep: "대표"}, Buyer: taxasp.Party{BizNo: syncBuyerBiz, Name: "에이엑스 주식회사"}, SupplyTotal: supply, TaxTotal: supply / 10, Total: supply + supply/10, ChargeType: "claim"}}
}

func TestTaxSyncInsertsDuplicatesAndClassifies(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	ctx := context.Background()
	st := mock.NewStore()
	bad := e.recv(1, "5555555555", 10_000)
	bad.ApprovalNo = "SHORT" // 형식 오류
	other := e.recv(1, "5555555555", 10_000)
	other.Invoice.Buyer.BizNo = "9999999999" // 우리 회사가 아님
	st.Seed(e.recv(1, "5555555555", 10_000), e.recv(2, "5555555555", 20_000), bad, other)
	s := e.syncer("none", st)
	res, c, err := s.RunOnce(ctx)
	if err != nil || res != "ok" || c.Inserted != 2 || c.Invalid != 1 || c.NotOurs != 1 {
		t.Fatalf("첫 동기화: %s %+v %v", res, c, err)
	}
	page, _ := e.tax.List(as(uFinance), domain.TaxInvoiceFilter{Direction: domain.TaxPurchase})
	if page.Total != 2 || page.Rows[0].Status != domain.TaxAccepted || page.Rows[0].Source != domain.TaxSourceASP {
		t.Fatalf("수신 건: %+v", page)
	}
	// 같은 목록을 다시 읽으면 모두 건너뛴다(겹침 창).
	res, c, err = s.RunOnce(ctx)
	if err != nil || res != "ok" || c.Inserted != 0 || c.Same != 2 {
		t.Fatalf("두 번째: %s %+v %v", res, c, err)
	}
	status, _ := e.tax.SyncStatus(as(uFinance))
	if status.LastResult != "ok" || status.LastCounts.Same != 2 {
		t.Fatalf("상태: %+v", status)
	}
	if _, err := e.tax.SyncStatus(as(uStaff)); err == nil {
		t.Fatal("직원은 동기화 상태를 볼 수 없다")
	}
	if !e.rec.Has("taxinvoice.sync", "tax_invoice", "success") {
		t.Error("동기화 감사")
	}
}

func TestTaxSyncConflictKeepsExistingRow(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	ctx := context.Background()
	st := mock.NewStore()
	item := e.recv(1, "5555555555", 10_000)
	st.Seed(item)
	s := e.syncer("conflict", st)
	if _, c, err := s.RunOnce(ctx); err != nil || c.Inserted != 1 { // 첫 조회는 원래 금액
		t.Fatalf("첫: %+v %v", c, err)
	}
	_, c, err := s.RunOnce(ctx) // 둘째 조회부터 같은 승인번호가 다른 금액
	if err != nil || c.Conflict != 1 || c.Inserted != 0 {
		t.Fatalf("충돌: %+v %v", c, err)
	}
	page, _ := e.tax.List(as(uFinance), domain.TaxInvoiceFilter{Direction: domain.TaxPurchase})
	if page.Total != 1 || page.Rows[0].SupplyTotal != 10_000 {
		t.Fatalf("기존 행은 덮어쓰지 않는다: %+v", page.Rows)
	}
	if !e.rec.Has("taxinvoice.sync_conflict", "tax_invoice", "success") {
		t.Error("충돌 감사")
	}
}

func TestTaxSyncOverlapWindowAndPaging(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	ctx := context.Background()
	st := mock.NewStore()
	st.Seed(e.recv(1, "5555555555", 1_000), e.recv(1, "5555555555", 2_000), e.recv(1, "5555555555", 3_000), e.recv(1, "5555555555", 4_000), e.recv(1, "5555555555", 5_000))
	s := e.syncer("none", st)
	s.PageLimit, s.MaxPages = 2, 1
	res, c, err := s.RunOnce(ctx)
	if err != nil || res != "partial" || c.Inserted != 2 {
		t.Fatalf("한 쪽만: %s %+v %v", res, c, err)
	}
	if status, _ := e.tax.SyncStatus(as(uFinance)); status.LastDone != "" {
		t.Fatalf("끝까지 읽지 못했으면 마지막 완료일을 올리지 않는다: %+v", status)
	}
	s.MaxPages = 10
	res, c, err = s.RunOnce(ctx) // 같은 창을 커서부터 이어서 읽는다
	if err != nil || res != "ok" || c.Inserted != 3 {
		t.Fatalf("이어서: %s %+v %v", res, c, err)
	}
	today := e.now.In(contract.KST).Format("2006-01-02")
	if status, _ := e.tax.SyncStatus(as(uFinance)); status.LastDone != today {
		t.Fatalf("마지막 완료일: %+v", status)
	}
	// 다음 날: 어제 날짜(마지막 완료일 - 1일)의 늦게 올라온 건이 겹침 창(3일)에 걸려 들어온다. 5일 전 건은 창 밖이라 안 들어온다.
	e.now = e.now.Add(24 * time.Hour)
	late, outside := e.recv(2, "5555555555", 7_000), e.recv(8, "5555555555", 8_000)
	st.Seed(late, outside)
	res, c, err = s.RunOnce(ctx)
	if err != nil || res != "ok" || c.Inserted != 1 {
		t.Fatalf("겹침 창: %s %+v %v", res, c, err)
	}
}

func TestTaxSyncResultCodes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := mock.NewStore()
	// 회사 사업자번호가 없으면 no_company.
	if res, _, err := e.syncer("none", st).RunOnce(ctx); err != nil || res != "no_company" {
		t.Fatalf("회사 없음: %s %v", res, err)
	}
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	// 설정이 꺼져 있으면 not_configured.
	reg := taxasp.NewRegistry()
	reg.Register("mock", mock.Factory(st))
	off := &taxasp.Syncer{Store: e.tax, Settings: taxasp.Static{On: false}, Registry: reg, Now: func() time.Time { return e.now }}
	if res, _, err := off.RunOnce(ctx); err != nil || res != "not_configured" {
		t.Fatalf("설정 없음: %s %v", res, err)
	}
	// 복구 리허설에서는 아무 호출도 하지 않는다.
	rh := e.syncer("none", st)
	rh.Rehearsal = true
	if res, _, _ := rh.RunOnce(ctx); res != "rehearsal" {
		t.Fatalf("리허설: %s", res)
	}
	// 잘못된 설정(auth 오류)이면 failed 로 남기고 다음 실행에서 다시 한다.
	if res, _, err := e.syncer("badmode", st).RunOnce(ctx); err == nil && res == "ok" {
		t.Fatalf("잘못된 설정이 성공으로 끝났다: %s", res)
	}
	// 수동 실행 권한은 taxinvoice:write.
	if err := e.tax.AuthorizeSync(as(uStaff)); err == nil {
		t.Fatal("직원은 수동 동기화를 할 수 없다")
	}
	if err := e.tax.AuthorizeSync(as(uFinance)); err != nil {
		t.Fatalf("재무: %v", err)
	}
}

// 수신 동기화가 만든 건이 대조 제안으로 이어지는 흐름은 대조 시험(TestTaxMatchSkipsAmendmentsAndSyncRegistersProposal)이 본다.
func TestTaxSyncDemoSeedMatchesDemoVendors(t *testing.T) {
	rs := mock.DemoReceived(syncBuyerBiz, time.Date(2026, 11, 13, 0, 0, 0, 0, contract.KST))
	if len(rs) != 5 {
		t.Fatalf("시연 수신 건 수: %d", len(rs))
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if len(r.ApprovalNo) != 24 || seen[r.ApprovalNo] {
			t.Fatalf("승인번호 24자리·유일: %q", r.ApprovalNo)
		}
		seen[r.ApprovalNo] = true
		if r.Invoice.Buyer.BizNo != syncBuyerBiz {
			t.Fatal("공급받는자는 우리 회사")
		}
	}
}

// 자동 실행으로 들어온 건의 작성자는 대행사 연동을 켠 사람이다. 수동 실행은 실행한 사람이다. 그 사람이 비활성이면 동기화를 멈추고 알린다.
func TestTaxSyncOwnerIsActivatorAndStopsWhenInactive(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	st := mock.NewStore()
	st.Seed(e.recv(1, "5555555555", 10_000), e.recv(2, "5555555555", 20_000))
	s := e.syncer("none", st)
	if res, c, err := s.RunOnce(context.Background()); err != nil || res != "ok" || c.Inserted != 2 {
		t.Fatalf("자동 실행: %s %+v %v", res, c, err)
	}
	var wrong int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoices WHERE direction = 'purchase' AND created_by <> ?`, uAdmin).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("자동 실행의 작성자는 연동을 켠 관리자여야 한다: 다른 작성자 %d건 %v", wrong, err)
	}
	// 수동 실행은 실행한 사람이 작성자다.
	st.Seed(e.recv(3, "5555555555", 30_000))
	if res, c, err := s.RunOnce(as(uFinance)); err != nil || res != "ok" || c.Inserted != 1 {
		t.Fatalf("수동 실행: %s %+v %v", res, c, err)
	}
	var by int64
	if err := e.db.QueryRow(`SELECT created_by FROM tax_invoices WHERE supply_total = 30000`).Scan(&by); err != nil || by != uFinance {
		t.Fatalf("수동 실행 작성자: %d %v", by, err)
	}
	// 감사 기록(taxasp.activate)이 있으면 그 행위자가 우선한다.
	if _, err := e.db.Exec(`INSERT INTO audit_logs (seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash) VALUES (900001, '2026-11-02T00:00:00Z', ?, 'admin', 'web', 'taxasp.activate', 'tax_asp', '1', 'success', '[]', '', '', 'h-test-1')`, uFinance); err != nil {
		t.Fatal(err)
	}
	st.Seed(e.recv(1, "5555555555", 40_000))
	if _, _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT created_by FROM tax_invoices WHERE supply_total = 40000`).Scan(&by); err != nil || by != uFinance {
		t.Fatalf("감사 행위자 우선: %d %v", by, err)
	}
	// 연동을 켠 사람이 비활성이면 자동 동기화를 멈추고(새 건 없음) 알림을 남긴다. 수동 실행은 영향이 없다.
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, uFinance); err != nil {
		t.Fatal(err)
	}
	st.Seed(e.recv(1, "5555555555", 50_000))
	res, c, err := s.RunOnce(context.Background())
	if err != nil || res != "owner_inactive" || c.Inserted != 0 {
		t.Fatalf("정지: %s %+v %v", res, c, err)
	}
	var cnt int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoices WHERE supply_total = 50000`).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("정지 중에는 새 건을 넣지 않는다: %d %v", cnt, err)
	}
	var notes int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'tax_invoice_sync'`).Scan(&notes); err != nil || notes == 0 {
		t.Fatalf("정지 알림: %d %v", notes, err)
	}
	if status, _ := e.tax.SyncStatus(as(uAdmin)); status.LastResult != "owner_inactive" {
		t.Fatalf("상태: %+v", status)
	}
}

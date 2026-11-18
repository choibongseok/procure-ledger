package service_test

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 주기 알림 이벤트(B36 단계 3): 지급 예정·월 마감 점검. 시계는 env.now(UTC)이고 KST = UTC+9.

func periodic(t *testing.T, e *env) *service.PeriodicNotifier {
	t.Helper()
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: nil, Now: func() time.Time { return e.now }}
	return service.NewPeriodicNotifier(b, service.NewMonthCloseService(b))
}

// kst 는 KST 시각을 env 시계(UTC)로 맞춘다.
func (e *env) atKST(day string, h, m int) {
	t, _ := time.Parse("2006-01-02", day)
	e.now = t.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute - 9*time.Hour)
}

func (e *env) execNoFK(t *testing.T, queries ...string) {
	t.Helper()
	if dbtest.PG() { // PG: 이 트랜잭션에서만 외래키·트리거 검사를 끈다
		tx, err := e.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		for _, q := range queries {
			if _, err := tx.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := e.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	defer e.db.Exec(`PRAGMA foreign_keys = ON`)
	for _, q := range queries {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func (e *env) addPayable(t *testing.T, id int64, due string, amount int64) {
	e.execNoFK(t, `INSERT INTO vendors (id, name) VALUES (1, '시험 거래처') ON CONFLICT DO NOTHING`, fmt.Sprintf(`INSERT INTO payables (id, vendor_id, journal_id, line_id, trade_date, due_date, amount) VALUES (%d, 1, 1, %d, '2026-10-01', '%s', %d)`, id, id, due, amount))
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPaymentDueNotifiesPayersOncePerDayWithinBusinessDays(t *testing.T) {
	e := newEnv(t)
	n := periodic(t, e)
	ctx := context.Background()
	// 2026-11-11(수). 3영업일 뒤는 11-16(월): 목 12, 금 13, 월 16.
	e.addPayable(t, 1, "2026-11-13", 5000000)
	e.addPayable(t, 2, "2026-11-16", 1000000) // 범위 끝(포함)
	e.addPayable(t, 3, "2026-11-17", 2000000) // 범위 밖
	e.addPayable(t, 4, "2026-11-12", 3000000)
	e.atKST("2026-11-11", 8, 59)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due'`) != 0 {
		t.Fatal("업무 시작(09:00) 전에는 만들지 않는다")
	}
	e.atKST("2026-11-11", 9, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// 지급 권한자(재무 2명)에게만, 범위 안 3건. 관리자·직원·감사에게는 없다.
	for _, u := range []int64{uFinance, uFinance2} {
		var body, link string
		if err := e.db.QueryRow(`SELECT body, link FROM notifications WHERE kind = 'payment_due' AND user_id = ?`, u).Scan(&body, &link); err != nil {
			t.Fatalf("재무 %d: %v", u, err)
		}
		if !strings.Contains(body, "11월 16일까지") || !strings.Contains(body, "3건") || link != "/finance/payments" {
			t.Errorf("본문·링크: %q %q", body, link)
		}
		if strings.Contains(body, "원") || strings.Contains(body, "5,000,000") {
			t.Errorf("금액은 알림에 싣지 않는다: %q", body)
		}
	}
	for _, u := range []int64{uAdmin, uStaff, uAuditor, uHead} {
		if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due' AND user_id = ?`, u) != 0 {
			t.Errorf("지급 권한이 없는 사용자 %d 에게 알림이 갔다", u)
		}
	}
	// 같은 날 다시 돌려도 사람당 한 건.
	e.atKST("2026-11-11", 15, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due'`); got != 2 {
		t.Errorf("하루 한 건씩: %d", got)
	}
}

func TestPaymentDueSkipsHolidaysAndMergesIntoNextBusinessDay(t *testing.T) {
	e := newEnv(t)
	n := periodic(t, e)
	ctx := context.Background()
	e.addPayable(t, 1, "2026-11-13", 1000000)
	// 2026-11-14(토)·15(일) 주말, 16(월)은 공휴일로 등록.
	e.execNoFK(t, `INSERT INTO holidays (date, name, created_at) VALUES ('2026-11-16', '임시 공휴일', '2026-11-01T00:00:00Z')`)
	for _, day := range []string{"2026-11-14", "2026-11-15", "2026-11-16"} {
		e.atKST(day, 10, 0)
		if err := n.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// 만기 11-13 은 이미 지났다(오늘 ≥ 만기)이므로 범위(오늘~)에 없다 → 이 시험은 휴일에 만들지 않음만 본다.
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due'`) != 0 {
		t.Fatal("휴일·주말에는 새로 만들지 않는다")
	}
	e.addPayable(t, 2, "2026-11-18", 1000000) // 17일(화) 기준 18일(수)은 범위 안
	e.atKST("2026-11-17", 9, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due' AND user_id = ?`, uFinance); got != 1 {
		t.Errorf("다음 영업일 첫 실행에서 한 건: %d", got)
	}
}

func TestPaymentDueRespectsSettingAndNothingDue(t *testing.T) {
	e := newEnv(t)
	n := periodic(t, e)
	ctx := context.Background()
	e.atKST("2026-11-11", 10, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due'`) != 0 {
		t.Fatal("만기가 다가온 채무가 없으면 알림이 없다")
	}
	e.addPayable(t, 1, "2026-11-13", 1000000) // 수요일 기준 3영업일째(금)
	if _, err := e.db.Exec(`UPDATE app_settings SET value = '1' WHERE key = 'notify.payment_due_bd'`); err != nil {
		t.Fatal(err)
	}
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due'`) != 0 {
		t.Fatal("범위를 1영업일로 줄이면 금요일 만기는 범위 밖이다")
	}
	if _, err := e.db.Exec(`UPDATE app_settings SET value = '3' WHERE key = 'notify.payment_due_bd'`); err != nil {
		t.Fatal(err)
	}
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'payment_due' AND user_id = ?`, uFinance) != 1 {
		t.Fatal("설정을 3영업일로 돌리면 알림이 생긴다")
	}
}

func (e *env) seedClosePrereqs(t *testing.T) {
	// 10월에 재고 기록과 임시 전표가 있어 11월 초에 10월 마감 점검에 경고가 나온다.
	e.execNoFK(t,
		`INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, created_at, warehouse_id) VALUES (999, 1, 'adjust', 1, '2026-10-05T01:00:00Z', 1)`,
		`INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at) VALUES ('JV-2026-90001', 'general', 'draft', '2026-10-20', '2026-10-20', 1, '2026-10-20T01:00:00Z')`)
}

func TestMonthCloseCheckStartsOnNthBusinessDayAndRepeatsByInterval(t *testing.T) {
	e := newEnv(t)
	n := periodic(t, e)
	ctx := context.Background()
	e.seedClosePrereqs(t)
	got := func() int { return e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'month_close_check'`) }
	// 11월 영업일: 2일(월) 1째, 3일(화) 2째, 4일(수) 3째.
	e.atKST("2026-11-03", 10, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got() != 0 {
		t.Fatal("이번 달 3영업일째 전에는 알리지 않는다")
	}
	e.atKST("2026-11-04", 10, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got() != 2 || e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'month_close_check' AND user_id IN (?, ?)`, uFinance, uFinance2) != 2 {
		t.Fatalf("3영업일째에 마감 권한자(재무)에게: %d", got())
	}
	var body, link string
	_ = e.db.QueryRow(`SELECT body, link FROM notifications WHERE kind = 'month_close_check' AND user_id = ?`, uFinance).Scan(&body, &link)
	if !strings.Contains(body, "경고 1건") || !strings.Contains(body, "차단 0건") || link != "/inventory/close" {
		t.Errorf("본문·링크: %q %q", body, link)
	}
	// 7일 안에는 다시 만들지 않고, 7일째에 다시 만든다.
	for _, day := range []string{"2026-11-05", "2026-11-10"} {
		e.atKST(day, 10, 0)
		if err := n.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got() != 2 {
		t.Fatalf("주기(7일) 안에는 다시 만들지 않는다: %d", got())
	}
	e.atKST("2026-11-11", 10, 0)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got() != 4 {
		t.Fatalf("7일 뒤에 다시 한 건씩: %d", got())
	}
}

func TestMonthCloseCheckSkipsWhenClosedOrNothingToCheck(t *testing.T) {
	e := newEnv(t)
	n := periodic(t, e)
	ctx := context.Background()
	e.atKST("2026-11-04", 10, 0)
	// 재고 기록이 없으면 마감 대상이 아니다.
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'month_close_check'`) != 0 {
		t.Fatal("재고 기록이 없는 달은 알리지 않는다")
	}
	// 기록은 있고 점검 항목이 없으면 알리지 않는다.
	e.execNoFK(t, `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, created_at, warehouse_id) VALUES (999, 1, 'adjust', 1, '2026-10-05T01:00:00Z', 1)`)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'month_close_check'`) != 0 {
		t.Fatal("차단·경고가 없으면 알리지 않는다")
	}
	// 경고가 있어도 이미 마감된 달이면 알리지 않는다.
	e.execNoFK(t,
		`INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at) VALUES ('JV-2026-90002', 'general', 'draft', '2026-10-20', '2026-10-20', 1, '2026-10-20T01:00:00Z')`,
		`INSERT INTO month_closes (month, status, version, closed_by, closed_at) VALUES ('2026-10', 'closed', 1, 4, '2026-11-02T01:00:00Z')`)
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'month_close_check'`) != 0 {
		t.Fatal("마감된 달은 알리지 않는다")
	}
}

func TestBusinessDayFuncUsesCompanyCalendar(t *testing.T) {
	e := newEnv(t)
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Now: func() time.Time { return e.now }}
	f := service.BusinessDayFunc(b)
	e.execNoFK(t, `INSERT INTO holidays (date, name, created_at) VALUES ('2026-11-18', '임시 공휴일', '2026-11-01T00:00:00Z')`)
	for day, want := range map[string]bool{"2026-11-13": true, "2026-11-14": false, "2026-11-15": false, "2026-11-16": true, "2026-11-18": false} {
		got, err := f(context.Background(), e.db, day)
		if err != nil || got != want {
			t.Errorf("%s: %v %v (want %v)", day, got, err, want)
		}
	}
}

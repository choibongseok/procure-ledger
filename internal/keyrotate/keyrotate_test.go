package keyrotate_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/keyrotate"
	"github.com/choibongseok/procure-ledger/migrations"
)

func keyOf(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Open(t)
	if !dbtest.PG() {
		if err := migrations.ApplyDialect(context.Background(), db, dbutil.DialectOf(db)); err != nil {
			t.Fatal(err)
		}
	}
	dbtest.FKOff(t, db) // 시험용 행을 직접 넣는다
	return db
}

type seeded struct{ vendor, bank, pay, hook, sub, asp, llm int64 }

const aspPlain = `{"endpoint":"https://api.asp.example.com/v1","api_key":"key_x","cert_pem":"-----BEGIN CERTIFICATE-----","password":"pw_x"}`

func seed(t *testing.T, db *sql.DB, c *acctsecret.Cipher) seeded {
	t.Helper()
	seal := func(s string) string {
		out, err := c.Seal(s)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	sealFor := func(s string) string {
		out, err := c.SealFor("notify-target", s)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var s seeded
	subSeal, err := c.SealFor("webhook-sub", `{"url":"https://hooks.example.com/in","secret":"whsec_x","prev":"whsec_old","prev_until":"2099-01-01T00:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	s.vendor = dbtest.ExecID(t, db, `INSERT INTO vendors (name, business_no, account_no) VALUES ('한빛상사', '123-45-67890', ?)`, seal("110-123-456789"))
	s.bank = dbtest.ExecID(t, db, `INSERT INTO bank_accounts (alias, bank_name, account_no, account_id, opening_date, created_at, account_no_enc) VALUES ('주거래', '가나은행', '***6789', 1, '2026-01-01', '2026-01-01T00:00:00Z', ?)`, seal("110-999-123456"))
	// 승인된 지급 요청: 받는 계좌 고정 트리거가 걸려 있어 일반 갱신으로는 바꿀 수 없다.
	s.pay = dbtest.ExecID(t, db, `INSERT INTO payment_requests (no, vendor_id, status, total, due_date, bank_account_id, payee_account_masked, payee_account_enc, created_by, created_at)
		VALUES ('PAY-1', ?, 'approved', 1000, '2026-12-01', ?, '***6789', ?, 1, '2026-11-01T00:00:00Z')`, s.vendor, s.bank, seal("110-123-456789"))
	s.hook = dbtest.ExecID(t, db, `INSERT INTO notification_targets (name, type, secret_sealed, created_at, updated_at) VALUES ('결재 채널', 'slack', ?, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, sealFor(`{"url":"https://hooks.slack.com/services/T0/B0/X"}`))
	s.sub = dbtest.ExecID(t, db, `INSERT INTO webhook_subscriptions (name, secret_sealed, created_at, updated_at) VALUES ('회계 연동', ?, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, subSeal)
	aspSeal, err := c.SealFor("tax-asp", aspPlain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tax_asp_settings (id, provider, secret_sealed, active, updated_at) VALUES (1, 'mock', ?, 1, '2026-11-01T00:00:00Z')`, aspSeal); err != nil {
		t.Fatal(err)
	}
	s.asp = 1
	// 법인별 대행사 설정(B44): 다른 법인의 행도 같은 키로 봉해져 있고 함께 교체된다.
	if _, err := db.Exec(`INSERT INTO entities (id, code, name, is_default, active, created_at, updated_at) VALUES (2, 'ABC', '계열사', 0, 1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tax_asp_settings (entity_id, provider, secret_sealed, active, updated_at) VALUES (2, 'mock', ?, 1, '2026-11-01T00:00:00Z')`, aspSeal); err != nil {
		t.Fatal(err)
	}
	llmSeal, err := c.SealFor("llm-provider", `{"base_url":"https://llm.example.com","api_key":"sk-test"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO llm_providers (code, kind, name, secret_sealed, created_at, updated_at) VALUES ('vllm-hq', 'openai_compat', '사내 모델', ?, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, llmSeal); err != nil {
		t.Fatal(err)
	}
	s.llm = 1
	return s
}

func col(t *testing.T, db *sql.DB, tbl, c string, id int64) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT `+c+` FROM `+tbl+` WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// AC-1850: 계좌 키 교체 뒤 모든 봉한 값이 새 키로만 풀린다. 보호 트리거(승인된 지급 요청의 받는 계좌 고정)는 교체가 끝나면 다시 켜져 있다.
func TestRotateAccountResealsEverySealedColumn(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	oldK, newK := keyOf(1), keyOf(2)
	oldC, _ := acctsecret.New(oldK)
	newC, _ := acctsecret.New(newK)
	s := seed(t, db, oldC)
	before := col(t, db, "vendors", "account_no", s.vendor)

	res, err := keyrotate.RotateAccount(ctx, db, oldK, newK)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 8 || len(res.Resealed) != len(keyrotate.AccountColumns) {
		t.Fatalf("건수: %+v", res)
	}
	for _, c := range keyrotate.AccountColumns {
		want := 1
		if c.Table == "tax_asp_settings" {
			want = 2 // 법인 2개
		}
		if res.Resealed[c.Table+"."+c.Column] != want {
			t.Errorf("%s.%s: %d건", c.Table, c.Column, res.Resealed[c.Table+"."+c.Column])
		}
	}
	if after := col(t, db, "vendors", "account_no", s.vendor); after == before {
		t.Fatal("값이 다시 봉해지지 않음")
	}
	cases := []struct {
		tbl, c, purpose, want string
		id                    int64
	}{
		{"vendors", "account_no", "", "110-123-456789", s.vendor},
		{"bank_accounts", "account_no_enc", "", "110-999-123456", s.bank},
		{"payment_requests", "payee_account_enc", "", "110-123-456789", s.pay},
		{"notification_targets", "secret_sealed", "notify-target", `{"url":"https://hooks.slack.com/services/T0/B0/X"}`, s.hook},
		{"webhook_subscriptions", "secret_sealed", "webhook-sub", `{"url":"https://hooks.example.com/in","secret":"whsec_x","prev":"whsec_old","prev_until":"2099-01-01T00:00:00Z"}`, s.sub},
		{"tax_asp_settings", "secret_sealed", "tax-asp", aspPlain, s.asp},
		{"llm_providers", "secret_sealed", "llm-provider", `{"base_url":"https://llm.example.com","api_key":"sk-test"}`, s.llm},
	}
	for _, c := range cases {
		v := col(t, db, c.tbl, c.c, c.id)
		open := func(ci *acctsecret.Cipher) (string, error) {
			if c.purpose == "" {
				return ci.Open(v)
			}
			return ci.OpenFor(c.purpose, v)
		}
		if got, err := open(newC); err != nil || got != c.want {
			t.Errorf("%s.%s 새 키: %q %v", c.tbl, c.c, got, err)
		}
		if _, err := open(oldC); err == nil {
			t.Errorf("%s.%s 옛 키로도 풀림", c.tbl, c.c)
		}
	}
	if err := keyrotate.VerifyAccount(ctx, db, oldK, newK); err != nil {
		t.Fatal(err)
	}
	// 보호 트리거가 다시 켜져 있다: 승인된 지급 요청의 받는 계좌를 일반 갱신으로 바꾸면 거부된다(PG 시험 연결은 트리거를 끈 세션이라 SQLite 에서만 본다).
	if !dbtest.PG() {
		if _, err := db.Exec(`UPDATE payment_requests SET payee_account_enc = 'enc:v1:abc' WHERE id = ?`, s.pay); err == nil {
			t.Fatal("교체 뒤에 보호 트리거가 꺼진 채로 남음")
		}
	}
}

// 옛 키가 틀리면 전체가 되돌려진다(일부만 바뀐 상태가 남지 않는다).
func TestRotateAccountWrongOldKeyChangesNothing(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	oldC, _ := acctsecret.New(keyOf(1))
	s := seed(t, db, oldC)
	snap := map[string]string{
		"v": col(t, db, "vendors", "account_no", s.vendor), "b": col(t, db, "bank_accounts", "account_no_enc", s.bank),
		"p": col(t, db, "payment_requests", "payee_account_enc", s.pay), "h": col(t, db, "notification_targets", "secret_sealed", s.hook),
		"w": col(t, db, "webhook_subscriptions", "secret_sealed", s.sub), "a": col(t, db, "tax_asp_settings", "secret_sealed", s.asp),
	}
	_, err := keyrotate.RotateAccount(ctx, db, keyOf(9), keyOf(2))
	if err == nil || !strings.Contains(err.Error(), "옛 키로 풀 수 없습니다") {
		t.Fatalf("틀린 옛 키: %v", err)
	}
	if strings.Contains(err.Error(), "110-123") || strings.Contains(err.Error(), "enc:v1:") {
		t.Fatalf("오류에 값이 있음: %v", err)
	}
	if col(t, db, "vendors", "account_no", s.vendor) != snap["v"] || col(t, db, "bank_accounts", "account_no_enc", s.bank) != snap["b"] ||
		col(t, db, "payment_requests", "payee_account_enc", s.pay) != snap["p"] || col(t, db, "notification_targets", "secret_sealed", s.hook) != snap["h"] || col(t, db, "webhook_subscriptions", "secret_sealed", s.sub) != snap["w"] || col(t, db, "tax_asp_settings", "secret_sealed", s.asp) != snap["a"] {
		t.Fatal("실패한 교체가 값을 바꿈")
	}
	if !dbtest.PG() {
		if _, err := db.Exec(`UPDATE payment_requests SET payee_account_enc = 'enc:v1:abc' WHERE id = ?`, s.pay); err == nil {
			t.Fatal("실패 뒤 보호 트리거가 사라짐")
		}
	}
}

// 소스 검사: 키로 봉하는 코드가 있는 파일 목록이 이 시험이 아는 목록과 다르면 실패한다. 새 봉인 열을 만들면 keyrotate.AccountColumns 에 더하고 이 목록도 고친다.
func TestSealingSitesMatchRotationColumns(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	var sites []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, "acctsecret/") || strings.HasPrefix(rel, "keyrotate/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, ".SealFor(") || strings.Contains(s, "SealAccount(") || strings.Contains(s, "acctsecret.Cipher") {
			sites = append(sites, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sites)
	// vendors.account_no(import_repo·inventory_repo), payment_requests.payee_account_enc·bank_accounts.account_no_enc(payment_repo), 이관 함수(account_box), 알림 웹훅 주소(notify/targets), LLM 공급자 설정(llmgw/providers.go·adminprov.go), 변경 웹훅 구독(webhooks/subscriptions·webhooks), 전자세금계산서 대행사 설정(taxaspadmin)
	want := []string{"llmgw/adminprov.go", "llmgw/providers.go", "notify/targets.go", "repository/account_box.go", "repository/import_repo.go", "repository/inventory_repo.go", "repository/payment_repo.go", "taxaspadmin/taxaspadmin.go", "webhooks/subscriptions.go", "webhooks/webhooks.go"}
	got := strings.Join(sites, ",")
	if got != strings.Join(want, ",") {
		t.Fatalf("봉인 코드가 있는 파일이 바뀌었습니다: %s (기대 %s). 새 봉인 열이면 keyrotate.AccountColumns 에 더하세요", got, strings.Join(want, ","))
	}
	// 열 목록 자체도: 계좌 키로 봉한다고 알려진 열이 AccountColumns 에 있다.
	have := map[string]bool{}
	for _, c := range keyrotate.AccountColumns {
		have[c.Table+"."+c.Column] = true
	}
	for _, k := range []string{"vendors.account_no", "payment_requests.payee_account_enc", "bank_accounts.account_no_enc", "notification_targets.secret_sealed", "webhook_subscriptions.secret_sealed", "tax_asp_settings.secret_sealed"} {
		if !have[k] {
			t.Errorf("AccountColumns 에 %s 가 없음", k)
		}
	}
}

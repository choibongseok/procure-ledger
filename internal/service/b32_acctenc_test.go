package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// dumpAll 은 모든 표의 모든 칸을 문자열로 이어 붙인 DB 덤프다(감사·알림 포함).
func (e *env) dumpAll(t *testing.T) string {
	t.Helper()
	listTables := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
	if dbtest.PG() {
		listTables = `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`
	}
	tables, err := e.db.Query(listTables)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	var b strings.Builder
	for _, n := range names {
		rows, err := e.db.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					b.Write(x)
				default:
					fmt.Fprint(&b, x)
				}
				b.WriteByte('|')
			}
			b.WriteByte('\n')
		}
		rows.Close()
	}
	return b.String()
}

// D1 후속: 거래처·회사 계좌·지급 요청의 받는 계좌가 모두 암호문으로 저장되어 DB 덤프에 평문 계좌번호가 한 건도 없다. 화면은 마스킹, 원문 보기는 여전히 되고 감사가 남는다.
func TestAccountNumbersAreEncryptedInDatabaseDump(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t) // 거래처 계좌 110-123-456789
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {
		t.Fatal(err)
	}
	dump := e.dumpAll(t)
	for _, plain := range []string{"110-123-456789", "123-456-789012", "110-123", "456789", "123-456-", "789012"} {
		if strings.Contains(dump, plain) {
			t.Fatalf("DB 덤프에 평문 계좌번호 조각 %q 가 있다", plain)
		}
	}
	if !strings.Contains(dump, acctsecret.Prefix) {
		t.Fatal("암호문이 저장되지 않았다")
	}
	// 화면 값은 마스킹이고 원문 보기는 감사와 함께 된다.
	got, _ := e.payments.Get(as(uFinance), p.No)
	if got.PayeeAccount != "***-***-**6789" {
		t.Fatalf("마스킹: %q", got.PayeeAccount)
	}
	accs, _ := e.funds.Accounts(as(uFinance), false)
	if len(accs) != 1 || accs[0].AccountNo != "***-***-**9012" {
		t.Fatalf("계좌 마스킹: %+v", accs)
	}
	if raw, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account"); err != nil || raw != "123-456-789012" {
		t.Fatalf("회사 계좌 원문: %q %v", raw, err)
	}
	if raw, err := e.funds.RevealPayee(as(uFinance), p.No); err != nil || raw != "110-123-456789" {
		t.Fatalf("받는 계좌 원문: %q %v", raw, err)
	}
	// 거래처 목록은 마스킹(재무는 mask:bypass 라 원문)으로 풀려 나온다: 저장소가 풀어서 서비스에 준다.
	vs, err := e.inventory.ListVendors(as(uAuditor))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range vs {
		if strings.Contains(x.AccountNo, acctsecret.Prefix) {
			t.Fatalf("암호문이 응답에 나감: %q", x.AccountNo)
		}
	}
	// 계좌 변경 감지는 복호화한 값끼리 비교한다.
	if got.AccountDiffers {
		t.Fatal("같은 계좌인데 변경으로 감지됨")
	}
}

// 이관: 암호화 이전의 평문 행(거래처·회사 계좌·승인된 지급 요청의 받는 계좌)을 한 번에 봉하고, 다시 해도 아무것도 하지 않는다. 값은 그대로 풀린다.
func TestLegacyPlainAccountsAreMigratedOnce(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p) // 승인된(고정된) 지급 요청
	// 암호화 이전 상태를 되돌려 만든다: 평문으로 덮어쓴다(승인된 지급 요청은 "평문 → 암호문" 이관 갱신만 트리거가 허용한다).
	for _, q := range []string{
		`UPDATE vendors SET account_no = '110-123-456789' WHERE id = ` + fmt.Sprint(v.ID),
		`UPDATE bank_accounts SET account_no = '123-456-789012', account_no_enc = '' WHERE id = ` + fmt.Sprint(bank.ID),
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// 승인된 요청의 평문 복원은 트리거가 막으므로(암호문 → 평문 변경 거부) 직접 확인한다.
	if _, err := e.db.Exec(`UPDATE payment_requests SET payee_account_enc = '110-123-456789' WHERE no = ?`, p.No); err == nil {
		t.Fatal("승인 뒤 받는 계좌가 암호문 → 평문으로 바뀜")
	}
	if n, err := repository.PlainAccountCount(context.Background(), e.db); err != nil || n != 2 {
		t.Fatalf("평문 행 수 %d %v", n, err)
	}
	var moved int
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(context.Background(), func(tx contract.Tx) error {
		var err error
		moved, err = repository.MigrateAccountSecrets(context.Background(), tx)
		return err
	}); err != nil || moved != 2 {
		t.Fatalf("이관 %d %v", moved, err)
	}
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(context.Background(), func(tx contract.Tx) error {
		var err error
		moved, err = repository.MigrateAccountSecrets(context.Background(), tx)
		return err
	}); err != nil || moved != 0 {
		t.Fatalf("두 번째 이관은 아무것도 하지 않아야 함: %d %v", moved, err)
	}
	if strings.Contains(e.dumpAll(t), "123-456-789012") || strings.Contains(e.dumpAll(t), "110-123-456789") {
		t.Fatal("이관 뒤에도 평문이 남음")
	}
	if raw, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account"); err != nil || raw != "123-456-789012" {
		t.Fatalf("이관 뒤 원문: %q %v", raw, err)
	}
	if accs, _ := e.funds.Accounts(as(uFinance), false); accs[0].AccountNo != "***-***-**9012" {
		t.Fatalf("이관 뒤 마스킹: %+v", accs)
	}
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {
		t.Fatalf("이관 뒤 지급 처리(받는 계좌 변경 감지 포함): %v", err)
	}
}

// 키가 없으면 계좌번호를 새로 저장할 수 없다(거래처 등록·회사 계좌 등록·지급 요청 작성). 계좌번호가 없는 등록은 된다. 풀 수 없는 값은 빈 값으로 읽힌다.
func TestAccountKeyMissingRejectsStoringAccounts(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	repository.SetAccountCipher(nil)
	t.Cleanup(func() {
		if c, err := acctsecret.New([]byte("0123456789abcdef0123456789abcdef")); err == nil {
			repository.SetAccountCipher(c)
		}
	})
	if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "새거래처", BusinessNo: "999-99-99999", AccountNo: "222-333-444555"}); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "PL_ACCOUNT_SECRET_KEY") {
		t.Fatalf("계좌 있는 거래처 등록: %v", err)
	}
	if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "계좌없음상사", BusinessNo: "888-88-88888"}); err != nil {
		t.Fatalf("계좌 없는 거래처는 등록되어야 함: %v", err)
	}
	if _, err := e.funds.SaveAccount(as(uFinance), 0, domain.BankAccountInput{Alias: "새계좌", BankName: "x", AccountNo: "111-2222-3333", OpeningDate: "2026-01-01"}); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("회사 계좌 등록: %v", err)
	}
	if _, err := e.payments.Create(as(uFinance), e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 1000})); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("지급 요청 작성: %v", err)
	}
	// 이미 봉한 값은 키가 없으면 풀리지 않는다(빈 값, 오류 없음). 원문 보기도 빈 값이다.
	if raw, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account"); err == nil && raw != "" {
		t.Fatalf("키 없이 원문이 나옴: %q", raw)
	}
}

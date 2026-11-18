package repository

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 계좌번호 저장 암호화(B32 D1 후속). 계좌번호가 저장되는 곳은 vendors.account_no·bank_accounts.account_no_enc·payment_requests.payee_account_enc 셋이고
// 저장소가 쓸 때 SealAccount 로 봉하고 읽을 때 OpenAccount 로 푼다. 서비스·핸들러는 평문만 다룬다(응답은 마스킹, 원문은 원문 보기 감사 경로뿐).
// 키는 기동 때 한 번 SetAccountCipher 로 넣는다. 키가 없으면 계좌번호를 새로 저장할 수 없다(읽기는 이관 전 평문이면 그대로 읽는다).

var accountCipher atomic.Pointer[acctsecret.Cipher]

// SetAccountCipher 는 기동 때(또는 시험에서) 계좌번호 봉인기를 정한다. nil 이면 계좌번호 저장을 거부한다.
func SetAccountCipher(c *acctsecret.Cipher) { accountCipher.Store(c) }

// AccountKeyReady 는 계좌번호를 저장할 수 있는지(키가 있는지)다.
func AccountKeyReady() bool { return accountCipher.Load() != nil }

// ErrAccountKeyMissing 은 키 없이 계좌번호를 저장하려 할 때의 오류다.
var ErrAccountKeyMissing = fmt.Errorf("%w: 계좌번호 암호화 키(PL_ACCOUNT_SECRET_KEY)가 설정되지 않아 계좌번호를 저장할 수 없습니다. 관리자에게 알려 주세요", contract.ErrInvalidState)

// ErrAccountUnreadable 은 봉한 계좌번호를 풀 수 없을 때(키가 없거나 다르거나 값이 바뀜)의 오류다.
var ErrAccountUnreadable = fmt.Errorf("%w: 저장된 계좌번호를 풀 수 없습니다(암호화 키가 없거나 다릅니다). 관리자에게 알려 주세요", contract.ErrInvalidState)

// SealAccount 는 저장 전 평문 계좌번호를 봉한다(빈 값은 그대로).
func SealAccount(plain string) (string, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", nil
	}
	c := accountCipher.Load()
	if c == nil {
		return "", ErrAccountKeyMissing
	}
	return c.Seal(plain)
}

// OpenAccount 는 저장된 값을 평문으로 푼다. 이관 전 평문은 그대로, 풀 수 없으면(키 없음·키 다름·변조) 빈 값이다(원인은 로그에만 남기고 값은 남기지 않는다).
func OpenAccount(stored string) string {
	if !acctsecret.IsSealed(stored) {
		return strings.TrimSpace(stored)
	}
	c := accountCipher.Load()
	if c == nil {
		log.Printf("account: 계좌번호를 풀 수 없습니다(PL_ACCOUNT_SECRET_KEY 없음)")
		return ""
	}
	plain, err := c.Open(stored)
	if err != nil {
		log.Printf("account: %v", err)
		return ""
	}
	return plain
}

// MaskAccount 는 계좌번호를 끝 4자리만 보이게 가린다.
func MaskAccount(s string) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= 4 {
		return strings.Repeat("*", len(rs))
	}
	return "***-***-**" + string(rs[len(rs)-4:])
}

// PlainAccountCount 는 아직 봉하지 않은 평문 계좌번호 행 수다(키가 없을 때의 기동 경고용).
func PlainAccountCount(ctx context.Context, q contract.Tx) (int, error) {
	var a, b, c int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM vendors WHERE account_no <> '' AND account_no NOT LIKE 'enc:v1:%'`).Scan(&a); err != nil {
		return 0, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM bank_accounts WHERE `+contract.SQLEntity(ctx, "bank_accounts")+` AND account_no_enc = ''`).Scan(&b); err != nil {
		return 0, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_requests WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND payee_account_enc <> '' AND payee_account_enc NOT LIKE 'enc:v1:%'`).Scan(&c); err != nil {
		return 0, err
	}
	return a + b + c, nil
}

// MigrateAccountSecrets 는 기존 평문 계좌번호를 한 번에 봉한다(기동 때 1회, 멱등: 이미 봉한 행은 건드리지 않는다). 봉한 행 수를 돌려준다.
// 키가 없으면 아무것도 하지 않는다.
func MigrateAccountSecrets(ctx context.Context, q contract.Tx) (int, error) {
	if !AccountKeyReady() {
		return 0, nil
	}
	n := 0
	type row struct {
		id    int64
		plain string
	}
	collect := func(query string) ([]row, error) {
		rs, err := q.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		var out []row
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.id, &r.plain); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rs.Err()
	}
	vendors, err := collect(`SELECT id, account_no FROM vendors WHERE account_no <> '' AND account_no NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return 0, err
	}
	for _, r := range vendors {
		sealed, err := SealAccount(r.plain)
		if err != nil {
			return 0, err
		}
		// changes:exempt 계좌번호 봉인 이관: 값은 같고 저장 표현만 바뀌므로 변경 피드에 남기지 않는다(소비자가 볼 값이 변하지 않는다).
		if _, err := q.ExecContext(ctx, `UPDATE vendors SET account_no = ? WHERE id = ?`, sealed, r.id); err != nil {
			return 0, err
		}
		n++
	}
	banks, err := collect(`SELECT id, account_no FROM bank_accounts WHERE ` + contract.SQLEntity(ctx, "bank_accounts") + ` AND account_no_enc = ''`)
	if err != nil {
		return 0, err
	}
	for _, r := range banks {
		sealed, err := SealAccount(r.plain)
		if err != nil {
			return 0, err
		}
		if _, err := q.ExecContext(ctx, `UPDATE bank_accounts SET account_no_enc = ?, account_no = ? WHERE `+contract.SQLEntity(ctx, "bank_accounts")+` AND id = ?`, sealed, MaskAccount(r.plain), r.id); err != nil {
			return 0, err
		}
		n++
	}
	pays, err := collect(`SELECT id, payee_account_enc FROM payment_requests WHERE ` + contract.SQLEntity(ctx, "payment_requests") + ` AND payee_account_enc <> '' AND payee_account_enc NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return 0, err
	}
	for _, r := range pays {
		sealed, err := SealAccount(r.plain)
		if err != nil {
			return 0, err
		}
		if _, err := q.ExecContext(ctx, `UPDATE payment_requests SET payee_account_enc = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, sealed, r.id); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// Package keyrotate 는 비밀 키 교체(B38 단계 4, D8)다.
//
// 키를 바꿔도 이미 저장된 암호문을 다시 봉하면 재등록·재입력이 필요 없다.
//   - auth    : PL_AUTH_SECRET_KEY — TOTP 비밀키(user_totp). 사용자 전원이 2단계 인증을 다시 등록할 필요가 없어진다.
//   - account : PL_ACCOUNT_SECRET_KEY — 계좌번호 봉인(enc:v1:)과 같은 키를 쓰는 알림 웹훅 주소 봉인.
//
// anchor(PL_AUDIT_ANCHOR_KEY)는 검증기가 키 세대를 알아야 해서 후속이다(명세 8절).
// 한 트랜잭션으로 전부 다시 봉하고, 하나라도 풀지 못하면 전체를 되돌린다. 값은 어디에도 출력·기록하지 않는다.
package keyrotate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SealedColumn 은 PL_ACCOUNT_SECRET_KEY 로 봉한 값이 들어 있는 열이다. 새 봉인 열을 만들면 여기에 더해야 하고,
// 더하지 않으면 소스 검사(keyrotate_test.go)가 실패한다(키를 바꿀 때 그 열만 옛 키로 남는 사고를 막는다).
type SealedColumn struct {
	Table, Column string
	Purpose       string // "" 이면 Seal/Open(계좌번호), 아니면 SealFor/OpenFor 의 용도
	// GuardTrigger 는 봉한 값의 갱신을 막는 트리거 이름이다. 교체하는 동안만 끈다(트랜잭션 안이라 실패하면 함께 되돌아간다).
	GuardTrigger string
}

// AccountColumns 는 계좌 키로 봉한 열 전부다.
var AccountColumns = []SealedColumn{
	{Table: "vendors", Column: "account_no"},
	{Table: "payment_requests", Column: "payee_account_enc", GuardTrigger: "trg_payment_requests_fixed"},
	{Table: "bank_accounts", Column: "account_no_enc"},
	{Table: "notification_targets", Column: "secret_sealed", Purpose: "notify-target"},
	// 변경 웹훅 구독(B42): 주소+서명 비밀+교체 중인 옛 비밀을 한 덩어리로 봉한다. 알림 웹훅 대상과 같은 계좌 키이고 용도만 다르다.
	{Table: "webhook_subscriptions", Column: "secret_sealed", Purpose: "webhook-sub"},
	// 전자세금계산서 대행사 설정(B43): 엔드포인트·API 키·인증서·개인키·비밀번호를 한 덩어리로 봉한다. 빠지면 키 교체 뒤 대행사 연동이 조용히 멈춘다.
	{Table: "tax_asp_settings", Column: "secret_sealed", Purpose: "tax-asp"},
	// LLM 공급자 설정(B45): 주소·API 키·자격 증명을 한 덩어리로 봉한다. 빠지면 키 교체 뒤 라우팅된 공급자 호출이 조용히 멈춘다.
	{Table: "llm_providers", Column: "secret_sealed", Purpose: "llm-provider"},
}

// Result 는 교체 결과(건수만)다.
type Result struct {
	Resealed map[string]int // 열 이름("표.열") → 건수
	Total    int
}

func isPG(db *sql.DB) bool { return dbutil.DialectOf(db).Kind() == dbutil.KindPostgres }

// RotateAccount 는 계좌 키 oldKey 로 봉한 값을 모두 newKey 로 다시 봉한다.
func RotateAccount(ctx context.Context, db *sql.DB, oldKey, newKey []byte) (Result, error) {
	res := Result{Resealed: map[string]int{}}
	oldC, err := acctsecret.New(oldKey)
	if err != nil {
		return res, fmt.Errorf("옛 키: %w", err)
	}
	newC, err := acctsecret.New(newKey)
	if err != nil {
		return res, fmt.Errorf("새 키: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	pg := isPG(db)
	for _, c := range AccountColumns {
		n, err := rotateColumn(ctx, tx, pg, c, oldC, newC)
		if err != nil {
			return Result{Resealed: map[string]int{}}, err
		}
		res.Resealed[c.Table+"."+c.Column] = n
		res.Total += n
	}
	if err := tx.Commit(); err != nil {
		return Result{Resealed: map[string]int{}}, err
	}
	return res, nil
}

func rotateColumn(ctx context.Context, tx *sql.Tx, pg bool, c SealedColumn, oldC, newC *acctsecret.Cipher) (int, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT id, %s FROM %s WHERE %s LIKE 'enc:v1:%%' ORDER BY id`, c.Column, c.Table, c.Column))
	if err != nil {
		return 0, fmt.Errorf("%s.%s 읽기: %w", c.Table, c.Column, err)
	}
	type item struct {
		id     int64
		sealed string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.sealed); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	restore, err := disableGuard(ctx, tx, pg, c)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		var plain string
		if c.Purpose == "" {
			plain, err = oldC.Open(it.sealed)
		} else {
			plain, err = oldC.OpenFor(c.Purpose, it.sealed)
		}
		if err != nil {
			return 0, fmt.Errorf("%s.%s 의 %d번 행을 옛 키로 풀 수 없습니다(키가 다르거나 값이 바뀌었습니다)", c.Table, c.Column, it.id)
		}
		var sealed string
		if c.Purpose == "" {
			sealed, err = newC.Seal(plain)
		} else {
			sealed, err = newC.SealFor(c.Purpose, plain)
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ? WHERE id = ?`, c.Table, c.Column), sealed, it.id); err != nil {
			return 0, fmt.Errorf("%s.%s 의 %d번 행 갱신: %w", c.Table, c.Column, it.id, err)
		}
	}
	if err := restore(); err != nil {
		return 0, err
	}
	return len(items), nil
}

// disableGuard 는 열의 갱신 보호 트리거를 이 트랜잭션 안에서만 끈다. 돌려주는 함수가 다시 켠다(SQLite 는 같은 정의로 다시 만든다).
func disableGuard(ctx context.Context, tx *sql.Tx, pg bool, c SealedColumn) (func() error, error) {
	if c.GuardTrigger == "" {
		return func() error { return nil }, nil
	}
	if pg {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER %s`, c.Table, c.GuardTrigger)); err != nil {
			return nil, fmt.Errorf("보호 트리거를 끌 수 없습니다: %w", err)
		}
		return func() error {
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER %s`, c.Table, c.GuardTrigger))
			return err
		}, nil
	}
	var def string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, c.GuardTrigger).Scan(&def); err != nil {
		return nil, fmt.Errorf("보호 트리거 %s 를 찾을 수 없습니다: %w", c.GuardTrigger, err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+c.GuardTrigger); err != nil {
		return nil, err
	}
	return func() error {
		_, err := tx.ExecContext(ctx, def)
		return err
	}, nil
}

// RotateAuth 는 인증 키로 봉한 TOTP 비밀키를 모두 다시 봉한다.
func RotateAuth(ctx context.Context, db *sql.DB, oldKey, newKey []byte) (Result, error) {
	res := Result{Resealed: map[string]int{}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	n, err := auth.ReencryptTOTP(ctx, tx, oldKey, newKey)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Resealed["user_totp"] = n
	res.Total = n
	return res, nil
}

// VerifyAccount 는 교체 뒤 표본 점검이다: 새 키로 각 열의 첫 값들이 풀리고 옛 키로는 풀리지 않아야 한다.
func VerifyAccount(ctx context.Context, db *sql.DB, oldKey, newKey []byte) error {
	oldC, err := acctsecret.New(oldKey)
	if err != nil {
		return err
	}
	newC, err := acctsecret.New(newKey)
	if err != nil {
		return err
	}
	for _, c := range AccountColumns {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s LIKE 'enc:v1:%%' ORDER BY id LIMIT 5`, c.Column, c.Table, c.Column))
		if err != nil {
			return err
		}
		var vals []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			vals = append(vals, v)
		}
		rows.Close()
		for _, v := range vals {
			open := func(ci *acctsecret.Cipher) error {
				var err error
				if c.Purpose == "" {
					_, err = ci.Open(v)
				} else {
					_, err = ci.OpenFor(c.Purpose, v)
				}
				return err
			}
			if err := open(newC); err != nil {
				return fmt.Errorf("%s.%s: 새 키로 풀 수 없습니다", c.Table, c.Column)
			}
			if open(oldC) == nil {
				return errors.New(c.Table + "." + c.Column + ": 옛 키로도 풀립니다(교체가 적용되지 않았습니다)")
			}
		}
	}
	return nil
}

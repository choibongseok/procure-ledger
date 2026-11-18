package auth

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ReencryptTOTP 는 모든 TOTP 비밀키(user_totp.secret_enc·pending_secret_enc)를 옛 키로 풀어 새 키로 다시 봉한다(B38 키 교체, erp-admin rotate-key auth).
// 호출한 쪽이 트랜잭션 하나로 부르고, 하나라도 풀지 못하면 오류로 돌려 전체를 되돌린다(어느 사용자 번호인지만 알리고 값은 알리지 않는다).
// 봉인 aad 는 사용자 번호라서 행이 바뀌지 않았다면 새 키로도 같은 aad 로 봉한다. 돌려주는 값은 다시 봉한 비밀키 개수다.
func ReencryptTOTP(ctx context.Context, tx contract.Tx, oldKey, newKey []byte) (int, error) {
	oldBox, err := newSecretBox(oldKey)
	if err != nil {
		return 0, fmt.Errorf("옛 키: %w", err)
	}
	newBox, err := newSecretBox(newKey)
	if err != nil {
		return 0, fmt.Errorf("새 키: %w", err)
	}
	type row struct {
		id              int64
		secret, pending sql.NullString
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id, secret_enc, pending_secret_enc FROM user_totp ORDER BY user_id`)
	if err != nil {
		return 0, err
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.secret, &r.pending); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	reseal := func(id int64, enc string) (string, error) {
		plain, err := oldBox.open(enc, idStr(id))
		if err != nil {
			return "", fmt.Errorf("사용자 %d 의 비밀키를 옛 키로 풀 수 없습니다", id)
		}
		return newBox.seal(plain, idStr(id))
	}
	for _, r := range all {
		var secret, pending any
		if r.secret.Valid && r.secret.String != "" {
			s, err := reseal(r.id, r.secret.String)
			if err != nil {
				return 0, err
			}
			secret = s
			n++
		}
		if r.pending.Valid && r.pending.String != "" {
			s, err := reseal(r.id, r.pending.String)
			if err != nil {
				return 0, err
			}
			pending = s
			n++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_totp SET secret_enc = ?, pending_secret_enc = ? WHERE user_id = ?`, secret, pending, r.id); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// CanOpenTOTP 는 key 로 첫 활성 TOTP 비밀키를 풀 수 있는지 본다(키 교체 뒤 표본 점검). 비밀키가 하나도 없으면 (true, 0).
func CanOpenTOTP(ctx context.Context, q contract.Tx, key []byte) (ok bool, checked int, err error) {
	box, err := newSecretBox(key)
	if err != nil {
		return false, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT user_id, secret_enc FROM user_totp WHERE secret_enc IS NOT NULL AND secret_enc <> '' ORDER BY user_id LIMIT 5`)
	if err != nil {
		return false, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var enc string
		if err := rows.Scan(&id, &enc); err != nil {
			return false, checked, err
		}
		checked++
		if _, err := box.open(enc, idStr(id)); err != nil {
			return false, checked, nil
		}
	}
	return true, checked, rows.Err()
}

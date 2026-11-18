package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// CLIUnlock 은 서버 호스트에서 `server auth-unlock <이메일>` 로 돌리는 복구 명령이다(DB 파일 접근 권한이 곧 인증). 계정 잠금을 풀고
// 이메일 로그인이 꺼져 있으면 다시 켠다(외부 로그인·메타데이터 오류로 모두 잠겼을 때). 감사 경로는 cli 다(B38 D7, 0053 이전에는 web + by=cli). 한 일을 문장으로 돌려준다.
func (s *Service) CLIUnlock(ctx context.Context, email string) ([]string, error) {
	email = normEmail(email)
	var done []string
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ?`, email).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		} else if err != nil {
			return err
		}
		actor := contract.Principal{Role: "system", Channel: contract.ChannelCLI}
		res, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = 0, locked_until = NULL WHERE user_id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			done = append(done, "계정 잠금을 풀었습니다")
			if err := s.record(ctx, tx, actor, "auth.account_unlock", audit.TargetCredential, idStr(id), audit.ResultSuccess,
				[]audit.DiffItem{{Key: "by", New: "cli"}}, "서버 명령 auth-unlock"); err != nil {
				return err
			}
		} else {
			done = append(done, "이 계정에는 이메일 로그인 자격이 없어 풀 잠금이 없습니다")
		}
		pol, err := loadPolicy(ctx, tx)
		if err != nil {
			return err
		}
		if !pol.EmailLoginEnabled {
			pol.EmailLoginEnabled = true
			cj, _ := json.Marshal(pol)
			if _, err := tx.ExecContext(ctx, `INSERT INTO auth_policy (id, config_json, updated_at) VALUES (1,?,?)
				ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at`, string(cj), s.now().UTC().Format(timeLayout)); err != nil {
				return err
			}
			done = append(done, "꺼져 있던 이메일 로그인을 다시 켰습니다")
			return s.record(ctx, tx, actor, "auth.policy_update", audit.TargetAuthProvider, "policy",
				audit.ResultSuccess, []audit.DiffItem{{Key: "email_login", Old: "false", New: "true"}, {Key: "by", New: "cli"}}, "서버 명령 auth-unlock")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth-unlock: %w", err)
	}
	return done, nil
}

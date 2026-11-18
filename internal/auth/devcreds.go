package auth

import (
	"context"
	"fmt"
	"log"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// SeedDevPasswords 는 개발 시드 계정 중 이메일 로그인 자격이 아직 없는 계정에 같은 초기 비밀번호로 argon2id 자격을 만든다.
// 모든 계정에 "첫 로그인 때 변경 강제"(제한 세션 password_change)를 붙인다. 이미 자격이 있는 계정은 건드리지 않는다(멱등).
//
// 운영 구성에서는 쓰이지 않는다: 사용자 중에 개발 시드 도메인(@pl.test) 밖의 이메일이 하나라도 있으면 실제 데이터가 있는 DB 로 보고
// 경고 한 줄만 남기고 아무것도 만들지 않는다. 비밀번호 값은 로그·감사 어디에도 남기지 않는다(감사에는 계정 수만).
// 해시는 한 번만 계산해 모든 시드 계정이 같은 해시를 쓴다(수십 개 계정 때문에 첫 기동이 느려지지 않게. 개발 전용이다).
func (s *Service) SeedDevPasswords(ctx context.Context, password string) (int, error) {
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return 0, err
	}
	if err := pol.Password.Check(password, "", ""); err != nil {
		return 0, fmt.Errorf("비밀번호 정책 위반: %w", err)
	}
	var foreign int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(email) NOT LIKE '%@pl.test'`).Scan(&foreign); err != nil {
		return 0, err
	}
	if foreign > 0 {
		log.Printf("경고: PL_DEV_SEED_PASSWORD 가 설정되어 있지만 개발 시드 밖의 사용자가 있는 DB(운영 데이터로 판단)라 무시합니다")
		return 0, nil
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		now := s.now().UTC().Format(timeLayout)
		res, err := tx.ExecContext(ctx, `INSERT INTO user_credentials (user_id, password_hash, must_change, changed_at)
			SELECT u.id, ?, 1, ? FROM users u WHERE u.active = 1 AND NOT EXISTS (SELECT 1 FROM user_credentials c WHERE c.user_id = u.id)`, hash, now)
		if err != nil {
			return err
		}
		cnt, _ := res.RowsAffected()
		n = int(cnt)
		if n == 0 {
			return nil
		}
		return s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "auth.dev_seed_credentials", audit.TargetCredential, "dev-seed", audit.ResultSuccess,
			[]audit.DiffItem{{Key: "accounts", New: fmt.Sprint(n)}}, "개발 시드 계정(첫 로그인에서 비밀번호 변경 강제)")
	})
	return n, err
}

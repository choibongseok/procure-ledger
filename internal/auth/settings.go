package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

type settingsAdapter struct{ s *Service }

var _ contract.SettingsManager = settingsAdapter{}

// Settings 는 관리자 설정 저장소를 돌려준다.
func (s *Service) Settings() contract.SettingsManager { return settingsAdapter{s} }

// LLMImageTransmission 은 이미지 전송 허용 여부다. 행이 없거나 읽기에 실패하면 꺼짐이다.
func (a settingsAdapter) LLMImageTransmission(ctx context.Context) bool {
	var v string
	err := a.s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, contract.SettingLLMImageTransmission).Scan(&v)
	return err == nil && v == "true"
}

// SetLLMImageTransmission 은 설정을 바꾼다. 바뀌지 않았으면 기록하지 않는다.
func (a settingsAdapter) SetLLMImageTransmission(ctx context.Context, p contract.Principal, enabled bool) error {
	s := a.s
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermLLMPolicyManage); err != nil {
		return err
	}
	val := "false"
	if enabled {
		val = "true"
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		old := "false"
		err := tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, contract.SettingLLMImageTransmission).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old == val {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			contract.SettingLLMImageTransmission, val, p.UserID, s.now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "setting.update", audit.TargetSetting, contract.SettingLLMImageTransmission, audit.ResultSuccess,
			[]audit.DiffItem{{Key: contract.SettingLLMImageTransmission, Old: old, New: val}}, "")
	})
}

package aicost

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const settingExpenseAccount = "aicost.expense_account"

// AccountOption 은 비용 계정으로 고를 수 있는 계정이다(사용 중인 비용 계정).
type AccountOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Settings 는 AI 비용 배부 설정이다: 비용 전표의 차변 계정.
type Settings struct {
	ExpenseAccount string          `json:"expenseAccount"`
	Options        []AccountOption `json:"options"`
}

// GetSettings 는 현재 설정과 고를 수 있는 비용 계정이다(cost:read).
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	if _, err := contract.Authorize(ctx, contract.PermCostRead); err != nil {
		return Settings{}, err
	}
	return s.settings(ctx)
}

func (s *Service) settings(ctx context.Context) (Settings, error) {
	cur := s.expenseAccount(ctx) // 열린 조회가 없을 때 읽는다(연결 하나)
	rows, err := s.o.DB.QueryContext(ctx, `SELECT code, name FROM accounts WHERE kind = 'expense' AND active = 1 ORDER BY code`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	out := Settings{ExpenseAccount: cur, Options: []AccountOption{}}
	for rows.Next() {
		var a AccountOption
		if err := rows.Scan(&a.Code, &a.Name); err != nil {
			return Settings{}, err
		}
		out.Options = append(out.Options, a)
	}
	return out, rows.Err()
}

// SetExpenseAccount 는 비용 전표의 차변 계정을 정한다(cost:allocate). 사용 중인 비용 계정만 되고, 이미 만든 전표·확정 결과는 바뀌지 않는다.
func (s *Service) SetExpenseAccount(ctx context.Context, code string) error {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	cur, err := s.settings(ctx)
	if err != nil {
		return err
	}
	ok := false
	for _, o := range cur.Options {
		ok = ok || o.Code == code
	}
	if !ok {
		return valErr("사용 중인 비용 계정을 고르세요")
	}
	if code == cur.ExpenseAccount {
		return fmt.Errorf("%w: 이미 같은 계정입니다.", contract.ErrInvalidState)
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
			ON CONFLICT (key) DO UPDATE SET value = ?, updated_by = ?, updated_at = ?`, settingExpenseAccount, code, p.UserID, s.stamp(), code, p.UserID, s.stamp()); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.setting_set", TargetType: audit.TargetAICost, TargetID: "setting/expense_account", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "account_code", Old: cur.ExpenseAccount, New: code}}})
	})
}

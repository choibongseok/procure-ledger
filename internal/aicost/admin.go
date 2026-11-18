package aicost

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const (
	minFxX1000 = 100_000    // 100원
	maxFxX1000 = 10_000_000 // 10,000원
)

// SetFX 는 월 환율(USD→KRW, 값×1000 정수)을 정한다(cost:allocate). 다시 정하면 값이 바뀐다(감사에 이전 값이 남는다).
// 확정된 실행이 있는 달의 잠금은 실행 표가 생기는 3단계(0098)가 건다.
func (s *Service) SetFX(ctx context.Context, month string, usdKrwX1000 int64) error {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return err
	}
	if !reMonth.MatchString(month) {
		return valErr("월은 YYYY-MM 형식으로 입력하세요")
	}
	if month > s.currentMonth() {
		return valErr("앞으로 올 달의 환율은 입력할 수 없습니다")
	}
	if usdKrwX1000 < minFxX1000 || usdKrwX1000 > maxFxX1000 {
		return valErr("환율은 100원에서 10,000원 사이여야 합니다")
	}
	if locked, err := s.monthConfirmed(ctx, month); err != nil {
		return err
	} else if locked {
		return fmt.Errorf("%w: %s", contract.ErrInvalidState, msgLocked)
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var old int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT usd_krw_x1000 FROM ai_cost_fx WHERE month = ?), 0)`, month).Scan(&old); err != nil {
			return err
		}
		if old == usdKrwX1000 {
			return fmt.Errorf("%w: 이미 같은 환율입니다.", contract.ErrInvalidState)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_fx (month, usd_krw_x1000, set_by, set_at) VALUES (?,?,?,?)
			ON CONFLICT (month) DO UPDATE SET usd_krw_x1000 = ?, set_by = ?, set_at = ?`,
			month, usdKrwX1000, p.UserID, s.stamp(), usdKrwX1000, p.UserID, s.stamp()); err != nil {
			return err
		}
		diff := []audit.DiffItem{{Key: "month", New: month}}
		it := audit.DiffItem{Key: "usd_krw", New: strconv.FormatInt(usdKrwX1000, 10)}
		if old != 0 {
			it.Old = strconv.FormatInt(old, 10)
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.fx_set", TargetType: audit.TargetAICost, TargetID: month, Result: audit.ResultSuccess, Diff: append(diff, it)})
	})
}

// AddAdjustment 는 월·공급자별 실제 청구액(원)을 입력한다(cost:allocate). 같은 (월, 공급자)를 다시 입력하면 새 행이 쌓이고 최신이 쓰인다.
func (s *Service) AddAdjustment(ctx context.Context, month, provider string, actualKRW int64, note string) error {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return err
	}
	if !reMonth.MatchString(month) {
		return valErr("월은 YYYY-MM 형식으로 입력하세요")
	}
	if month > s.currentMonth() {
		return valErr("앞으로 올 달의 청구액은 입력할 수 없습니다")
	}
	provider = strings.TrimSpace(provider)
	if n := len([]rune(provider)); n < 1 || n > 40 {
		return valErr("공급자를 선택하세요")
	}
	if actualKRW < 0 || actualKRW > 100_000_000_000 {
		return valErr("청구액은 0원 이상 1,000억 원 이하의 정수여야 합니다")
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return valErr("메모는 200자 이내로 쓰세요")
	}
	if locked, err := s.monthConfirmed(ctx, month); err != nil {
		return err
	} else if locked {
		return fmt.Errorf("%w: %s", contract.ErrInvalidState, msgLocked)
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var old int64 = -1
		var prev int64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT actual_krw FROM ai_cost_adjustments WHERE month = ? AND provider_code = ? ORDER BY id DESC LIMIT 1), -1)`, month, provider).Scan(&prev)
		if err != nil {
			return err
		}
		old = prev
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_adjustments (month, provider_code, actual_krw, note, entered_by, entered_at) VALUES (?,?,?,?,?,?)`,
			month, provider, actualKRW, note, p.UserID, s.stamp()); err != nil {
			return err
		}
		it := audit.DiffItem{Key: "actual_krw", New: strconv.FormatInt(actualKRW, 10)}
		if old >= 0 {
			it.Old = strconv.FormatInt(old, 10)
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.adjust_set", TargetType: audit.TargetAICost, TargetID: month + "/" + provider, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: month}, {Key: "provider", New: provider}, it}})
	})
}

func (s *Service) stamp() string { return s.now().UTC().Format("2006-01-02T15:04:05Z") }

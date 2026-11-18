package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// BackfillValuation 은 평가 줄이 없는 기존 원장 행을 처음부터 재생해 이동평균 평가를 채운다(B27 D2: 기동 때 한 번, 멱등).
// 원장은 읽기만 하고, 재생한 줄이 하나라도 있으면 결과 건수를 감사 1건으로 남긴다. 이미 평가된 DB 에서는 아무것도 하지 않는다.
func BackfillValuation(ctx context.Context, b Base) error {
	return b.Txs.WithTx(ctx, func(tx contract.Tx) error {
		rows, warned, err := repository.BackfillValuation(ctx, tx)
		if err != nil {
			return fmt.Errorf("재고 평가 재생: %w", err)
		}
		if rows == 0 {
			return nil
		}
		return b.Audit.Record(ctx, tx, audit.Entry{
			Actor: contract.Principal{Role: "system", Channel: contract.ChannelWeb}, Action: "valuation.backfill",
			TargetType: audit.TargetMonthClose, TargetID: "all", Result: audit.ResultSuccess, Detail: "기존 원장 재고 평가 재생",
			Diff: []audit.DiffItem{{Key: "revalued_rows", New: fmt.Sprint(rows)}, {Key: "warnings", New: fmt.Sprint(warned)}},
		})
	})
}

// SplitValuationByEntity 는 이관 전 품목 전체 기준 평가 줄(entity_basis = 0)을 법인 기준으로 옮긴다(B51 5.2, 기동 때 한 번, 멱등).
// 닫힌 달 줄은 읽기만 하고 법인별 첫 열린 달부터 다시 계산한다. (법인, 품목)마다 트랜잭션을 따로 잡아 한 곳의 실패가 나머지를 막지 않고,
// 실패한 그룹은 부팅을 막지 않고 기록만 남겨 다음 부팅에 다시 시도한다. 다시 계산한 줄이 있으면 감사 1건(valuation.split)을 남긴다.
func SplitValuationByEntity(ctx context.Context, b Base) error {
	var groups []repository.SplitGroup
	if err := b.Txs.WithTx(ctx, func(tx contract.Tx) (err error) {
		groups, err = repository.ValuationSplitGroups(ctx, tx)
		return err
	}); err != nil {
		slog.Warn("재고 평가 법인 분리 대상 조회 실패(다음 기동에 다시 시도)", "error", err)
		return nil
	}
	var rows, done, failed int
	var qty, amt int64
	entities := map[int64]bool{}
	for _, g := range groups {
		var res repository.SplitOutcome
		err := b.Txs.WithTx(ctx, func(tx contract.Tx) (err error) {
			res, err = repository.SplitValuationGroup(ctx, tx, g)
			return err
		})
		if err != nil {
			failed++
			slog.Warn("재고 평가 법인 분리 실패(다음 기동에 다시 시도)", "entity", g.EntityID, "item", g.ItemID, "error", err)
			continue
		}
		if res.Recomputed == 0 {
			continue
		}
		rows += res.Recomputed
		done++
		qty += res.StartQty
		amt += res.StartAmt
		entities[g.EntityID] = true
	}
	if rows == 0 {
		return nil
	}
	err := b.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return b.Audit.Record(ctx, tx, audit.Entry{
			Actor: contract.Principal{Role: "system", Channel: contract.ChannelWeb}, Action: "valuation.split",
			TargetType: audit.TargetMonthClose, TargetID: "all", Result: audit.ResultSuccess, Detail: "재고 평가를 법인별로 분리",
			Diff: []audit.DiffItem{{Key: "revalued_rows", New: fmt.Sprint(rows)}, {Key: "groups", New: fmt.Sprint(done)},
				{Key: "entities", New: fmt.Sprint(len(entities))}, {Key: "start_qty", New: fmt.Sprint(qty)}, {Key: "start_amount", New: fmt.Sprint(amt)}, {Key: "failed", New: fmt.Sprint(failed)}},
		})
	})
	if err != nil {
		slog.Warn("재고 평가 법인 분리 감사 기록 실패", "error", err)
	}
	return nil
}

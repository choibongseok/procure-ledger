package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 다법인(B44 2단계) 저장소 배관.(업무 저장소는 erp 가 이 함수들을 쓴다).
//
//   - 읽기: 서비스가 contract.ReadScope(ctx, perm) 로 받은 EntityScope 를 저장소 함수에 넘기고, 저장소는 scope.Cond("별칭") 조각을 WHERE 에 붙인다.
//     그룹 범위(All)는 조건이 없다. 법인 소유 표를 조건 없이 읽는 SQL 은 법인 필터 래칫(cicheck)이 늘리지 못한다.
//   - 쓰기: 서비스가 contract.WriteEntity(ctx, perm) 로 대상 법인을 정하고 EnsureEntityWritable 로 비활성 법인을 거르며, 새 행의 entity_id 를 반드시 명시한다.
//   - 번호: NextEntityCounter / NextDocSeq 가 법인별 카운터를 올리고, EntityNoPrefix 가 기본 법인이 아닌 법인의 번호 접두(법인 코드)를 준다(명세 6절 D4).

// EntityCond 는 scope 를 법인 열(alias.entity_id)의 SQL 조건 조각으로 바꾼다(contract.EntityScope.Cond 와 같다. 저장소가 contract 를 직접 부르지 않아도 되게 한 얇은 래퍼).
func EntityCond(scope contract.EntityScope, alias string) (string, []any) { return scope.Cond(alias) }

type entityQ interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// EnsureEntityWritable 은 법인이 있고 활성인지 본다. 없으면 ErrNotFound, 비활성이면 ErrInvalidState("비활성 법인입니다", 읽기·내보내기는 된다, 명세 E6).
func EnsureEntityWritable(ctx context.Context, q entityQ, entityID int64) error {
	var active int
	err := q.QueryRowContext(ctx, `SELECT active FROM entities WHERE id = ?`, entityID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.ErrNotFound
	}
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("%w: 비활성 법인입니다", contract.ErrInvalidState)
	}
	return nil
}

// EntityCode 는 법인 코드다.
func EntityCode(ctx context.Context, q entityQ, entityID int64) (string, error) {
	var code string
	err := q.QueryRowContext(ctx, `SELECT code FROM entities WHERE id = ?`, entityID).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.ErrNotFound
	}
	return code, err
}

// EntityNoPrefix 는 문서 번호 앞에 붙는 법인 접두다: 기본 법인은 "" (이관 전 번호 형식 그대로), 다른 법인은 "<코드>-" (예: ABC-JV-2026-000001).
func EntityNoPrefix(ctx context.Context, q entityQ, entityID int64) (string, error) {
	if entityID == contract.DefaultEntityID {
		return "", nil
	}
	code, err := EntityCode(ctx, q, entityID)
	if err != nil {
		return "", err
	}
	return code + "-", nil
}

// NextEntityCounter 는 법인별 번호 카운터(inventory_counters)를 올려 새 순번을 돌려준다. 행이 없으면 만든다. 키가 법인마다 달라 법인 사이 번호 경합이 없다.
func NextEntityCounter(ctx context.Context, q contract.Tx, entityID int64, prefix string, year int) (int64, error) {
	if _, err := q.ExecContext(ctx, `INSERT INTO inventory_counters (entity_id, prefix, year, last) VALUES (?,?,?,0) ON CONFLICT DO NOTHING`, entityID, prefix, year); err != nil {
		return 0, err
	}
	var n int64
	if err := q.QueryRowContext(ctx, `UPDATE inventory_counters SET last = last + 1 WHERE entity_id = ? AND prefix = ? AND year = ? RETURNING last`, entityID, prefix, year).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// NextDocSeq 는 결재 문서 번호의 법인별 일련번호(document_counters)를 올려 돌려준다.
func NextDocSeq(ctx context.Context, q contract.Tx, entityID, formID, scopeUnitID int64, year int) (int64, error) {
	if _, err := q.ExecContext(ctx, `INSERT INTO document_counters (entity_id, form_id, scope_unit_id, year, last_seq) VALUES (?,?,?,?,0) ON CONFLICT DO NOTHING`, entityID, formID, scopeUnitID, year); err != nil {
		return 0, err
	}
	var n int64
	if err := q.QueryRowContext(ctx, `UPDATE document_counters SET last_seq = last_seq + 1 WHERE entity_id = ? AND form_id = ? AND scope_unit_id = ? AND year = ? RETURNING last_seq`, entityID, formID, scopeUnitID, year).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

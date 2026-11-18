package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 연결 재무 집계 조회(B44 3단계, 명세 11.2). 읽기 전용이고 승인된 전표만 센다. 현재 법인 하나가 아니라 호출자가 볼 수 있는 법인 집합(scope)으로 거른다.
// 전표 줄에는 법인 열이 없어 부모 전표(journals.entity_id)로 법인을 안다. 색인: idx_journals_entity_posting(entity_id, posting_date, status).

type SQLConsolidated struct{}

// ConsEntity 는 집계 대상 법인이다.
type ConsEntity struct {
	ID         int64
	Code, Name string
	Active     bool
}

// colIn 은 법인 번호를 담는 열(col)의 범위 조건이다.
func colIn(col string, scope contract.EntityScope) string {
	if scope.All {
		return "1=1"
	}
	if len(scope.IDs) == 0 {
		return "1=0"
	}
	ids := make([]string, len(scope.IDs))
	for i, id := range scope.IDs {
		ids[i] = fmt.Sprint(id)
	}
	return col + " IN (" + strings.Join(ids, ",") + ")"
}

// Entities 는 범위 안의 법인이다(번호 순). 활성 법인 수(allActive)는 범위와 무관한 설치 전체의 활성 법인 수다("일부 법인만의 합" 안내용).
func (SQLConsolidated) Entities(ctx context.Context, q contract.Tx, scope contract.EntityScope) (out []ConsEntity, allActive int, err error) {
	rows, err := q.QueryContext(ctx, `SELECT id, code, name, active FROM entities WHERE `+colIn("id", scope)+` ORDER BY id`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var e ConsEntity
		var act int
		if err := rows.Scan(&e.ID, &e.Code, &e.Name, &act); err != nil {
			rows.Close()
			return nil, 0, err
		}
		e.Active = act == 1
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE active = 1`).Scan(&allActive); err != nil {
		return nil, 0, err
	}
	return out, allActive, nil
}

// ConsBalance 는 (법인, 계정) 한 칸의 차변 − 대변이다.
type ConsBalance struct {
	EntityID         int64
	Code, Name, Kind string
	Net              int64
}

// Balances 는 법인·계정별 차변 − 대변이다. 자산·부채·자본은 전기일 ≤ to 의 누계, 수익·비용은 [from, to] 의 발생액이다(승인된 전표).
func (SQLConsolidated) Balances(ctx context.Context, q contract.Tx, scope contract.EntityScope, from, to string) ([]ConsBalance, error) {
	rows, err := q.QueryContext(ctx, `SELECT j.entity_id, a.code, a.name, a.kind, COALESCE(SUM(CASE l.side WHEN 'D' THEN l.amount ELSE -l.amount END),0)
		FROM journals j JOIN journal_lines l ON l.journal_id = j.id JOIN accounts a ON a.id = l.account_id
		WHERE `+contract.SQLEntityIn(scope, "j")+` AND j.status = 'approved' AND j.posting_date <= ? AND (a.kind IN ('asset','liability','equity') OR j.posting_date >= ?)
		GROUP BY j.entity_id, a.code, a.name, a.kind ORDER BY a.code, j.entity_id`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConsBalance
	for rows.Next() {
		var b ConsBalance
		if err := rows.Scan(&b.EntityID, &b.Code, &b.Name, &b.Kind, &b.Net); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PendingCount 는 기간 안의 승인 전(임시·승인 요청) 전표 수다.
func (SQLConsolidated) PendingCount(ctx context.Context, q contract.Tx, scope contract.EntityScope, from, to string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journals WHERE `+contract.SQLEntityIn(scope, "journals")+` AND status IN ('draft','requested') AND posting_date >= ? AND posting_date <= ?`, from, to).Scan(&n)
	return n, err
}

// ConsPairLine 은 (우리 법인, 상대 법인, 계정)별 기간 차변·대변 합이다. 상대 법인은 거래처의 연결 법인(vendors.linked_entity_id)이다.
type ConsPairLine struct {
	FromID, ToID     int64
	Code, Name, Kind string
	Debit, Credit    int64
}

// Intercompany 는 연결 거래처를 상대로 둔 승인 전표 줄(journal_lines.vendor_id)의 법인 쌍·계정별 합이다(기간 안, 전기일 기준). 두 법인이 모두 범위 안인 쌍만 센다.
func (SQLConsolidated) Intercompany(ctx context.Context, q contract.Tx, scope contract.EntityScope, from, to string) ([]ConsPairLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT j.entity_id, v.linked_entity_id, a.code, a.name, a.kind,
		COALESCE(SUM(CASE WHEN l.side = 'D' THEN l.amount ELSE 0 END),0), COALESCE(SUM(CASE WHEN l.side = 'C' THEN l.amount ELSE 0 END),0)
		FROM journals j JOIN journal_lines l ON l.journal_id = j.id JOIN vendors v ON v.id = l.vendor_id JOIN accounts a ON a.id = l.account_id
		WHERE `+contract.SQLEntityIn(scope, "j")+` AND j.status = 'approved' AND j.posting_date >= ? AND j.posting_date <= ?
		AND v.linked_entity_id > 0 AND v.linked_entity_id <> j.entity_id AND `+colIn("v.linked_entity_id", scope)+`
		GROUP BY j.entity_id, v.linked_entity_id, a.code, a.name, a.kind ORDER BY j.entity_id, v.linked_entity_id, a.code`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConsPairLine
	for rows.Next() {
		var p ConsPairLine
		if err := rows.Scan(&p.FromID, &p.ToID, &p.Code, &p.Name, &p.Kind, &p.Debit, &p.Credit); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

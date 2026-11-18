package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// 복구본(백업 스냅샷)의 불변식 점검(명세 7.4-2). 두 DB 종류가 같은 SQL 을 쓴다(방언 고유 구문 없음).
//  1. 전표 차대변: 전표마다 차변 합 = 대변 합.
//  2. 재고 합계 대 원장 합: 품목별·창고별 로트별 재고 보기의 합이 재고 원장(stock_ledger)의 수량 합과 같다.
//     (보기는 원장에서 계산되므로 이 점검은 보기·원장이 어긋나는 손상, 예컨대 원장이 일부만 복구된 경우를 잡는다.)
//  3. 번호 카운터 ≥ 실제 최대 번호: inventory_counters 의 last 가 같은 (접두, 연도)로 이미 발급된 전표 번호의 최대 순번 이상이다.
//     카운터가 뒤처지면 복구 뒤 첫 전표가 이미 있는 번호를 다시 발급하려 한다.

type invQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// counterTables 는 inventory_counters 접두와 그 번호를 쓰는 표다(stock_repo.NextNo 호출 지점: 출고 OUT, 이동 MV, 실사 SC).
var counterTables = map[string]string{"OUT": "issue_slips", "MV": "stock_transfers", "SC": "stock_counts"}

// CheckInvariants 는 위반 설명들을 돌려준다(없으면 nil). 표가 아직 없는 오래된 스키마는 해당 점검을 건너뛴다.
func CheckInvariants(ctx context.Context, q invQuerier) []string {
	var out []string
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT journal_id FROM journal_lines GROUP BY journal_id
		HAVING SUM(CASE side WHEN 'D' THEN amount ELSE -amount END) <> 0) t`).Scan(&n); err == nil && n > 0 {
		out = append(out, fmt.Sprintf("차대변이 맞지 않는 전표 %d건", n))
	}

	var ledger, byItem, byWh sql.NullInt64
	if q.QueryRowContext(ctx, `SELECT COALESCE(SUM(qty_delta), 0) FROM stock_ledger`).Scan(&ledger) == nil {
		if q.QueryRowContext(ctx, `SELECT COALESCE(SUM(on_hand), 0) FROM v_item_stock`).Scan(&byItem) == nil && byItem.Int64 != ledger.Int64 {
			out = append(out, fmt.Sprintf("품목별 재고 합(%d)이 원장 수량 합(%d)과 다릅니다", byItem.Int64, ledger.Int64))
		}
		if q.QueryRowContext(ctx, `SELECT COALESCE(SUM(on_hand), 0) FROM v_wh_lot_stock`).Scan(&byWh) == nil && byWh.Int64 != ledger.Int64 {
			out = append(out, fmt.Sprintf("창고·로트별 재고 합(%d)이 원장 수량 합(%d)과 다릅니다", byWh.Int64, ledger.Int64))
		}
	}

	// 법인별 평가 잔량(B51 0104): (법인, 품목)의 마지막 평가 줄 잔량은 그 법인 창고의 원장 수량 합과 같아야 한다.
	// 이관 전 품목 전체 기준 줄(entity_basis = 0)은 이 약속 밖이라 건너뛴다. 열이 없는 오래된 스키마는 질의가 실패하므로 점검을 건너뛴다.
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_valuation v
		WHERE v.entity_basis = 1
		  AND v.ledger_id = (SELECT v2.ledger_id FROM stock_valuation v2 WHERE v2.entity_id = v.entity_id AND v2.item_id = v.item_id ORDER BY v2.book_date DESC, v2.ledger_id DESC LIMIT 1)
		  AND v.bal_qty <> COALESCE((SELECT SUM(l.qty_delta) FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.item_id = v.item_id AND w.entity_id = v.entity_id), 0)`).Scan(&n); err == nil && n > 0 {
		out = append(out, fmt.Sprintf("법인별 평가 잔량이 그 법인 창고의 원장 수량 합과 다른 (법인, 품목) %d건", n))
	}

	// 번호 카운터는 법인별이다(B44): 기본 법인의 번호는 "MV-2026-0001", 다른 법인은 법인 코드가 앞에 붙은 "ABC-MV-2026-0001" 이다.
	// 법인 열이 없는 오래된 스키마는 첫 질의가 실패하므로 법인 구분 없이 점검한다.
	rows, err := q.QueryContext(ctx, `SELECT c.prefix, c.year, c.last, CASE WHEN e.is_default = 1 THEN '' ELSE e.code || '-' END FROM inventory_counters c JOIN entities e ON e.id = c.entity_id ORDER BY c.entity_id, c.prefix, c.year`)
	if err != nil {
		rows, err = q.QueryContext(ctx, `SELECT prefix, year, last, '' FROM inventory_counters ORDER BY prefix, year`)
		if err != nil {
			return out
		}
	}
	type counter struct {
		prefix, entityPre string
		year, last        int
	}
	var cs []counter
	for rows.Next() {
		var c counter
		if rows.Scan(&c.prefix, &c.year, &c.last, &c.entityPre) == nil {
			cs = append(cs, c)
		}
	}
	rows.Close()
	for _, c := range cs {
		tbl, ok := counterTables[c.prefix]
		if !ok {
			continue
		}
		max := maxSeq(ctx, q, tbl, c.entityPre+c.prefix, c.year)
		if max > c.last {
			out = append(out, fmt.Sprintf("번호 카운터 %s%s-%d 의 마지막 번호(%d)가 이미 발급된 최대 번호(%d)보다 작습니다", c.entityPre, c.prefix, c.year, c.last, max))
		}
	}
	// 카운터 행이 아예 없는데 번호가 발급된 경우도 같은 위반이다.
	have := map[string]bool{}
	for _, c := range cs {
		have[fmt.Sprintf("%s%s-%d", c.entityPre, c.prefix, c.year)] = true
	}
	for prefix, tbl := range counterTables {
		yrs, err := q.QueryContext(ctx, `SELECT no FROM `+tbl)
		if err != nil {
			continue
		}
		seen := map[string]bool{} // "법인접두+접두-연도"
		for yrs.Next() {
			var no string
			if yrs.Scan(&no) != nil {
				continue
			}
			if full, y, _, ok := splitNo(no, prefix); ok {
				seen[fmt.Sprintf("%s-%d", full, y)] = true
			}
		}
		yrs.Close()
		for key := range seen {
			if !have[key] {
				out = append(out, fmt.Sprintf("번호 카운터 %s 가 없는데 전표 번호가 발급되어 있습니다", key))
			}
		}
	}
	return out
}

// splitNo 는 "PREFIX-YYYY-NNNN" 또는 법인 접두가 붙은 "ENT-PREFIX-YYYY-NNNN" 에서 (법인 접두를 포함한 전체 접두, 연도, 순번)을 읽는다.
func splitNo(no, base string) (full string, year, seq int, ok bool) {
	parts := strings.Split(no, "-")
	switch {
	case len(parts) == 3 && parts[0] == base:
		full = base
	case len(parts) == 4 && parts[1] == base:
		full = parts[0] + "-" + base
	default:
		return "", 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[len(parts)-2])
	s, err2 := strconv.Atoi(parts[len(parts)-1])
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return full, y, s, true
}

func maxSeq(ctx context.Context, q invQuerier, tbl, prefix string, year int) int {
	rows, err := q.QueryContext(ctx, `SELECT no FROM `+tbl)
	if err != nil {
		return 0
	}
	defer rows.Close()
	max := 0
	for rows.Next() {
		var no string
		if rows.Scan(&no) != nil {
			continue
		}
		if full, y, s, ok := splitNo(no, baseOf(prefix)); ok && full == prefix && y == year && s > max {
			max = s
		}
	}
	return max
}

// baseOf 는 법인 접두("ABC-")가 붙은 전체 접두에서 기본 접두("MV")를 돌려준다.
func baseOf(full string) string {
	if i := strings.LastIndex(full, "-"); i >= 0 {
		return full[i+1:]
	}
	return full
}

package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// AC-4429: 전표 20만 건 시드에서 연결 집계가 3초 안에 끝나고, 법인 필터 질의가 법인 하나일 때도 색인을 쓴다(실행 계획).
// 시드는 승인된 전표를 두 법인에 번갈아 넣고(전기일은 2025-01 ~ 2026-12 에 고르게) 줄을 두 개씩 단다. `go test -short` 에서는 건너뛴다.

const perfJournals = 200_000

func (e *env) seedPerfJournals(t *testing.T, abc int64) {
	t.Helper()
	cte := `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ` + "200000" + `) `
	// 날짜: 24개월 × 28일에 고르게(i % 672).
	day := `printf('%04d-%02d-%02d', 2025 + ((i % 672) / 28) / 12, 1 + ((i % 672) / 28) % 12, 1 + (i % 672) % 28)`
	if dbtest.PG() {
		day = `to_char(make_date(2025 + ((i % 672) / 28) / 12, 1 + ((i % 672) / 28) % 12, 1 + (i % 672) % 28), 'YYYY-MM-DD')`
	}
	if _, err := e.db.Exec(cte + `INSERT INTO journals (entity_id, no, kind, status, posting_date, trade_date, memo, total_amount, created_by, created_at)
		SELECT CASE WHEN i % 2 = 0 THEN 1 ELSE ` + itoa(abc) + ` END, 'PERF-' || i, 'general', 'approved', ` + day + `, ` + day + `, '', 1000, 1, '2026-01-01T00:00:00Z' FROM n`); err != nil {
		t.Fatalf("전표 시드: %v", err)
	}
	dbtest.DropTrigger(t, e.db, "trg_journal_lines_ins") // 대량 적재 흉내: 시험 DB 에서만 줄 입력 보호를 푼다
	for seq, side := range []struct{ code, side string }{{"1020", "D"}, {"4110", "C"}} {
		if _, err := e.db.Exec(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount) SELECT j.id, ` + itoa(int64(seq+1)) + `, a.id, '` + side.side + `', 1000
			FROM journals j JOIN accounts a ON a.code = '` + side.code + `' WHERE j.no LIKE 'PERF-%'`); err != nil {
			t.Fatalf("줄 시드: %v", err)
		}
	}
	if dbtest.PG() {
		for _, tb := range []string{"journals", "journal_lines"} {
			if _, err := e.db.Exec(`ANALYZE ` + tb); err != nil {
				t.Fatal(err)
			}
		}
	} else if _, err := e.db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
}

func TestConsolidatedPerf200kJournals(t *testing.T) {
	if testing.Short() {
		t.Skip("시드가 커서 -short 에서는 건너뜁니다")
	}
	e := newEnv(t)
	abc := e.secondEntity(t)
	e.seedPerfJournals(t, abc)
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM journals WHERE no LIKE 'PERF-%'`).Scan(&n); err != nil || n != perfJournals {
		t.Fatalf("시드 수: %d %v", n, err)
	}
	svc := service.NewConsolidatedService(e.journals.Base)
	for _, f := range []domain.ConsolidatedFilter{{Unit: "month", Month: "2026-06"}, {Unit: "quarter", Year: 2026, Quarter: 2}, {Unit: "year", Year: 2026}} {
		start := time.Now()
		c, err := svc.Summary(inEntity(uFinance, 0), f)
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if took > 3*time.Second {
			t.Errorf("연결 집계 %+v 가 %v 걸렸습니다(목표 3초)", f, took)
		}
		t.Logf("연결 집계 %s: %v, 계정 %d줄", f.Unit, took, len(c.Trial))
		if r := consRow(c, "1020"); r == nil || len(r.Amounts) != 2 || r.Total <= 0 {
			t.Fatalf("1020: %+v", r)
		}
	}
}

// 법인 필터가 법인 하나(= N, IN (N))로 붙은 질의가 법인 색인을 쓴다(전체 스캔이 아니다).
func TestEntityFilteredQueryUsesIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("시드가 커서 -short 에서는 건너뜁니다")
	}
	e := newEnv(t)
	abc := e.secondEntity(t)
	e.seedPerfJournals(t, abc)
	scoped := []string{
		contract.SQLEntity(inEntity(uFinance, contract.DefaultEntityID), "j"),
		contract.SQLEntityIn(contract.NewEntitySet(contract.DefaultEntityID).ScopeOf(), "j"),
	}
	for _, cond := range scoped {
		q := `SELECT j.no FROM journals j WHERE ` + cond + ` AND j.status = 'approved' AND j.posting_date >= '2026-06-01' AND j.posting_date <= '2026-06-30'`
		explain := `EXPLAIN QUERY PLAN `
		if dbtest.PG() {
			explain = `EXPLAIN `
		}
		rows, err := e.db.Query(explain + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			cols, _ := rows.Columns()
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var line []string
			for _, v := range vals {
				switch x := v.(type) {
				case string:
					line = append(line, x)
				case []byte:
					line = append(line, string(x))
				}
			}
			plan = append(plan, strings.Join(line, " "))
		}
		rows.Close()
		text := strings.Join(plan, "\n")
		if !strings.Contains(text, "idx_journals_entity_posting") {
			t.Errorf("%q 의 실행 계획이 법인 색인을 쓰지 않습니다:\n%s", cond, text)
		}
	}
}

package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0028: month_closes 는 서버 경로를 우회해도 지울 수 없고, 마감·해제 두 전이로만 바뀐다(검증자 B1).
func TestMonthCloseStateGuards(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	denied := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err == nil {
			t.Fatalf("거부되어야 함: %s", q)
		}
	}
	// 처음 값: 마감됨 버전 1 만 들어간다.
	denied(`INSERT INTO month_closes (month, status, version, closed_at) VALUES ('2026-10','open',1,'x')`)
	denied(`INSERT INTO month_closes (month, status, version, closed_at) VALUES ('2026-10','closed',3,'x')`)
	mustExec(t, db, `INSERT INTO month_closes (month, status, version, closed_at) VALUES ('2026-10','closed',1,'2026-11-01T00:00:00Z')`)
	// 직접 수정·삭제는 거부된다.
	denied(`DELETE FROM month_closes WHERE month='2026-10'`)
	denied(`UPDATE month_closes SET version = 9 WHERE month='2026-10'`)
	denied(`UPDATE month_closes SET status='closed', closed_at='y' WHERE month='2026-10'`)
	denied(`UPDATE month_closes SET month='2026-09' WHERE month='2026-10'`)
	denied(`UPDATE month_closes SET status='open' WHERE month='2026-10'`) // 해제 일시 없음
	// 정상 전이: 해제 → 재마감(버전 +1).
	mustExec(t, db, `UPDATE month_closes SET status='open', released_at='2026-11-05T00:00:00Z' WHERE month='2026-10'`)
	denied(`UPDATE month_closes SET status='closed', version=5, closed_at='z' WHERE month='2026-10'`)
	mustExec(t, db, `UPDATE month_closes SET status='closed', version=2, closed_at='2026-11-06T00:00:00Z', released_at=NULL WHERE month='2026-10'`)
	var v int
	if err := db.QueryRow(`SELECT version FROM month_closes WHERE month='2026-10'`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("버전 %d %v", v, err)
	}
	if err := func() error { _, e := db.Exec(`DELETE FROM month_closes`); return e }(); err == nil || !strings.Contains(err.Error(), "지울 수 없습니다") {
		t.Fatalf("삭제 거부 문구: %v", err)
	}
}

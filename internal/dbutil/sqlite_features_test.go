package dbutil

import "testing"

// B37: 공통 부분집합 이행이 기대는 SQLite 기능(RETURNING·string_agg·ON CONFLICT DO NOTHING·IS DISTINCT FROM)이 번들 SQLite 에 있다.
func TestBundledSQLiteSupportsCommonSubset(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, k TEXT UNIQUE, g TEXT)`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO t (k, g) VALUES ('a','x') RETURNING id`).Scan(&id); err != nil || id != 1 {
		t.Fatalf("RETURNING: %d %v", id, err)
	}
	if _, err := db.Exec(`INSERT INTO t (k, g) VALUES ('a','y') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("ON CONFLICT DO NOTHING: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (k, g) VALUES ('b','x')`); err != nil {
		t.Fatal(err)
	}
	var agg string
	// DISTINCT 와 구분자를 함께 쓰는 string_agg 는 SQLite 가 받지 않는다: 중복 제거를 안쪽 SELECT DISTINCT 로 한다(두 방언 공통 형태).
	if err := db.QueryRow(`SELECT string_agg(g, ',') FROM (SELECT DISTINCT g FROM t ORDER BY g)`).Scan(&agg); err != nil || agg != "x" {
		t.Fatalf("string_agg: %q %v", agg, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t WHERE g IS DISTINCT FROM 'y'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("IS DISTINCT FROM: %d %v", n, err)
	}
}

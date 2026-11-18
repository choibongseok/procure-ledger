package dbmigrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

func tbl(names ...string) map[string]*tableInfo {
	m := map[string]*tableInfo{}
	for _, n := range names {
		m[n] = &tableInfo{Name: n}
	}
	return m
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func TestOrderPutsReferencedTablesFirst(t *testing.T) {
	tables := tbl("a", "b", "c", "skipme")
	fks := []fk{
		{Table: "b", Ref: "a", Cols: []string{"a_id"}},
		{Table: "c", Ref: "b", Cols: []string{"b_id"}},
		{Table: "c", Ref: "skipme", Cols: []string{"s_id"}}, // 제외 표로 가는 외래 키는 순서에 영향이 없다
	}
	delete(tables, "skipme")
	got, err := order(tables, fks)
	if err != nil {
		t.Fatal(err)
	}
	if !(indexOf(got, "a") < indexOf(got, "b") && indexOf(got, "b") < indexOf(got, "c")) {
		t.Fatalf("순서: %v", got)
	}
}

func TestOrderBreaksCyclesAndSelfReferencesWithNullableForeignKeys(t *testing.T) {
	tables := tbl("dept", "user", "unit")
	fks := []fk{
		{Table: "dept", Ref: "user", Cols: []string{"head_id"}, Nullable: true},
		{Table: "user", Ref: "dept", Cols: []string{"dept_id"}, Nullable: false},
		{Table: "unit", Ref: "unit", Cols: []string{"parent_id"}, Nullable: true},
	}
	got, err := order(tables, fks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("순서: %v", got)
	}
	if len(tables["dept"].Deferred) != 1 || tables["dept"].Deferred[0][0] != "head_id" {
		t.Fatalf("순환은 NULL 허용 외래 키(dept.head_id)를 나중에 채우기로 끊어야 함: %+v", tables["dept"].Deferred)
	}
	if len(tables["unit"].Deferred) != 1 {
		t.Fatalf("자기 참조는 나중에 채우기여야 함: %+v", tables["unit"].Deferred)
	}
	if indexOf(got, "dept") > indexOf(got, "user") && len(tables["user"].Deferred) == 0 {
		// dept 가 user 보다 먼저여야 user.dept_id 를 바로 넣을 수 있다.
		t.Fatalf("user 가 dept 보다 먼저 들어가면 안 됨: %v", got)
	}
}

func TestOrderRejectsCycleWithoutNullableForeignKey(t *testing.T) {
	tables := tbl("x", "y")
	_, err := order(tables, []fk{{Table: "x", Ref: "y", Cols: []string{"y_id"}}, {Table: "y", Ref: "x", Cols: []string{"x_id"}}})
	if err == nil || !strings.Contains(err.Error(), "순환") {
		t.Fatalf("NULL 을 허용하지 않는 순환은 거부해야 함: %v", err)
	}
	_, err = order(tbl("z"), []fk{{Table: "z", Ref: "z", Cols: []string{"p"}}})
	if err == nil {
		t.Fatal("NULL 을 허용하지 않는 자기 참조는 거부해야 함")
	}
}

func TestConvertValues(t *testing.T) {
	cases := []struct {
		in   any
		typ  string
		want any
		bad  bool
	}{
		{int64(5), "bigint", int64(5), false},
		{"7", "bigint", int64(7), false},
		{3.0, "bigint", int64(3), false},
		{3.5, "bigint", nil, true},
		{"abc", "bigint", nil, true},
		{"한글", "text", "한글", false},
		{"a\x00b", "text", nil, true},
		{"\xff\xfe", "text", nil, true},
		{[]byte{1, 2}, "bytea", []byte{1, 2}, false},
		{int64(1), "boolean", true, false},
		{nil, "text", nil, false},
	}
	for _, c := range cases {
		got, err := convert(c.in, c.typ)
		if c.bad {
			if err == nil {
				t.Errorf("%v → %s: 오류여야 함", c.in, c.typ)
			}
			continue
		}
		if err != nil || canon(got) != canon(c.want) {
			t.Errorf("%v → %s: %v %v", c.in, c.typ, got, err)
		}
	}
}

func TestTableSumIgnoresRowOrderAndSeesChanges(t *testing.T) {
	a, b, c := &tableSum{}, &tableSum{}, &tableSum{}
	a.add([]any{int64(1), "x"})
	a.add([]any{int64(2), "y"})
	b.add([]any{int64(2), "y"})
	b.add([]any{int64(1), "x"})
	c.add([]any{int64(1), "x"})
	c.add([]any{int64(2), "z"})
	if a.sum() != b.sum() {
		t.Error("행 순서가 달라도 체크섬은 같아야 함")
	}
	if a.sum() == c.sum() {
		t.Error("값이 달라지면 체크섬도 달라야 함")
	}
}

// AC-1750: 모든 표는 "이관"이거나 "제외"로 분류돼 있고, 제외 목록에는 실제로 있는 표만 있다(오타·삭제된 표가 남지 않는다).
func TestEveryTableIsClassifiedAndSkipListHasNoStaleNames(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	migrate, skip := 0, 0
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		have[n] = true
		if ok, why := Classified(n); ok {
			migrate++
		} else {
			skip++
			if strings.TrimSpace(why) == "" {
				t.Errorf("제외 표 %s 에 사유가 없음", n)
			}
		}
	}
	for n := range Skip {
		if !have[n] {
			t.Errorf("제외 목록의 표 %s 가 스키마에 없음", n)
		}
	}
	if migrate+skip != len(have) || migrate == 0 {
		t.Fatalf("분류: 이관 %d + 제외 %d != 전체 %d", migrate, skip, len(have))
	}
	// 보호 장치 표(감사·원장·재고)는 반드시 이관 대상이다.
	for _, n := range []string{"audit_logs", "audit_anchor_stamps", "stock_ledger", "journals", "journal_lines", "users", "inventory_counters"} {
		if ok, _ := Classified(n); !ok {
			t.Errorf("%s 는 이관 대상이어야 함", n)
		}
	}
}

package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// column 은 대상(PG) 표의 열이다.
type column struct {
	Name     string
	Type     string // information_schema.data_type
	Identity bool
	NotNull  bool
}

// fk 는 외래 키 하나다(한 표에서 다른 표로).
type fk struct {
	Table, Ref string
	Cols       []string
	Nullable   bool // 열이 모두 NULL 을 허용한다
}

// tableInfo 는 이관 계획의 표 한 개다.
type tableInfo struct {
	Name     string
	Cols     []column
	PK       []string
	Deferred [][]string // 먼저 NULL 로 넣고 나중에 채우는 외래 키 열 묶음(순환·자기 참조)
}

func (t tableInfo) identityCols() []string {
	var out []string
	for _, c := range t.Cols {
		if c.Identity {
			out = append(out, c.Name)
		}
	}
	return out
}

func (t tableInfo) deferredSet() map[string]bool {
	m := map[string]bool{}
	for _, cs := range t.Deferred {
		for _, c := range cs {
			m[c] = true
		}
	}
	return m
}

// catalog 는 대상 PG 의 표·열·기본키·외래 키를 읽는다(public 스키마).
func catalog(ctx context.Context, db *sql.DB) (map[string]*tableInfo, []fk, error) {
	tables := map[string]*tableInfo{}
	rows, err := db.QueryContext(ctx, `SELECT c.table_name, c.column_name, c.data_type, c.is_identity = 'YES', c.is_nullable = 'NO'
		FROM information_schema.columns c JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE' ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var tn string
		var col column
		if err := rows.Scan(&tn, &col.Name, &col.Type, &col.Identity, &col.NotNull); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if tables[tn] == nil {
			tables[tn] = &tableInfo{Name: tn}
		}
		tables[tn].Cols = append(tables[tn].Cols, col)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	pk, err := db.QueryContext(ctx, `SELECT rel.relname, string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_constraint c JOIN pg_class rel ON rel.oid = c.conrelid
		CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.contype = 'p' AND c.connamespace = 'public'::regnamespace GROUP BY c.oid, rel.relname`)
	if err != nil {
		return nil, nil, err
	}
	for pk.Next() {
		var tn, cols string
		if err := pk.Scan(&tn, &cols); err != nil {
			pk.Close()
			return nil, nil, err
		}
		if t := tables[tn]; t != nil {
			t.PK = strings.Split(cols, ",")
		}
	}
	if err := pk.Close(); err != nil {
		return nil, nil, err
	}
	fr, err := db.QueryContext(ctx, `SELECT rel.relname, ref.relname, string_agg(a.attname, ',' ORDER BY k.ord), bool_and(NOT a.attnotnull)
		FROM pg_constraint c JOIN pg_class rel ON rel.oid = c.conrelid JOIN pg_class ref ON ref.oid = c.confrelid
		CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace GROUP BY c.oid, rel.relname, ref.relname`)
	if err != nil {
		return nil, nil, err
	}
	var fks []fk
	for fr.Next() {
		var f fk
		var cols string
		if err := fr.Scan(&f.Table, &f.Ref, &cols, &f.Nullable); err != nil {
			fr.Close()
			return nil, nil, err
		}
		f.Cols = strings.Split(cols, ",")
		fks = append(fks, f)
	}
	return tables, fks, fr.Close()
}

// order 는 이관할 표를 외래 키 위상 순서(참조되는 표가 먼저)로 늘어놓는다. 순환·자기 참조는 NULL 을 허용하는 외래 키를 골라
// "먼저 NULL 로 넣고 나중에 채우기"(Deferred)로 끊는다. NULL 을 허용하지 않는 순환이면 오류다.
// tables 는 이관 대상 표만 담아 넘긴다(제외 표로 가는 외래 키는 무시한다).
func order(tables map[string]*tableInfo, fks []fk) ([]string, error) {
	deps := map[string]map[string]bool{}
	for n := range tables {
		deps[n] = map[string]bool{}
	}
	var live []fk
	for _, f := range fks {
		if tables[f.Table] == nil || tables[f.Ref] == nil {
			continue
		}
		live = append(live, f)
	}
	// 자기 참조는 처음부터 지연 대상이다.
	var rest []fk
	for _, f := range live {
		if f.Table == f.Ref {
			if !f.Nullable {
				return nil, fmt.Errorf("표 %s 의 자기 참조 외래 키(%s)가 NULL 을 허용하지 않아 이관 순서를 정할 수 없습니다", f.Table, strings.Join(f.Cols, ","))
			}
			tables[f.Table].Deferred = append(tables[f.Table].Deferred, f.Cols)
			continue
		}
		rest = append(rest, f)
	}
	for _, f := range rest {
		deps[f.Table][f.Ref] = true
	}
	var out []string
	done := map[string]bool{}
	remaining := len(tables)
	for remaining > 0 {
		var ready []string
		for n := range tables {
			if done[n] {
				continue
			}
			ok := true
			for d := range deps[n] {
				if !done[d] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			// 순환: 남은 표 사이의 외래 키 중 NULL 을 허용하는 것 하나를 지연으로 돌린다(결정적이도록 이름순).
			var cands []fk
			for _, f := range rest {
				if !done[f.Table] && !done[f.Ref] && deps[f.Table][f.Ref] && f.Nullable {
					cands = append(cands, f)
				}
			}
			if len(cands) == 0 {
				var left []string
				for n := range tables {
					if !done[n] {
						left = append(left, n)
					}
				}
				sort.Strings(left)
				return nil, fmt.Errorf("외래 키 순환을 끊을 수 없습니다(NULL 을 허용하는 외래 키가 없음): %s", strings.Join(left, ", "))
			}
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].Table != cands[j].Table {
					return cands[i].Table < cands[j].Table
				}
				return cands[i].Ref < cands[j].Ref
			})
			c := cands[0]
			delete(deps[c.Table], c.Ref)
			tables[c.Table].Deferred = append(tables[c.Table].Deferred, c.Cols)
			continue
		}
		sort.Strings(ready)
		for _, n := range ready {
			done[n] = true
			remaining--
			out = append(out, n)
		}
	}
	return out, nil
}

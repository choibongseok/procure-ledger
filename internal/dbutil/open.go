package dbutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultSQLitePath 는 PL_DB_PATH 의 기본값이다(config 와 같은 값).
const DefaultSQLitePath = "data/erp.db"

// Options 는 OpenTarget 의 선택 설정이다.
type Options struct {
	PG PGOptions
}

// OpenTarget 은 대상 종류에 맞게 DB 를 열고 방언을 함께 돌려준다.
// SQLite 는 기존 Open 과 완전히 같은 동작이다(디렉터리 생성 포함): 연결 1개, WAL, 외래 키 강제.
func OpenTarget(ctx context.Context, t Target, o Options) (*sql.DB, Dialect, error) {
	switch t.Kind {
	case KindPostgres:
		db, err := OpenPG(ctx, t.URL, o.PG)
		if err != nil {
			return nil, nil, err
		}
		return db, Postgres, nil
	case KindSQLite:
		if t.Path != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
				return nil, nil, err
			}
		}
		db, err := Open(t.Path)
		if err != nil {
			return nil, nil, err
		}
		return db, SQLite, nil
	}
	return nil, nil, fmt.Errorf("dbutil: 알 수 없는 DB 종류 %q", t.Kind)
}

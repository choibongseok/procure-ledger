// Package dbutil 은 SQLite(modernc, cgo 없음) 연결 헬퍼다.
package dbutil

import (
	"database/sql"
	"net/url"

	_ "modernc.org/sqlite" // 드라이버 등록
)

// Open 은 path 의 SQLite 파일을 연다(":memory:" 이면 메모리 DB).
// 외래 키 강제·WAL·busy timeout 을 연결마다 적용한다. 쓰기 직렬화를 위해 연결은 1개로 둔다.
func Open(path string) (*sql.DB, error) {
	SetActive(SQLite) // 이 프로세스가 연 DB 의 방언(잠금·시퀀스 보정 구문을 넣을지 가린다)
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	dsn := "file:" + path + "?" + q.Encode()
	if path == ":memory:" {
		dsn = "file::memory:?" + q.Encode()
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		dsn = "file:" + path + "?" + q.Encode()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

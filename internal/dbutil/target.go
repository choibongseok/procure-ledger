package dbutil

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Target 은 설정이 가리키는 DB 다(B37 4절).
type Target struct {
	Kind Kind
	// Path 는 SQLite 파일 경로(":memory:" 허용). Kind 가 SQLite 일 때만 쓴다.
	Path string
	// URL 은 PostgreSQL 연결 문자열이다. 비밀번호가 들어 있으므로 로그·오류에는 Redact 한 값만 쓴다.
	URL string
}

// pgTypoSchemes 는 PostgreSQL 을 가리키려다 틀린 스킴이다. 조용히 SQLite 파일 이름으로 해석되는 사고를 막으려고 거부한다.
var pgTypoSchemes = map[string]bool{"postgre": true, "postgress": true, "postgressql": true, "pgsql": true, "pg": true, "psql": true, "postgresq": true}

// ParseTarget 은 PL_DB_URL(dbURL)과 PL_DB_PATH(dbPath)에서 대상을 정한다.
//   - dbURL 이 비어 있으면 SQLite(dbPath).
//   - 스킴이 postgres:// 또는 postgresql:// 이면 PostgreSQL. dbPath 는 무시된다(호출자가 경고한다: Ignored 로 확인).
//   - PostgreSQL 과 비슷한 오타 스킴(postgre://, pgsql:// 등)은 오류.
//   - 그 밖의 "://" 스킴(mysql:// 등)은 오류다(SQLite 파일 이름으로 조용히 읽지 않는다). sqlite://·file:// 은 허용한다.
//   - 스킴이 없는 값은 SQLite 경로로 읽는다(앞의 sqlite: / file: 접두사는 벗긴다).
func ParseTarget(dbPath, dbURL string) (Target, error) {
	dbURL = strings.TrimSpace(dbURL)
	if dbURL == "" {
		return Target{Kind: KindSQLite, Path: dbPath}, nil
	}
	if i := strings.Index(dbURL, "://"); i > 0 {
		scheme := strings.ToLower(dbURL[:i])
		switch {
		case scheme == "postgres" || scheme == "postgresql":
			return Target{Kind: KindPostgres, URL: dbURL}, nil
		case pgTypoSchemes[scheme]:
			return Target{}, fmt.Errorf("PL_DB_URL 의 스킴 %q 을(를) 알 수 없습니다. PostgreSQL 은 postgres:// 또는 postgresql:// 로 씁니다", scheme)
		case scheme != "sqlite" && scheme != "file":
			return Target{}, fmt.Errorf("PL_DB_URL 의 스킴 %q 은(는) 지원하지 않습니다. PostgreSQL(postgres://, postgresql://) 또는 SQLite 경로만 쓸 수 있습니다", scheme)
		}
	}
	p := dbURL
	for _, pre := range []string{"sqlite://", "sqlite:", "file:"} {
		if strings.HasPrefix(strings.ToLower(p), pre) {
			p = p[len(pre):]
			break
		}
	}
	if p == "" {
		return Target{}, errors.New("PL_DB_URL 이 비어 있는 경로를 가리킵니다")
	}
	return Target{Kind: KindSQLite, Path: p}, nil
}

// IgnoredPath 는 PostgreSQL 대상인데 PL_DB_PATH 가 기본값이 아닌 값으로 함께 설정된 경우 true 다(경고용).
func (t Target) IgnoredPath(dbPath, defaultPath string) bool {
	return t.Kind == KindPostgres && dbPath != "" && dbPath != defaultPath
}

// Describe 는 로그에 쓸 수 있는 한 줄 설명이다(비밀번호 없음).
func (t Target) Describe() string {
	if t.Kind == KindPostgres {
		return "postgres " + Redact(t.URL)
	}
	return "sqlite " + t.Path
}

// Redact 는 연결 문자열의 비밀번호를 가린다(postgres://user:***@host/db). 파싱할 수 없으면 값을 전부 가린다.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(해석할 수 없는 연결 문자열)"
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "***")
		}
	}
	q := u.Query()
	for _, k := range []string{"password", "sslpassword", "sslkey"} {
		if q.Has(k) {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// isLoopbackHost 는 호스트가 루프백이거나 유닉스 소켓 디렉터리(/로 시작)인지다.
func isLoopbackHost(h string) bool {
	if h == "" || strings.HasPrefix(h, "/") || strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// CheckTLS 는 암호화 없는 연결 정책을 검사한다(B37 4절 4항, D8).
//   - 유닉스 소켓(host 가 / 로 시작)은 항상 허용한다.
//   - 루프백 TCP 는 sslmode 가 require 이상이면 허용하고, 아니면 allowInsecure(PL_DB_ALLOW_INSECURE=1)일 때만 허용한다.
//   - 그 밖의 호스트는 sslmode 가 require·verify-ca·verify-full 이어야 한다(allowInsecure 로도 풀 수 없다).
func CheckTLS(rawURL string, allowInsecure bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("PL_DB_URL 을 해석할 수 없습니다")
	}
	hosts := []string{u.Hostname()}
	if h := u.Query().Get("host"); h != "" { // ?host=/var/run/postgresql 형태
		hosts = strings.Split(h, ",")
	}
	mode := strings.ToLower(u.Query().Get("sslmode"))
	secure := mode == "require" || mode == "verify-ca" || mode == "verify-full"
	for _, h := range hosts {
		switch {
		case strings.HasPrefix(h, "/"):
			continue
		case isLoopbackHost(h):
			if !secure && !allowInsecure {
				return fmt.Errorf("암호화 없는 DB 연결입니다(호스트 %s). sslmode=require 이상을 붙이거나 로컬 개발이면 PL_DB_ALLOW_INSECURE=1 을 설정하세요", h)
			}
		default:
			if !secure {
				return fmt.Errorf("암호화 없는 DB 연결은 허용하지 않습니다(호스트 %s). PL_DB_URL 에 sslmode=verify-full 을 붙이세요", h)
			}
		}
	}
	return nil
}

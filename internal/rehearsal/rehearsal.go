// Package rehearsal 은 복구 리허설 모드(PL_REHEARSAL=1, B38 D15)의 허용 판정이다.
//
// 리허설 서버는 운영 알림·앵커·주기 작업을 건드리면 안 되므로, 운영 DB 를 가리키는 구성에서는 켜지지 않아야 한다.
// 허용 조건은 둘 중 하나다: (1) DB 파일이 있는 디렉터리에 리허설 마커(.rehearsal)가 있다(erp-admin restore --rehearse 가 작업 디렉터리에 만든다),
// (2) DB 파일이 운영체제 임시 디렉터리(os.TempDir()) 아래에 있다. PostgreSQL 대상은 운영 DB 와 구분할 수 없으므로 항상 거부한다.
package rehearsal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Marker 는 리허설 작업 디렉터리에 erp-admin 이 만드는 빈 파일의 이름이다(internal/backup.RehearsalMarker 와 같은 값).
const Marker = ".rehearsal"

// ErrNotAllowed 는 리허설 모드가 이 DB 에서 허용되지 않는다는 오류다. 메시지에 경로·접속 문자열을 싣지 않는다.
var ErrNotAllowed = errors.New("PL_REHEARSAL=1 은 임시 위치(운영체제 임시 디렉터리 아래이거나 .rehearsal 마커가 있는 디렉터리)의 SQLite 에서만 쓸 수 있습니다. 운영 경로와 PL_DB_URL(PostgreSQL)에서는 기동하지 않습니다")

// Allowed 는 이 DB 에서 리허설 모드를 켜도 되는지 본다. postgres 는 대상이 PG 인지다. dbPath 는 SQLite 파일 경로다.
func Allowed(postgres bool, dbPath string) error {
	return allowed(postgres, dbPath, os.TempDir())
}

func allowed(postgres bool, dbPath, tempDir string) error {
	if postgres || strings.TrimSpace(dbPath) == "" || dbPath == ":memory:" {
		return ErrNotAllowed
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return ErrNotAllowed
	}
	dir := realDir(filepath.Dir(abs))
	if _, err := os.Stat(filepath.Join(dir, Marker)); err == nil {
		return nil
	}
	if under(dir, realDir(tempDir)) {
		return nil
	}
	return ErrNotAllowed
}

// realDir 은 심볼릭 링크를 풀어(macOS 의 /var → /private/var 등) 디렉터리를 정규화한다. 아직 없는 경로는 있는 가장 가까운 윗 경로를 푼 뒤 나머지를 붙인다.
func realDir(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// under 는 dir 이 root 의 하위(같거나 아래)인지다. ".." 로 빠져나오는 경로는 이미 Clean 으로 정리됐다.
func under(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

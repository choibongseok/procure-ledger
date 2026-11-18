package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// PG 백업·복구는 pg_dump/pg_restore 를 외부 프로그램으로 호출한다(D4). 컨테이너 이미지에는 없으므로 호스트나 사이드카에서 실행한다.
// 접속 비밀번호는 명령 인자에 싣지 않고 PGPASSWORD 환경변수로만 넘긴다(프로세스 목록에 보이지 않게).

// ErrNoPGTool 은 pg_dump/pg_restore 를 찾지 못한 경우다.
var ErrNoPGTool = errors.New("pg_dump 를 찾을 수 없습니다. 설치하거나 PL_PG_DUMP 를 지정하세요(컨테이너 이미지에는 들어 있지 않습니다. 호스트나 사이드카에서 실행하세요)")

// findPGTool 은 override 경로나 PATH 에서 도구를 찾는다. pg_restore 는 pg_dump 와 같은 디렉터리에서 먼저 찾는다.
func findPGTool(override, name string) (string, error) {
	if override != "" {
		if name == "pg_dump" {
			return override, nil
		}
		sib := filepath.Join(filepath.Dir(override), name)
		if _, err := os.Stat(sib); err == nil {
			return sib, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", ErrNoPGTool
	}
	return p, nil
}

// splitURL 은 접속 문자열에서 비밀번호를 떼어 낸다.
func splitURL(raw string) (clean, password string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("PL_DB_URL 형식이 올바르지 않습니다")
	}
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	return u.String(), password, nil
}

func runPG(ctx context.Context, tool, password string, args ...string) error {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return fmt.Errorf("%s 실행 실패: %v: %s", filepath.Base(tool), err, msg)
	}
	return nil
}

func openPG(ctx context.Context, rawURL string, insecure bool) (*sql.DB, error) {
	t, err := dbutil.ParseTarget("", rawURL)
	if err != nil {
		return nil, err
	}
	db, _, err := dbutil.OpenTarget(ctx, t, dbutil.Options{PG: dbutil.PGOptions{MaxConns: 4, AllowInsecure: insecure}})
	return db, err
}

// backupPG 는 REPEATABLE READ 스냅샷을 내보내고 같은 스냅샷으로 pg_dump 를 돌리며, 같은 스냅샷에서 체인 머리와 앵커 대장을 읽는다(명세 6.4).
func backupPG(ctx context.Context, o Options, work string, m *Manifest) error {
	tool, err := findPGTool(o.PGDump, "pg_dump")
	if err != nil {
		return err
	}
	clean, pw, err := splitURL(o.DBURL)
	if err != nil {
		return err
	}
	db, err := openPG(ctx, o.DBURL, o.AllowInsecure)
	if err != nil {
		return fmt.Errorf("PG 에 연결할 수 없습니다: %w", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snap string
	if err := tx.QueryRowContext(ctx, `SELECT pg_export_snapshot()`).Scan(&snap); err != nil {
		return fmt.Errorf("스냅샷을 내보내지 못했습니다: %w", err)
	}
	dump := filepath.Join(work, DBFilePG)
	if err := runPG(ctx, tool, pw, "-Fc", "--no-owner", "--no-acl", "--snapshot="+snap, "-f", dump, "-d", clean); err != nil {
		return err
	}
	m.DBFile = DBFilePG
	var name sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&name)
	if len(name.String) >= 4 {
		m.Migration = name.String[:4]
	}
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM audit_logs`).Scan(&m.ChainSeq)
	if m.ChainSeq > 0 {
		_ = tx.QueryRowContext(ctx, `SELECT hash FROM audit_logs WHERE seq = ?`, m.ChainSeq).Scan(&m.ChainHash)
	}
	stamps := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT line_hash FROM audit_anchor_stamps WHERE kind = 'stamp'`)
	if err == nil {
		for rows.Next() {
			var h string
			_ = rows.Scan(&h)
			stamps[h] = true
		}
		rows.Close()
	}
	m.AnchorStamps = len(stamps)
	if o.AnchorPath != "" || len(stamps) > 0 {
		lines, warns, aerr := AnchorPrefix(o.AnchorPath, filepath.Join(work, AnchorName), stamps)
		if aerr != nil {
			return fmt.Errorf("앵커 사본을 만들지 못했습니다: %w", aerr)
		}
		m.AnchorFile, m.AnchorLines = AnchorName, lines
		m.Warnings = append(m.Warnings, warns...)
	}
	m.Warnings = append(m.Warnings, "PostgreSQL 백업은 논리 덤프입니다. 시점 복구(WAL 보관)는 DB 운영자의 몫입니다. 체인 검증은 복구 뒤에 합니다")
	return nil
}

// InspectPG 는 열린 PG 데이터베이스를 점검한다(복구 뒤 판정용). anchorPath 가 있으면 앵커 대조까지 한다.
func InspectPG(ctx context.Context, db *sql.DB, anchorPath string, anchorKey []byte) (Report, error) {
	var rep Report
	rep.Integrity = "ok" // PG 는 덤프 복원이 성공했다는 것이 구조 점검이다
	var name sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&name)
	if len(name.String) >= 4 {
		rep.Migration = name.String[:4]
	}
	store := audit.New(db)
	if len(anchorKey) >= audit.MinAnchorKeyLen && anchorPath != "" {
		if _, serr := os.Stat(anchorPath); serr == nil {
			if _, aerr := audit.NewAnchor(store, anchorPath, anchorKey); aerr == nil {
				rep.AnchorKnown = true
			}
		}
	}
	vr, err := store.Verify(ctx)
	if err != nil {
		return rep, fmt.Errorf("감사 체인 검증 실패: %w", err)
	}
	rep.ChainOK, rep.ChainChecked, rep.ChainHash, rep.ChainWhy, rep.Anchor = vr.ChainOK, vr.Checked, vr.HeadHash, vr.BrokenWhy, vr.Anchor
	if vr.ChainOK {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM audit_logs`).Scan(&rep.ChainSeq)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_anchor_stamps WHERE kind = 'stamp'`).Scan(&rep.StampCount)
	rep.Invariants = CheckInvariants(ctx, db)
	return rep, nil
}

// pgDatabaseEmpty 는 대상 데이터베이스에 사용자 표가 하나도 없는지 본다.
func pgDatabaseEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

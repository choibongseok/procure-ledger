package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// RehearsalMarker 는 리허설용 임시 디렉터리에 만드는 표식 파일 이름이다. 서버의 PL_REHEARSAL 모드는 이 파일이 있는 디렉터리의 DB 만 리허설로 인정한다.
const RehearsalMarker = ".rehearsal"

// RestoreOptions 는 복구(또는 리허설) 입력이다.
type RestoreOptions struct {
	SetDir        string
	Kind          string // 대상 DB 종류: sqlite | postgres
	DBPath        string
	DBURL         string
	AllowInsecure bool
	PGDump        string
	AnchorPath    string
	AnchorKey     []byte
	Key           []byte // 암호화 세트 복호화 키
	Replace       bool
	Rehearse      bool
	RevokeTokens  bool
	MaxMigration  string // 이 바이너리가 아는 최종 마이그레이션 번호
	TmpRoot       string // 리허설 작업 위치(기본 os.TempDir())
	WorkDir       string // 리허설 작업 디렉터리를 이 이름으로 만들고 끝나도 남긴다(서버 기동 확인용). 비어 있거나 없어야 한다
	Now           func() time.Time
	ToolVersion   string
}

// RestoreResult 는 판정표와 보고 내용이다.
type RestoreResult struct {
	Checks          []Check
	OK              bool // 실패 항목이 없다
	Manifest        Manifest
	Loss            *LossRange // 복구로 사라지는 감사 기록 범위(이전 앵커 파일 기준)
	LostRecords     int64      // 현재 DB 의 마지막 기록 번호가 복구본보다 앞서 있는 만큼
	SessionsRevoked int64
	TokensRevoked   int64
	PreRestore      []string // 옆으로 옮겨 보존한 파일
	Rehearsal       bool
	Notes           []string
	Invariants      []string // 복구본의 업무 불변식 위반(경고: 복구는 진행된다)
	WorkDB          string   // --work 로 남긴 리허설 DB 경로
	WorkAnchor      string   // 같은 앵커 사본 경로
}

func (r *RestoreResult) add(item, status, detail string) {
	r.Checks = append(r.Checks, Check{item, status, detail})
	if status == Fail {
		r.OK = false
	}
}

func (o *RestoreOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Actor 는 복구가 남기는 감사의 행위자다(사용자 없는 시스템 작업, 경로 cli).
var Actor = contract.Principal{Role: "system", Channel: contract.ChannelCLI}

// SQLiteInUse 는 다른 연결(서버·erp-mcp 등)이 파일을 쓰고 있는지 본다. WAL 모드 파일은 다른 연결이 있으면 DELETE 모드로 바꿀 수 없다.
func SQLiteInUse(ctx context.Context, path string) (bool, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(300)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return true, nil // 잠겨 있다
	}
	return !strings.EqualFold(mode, "delete"), nil
}

func sqliteHasTables(ctx context.Context, path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if st.Size() == 0 {
		return false, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(300)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
		return true, nil // 읽을 수 없으면 비어 있다고 보지 않는다
	}
	return n > 0, nil
}

func sqliteHeadSeq(ctx context.Context, path string) int64 {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(300)")
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int64
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM audit_logs`).Scan(&n)
	return n
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Restore 는 백업 세트를 복구한다. Rehearse 면 임시 위치에서 같은 절차를 돌리고 운영 파일을 바꾸지 않는다.
// 실패 항목이 하나라도 있으면 확정하지 않는다(res.OK=false). 반환 오류는 판정 이전의 실패(입력·IO)다.
func Restore(ctx context.Context, o RestoreOptions) (RestoreResult, error) {
	res := RestoreResult{OK: true, Rehearsal: o.Rehearse}
	vr, err := VerifySet(ctx, o.SetDir, VerifyOptions{Key: o.Key, AnchorKey: o.AnchorKey})
	if err != nil {
		return res, err
	}
	res.Manifest = vr.Manifest
	m := vr.Manifest
	for _, c := range vr.Checks {
		res.add("세트: "+c.Item, c.Status, c.Detail)
	}
	if !vr.OK {
		return res, nil
	}
	if m.DBKind != o.Kind {
		res.add("DB 종류", Fail, fmt.Sprintf("세트는 %s, 대상은 %s 입니다. 다른 종류로는 복구하지 않습니다(이관은 별도 도구의 일입니다)", m.DBKind, o.Kind))
		return res, nil
	}
	res.add("DB 종류", Pass, m.DBKind)
	if o.MaxMigration != "" && m.Migration > o.MaxMigration {
		res.add("마이그레이션 번호", Fail, fmt.Sprintf("이 백업(%s)은 더 새로운 버전에서 만들어졌습니다(이 프로그램은 %s까지 압니다). 앱을 먼저 올리세요", m.Migration, o.MaxMigration))
		return res, nil
	}
	if o.MaxMigration != "" && m.Migration < o.MaxMigration {
		res.Notes = append(res.Notes, fmt.Sprintf("백업의 마이그레이션 번호(%s)가 현재(%s)보다 낮습니다. 복구 뒤 첫 기동 때 마이그레이션이 적용됩니다", m.Migration, o.MaxMigration))
	}
	if o.Kind == "postgres" {
		return restorePG(ctx, o, m, res)
	}
	return restoreSQLite(ctx, o, m, res)
}

func restoreSQLite(ctx context.Context, o RestoreOptions, m Manifest, res RestoreResult) (RestoreResult, error) {
	if o.DBPath == "" {
		return res, errors.New("DB 경로가 없습니다")
	}
	// 대상 상태 점검(리허설은 운영 파일을 건드리지 않으므로 건너뛴다).
	exists := false
	if !o.Rehearse {
		has, err := sqliteHasTables(ctx, o.DBPath)
		if err != nil {
			return res, err
		}
		exists = has
		if has && !o.Replace {
			res.add("대상 상태", Fail, "대상 DB 가 비어 있지 않습니다. 기존 DB 를 옆으로 옮기고 덮으려면 --replace 를 주세요(삭제하지 않고 .pre-restore 로 보존합니다)")
			return res, nil
		}
		res.add("대상 상태", Pass, map[bool]string{true: "기존 DB 는 .pre-restore 로 보존합니다", false: "대상이 비어 있습니다"}[has])
	}

	// 임시 작업 위치: 리허설은 임시 디렉터리, 복구는 대상 DB 와 같은 디렉터리(같은 파일 시스템이라 이름 바꿈이 원자적이다).
	var work string
	var err error
	if o.Rehearse {
		if o.WorkDir != "" {
			work = o.WorkDir
			if ents, rerr := os.ReadDir(work); rerr == nil && len(ents) > 0 {
				return res, errors.New("--work 디렉터리가 비어 있지 않습니다")
			}
			err = os.MkdirAll(work, 0o700)
		} else {
			work, err = os.MkdirTemp(o.TmpRoot, "plrehearsal-")
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(work, RehearsalMarker), []byte("restore --rehearse\n"), 0o600)
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(o.DBPath), 0o755); err == nil {
			work, err = os.MkdirTemp(filepath.Dir(o.DBPath), ".restore-")
		}
	}
	if err != nil {
		return res, err
	}
	keepWork := o.Rehearse && o.WorkDir != ""
	res.WorkDB, res.WorkAnchor = "", ""
	defer func() {
		if !keepWork {
			_ = os.RemoveAll(work)
		}
	}()

	dbTmp, anchorTmp, err := Materialize(o.SetDir, m, o.Key, work)
	if err != nil {
		return res, err
	}
	// 앵커 사본이 세트에 없으면 빈 파일을 둔다(대조는 "검증 안 됨"이 된다).
	if anchorTmp == "" {
		anchorTmp = filepath.Join(work, AnchorName)
		if err := os.WriteFile(anchorTmp, nil, 0o600); err != nil {
			return res, err
		}
	}

	// 복구본 점검(임시 파일에서): 무결성·외래 키·번호·체인·머리·앵커·불변식.
	rep, err := Inspect(ctx, dbTmp, anchorTmp, o.AnchorKey)
	if err != nil {
		return res, err
	}
	vrep := VerifyReport{OK: true}
	vrep.addDBChecks(m, rep, o.AnchorKey)
	for _, c := range vrep.Checks {
		res.add("복구본: "+c.Item, c.Status, c.Detail)
	}
	if !vrep.OK {
		return res, nil
	}
	if len(rep.Invariants) > 0 {
		res.Invariants = rep.Invariants
		res.Notes = append(res.Notes, "복구본에 업무 불변식 위반이 있습니다(복구는 진행했습니다). 복구 뒤 해당 업무 데이터를 점검하세요: "+strings.Join(rep.Invariants, "; "))
	}

	// 손실 구간: 이전 앵커 파일과 현재 DB 의 머리를 복구 시점과 비교한다.
	if o.AnchorPath != "" {
		if lr, lerr := AnchorLossRange(o.AnchorPath, m.ChainSeq); lerr == nil && lr != nil {
			res.Loss = lr
		}
	}
	if !o.Rehearse && exists {
		if head := sqliteHeadSeq(ctx, o.DBPath); head > m.ChainSeq {
			res.LostRecords = head - m.ChainSeq
		}
	}
	switch {
	case res.Loss != nil:
		res.add("손실 구간", Warn, fmt.Sprintf("복구로 사라지는 감사 기록: %d번~%d번(%s ~ %s), 앵커 %d줄. 이전 앵커 파일은 보존합니다", res.Loss.FromSeq, res.Loss.ToSeq, res.Loss.FromAt, res.Loss.ToAt, res.Loss.Lines))
	case res.LostRecords > 0:
		res.add("손실 구간", Warn, fmt.Sprintf("현재 DB 의 마지막 기록이 복구본보다 %d건 앞서 있습니다(앵커로 범위를 확인하지 못했습니다)", res.LostRecords))
	default:
		res.add("손실 구간", Pass, "없음")
	}

	// 서버 정지 확인과 원자적 교체는 마지막에 한다(앞 단계가 모두 통과한 뒤에만 운영 파일에 손댄다).
	if !o.Rehearse && exists {
		if used, uerr := SQLiteInUse(ctx, o.DBPath); uerr != nil {
			return res, uerr
		} else if used {
			res.add("서버 정지", Fail, "서버나 다른 프로세스가 사용 중입니다. 서버와 erp-mcp 를 모두 멈춘 뒤 다시 실행하세요")
			return res, nil
		}
		res.add("서버 정지", Pass, "다른 프로세스가 사용하지 않습니다")
	}

	// 임시 DB 에 복구 사실을 기록한다: 세션(과 선택적으로 토큰) 폐기, backup_runs, 감사 system.restore.
	tdb, err := dbutil.Open(dbTmp)
	if err != nil {
		return res, err
	}
	if err := finishRestore(ctx, tdb, o, m, &res); err != nil {
		tdb.Close()
		return res, err
	}
	_ = tdb.Close()
	rep2, err := Inspect(ctx, dbTmp, anchorTmp, o.AnchorKey)
	if err != nil {
		return res, err
	}
	if rep2.ChainOK {
		res.add("감사 체인(복구 기록 후)", Pass, fmt.Sprintf("%d번까지", rep2.ChainSeq))
	} else {
		res.add("감사 체인(복구 기록 후)", Fail, rep2.ChainWhy)
	}
	if o.Rehearse && o.WorkDir != "" {
		res.WorkDB, res.WorkAnchor = dbTmp, anchorTmp
	}
	if !res.OK || o.Rehearse {
		return res, nil
	}

	// 원자적 교체. 기존 파일은 삭제하지 않고 옆으로 보존한다.
	suffix := ".pre-restore-" + o.now().UTC().Format(setTimeFmt)
	var moved [][2]string
	rollback := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i][1], moved[i][0])
		}
	}
	mv := func(from string) error {
		if _, err := os.Stat(from); err != nil {
			return nil
		}
		to := from + suffix
		if err := os.Rename(from, to); err != nil {
			return err
		}
		moved = append(moved, [2]string{from, to})
		res.PreRestore = append(res.PreRestore, to)
		return nil
	}
	for _, ext := range []string{"", "-wal", "-shm"} {
		if err := mv(o.DBPath + ext); err != nil {
			rollback()
			return res, fmt.Errorf("기존 DB 를 옮기지 못했습니다: %w", err)
		}
	}
	if o.AnchorPath != "" {
		if err := mv(o.AnchorPath); err != nil {
			rollback()
			return res, fmt.Errorf("기존 앵커 파일을 옮기지 못했습니다: %w", err)
		}
	}
	if err := os.Rename(dbTmp, o.DBPath); err != nil {
		rollback()
		return res, fmt.Errorf("복구본을 제자리에 놓지 못했습니다(기존 파일은 되돌렸습니다): %w", err)
	}
	if o.AnchorPath != "" {
		if err := placeFile(anchorTmp, o.AnchorPath); err != nil {
			res.add("앵커 파일 배치", Fail, "앵커 사본을 놓지 못했습니다: "+err.Error()+". 보존 파일: "+strings.Join(res.PreRestore, ", "))
			keepWork = true
			return res, nil
		}
	}
	return res, nil
}

// placeFile 은 src 를 dst 로 옮긴다(파일 시스템이 다르면 복사 뒤 지운다). 권한 0600.
func placeFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return os.Chmod(dst, 0o600)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// finishRestore 는 복구된 DB 에 세션 폐기·backup_runs·감사 기록을 한 트랜잭션으로 남긴다.
func finishRestore(ctx context.Context, db *sql.DB, o RestoreOptions, m Manifest, res *RestoreResult) error {
	now := o.now().UTC().Format(time.RFC3339)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 백업 뒤에 폐기된 세션이 되살아나 로그인 상태가 되지 않도록 전부 폐기한다(D6). 토큰은 옵션.
	r, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`, now)
	if err != nil {
		return fmt.Errorf("세션 폐기 실패: %w", err)
	}
	res.SessionsRevoked, _ = r.RowsAffected()
	if o.RevokeTokens {
		r, err := tx.ExecContext(ctx, `UPDATE personal_tokens SET revoked_at = ? WHERE revoked_at IS NULL`, now)
		if err != nil {
			return fmt.Errorf("토큰 폐기 실패: %w", err)
		}
		res.TokensRevoked, _ = r.RowsAffected()
	}
	res.add("세션 폐기", Pass, fmt.Sprintf("세션 %d개 폐기, 토큰 %d개 폐기", res.SessionsRevoked, res.TokensRevoked))

	kind, action := "restore", "system.restore"
	if o.Rehearse {
		kind, action = "rehearsal", "system.restore_rehearsal"
	}
	if hasTable(ctx, tx, "backup_runs") {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs (kind, started_at, finished_at, result, set_name, db_kind, bytes, chain_seq, error_code, tool_version, warnings)
			VALUES (?, ?, ?, 'success', ?, ?, ?, ?, '', ?, ?)`, kind, now, now, filepath.Base(o.SetDir), m.DBKind, totalBytes(m), m.ChainSeq, ToolVersion, strings.Join(res.Invariants, "; ")); err != nil {
			return fmt.Errorf("backup_runs 기록 실패: %w", err)
		}
	}
	detail := fmt.Sprintf("세트 %s, 백업 시각 %s, 체인 머리 %d번", filepath.Base(o.SetDir), m.CreatedAt, m.ChainSeq)
	switch {
	case res.Loss != nil:
		detail += fmt.Sprintf(", 손실 구간 %d~%d번(%s ~ %s, %d줄)", res.Loss.FromSeq, res.Loss.ToSeq, res.Loss.FromAt, res.Loss.ToAt, res.Loss.Lines)
	case res.LostRecords > 0:
		detail += fmt.Sprintf(", 손실 기록 %d건", res.LostRecords)
	default:
		detail += ", 손실 구간 없음"
	}
	detail += fmt.Sprintf(", 세션 %d개 폐기", res.SessionsRevoked)
	if len(res.Invariants) > 0 {
		detail += ", 불변식 경고: " + strings.Join(res.Invariants, "; ")
	}
	store := audit.New(db)
	if err := store.Record(ctx, tx, caudit.Entry{Actor: Actor, Action: action, TargetType: caudit.TargetSystem, TargetID: filepath.Base(o.SetDir),
		Result: caudit.ResultSuccess, Detail: detail,
		Diff: []caudit.DiffItem{{Key: "chainSeq", New: fmt.Sprint(m.ChainSeq)}, {Key: "sessionsRevoked", New: fmt.Sprint(res.SessionsRevoked)}}}); err != nil {
		return err
	}
	return tx.Commit()
}

func totalBytes(m Manifest) int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

func hasTable(ctx context.Context, tx *sql.Tx, name string) bool {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&n); err == nil {
		return n > 0
	}
	// PG
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`, name).Scan(&n); err == nil {
		return n > 0
	}
	return false
}

// restorePG 는 pg_restore 로 비어 있는 데이터베이스에 복구한다. 리허설은 임시 데이터베이스(plrehearsal_<무작위>)를 만들고 끝나면 지운다.
func restorePG(ctx context.Context, o RestoreOptions, m Manifest, res RestoreResult) (RestoreResult, error) {
	tool, err := findPGTool(o.PGDump, "pg_restore")
	if err != nil {
		return res, errors.New(strings.Replace(ErrNoPGTool.Error(), "pg_dump", "pg_restore", 1))
	}
	base, pw, err := splitURL(o.DBURL)
	if err != nil {
		return res, err
	}
	target := base
	var adminDB *sql.DB
	var dropName string
	if o.Rehearse {
		adminDB, err = openPG(ctx, o.DBURL, o.AllowInsecure)
		if err != nil {
			return res, fmt.Errorf("PG 에 연결할 수 없습니다: %w", err)
		}
		defer adminDB.Close()
		dropName = "plrehearsal_" + randHex(6)
		if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE `+dropName); err != nil {
			return res, fmt.Errorf("리허설 데이터베이스를 만들 수 없습니다: %w", err)
		}
		defer func() {
			_, _ = adminDB.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dropName+` WITH (FORCE)`)
		}()
		u, _ := url.Parse(base)
		u.Path = "/" + dropName
		target = u.String()
	}
	targetWithPw := target
	if pw != "" {
		u, _ := url.Parse(target)
		u.User = url.UserPassword(u.User.Username(), pw)
		targetWithPw = u.String()
	}
	tdb, err := openPG(ctx, targetWithPw, o.AllowInsecure)
	if err != nil {
		return res, fmt.Errorf("대상 데이터베이스에 연결할 수 없습니다: %w", err)
	}
	defer tdb.Close()
	empty, err := pgDatabaseEmpty(ctx, tdb)
	if err != nil {
		return res, err
	}
	if !empty {
		res.add("대상 상태", Fail, "대상 데이터베이스가 비어 있지 않습니다. PG 복구는 비어 있는 새 데이터베이스에만 합니다(덮어쓰기 없음)")
		return res, nil
	}
	res.add("대상 상태", Pass, "대상 데이터베이스가 비어 있습니다")

	work, err := os.MkdirTemp(o.TmpRoot, "plrestore-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	dump, anchorTmp, err := Materialize(o.SetDir, m, o.Key, work)
	if err != nil {
		return res, err
	}
	if anchorTmp == "" {
		anchorTmp = filepath.Join(work, AnchorName)
		_ = os.WriteFile(anchorTmp, nil, 0o600)
	}
	if err := runPG(ctx, tool, pw, "--no-owner", "--no-acl", "-d", target, dump); err != nil {
		return res, err
	}
	rep, err := InspectPG(ctx, tdb, anchorTmp, o.AnchorKey)
	if err != nil {
		return res, err
	}
	vrep := VerifyReport{OK: true}
	vrep.addDBChecks(m, rep, o.AnchorKey)
	for _, c := range vrep.Checks {
		res.add("복구본: "+c.Item, c.Status, c.Detail)
	}
	if !vrep.OK {
		res.Notes = append(res.Notes, "복구본 점검에 실패했습니다. 이 데이터베이스는 사용하지 말고 지운 뒤 다시 만드세요")
		return res, nil
	}
	if len(rep.Invariants) > 0 {
		res.Invariants = rep.Invariants
		res.Notes = append(res.Notes, "복구본에 업무 불변식 위반이 있습니다(복구는 진행했습니다). 복구 뒤 해당 업무 데이터를 점검하세요: "+strings.Join(rep.Invariants, "; "))
	}
	if err := finishRestorePG(ctx, tdb, o, m, &res); err != nil {
		return res, err
	}
	if !o.Rehearse && o.AnchorPath != "" {
		suffix := ".pre-restore-" + o.now().UTC().Format(setTimeFmt)
		if _, err := os.Stat(o.AnchorPath); err == nil {
			if err := os.Rename(o.AnchorPath, o.AnchorPath+suffix); err != nil {
				return res, err
			}
			res.PreRestore = append(res.PreRestore, o.AnchorPath+suffix)
		}
		if err := placeFile(anchorTmp, o.AnchorPath); err != nil {
			return res, err
		}
	}
	return res, nil
}

func finishRestorePG(ctx context.Context, db *sql.DB, o RestoreOptions, m Manifest, res *RestoreResult) error {
	now := o.now().UTC().Format(time.RFC3339)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`, now)
	if err != nil {
		return fmt.Errorf("세션 폐기 실패: %w", err)
	}
	res.SessionsRevoked, _ = r.RowsAffected()
	if o.RevokeTokens {
		r, err := tx.ExecContext(ctx, `UPDATE personal_tokens SET revoked_at = ? WHERE revoked_at IS NULL`, now)
		if err != nil {
			return err
		}
		res.TokensRevoked, _ = r.RowsAffected()
	}
	res.add("세션 폐기", Pass, fmt.Sprintf("세션 %d개 폐기, 토큰 %d개 폐기", res.SessionsRevoked, res.TokensRevoked))
	kind, action := "restore", "system.restore"
	if o.Rehearse {
		kind, action = "rehearsal", "system.restore_rehearsal"
	}
	var n int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'backup_runs'`).Scan(&n) == nil && n > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs (kind, started_at, finished_at, result, set_name, db_kind, bytes, chain_seq, error_code, tool_version, warnings)
			VALUES (?, ?, ?, 'success', ?, ?, ?, ?, '', ?, ?)`, kind, now, now, filepath.Base(o.SetDir), m.DBKind, totalBytes(m), m.ChainSeq, ToolVersion, strings.Join(res.Invariants, "; ")); err != nil {
			return fmt.Errorf("backup_runs 기록 실패: %w", err)
		}
	}
	detail := fmt.Sprintf("세트 %s, 백업 시각 %s, 체인 머리 %d번, 세션 %d개 폐기", filepath.Base(o.SetDir), m.CreatedAt, m.ChainSeq, res.SessionsRevoked)
	if err := audit.New(db).Record(ctx, tx, caudit.Entry{Actor: Actor, Action: action, TargetType: caudit.TargetSystem, TargetID: filepath.Base(o.SetDir),
		Result: caudit.ResultSuccess, Detail: detail}); err != nil {
		return err
	}
	return tx.Commit()
}

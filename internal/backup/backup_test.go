package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/backup"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

var anchorKey = []byte("backup-test-anchor-key-0123456789")

type fixture struct {
	t      *testing.T
	root   string
	db     *sql.DB
	dbPath string
	anchor string
	store  *audit.Store
	a      *audit.Anchor
	dir    string
	clock  time.Time
	userID int64
}

// newFixture 는 마이그레이션을 적용한 SQLite DB, 앵커, 업무 행(조직·사용자·거래처·로고 BLOB·세션·토큰)을 만든다.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, dbPath: filepath.Join(root, "data", "erp.db"), anchor: filepath.Join(root, "anchor", "audit.anchor"),
		dir: filepath.Join(root, "backups"), clock: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	if err := os.MkdirAll(filepath.Dir(f.dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.anchor), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := dbutil.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.closeDB() })
	f.db = db
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	f.store = audit.New(db)
	if f.a, err = audit.NewAnchor(f.store, f.anchor, anchorKey); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO departments (id, name) VALUES (1, '구매팀')`)
	f.exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'a@example.com', '직원', 'staff', 1)`)
	f.userID = 1
	f.exec(`INSERT INTO vendors (name, business_no) VALUES ('한빛상사', '123-45-67890')`)
	blob := make([]byte, 300_000)
	rand.New(rand.NewSource(7)).Read(blob)
	f.exec(`INSERT INTO company_assets (kind, content, content_type, size, sha256, width, height, uploaded_by, uploaded_at, current) VALUES ('logo', ?, 'image/png', ?, 'x', 10, 10, 1, '2026-11-01T00:00:00Z', 1)`, blob, len(blob))
	f.exec(`INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at) VALUES ('s1', 1, 'c', '2026-11-13T00:00:00Z', '2099-01-01T00:00:00Z', '2026-11-13T00:00:00Z')`)
	f.exec(`INSERT INTO personal_tokens (user_id, name, token_prefix, token_hash, created_at, expires_at) VALUES (1, 't', 'plp_', 'h1', '2026-11-13T00:00:00Z', '2099-01-01T00:00:00Z')`)
	f.addAudit(40)
	f.stamp()
	return f
}

func (f *fixture) closeDB() {
	if f.db != nil {
		_ = f.db.Close()
		f.db = nil
	}
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) addAudit(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		tx, err := f.db.Begin()
		if err != nil {
			f.t.Fatal(err)
		}
		if err := f.store.Record(context.Background(), tx, caudit.Entry{Actor: backup.Actor, Action: "system.backup", TargetType: caudit.TargetSystem, TargetID: fmt.Sprintf("n%d", i),
			Result: caudit.ResultSuccess, Detail: "시험 기록"}); err != nil {
			f.t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) stamp() {
	f.t.Helper()
	if _, err := f.a.Stamp(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) opts() backup.Options {
	f.clock = f.clock.Add(time.Minute)
	now := f.clock
	return backup.Options{Kind: "sqlite", DBPath: f.dbPath, AnchorPath: f.anchor, AnchorKey: anchorKey, Dir: f.dir, Keep: 14, AppVersion: "test",
		Now: func() time.Time { return now }}
}

func (f *fixture) restoreOpts(set string) backup.RestoreOptions {
	return backup.RestoreOptions{SetDir: set, Kind: "sqlite", DBPath: f.dbPath, AnchorPath: f.anchor, AnchorKey: anchorKey, MaxMigration: migrations.Latest(),
		TmpRoot: f.root, Now: func() time.Time { return f.clock }}
}

// digest 는 표별 내용 해시다(복구 전후 비교). 복구가 일부러 바꾸는 표(세션·기록 표·감사)는 제외한다.
func digest(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, tb := range tables {
		switch tb {
		case "sessions", "backup_runs", "audit_logs", "personal_tokens":
			continue
		}
		r, err := db.Query(`SELECT * FROM ` + tb)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		h := sha256.New()
		n := 0
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			fmt.Fprintf(h, "%v\n", vals)
			n++
		}
		r.Close()
		out[tb] = fmt.Sprintf("%d:%s", n, hex.EncodeToString(h.Sum(nil)))
	}
	return out
}

func fileHash(t *testing.T, p string) string {
	t.Helper()
	h, _, err := backup.FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestEncryptRoundTripAndTamper(t *testing.T) {
	key, err := backup.DeriveKey("a-sufficiently-long-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.DeriveKey("short"); err == nil {
		t.Fatal("짧은 키는 거부여야 함")
	}
	dir := t.TempDir()
	for _, size := range []int{0, 1, 64*1024 - 1, 64 * 1024, 64*1024 + 1, 300_000} {
		plain := make([]byte, size)
		rand.New(rand.NewSource(int64(size))).Read(plain)
		src, enc, dec := filepath.Join(dir, "p"), filepath.Join(dir, "e"), filepath.Join(dir, "d")
		_ = os.Remove(enc)
		_ = os.Remove(dec)
		if err := os.WriteFile(src, plain, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := backup.EncryptFile(src, enc, key); err != nil {
			t.Fatal(err)
		}
		if err := backup.DecryptFile(enc, dec, key); err != nil {
			t.Fatalf("크기 %d 복호화: %v", size, err)
		}
		got, _ := os.ReadFile(dec)
		if !bytes.Equal(got, plain) {
			t.Fatalf("크기 %d: 왕복이 다름", size)
		}
		if size >= 100 && bytes.Contains(mustRead(t, enc), plain[:100]) {
			t.Fatalf("크기 %d: 암호문에 평문이 보임", size)
		}
	}
	// 틀린 키, 변조, 잘림은 모두 같은 오류이고 출력 파일을 남기지 않는다.
	plain := bytes.Repeat([]byte("abc"), 100_000)
	src, enc := filepath.Join(dir, "q"), filepath.Join(dir, "q.enc")
	_ = os.WriteFile(src, plain, 0o600)
	if err := backup.EncryptFile(src, enc, key); err != nil {
		t.Fatal(err)
	}
	other, _ := backup.DeriveKey("another-long-secret-value")
	raw := mustRead(t, enc)
	cases := map[string]struct {
		file []byte
		key  []byte
	}{
		"틀린 키": {raw, other},
		"비트 변조": func() struct {
			file []byte
			key  []byte
		} {
			b := append([]byte(nil), raw...)
			b[len(b)/2] ^= 0x01
			return struct {
				file []byte
				key  []byte
			}{b, key}
		}(),
		"뒤 잘림":     {raw[:len(raw)-100], key},
		"청크 경계 잘림": {raw[:14+4+65536+16], key},
	}
	for name, c := range cases {
		bad, out := filepath.Join(dir, "bad.enc"), filepath.Join(dir, "bad.out")
		_ = os.Remove(out)
		_ = os.WriteFile(bad, c.file, 0o600)
		if err := backup.DecryptFile(bad, out, c.key); err != backup.ErrWrongKey {
			t.Errorf("%s: 오류 %v", name, err)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: 실패했는데 출력 파일이 남음", name)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// AC-1820: 서버가 쓰는 중에 만든 백업이 점검을 통과하고 체인 머리가 스냅샷과 같다. 단순 파일 복사가 아니라 VACUUM INTO 이다(소스 검사).
func TestBackupConsistentWhileWriting(t *testing.T) {
	f := newFixture(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := f.db.Begin()
			if err != nil {
				return
			}
			if err := f.store.Record(context.Background(), tx, caudit.Entry{Actor: backup.Actor, Action: "system.backup", TargetType: caudit.TargetSystem, TargetID: fmt.Sprintf("w%d", i),
				Result: caudit.ResultSuccess}); err != nil {
				_ = tx.Rollback()
				return
			}
			_ = tx.Commit()
		}
	}()
	res, err := backup.Run(context.Background(), f.opts())
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := backup.VerifySet(context.Background(), res.Path, backup.VerifyOptions{AnchorKey: anchorKey})
	if err != nil || !rep.OK {
		t.Fatalf("검증: %v %+v", err, rep.Checks)
	}
	if rep.Report.ChainSeq != res.Manifest.ChainSeq || rep.Report.ChainHash != res.Manifest.ChainHash {
		t.Fatalf("체인 머리가 다름: 스냅샷 %d, 매니페스트 %d", rep.Report.ChainSeq, res.Manifest.ChainSeq)
	}
	src := string(mustRead(t, "backup.go"))
	if !strings.Contains(src, "VACUUM INTO") {
		t.Fatal("스냅샷은 VACUUM INTO 여야 함")
	}
	for _, bad := range []string{"io.Copy(", "os.ReadFile(o.DBPath", "CopyFile"} {
		if strings.Contains(src, bad) {
			t.Fatalf("백업 소스에 단순 복사가 보임: %s", bad)
		}
	}
	for _, name := range []string{backup.ManifestName, backup.SumsName, backup.DBFileSQLite, backup.AnchorName} {
		st, err := os.Stat(filepath.Join(res.Path, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s 권한: %v %v", name, err, st)
		}
	}
	if st, _ := os.Stat(res.Path); st.Mode().Perm() != 0o700 {
		t.Errorf("세트 디렉터리 권한 %v", st.Mode().Perm())
	}
}

// AC-1821: 백업 뒤에 앵커 줄이 늘어도 사본은 스냅샷 대장에 대응하는 접두부만 갖고, 그 사본과 스냅샷으로 앵커 대조가 통과한다.
func TestAnchorPrefixOnly(t *testing.T) {
	f := newFixture(t)
	f.addAudit(3)
	f.stamp()
	res, err := backup.Run(context.Background(), f.opts())
	if err != nil {
		t.Fatal(err)
	}
	live := strings.Count(string(mustRead(t, f.anchor)), "\n")
	f.addAudit(2)
	f.stamp()
	if after := strings.Count(string(mustRead(t, f.anchor)), "\n"); after != live+1 {
		t.Fatalf("전제: 줄이 하나 늘어야 함 %d→%d", live, after)
	}
	copyLines := strings.Count(string(mustRead(t, filepath.Join(res.Path, backup.AnchorName))), "\n")
	if copyLines != res.Manifest.AnchorLines || copyLines != live {
		t.Fatalf("사본 줄 수 %d, 매니페스트 %d, 백업 시점 파일 %d", copyLines, res.Manifest.AnchorLines, live)
	}
	rep, err := backup.VerifySet(context.Background(), res.Path, backup.VerifyOptions{AnchorKey: anchorKey})
	if err != nil || !rep.OK {
		t.Fatalf("%v %+v", err, rep.Checks)
	}
	for _, c := range rep.Checks {
		if c.Item == "앵커 대조" && c.Status != backup.Pass {
			t.Fatalf("앵커 대조 %s: %s", c.Status, c.Detail)
		}
	}
}

// AC-1822: 손상된 DB 에서는 세트가 만들어지지 않고 원인이 보고된다. .partial 이 세트로 나타나지 않는다.
func TestCorruptDBIsNotBackedUp(t *testing.T) {
	f := newFixture(t)
	f.exec(`DROP TRIGGER trg_audit_no_update`)
	f.exec(`UPDATE audit_logs SET detail = '변조' WHERE seq = 5`)
	_, err := backup.Run(context.Background(), f.opts())
	if err == nil || !strings.Contains(err.Error(), "감사 체인") {
		t.Fatalf("체인 변조는 백업 거부여야 함: %v", err)
	}
	sets, _ := backup.List(f.dir)
	if len(sets) != 0 {
		t.Fatalf("세트가 만들어짐: %+v", sets)
	}
	ents, _ := os.ReadDir(f.dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".partial") {
			t.Fatalf(".partial 이 남음: %s", e.Name())
		}
	}
	// 파일 자체가 DB 가 아닌 경우.
	g := newFixture(t)
	g.closeDB()
	if err := os.WriteFile(g.dbPath, bytes.Repeat([]byte("not a database "), 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Run(context.Background(), g.opts()); err == nil {
		t.Fatal("DB 가 아닌 파일은 백업 거부여야 함")
	}
}

// AC-1823: 보존은 새 백업이 검증 성공한 뒤에만, 실패한 실행은 아무것도 지우지 않는다, 마지막 검증 성공 세트는 보호된다.
func TestRetention(t *testing.T) {
	f := newFixture(t)
	var names []string
	for i := 0; i < 4; i++ {
		o := f.opts()
		o.Keep = 2
		res, err := backup.Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, res.Name)
	}
	sets, _ := backup.List(f.dir)
	if len(sets) != 2 || sets[0].Name != names[2] || sets[1].Name != names[3] {
		t.Fatalf("최신 2개만 남아야 함: %v (만든 순서 %v)", setNames(sets), names)
	}
	if _, err := backup.Run(context.Background(), func() backup.Options { o := f.opts(); o.Keep = 1; return o }()); err == nil {
		t.Fatal("--keep 1 은 거부여야 함")
	}
	// 실패한 실행은 지우지 않는다.
	f.exec(`DROP TRIGGER trg_audit_no_update`)
	f.exec(`UPDATE audit_logs SET detail = '변조' WHERE seq = 3`)
	o := f.opts()
	o.Keep = 2
	if _, err := backup.Run(context.Background(), o); err == nil {
		t.Fatal("깨진 DB 백업은 실패여야 함")
	}
	if after, _ := backup.List(f.dir); len(after) != 2 {
		t.Fatalf("실패한 실행이 세트를 지움: %v", setNames(after))
	}
}

func setNames(s []backup.SetInfo) []string {
	var n []string
	for _, x := range s {
		n = append(n, x.Name)
	}
	sort.Strings(n)
	return n
}

// AC-1824: 암호화 세트는 키 없이 읽을 수 없고, 올바른 키로 검증·복구되며, 틀린 키는 내용을 드러내지 않고 거부된다.
func TestEncryptedBackup(t *testing.T) {
	f := newFixture(t)
	key, _ := backup.DeriveKey("a-sufficiently-long-secret")
	o := f.opts()
	o.Encrypt, o.Key = true, key
	res, err := backup.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Manifest.Encrypted || res.Manifest.KeyFingerprint == "" || !strings.HasSuffix(res.Manifest.DBFile, ".enc") {
		t.Fatalf("매니페스트: %+v", res.Manifest)
	}
	if bytes.Contains(mustRead(t, filepath.Join(res.Path, res.Manifest.DBFile)), []byte("SQLite format")) {
		t.Fatal("암호화 세트에 평문 SQLite 머리가 보임")
	}
	if _, err := os.Stat(filepath.Join(res.Path, backup.DBFileSQLite)); err == nil {
		t.Fatal("평문 DB 가 남아 있음")
	}
	check := func(k []byte) backup.VerifyReport {
		r, err := backup.VerifySet(context.Background(), res.Path, backup.VerifyOptions{Key: k, AnchorKey: anchorKey})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := check(nil); r.OK || !strings.Contains(r.FirstFailure(), "PL_BACKUP_KEY") {
		t.Fatalf("키 없이 통과하면 안 됨: %+v", r.Checks)
	}
	wrong, _ := backup.DeriveKey("another-long-secret-value")
	if r := check(wrong); r.OK || !strings.Contains(r.FirstFailure(), "키 지문") {
		t.Fatalf("틀린 키로 통과하면 안 됨: %+v", r.Checks)
	}
	if r := check(key); !r.OK {
		t.Fatalf("올바른 키: %+v", r.Checks)
	}
	if _, err := backup.Run(context.Background(), func() backup.Options { x := f.opts(); x.Encrypt = true; return x }()); err == nil {
		t.Fatal("키 없이 --encrypt 는 거부여야 함")
	}
}

// AC-1830: 백업 → 삭제 → 복구하면 표별 내용이 같고 감사 체인·앵커 대조가 통과하며 마지막 번호·해시가 매니페스트와 같다.
func TestRestoreRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.addAudit(300)
	f.stamp()
	want := digest(t, f.dbPath)
	res, err := backup.Run(context.Background(), f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.closeDB()
	anchorBefore := mustRead(t, f.anchor)
	for _, p := range []string{f.dbPath, f.dbPath + "-wal", f.dbPath + "-shm", f.anchor} {
		_ = os.Remove(p)
	}
	rr, err := backup.Restore(context.Background(), f.restoreOpts(res.Path))
	if err != nil || !rr.OK {
		t.Fatalf("복구: %v\n%+v", err, rr.Checks)
	}
	if got := digest(t, f.dbPath); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("표 내용이 다름\n원본 %v\n복구 %v", want, got)
	}
	if !bytes.Equal(mustRead(t, f.anchor), anchorBefore) {
		t.Fatal("앵커 사본이 백업 시점 파일과 다름")
	}
	rep, err := backup.Inspect(context.Background(), f.dbPath, f.anchor, anchorKey)
	if err != nil || !rep.ChainOK || rep.Anchor.State != "ok" {
		t.Fatalf("복구 후 점검: %v %+v", err, rep)
	}
	if rep.ChainSeq != res.Manifest.ChainSeq+1 { // 복구 기록(system.restore) 한 건이 덧붙는다
		t.Fatalf("체인 머리 %d, 기대 %d(백업 %d + 복구 기록 1)", rep.ChainSeq, res.Manifest.ChainSeq+1, res.Manifest.ChainSeq)
	}
	// system.restore 감사가 마지막 기록이다.
	db, _ := dbutil.Open(f.dbPath)
	defer db.Close()
	var act, actor, ch string
	_ = db.QueryRow(`SELECT action, actor_role, channel FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&act, &actor, &ch)
	if act != "system.restore" || actor != "system" || ch != "cli" {
		t.Fatalf("마지막 감사 %s %s %s", act, actor, ch)
	}
}

// AC-1831: 시작하지 않는 조건들과 --replace 의 보존, 중간 실패 시 기존 DB 보존.
func TestRestoreSafety(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	res, err := backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.addAudit(5)
	f.stamp()
	hold := f.db // 서버처럼 연결을 연 채로 둔다
	before := fileHash(t, f.dbPath)

	// 비어 있지 않은 대상은 --replace 없이 거부(서버가 켜져 있어도 먼저 이 점검에서 걸린다).
	r, err := backup.Restore(ctx, f.restoreOpts(res.Path))
	if err != nil || r.OK || !hasFail(r.Checks, "대상 상태") {
		t.Fatalf("--replace 없는 거부: %v %+v", err, r.Checks)
	}
	// 서버 사용 중이면 --replace 여도 거부.
	o := f.restoreOpts(res.Path)
	o.Replace = true
	r, err = backup.Restore(ctx, o)
	if err != nil || r.OK || !hasFail(r.Checks, "서버 정지") {
		t.Fatalf("사용 중 거부: %v %+v", err, r.Checks)
	}
	_ = hold.Ping()
	// 서버를 멈춘 뒤에는 성공하고, 기존 DB·앵커는 .pre-restore 로 보존된다.
	f.closeDB()
	if fileHash(t, f.dbPath) == "" {
		t.Fatal("전제")
	}
	_ = before
	preAnchor := mustRead(t, f.anchor)
	r, err = backup.Restore(ctx, o)
	if err != nil || !r.OK {
		t.Fatalf("복구: %v %+v", err, r.Checks)
	}
	if len(r.PreRestore) < 2 {
		t.Fatalf("보존 파일: %v", r.PreRestore)
	}
	var sawAnchor bool
	for _, p := range r.PreRestore {
		if strings.HasPrefix(p, f.anchor+".pre-restore-") && bytes.Equal(mustRead(t, p), preAnchor) {
			sawAnchor = true
		}
	}
	if !sawAnchor {
		t.Fatalf("이전 앵커 파일이 보존되지 않음: %v", r.PreRestore)
	}
}

func hasFail(cs []backup.Check, item string) bool {
	for _, c := range cs {
		if strings.Contains(c.Item, item) && c.Status == backup.Fail {
			return true
		}
	}
	return false
}

func TestRestoreRefusesBadSets(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	res, err := backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.closeDB()
	pre := fileHash(t, f.dbPath)

	// 변조된 세트(해시 불일치)
	tampered := filepath.Join(f.root, "tampered")
	copyDir(t, res.Path, tampered)
	dbf := filepath.Join(tampered, backup.DBFileSQLite)
	b := mustRead(t, dbf)
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(dbf, b, 0o600); err != nil {
		t.Fatal(err)
	}
	o := f.restoreOpts(tampered)
	o.Replace = true
	if r, err := backup.Restore(ctx, o); err != nil || r.OK || !hasFail(r.Checks, "해시") {
		t.Fatalf("변조 세트 거부: %v %+v", err, r.Checks)
	}
	// 매니페스트를 고쳐 해시를 맞춰도(SHA256SUMS 와 어긋남) 거부.
	forged := filepath.Join(f.root, "forged")
	copyDir(t, res.Path, forged)
	// 파일 배치에 기대지 않고 의미 있는 내용을 바꾼다: 세트 사본의 감사 기록 한 행을 고친다(추가 전용 트리거는 사본에서만 지운다).
	fdb, err := sql.Open("sqlite", "file:"+filepath.Join(forged, backup.DBFileSQLite))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fdb.Exec(`DROP TRIGGER trg_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if r, err := fdb.Exec(`UPDATE audit_logs SET detail = detail || ' 변조' WHERE seq = (SELECT MIN(seq) FROM audit_logs)`); err != nil {
		t.Fatal(err)
	} else if n, _ := r.RowsAffected(); n != 1 {
		t.Fatalf("위조 대상 감사 행이 없음: %d", n)
	}
	if err := fdb.Close(); err != nil {
		t.Fatal(err)
	}
	m, _ := backup.ReadManifest(forged)
	h, size, _ := backup.FileSHA256(filepath.Join(forged, backup.DBFileSQLite))
	m.Files[backup.DBFileSQLite] = backup.FileInfo{SHA256: h, Size: size}
	if err := backup.WriteSums(forged, m); err != nil { // 매니페스트와 SHA256SUMS 를 함께 다시 써도 DB 점검(무결성·체인)에서 걸린다
		t.Fatal(err)
	}
	o = f.restoreOpts(forged)
	o.Replace = true
	if r, err := backup.Restore(ctx, o); err != nil || r.OK || !hasFail(r.Checks, "감사 체인") {
		t.Fatalf("위조 세트는 DB 점검(감사 체인)에서 거부되어야 함: %v %+v", err, r.Checks)
	}
	// 더 새로운 버전의 백업
	o = f.restoreOpts(res.Path)
	o.Replace = true
	o.MaxMigration = "0001"
	if r, err := backup.Restore(ctx, o); err != nil || r.OK || !hasFail(r.Checks, "마이그레이션 번호") {
		t.Fatalf("새 버전 거부: %v %+v", err, r.Checks)
	}
	// DB 종류 불일치
	o = f.restoreOpts(res.Path)
	o.Replace = true
	o.Kind = "postgres"
	if r, err := backup.Restore(ctx, o); err != nil || r.OK || !hasFail(r.Checks, "DB 종류") {
		t.Fatalf("종류 불일치 거부: %v %+v", err, r.Checks)
	}
	if fileHash(t, f.dbPath) != pre {
		t.Fatal("거부된 복구가 기존 DB 를 바꿈")
	}
	ents, _ := os.ReadDir(filepath.Dir(f.dbPath))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".restore-") || strings.Contains(e.Name(), ".pre-restore-") {
			t.Fatalf("거부된 복구가 흔적을 남김: %s", e.Name())
		}
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(from)
	for _, e := range ents {
		if err := os.WriteFile(filepath.Join(to, e.Name()), mustRead(t, filepath.Join(from, e.Name())), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// AC-1832: 백업 이후 기록이 쌓인 DB 를 복구하면 손실 구간이 보고서와 system.restore 감사에 있고, 이전 앵커 파일이 보존된다.
func TestRestoreLossRange(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	res, err := backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.addAudit(7)
	f.stamp()
	f.addAudit(3)
	f.stamp()
	f.closeDB()
	o := f.restoreOpts(res.Path)
	o.Replace = true
	r, err := backup.Restore(ctx, o)
	if err != nil || !r.OK {
		t.Fatalf("%v %+v", err, r.Checks)
	}
	if r.Loss == nil || r.Loss.Lines != 2 || r.Loss.FromSeq <= res.Manifest.ChainSeq || r.Loss.ToSeq <= r.Loss.FromSeq || r.LostRecords != 10 {
		t.Fatalf("손실 구간: %+v 손실 기록 %d", r.Loss, r.LostRecords)
	}
	db, _ := dbutil.Open(f.dbPath)
	defer db.Close()
	var detail string
	_ = db.QueryRow(`SELECT detail FROM audit_logs WHERE action = 'system.restore'`).Scan(&detail)
	if !strings.Contains(detail, "손실 구간") || !strings.Contains(detail, fmt.Sprint(r.Loss.FromSeq)) {
		t.Fatalf("감사 상세에 손실 구간이 없음: %s", detail)
	}
	var pre string
	for _, p := range r.PreRestore {
		if strings.HasPrefix(p, f.anchor+".pre-restore-") {
			pre = p
		}
	}
	if pre == "" || strings.Count(string(mustRead(t, pre)), "\n") <= strings.Count(string(mustRead(t, f.anchor)), "\n") {
		t.Fatalf("이전 앵커 파일이 증거로 보존되지 않음: %v", r.PreRestore)
	}
}

// 앵커 키가 없거나 앵커 사본이 없으면 대조는 "검증 안 됨" 경고다(종료는 성공).
func TestRestoreAnchorNotVerifiedIsWarning(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	res, err := backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.closeDB()
	o := f.restoreOpts(res.Path)
	o.Replace = true
	o.AnchorKey = nil
	r, err := backup.Restore(ctx, o)
	if err != nil || !r.OK {
		t.Fatalf("%v %+v", err, r.Checks)
	}
	found := false
	for _, c := range r.Checks {
		if strings.Contains(c.Item, "앵커 대조") && c.Status == backup.Warn && strings.Contains(c.Detail, "앵커 검증 안 됨") {
			found = true
		}
	}
	if !found {
		t.Fatalf("앵커 검증 안 됨 경고가 없음: %+v", r.Checks)
	}
}

// AC-1833: 복구 뒤 모든 세션이 무효가 된다. 토큰은 기본 유지, --revoke-tokens 면 폐기.
func TestRestoreRevokesSessions(t *testing.T) {
	ctx := context.Background()
	for _, revoke := range []bool{false, true} {
		f := newFixture(t)
		res, err := backup.Run(ctx, f.opts())
		if err != nil {
			t.Fatal(err)
		}
		f.closeDB()
		o := f.restoreOpts(res.Path)
		o.Replace, o.RevokeTokens = true, revoke
		r, err := backup.Restore(ctx, o)
		if err != nil || !r.OK {
			t.Fatalf("%v %+v", err, r.Checks)
		}
		db, _ := dbutil.Open(f.dbPath)
		var live, liveTok int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live)
		_ = db.QueryRow(`SELECT COUNT(*) FROM personal_tokens WHERE revoked_at IS NULL`).Scan(&liveTok)
		db.Close()
		if live != 0 || r.SessionsRevoked != 1 {
			t.Fatalf("세션이 남음: %d 폐기 %d", live, r.SessionsRevoked)
		}
		if revoke && (liveTok != 0 || r.TokensRevoked != 1) || !revoke && liveTok != 1 {
			t.Fatalf("revoke=%v 토큰 활성 %d 폐기 %d", revoke, liveTok, r.TokensRevoked)
		}
	}
}

// AC-1834: 리허설은 운영 파일을 바꾸지 않고(전후 해시 동일) 임시 위치에서 끝낸 뒤 모두 지운다. --work 는 표식 파일과 함께 남긴다.
func TestRehearsalLeavesProductionUntouched(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	res, err := backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	f.addAudit(2)
	f.stamp()
	f.closeDB()
	dbH, anH := fileHash(t, f.dbPath), fileHash(t, f.anchor)
	tmpRoot := filepath.Join(f.root, "tmp")
	_ = os.MkdirAll(tmpRoot, 0o700)
	o := f.restoreOpts(res.Path)
	o.Rehearse, o.TmpRoot = true, tmpRoot
	r, err := backup.Restore(ctx, o)
	if err != nil || !r.OK || !r.Rehearsal {
		t.Fatalf("리허설: %v %+v", err, r.Checks)
	}
	if fileHash(t, f.dbPath) != dbH || fileHash(t, f.anchor) != anH {
		t.Fatal("리허설이 운영 파일을 바꿈")
	}
	if left, _ := os.ReadDir(tmpRoot); len(left) != 0 {
		t.Fatalf("임시물이 남음: %v", left)
	}
	if r.Loss == nil {
		t.Fatal("리허설도 손실 구간을 계산해야 함")
	}
	// --work: 남기고 표식이 있다.
	work := filepath.Join(f.root, "rehearsal-work")
	o.WorkDir = work
	r, err = backup.Restore(ctx, o)
	if err != nil || !r.OK || r.WorkDB == "" {
		t.Fatalf("%v %+v", err, r.Checks)
	}
	if _, err := os.Stat(filepath.Join(work, backup.RehearsalMarker)); err != nil {
		t.Fatalf("표식 파일이 없음: %v", err)
	}
	if _, err := os.Stat(r.WorkDB); err != nil {
		t.Fatal(err)
	}
	if fileHash(t, f.dbPath) != dbH {
		t.Fatal("--work 리허설이 운영 파일을 바꿈")
	}
	if _, err := backup.Restore(ctx, o); err == nil {
		t.Fatal("비어 있지 않은 --work 는 거부여야 함")
	}
}

func TestRecordRunWritesRowAndAudit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	now := time.Now()
	if err := backup.RecordRun(ctx, f.db, backup.RunRecord{Kind: "backup", Result: "success", SetName: "pl-x", DBKind: "sqlite", Bytes: 10, ChainSeq: 5, Started: now, Finished: now,
		Action: "system.backup", Detail: "세트 pl-x"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM backup_runs WHERE kind='backup' AND set_name='pl-x'`).Scan(&n)
	if n != 1 {
		t.Fatalf("backup_runs %d", n)
	}
	if _, err := f.db.Exec(`UPDATE backup_runs SET result = 'failed'`); err == nil {
		t.Fatal("backup_runs 는 추가 전용이어야 함")
	}
	if _, err := f.db.Exec(`DELETE FROM backup_runs`); err == nil {
		t.Fatal("backup_runs 는 삭제할 수 없어야 함")
	}
	vr, err := f.store.Verify(ctx)
	if err != nil || !vr.ChainOK {
		t.Fatalf("%v %+v", err, vr)
	}
}

// 불변식(전표 차대변·재고 합계 대 원장 합·번호 카운터): 위반은 백업을 막지 않고 경고로 남는다(매니페스트·경고 출력·backup_runs·감사). 복구 판정도 경고다.
func TestInvariantViolationsAreWarningsNotBlockers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.exec(`INSERT INTO items (id, sku, name) VALUES (1, 'SK-1', '볼트')`)
	f.exec(`INSERT INTO stock_ledger (item_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id) VALUES (1, 10, 'receipt', 'receipt', 1, 1, '2026-11-01T00:00:00Z', 1)`)
	f.exec(`INSERT INTO stock_ledger (item_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id) VALUES (1, -3, 'issue', 'issue', 1, 1, '2026-11-02T00:00:00Z', 1)`)
	for i, no := range []string{"OUT-2026-0001", "OUT-2026-0002"} {
		f.exec(`INSERT INTO issue_slips (id, no, department_id, warehouse_id, issue_date, method, actor_id, created_at) VALUES (?, ?, 1, 1, '2026-11-02', 'fifo', 1, '2026-11-02T00:00:00Z')`, i+1, no)
	}
	f.exec(`INSERT INTO inventory_counters (prefix, year, last) VALUES ('OUT', 2026, 2)`)
	if v := backup.CheckInvariants(ctx, f.db); len(v) != 0 {
		t.Fatalf("정상 DB 에 위반이 보고됨: %v", v)
	}
	res, err := backup.Run(ctx, f.opts())
	if err != nil || len(res.Manifest.Invariants) != 0 {
		t.Fatalf("정상 DB 백업: %v %+v", err, res.Manifest.Invariants)
	}

	// 카운터가 뒤처짐 → 백업은 만들어지고 경고가 남는다.
	f.exec(`UPDATE inventory_counters SET last = 1 WHERE prefix = 'OUT'`)
	v := backup.CheckInvariants(ctx, f.db)
	if len(v) != 1 || !strings.Contains(v[0], "OUT-2026") || !strings.Contains(v[0], "(1)") || !strings.Contains(v[0], "(2)") {
		t.Fatalf("카운터 뒤처짐: %v", v)
	}
	res, err = backup.Run(ctx, f.opts())
	if err != nil {
		t.Fatalf("불변식 위반은 백업을 막지 않아야 함: %v", err)
	}
	if len(res.Manifest.Invariants) != 1 || !strings.Contains(strings.Join(res.Warnings, ";"), "업무 불변식 위반") {
		t.Fatalf("매니페스트·경고에 위반이 없음: %+v %v", res.Manifest.Invariants, res.Warnings)
	}
	vr, err := backup.VerifySet(ctx, res.Path, backup.VerifyOptions{AnchorKey: anchorKey})
	if err != nil || !vr.OK {
		t.Fatalf("경고만 있으면 검증은 통과해야 함: %v %+v", err, vr.Checks)
	}
	warn := false
	for _, c := range vr.Checks {
		if c.Item == "불변식" && c.Status == backup.Warn && strings.Contains(c.Detail, "OUT-2026") {
			warn = true
		}
	}
	if !warn {
		t.Fatalf("판정표의 불변식 줄이 경고가 아님: %+v", vr.Checks)
	}
	// backup_runs 와 감사에 위반 내용이 남는다.
	now := time.Now()
	if err := backup.RecordRun(ctx, f.db, backup.RunRecord{Kind: "backup", Result: "success", SetName: res.Name, DBKind: "sqlite", Started: now, Finished: now, Action: "system.backup",
		Detail: "세트 " + res.Name, Warnings: []string{"업무 불변식 위반: " + v[0]}}); err != nil {
		t.Fatal(err)
	}
	var w, det string
	_ = f.db.QueryRow(`SELECT warnings FROM backup_runs WHERE set_name = ?`, res.Name).Scan(&w)
	_ = f.db.QueryRow(`SELECT detail FROM audit_logs WHERE action = 'system.backup' ORDER BY seq DESC LIMIT 1`).Scan(&det)
	if !strings.Contains(w, "OUT-2026") || !strings.Contains(det, "경고 1건") {
		t.Fatalf("backup_runs 경고 %q, 감사 상세 %q", w, det)
	}
	// 복구(리허설)도 진행되고 결과에 위반이 명시된다.
	f.closeDB()
	o := f.restoreOpts(res.Path)
	o.Rehearse = true
	rr, err := backup.Restore(ctx, o)
	if err != nil || !rr.OK || len(rr.Invariants) != 1 {
		t.Fatalf("불변식 위반 세트의 복구 리허설: %v %+v", err, rr.Checks)
	}
	if !strings.Contains(strings.Join(rr.Notes, ";"), "불변식 위반") {
		t.Fatalf("복구 결과 보고에 위반이 없음: %v", rr.Notes)
	}
	// 카운터 행이 없는데 번호가 있음도 같은 점검에 걸린다.
	g := newFixture(t)
	g.exec(`INSERT INTO issue_slips (id, no, department_id, warehouse_id, issue_date, method, actor_id, created_at) VALUES (1, 'OUT-2026-0001', 1, 1, '2026-11-02', 'fifo', 1, '2026-11-02T00:00:00Z')`)
	if v := backup.CheckInvariants(ctx, g.db); len(v) != 1 || !strings.Contains(v[0], "카운터") {
		t.Fatalf("카운터 없음: %v", v)
	}
}

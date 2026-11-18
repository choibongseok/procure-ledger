package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// ToolVersion 은 도구 버전 표시다(매니페스트용).
const ToolVersion = "1"

const lastVerifiedFile = ".last-verified"

// Options 는 백업 한 번의 입력이다.
type Options struct {
	Kind          string // sqlite | postgres
	DBPath        string // SQLite 파일(Kind=sqlite)
	DBURL         string // PG 접속 문자열(Kind=postgres). 비밀번호가 있다: 출력·기록하지 않는다
	PGDump        string // pg_dump 경로(비면 PATH)
	AllowInsecure bool   // PL_DB_ALLOW_INSECURE
	AnchorPath    string
	AnchorKey     []byte
	Dir           string
	Keep          int
	Encrypt       bool
	Key           []byte // 암호화 키(Encrypt 일 때 필수)
	Label         string
	AppVersion    string
	Now           func() time.Time
}

// Result 는 백업 결과다.
type Result struct {
	Name     string
	Path     string
	Manifest Manifest
	Size     int64
	Deleted  []string
	Warnings []string
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// freeBytes 는 dir 가 있는 파일 시스템의 여유 공간이다.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// cleanPartials 는 이전 실행이 남긴 .partial 디렉터리를 지운다(세트로 인정되지 않는다).
func cleanPartials(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), partialDir) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// Run 은 백업 세트 한 개를 만든다. 검사 중 하나라도 실패하면 세트를 만들지 않고(.partial 도 정리) 오류를 돌려준다.
// 보존(오래된 세트 삭제)은 새 세트가 검증까지 통과한 뒤에만 한다.
func Run(ctx context.Context, o Options) (Result, error) {
	var res Result
	if o.Dir == "" {
		return res, errors.New("백업 디렉터리가 없습니다(--dir 또는 PL_BACKUP_DIR)")
	}
	if o.Label != "" && !ValidLabel(o.Label) {
		return res, errors.New("라벨은 영문·숫자·밑줄·하이픈 32자까지입니다")
	}
	if o.Encrypt && len(o.Key) == 0 {
		return res, errors.New("--encrypt 에는 PL_BACKUP_KEY 가 필요합니다")
	}
	if o.Keep == 0 {
		o.Keep = 14
	}
	if o.Keep < 2 {
		return res, errors.New("--keep 은 2 이상이어야 합니다")
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return res, fmt.Errorf("백업 디렉터리를 만들 수 없습니다: %w", err)
	}
	cleanPartials(o.Dir)

	// 여유 공간: 직전 백업 크기(없으면 DB 크기)의 1.5배가 있어야 시작한다.
	need := uint64(0)
	if sets, _ := List(o.Dir); len(sets) > 0 {
		need = uint64(sets[len(sets)-1].Size)
	} else if o.Kind != "postgres" {
		if st, err := os.Stat(o.DBPath); err == nil {
			need = uint64(st.Size())
		}
	}
	if free, err := freeBytes(o.Dir); err == nil && need > 0 && free < need+need/2 {
		return res, fmt.Errorf("백업 디렉터리의 여유 공간이 부족합니다(필요 약 %d MiB, 여유 %d MiB)", (need+need/2)>>20, free>>20)
	}

	at := o.now().UTC()
	name := SetName(at, o.Label)
	final := filepath.Join(o.Dir, name)
	if _, err := os.Stat(final); err == nil {
		return res, fmt.Errorf("같은 이름의 세트가 이미 있습니다: %s", name)
	}
	work := filepath.Join(o.Dir, partialDir+name)
	if err := os.Mkdir(work, 0o700); err != nil {
		return res, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(work)
		}
	}()

	m := Manifest{Format: FormatVersion, AppVersion: o.AppVersion, ToolVersion: ToolVersion, DBKind: o.Kind,
		CreatedAt: at.Format(time.RFC3339), Files: map[string]FileInfo{}, Encrypted: o.Encrypt}
	if o.Encrypt {
		m.KeyFingerprint = Fingerprint(o.Key)
	}
	switch o.Kind {
	case "postgres":
		if err := backupPG(ctx, o, work, &m); err != nil {
			return res, err
		}
	default:
		if err := backupSQLite(ctx, o, work, &m); err != nil {
			return res, err
		}
	}

	// 암호화: 평문을 .enc 로 바꾸고 평문은 지운다.
	stored := map[string]string{m.DBFile: m.DBFile}
	if m.AnchorFile != "" {
		stored[m.AnchorFile] = m.AnchorFile
	}
	if o.Encrypt {
		for plain := range stored {
			if err := EncryptFile(filepath.Join(work, plain), filepath.Join(work, plain+".enc"), o.Key); err != nil {
				return res, fmt.Errorf("암호화 실패: %w", err)
			}
			_ = os.Remove(filepath.Join(work, plain))
			stored[plain] = plain + ".enc"
		}
		m.DBFile += ".enc"
		if m.AnchorFile != "" {
			m.AnchorFile += ".enc"
		}
	}
	for _, sn := range stored {
		h, size, err := FileSHA256(filepath.Join(work, sn))
		if err != nil {
			return res, err
		}
		m.Files[sn] = FileInfo{SHA256: h, Size: size}
		if err := os.Chmod(filepath.Join(work, sn), 0o600); err != nil {
			return res, err
		}
	}
	if err := WriteSums(work, m); err != nil {
		return res, err
	}
	if err := os.Rename(work, final); err != nil {
		return res, err
	}
	ok = true
	res = Result{Name: name, Path: final, Manifest: m, Warnings: m.Warnings}
	for _, fi := range m.Files {
		res.Size += fi.Size
	}

	// 확정 뒤 검증: 통과해야 "마지막 검증 성공 세트"가 되고 보존 정리가 일어난다.
	rep, verr := VerifySet(ctx, final, VerifyOptions{Key: o.Key, AnchorKey: o.AnchorKey})
	if verr != nil || !rep.OK {
		msg := "세트를 만들었지만 검증에 실패했습니다"
		if verr != nil {
			msg += ": " + verr.Error()
		} else {
			msg += ": " + rep.FirstFailure()
		}
		return res, errors.New(msg + " (보존 정리는 하지 않았습니다)")
	}
	_ = os.WriteFile(filepath.Join(o.Dir, lastVerifiedFile), []byte(name+"\n"), 0o600)
	res.Deleted = retain(o.Dir, o.Keep, name)
	return res, nil
}

// retain 은 완전한 세트를 최신 keep 개만 남기고 지운다. 마지막 검증 성공 세트는 개수와 무관하게 지키지 않는다고 하지 않는다: 지킨다.
func retain(dir string, keep int, justMade string) []string {
	sets, _ := List(dir)
	var complete []SetInfo
	for _, s := range sets {
		if s.Complete {
			complete = append(complete, s)
		}
	}
	protect := justMade
	if raw, err := os.ReadFile(filepath.Join(dir, lastVerifiedFile)); err == nil {
		protect = strings.TrimSpace(string(raw))
	}
	var deleted []string
	if len(complete) > keep {
		for _, s := range complete[:len(complete)-keep] {
			if s.Name == protect {
				continue
			}
			if os.RemoveAll(s.Path) == nil {
				deleted = append(deleted, s.Name)
			}
		}
	}
	// 불완전(매니페스트 없음)한 세트는 30일 뒤 정리한다.
	for _, s := range sets {
		if s.Complete {
			continue
		}
		if st, err := os.Stat(s.Path); err == nil && time.Since(st.ModTime()) > 30*24*time.Hour {
			if os.RemoveAll(s.Path) == nil {
				deleted = append(deleted, s.Name)
			}
		}
	}
	sort.Strings(deleted)
	return deleted
}

// backupSQLite 는 VACUUM INTO 로 일관된 스냅샷을 만들고, 스냅샷을 점검하고, 앵커 접두부를 복사한다(명세 6.3).
func backupSQLite(ctx context.Context, o Options, work string, m *Manifest) error {
	if o.DBPath == "" {
		return errors.New("DB 경로가 없습니다")
	}
	if _, err := os.Stat(o.DBPath); err != nil {
		return fmt.Errorf("DB 파일을 찾을 수 없습니다: %w", err)
	}
	src, err := dbutil.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("DB 를 열 수 없습니다: %w", err)
	}
	defer src.Close()

	// 스냅샷 직전에 앵커를 한 번 찍어 본다(키가 있을 때). 실패해도 백업은 계속한다.
	if len(o.AnchorKey) >= audit.MinAnchorKeyLen && o.AnchorPath != "" {
		store := audit.New(src)
		if a, aerr := audit.NewAnchor(store, o.AnchorPath, o.AnchorKey); aerr == nil {
			if _, serr := a.Stamp(ctx); serr != nil {
				m.Warnings = append(m.Warnings, "스냅샷 직전 앵커 기록에 실패했습니다: "+serr.Error())
			}
		} else {
			m.Warnings = append(m.Warnings, "앵커를 열지 못해 스냅샷 직전 기록을 건너뜁니다: "+aerr.Error())
		}
	}

	dst := filepath.Join(work, DBFileSQLite)
	// 단순 파일 복사는 하지 않는다: WAL 모드에서 복사하면 깨진 사본이 된다. VACUUM INTO 는 한 시점의 일관된 사본을 새 파일로 만든다.
	if _, err := src.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(dst, "'", "''")+"'"); err != nil {
		return fmt.Errorf("스냅샷을 만들지 못했습니다: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return err
	}
	m.DBFile = DBFileSQLite

	// 앵커 접두부: 스냅샷의 대장에 해시가 있는 줄까지만.
	anchorCopy := filepath.Join(work, AnchorName)
	stamps, err := StampHashes(ctx, dst)
	if err != nil {
		return fmt.Errorf("스냅샷의 앵커 대장을 읽지 못했습니다: %w", err)
	}
	withAnchor := o.AnchorPath != "" || len(stamps) > 0
	if withAnchor {
		lines, warns, aerr := AnchorPrefix(o.AnchorPath, anchorCopy, stamps)
		if aerr != nil {
			return fmt.Errorf("앵커 사본을 만들지 못했습니다: %w", aerr)
		}
		m.AnchorFile, m.AnchorLines = AnchorName, lines
		m.Warnings = append(m.Warnings, warns...)
	}

	rep, err := Inspect(ctx, dst, anchorCopy, o.AnchorKey)
	if err != nil {
		return err
	}
	if rep.Integrity != "ok" {
		return fmt.Errorf("DB 가 손상되어 백업하지 않습니다: 무결성 검사 결과 %q", rep.Integrity)
	}
	if rep.FKViolations > 0 {
		return fmt.Errorf("DB 가 외래 키 위반 %d건을 갖고 있어 백업하지 않습니다", rep.FKViolations)
	}
	if !rep.ChainOK {
		return fmt.Errorf("감사 체인이 깨져 있어 백업하지 않습니다: %s(기록 %d번)", rep.ChainWhy, rep.ChainSeq)
	}
	// 업무 불변식(재고 합계 대 원장, 번호 카운터, 전표 차대변) 위반은 세트를 막지 않고 경고로 남긴다: 업무 데이터가 조금 틀어진 회사가 백업을 아예 못 하는 것이 더 위험하다.
	// (무결성·외래 키·감사 체인 실패는 위에서 세트를 만들지 않는다.)
	for _, v := range rep.Invariants {
		m.Invariants = append(m.Invariants, v)
		m.Warnings = append(m.Warnings, "업무 불변식 위반: "+v)
	}
	if rep.Anchor.State.Failed() {
		m.Warnings = append(m.Warnings, "앵커 대조에 문제가 있습니다: "+rep.Anchor.Message)
	}
	m.Migration, m.ChainSeq, m.ChainHash, m.AnchorStamps = rep.Migration, rep.ChainSeq, rep.ChainHash, rep.StampCount
	if !withAnchor {
		m.AnchorFile = ""
	}
	return nil
}

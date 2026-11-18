package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/audit"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// Report 는 DB 파일 한 개를 점검한 결과다(백업 때 스냅샷, 복구 때 임시 파일, verify-backup 때 복호화한 사본).
type Report struct {
	Integrity    string // PRAGMA integrity_check 결과("ok" 이면 정상)
	FKViolations int    // PRAGMA foreign_key_check 위반 수
	Migration    string // 적용된 마이그레이션의 최종 번호(4자리, 모르면 "")
	ChainOK      bool   // 감사 체인
	ChainChecked int64
	ChainSeq     int64  // 체인 머리 번호(기록이 없으면 0)
	ChainHash    string // 체인 머리 해시
	ChainWhy     string // 체인이 깨졌다면 이유
	Anchor       caudit.AnchorStatus
	AnchorKnown  bool     // 앵커 키가 있어 대조를 시도했는가
	StampCount   int      // audit_anchor_stamps 의 stamp 행 수
	Invariants   []string // 불변식 위반 설명(없으면 비어 있다)
}

// Inspect 는 SQLite 파일을 열어 무결성·외래 키·마이그레이션 번호·감사 체인·앵커 대조·전표 균형을 점검한다.
// anchorPath 와 anchorKey 가 모두 있으면 그 앵커 파일로 외부 대조까지 한다(파일은 읽기만 한다. 없으면 만들지 않는다).
// 끝나면 파일을 WAL 이 없는 한 파일 상태로 정리한다(스냅샷을 그대로 옮기거나 해시를 낼 수 있게).
func Inspect(ctx context.Context, dbPath, anchorPath string, anchorKey []byte) (rep Report, err error) {
	db, err := dbutil.Open(dbPath)
	if err != nil {
		return rep, fmt.Errorf("DB 파일을 열 수 없습니다: %w", err)
	}
	defer func() {
		// 정리: WAL 을 합치고 한 파일로 만든다. 실패해도 점검 결과는 그대로다.
		_, _ = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		_, _ = db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`)
		_ = db.Close()
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}()

	var integ []string
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		rep.Integrity = "점검 실패: " + err.Error()
		return rep, nil
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		integ = append(integ, s)
	}
	rows.Close()
	rep.Integrity = strings.Join(integ, "; ")
	if rep.Integrity == "" {
		rep.Integrity = "결과 없음"
	}
	if rep.Integrity != "ok" {
		return rep, nil // 손상된 파일에서는 더 읽지 않는다
	}

	fk, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return rep, err
	}
	for fk.Next() {
		rep.FKViolations++
	}
	fk.Close()

	var name sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&name); err == nil && len(name.String) >= 4 {
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
	rep.ChainOK = vr.ChainOK
	rep.ChainChecked = vr.Checked
	rep.ChainHash = vr.HeadHash
	rep.ChainWhy = vr.BrokenWhy
	rep.Anchor = vr.Anchor
	if vr.ChainOK {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM audit_logs`).Scan(&rep.ChainSeq)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_anchor_stamps WHERE kind = 'stamp'`).Scan(&rep.StampCount)
	rep.Invariants = CheckInvariants(ctx, db)
	return rep, nil
}

// StampHashes 는 DB 의 앵커 대장에 있는 줄 해시들이다(스냅샷에 대응하는 앵커 접두부를 가릴 때 쓴다).
func StampHashes(ctx context.Context, dbPath string) (map[string]bool, error) {
	db, err := dbutil.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT line_hash FROM audit_anchor_stamps WHERE kind = 'stamp'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// AnchorPrefix 는 앵커 파일 src 에서, 대장(stamps)에 해시가 있는 마지막 줄까지의 접두부를 dst 에 쓴다(0600).
// 스냅샷 이후에 쌓인 줄은 빠지므로 복구 뒤의 앵커 대조가 "꼬리 삭제"로 잘못 판정되지 않는다. 돌려주는 값은 사본의 줄 수와 경고들이다.
// src 가 없으면 빈 사본을 만들고 경고를 돌려준다.
func AnchorPrefix(src, dst string, stamps map[string]bool) (lines int, warns []string, err error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer out.Close()
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(stamps) > 0 {
				warns = append(warns, "앵커 파일이 없어 앵커 사본이 비어 있습니다(DB 에는 앵커 기록 이력이 있습니다)")
			}
			return 0, warns, nil
		}
		return 0, nil, err
	}
	defer f.Close()
	var all []string
	rd := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := rd.ReadString('\n')
		if strings.HasSuffix(line, "\n") { // 끝나지 않은 마지막 줄은 버린다(기록 도중)
			all = append(all, strings.TrimSuffix(line, "\n"))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, nil, rerr
		}
	}
	last := -1
	matched := map[string]bool{}
	for i, raw := range all {
		sum := sha256.Sum256([]byte(raw))
		h := hex.EncodeToString(sum[:])
		if stamps[h] {
			last = i
			matched[h] = true
		}
	}
	for i := 0; i <= last; i++ {
		if _, err := out.WriteString(all[i] + "\n"); err != nil {
			return 0, nil, err
		}
	}
	if missing := len(stamps) - len(matched); missing > 0 {
		warns = append(warns, fmt.Sprintf("앵커 대장에는 있는데 파일에 없는 줄이 %d개 있습니다(앵커 이상)", missing))
	}
	if last >= 0 && last+1 > len(matched) {
		warns = append(warns, "앵커 접두부에 대장에 없는 줄이 끼어 있습니다(앵커 이상)")
	}
	return last + 1, warns, out.Sync()
}

// AnchorLossRange 는 이전 앵커 파일 path 에서 복구 시점 머리(headSeq)보다 큰 번호의 줄들이 가리키는 범위다.
// 서명은 검증하지 않는다(증거 보존용이며 키가 바뀌었을 수 있다): 줄의 seq·at 만 읽는다.
type LossRange struct {
	FromSeq, ToSeq int64
	FromAt, ToAt   string
	Lines          int
}

func AnchorLossRange(path string, headSeq int64) (*LossRange, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lr *LossRange
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var l struct {
			Seq int64  `json:"seq"`
			At  string `json:"at"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Seq <= headSeq {
			continue
		}
		if lr == nil {
			lr = &LossRange{FromSeq: l.Seq, FromAt: l.At}
		}
		lr.ToSeq, lr.ToAt = l.Seq, l.At
		lr.Lines++
	}
	return lr, sc.Err()
}

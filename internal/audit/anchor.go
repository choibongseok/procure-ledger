package audit

// 감사 체인 외부 앵커(B4).
//
// 해시 체인만으로는 DB 를 쓸 수 있는 공격자가 (1) 마지막 N건을 지우거나 (2) 체인 전체를 다시 계산해
// 덮어쓰는 것을 구분하지 못한다. 그래서 체인 머리(마지막 seq 와 해시)를 주기적으로 HMAC 서명해
// DB 밖의 추가 전용 파일에 한 줄씩 남기고, 검증할 때 그 줄들과 DB 를 대조한다.
//
// 파일 형식: 한 줄에 JSON 하나 {"v","at","seq","head","prev","sig"}.
//   - sig  = HMAC-SHA256(키, v|at|seq|head|prev). 키를 모르면 줄을 위조할 수 없다.
//   - prev = 앞 줄 원문의 SHA-256(첫 줄은 빈 문자열). 줄 중간 삭제·순서 바꿈이 연결 끊김으로 드러난다.
//
// 검증이 가려내는 것: 서명 위조·줄 변조(앵커 파일 손상), 앵커가 가리키는 기록이 DB 에 없음(꼬리 삭제),
// 앵커 시점의 해시가 DB 와 다름(내용 재계산).
//
// 한계(운영 지침은 docs/deploy-checklist.md)
//   - HMAC 키와 앵커 파일을 둘 다 가진 공격자는 앵커를 새로 계산해 쓸 수 있다. 키는 비밀 저장소에 두고,
//     앵커 파일은 DB 와 다른 볼륨·권한(애플리케이션은 추가만 가능)에 둔다.
//   - 마지막 앵커 이후에 쌓인 기록(기본 간격 안의 구간)은 외부 대조가 없다. 이 구간의 꼬리 삭제는 잡지 못한다.
//   - 파일 끝쪽 줄을 통째로 잘라 내면 파일 자체는 정상으로 보이고 그만큼 외부 대조 범위가 줄어든다.
//     (B9 이후) 줄을 쓸 때마다 DB 의 audit_anchor_stamps 에 줄 해시를 남기므로, 파일 삭제·줄 잘림은
//     "앵커 유실"(anchor_lost)로 잡힌다. 유실 뒤에는 새 앵커를 쓰지 않고, 감사자가 Restart 로 명시적으로 다시
//     시작해야 하며 이후에도 검증 결과에 "앵커 유실 이력 있음"이 남는다. DB 와 파일을 함께 바꾸는 공격자는 못 잡는다.
//   - 앵커 기록은 DB 쓰기와 같은 트랜잭션이 아니다(요청 경로에 파일 쓰기를 넣지 않기 위해).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// MinAnchorKeyLen 은 HMAC 키의 최소 길이(바이트)다.
const MinAnchorKeyLen = 16

const anchorVersion = 1

type anchorLine struct {
	V    int    `json:"v"`
	At   string `json:"at"`
	Seq  int64  `json:"seq"`
	Head string `json:"head"`
	Prev string `json:"prev"`
	Sig  string `json:"sig"`
}

type parsedAnchor struct {
	anchorLine
	raw string
}

// Anchor 는 caudit.Anchorer 를 구현한다. Store.SetAnchor 로 Store 에 연결하면 Verify 가 대조한다.
type Anchor struct {
	store *Store
	path  string
	key   []byte
	mu    sync.Mutex
	now   func() time.Time
}

var _ caudit.Anchorer = (*Anchor)(nil)

// NewAnchor 는 앵커를 만들고 store 에 연결한다. 키가 짧거나 파일을 열어 추가할 수 없으면 오류다(기동 거부 사유).
// 파일이 없으면 빈 파일로 만든다(권한 0600). 상위 디렉터리는 만들지 않는다.
func NewAnchor(store *Store, path string, key []byte) (*Anchor, error) {
	if len(key) < MinAnchorKeyLen {
		return nil, fmt.Errorf("앵커 키는 %d바이트 이상이어야 합니다", MinAnchorKeyLen)
	}
	if path == "" {
		return nil, errors.New("앵커 파일 경로가 없습니다")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("앵커 파일을 열 수 없습니다: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	a := &Anchor{store: store, path: path, key: append([]byte(nil), key...), now: time.Now}
	store.SetAnchor(a)
	return a, nil
}

func (a *Anchor) sign(l anchorLine) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strings.Join([]string{"pl-audit-anchor", strconv.Itoa(l.V), l.At, strconv.FormatInt(l.Seq, 10), l.Head, l.Prev}, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

func hashLine(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// parse 는 파일 내용을 검사해 줄 목록을 돌려준다. 문제가 있으면 한국어 설명을 돌려준다.
func (a *Anchor) parse(data []byte) ([]parsedAnchor, string) {
	if len(data) == 0 {
		return nil, ""
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		return nil, "마지막 줄이 끝나지 않았습니다(기록 도중 끊겼거나 잘렸습니다)"
	}
	rawLines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	out := make([]parsedAnchor, 0, len(rawLines))
	prevRaw := ""
	var prevSeq int64
	for i, raw := range rawLines {
		n := i + 1
		var l anchorLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil || l.V != anchorVersion {
			return nil, fmt.Sprintf("%d번째 줄을 읽을 수 없습니다", n)
		}
		if !hmac.Equal([]byte(a.sign(l)), []byte(l.Sig)) {
			return nil, fmt.Sprintf("%d번째 줄의 서명이 올바르지 않습니다(위조 또는 손상)", n)
		}
		want := ""
		if i > 0 {
			want = hashLine(prevRaw)
		}
		if l.Prev != want {
			return nil, fmt.Sprintf("%d번째 줄이 앞 줄과 이어지지 않습니다(줄 삭제 또는 순서 변경)", n)
		}
		if l.Seq <= prevSeq {
			return nil, fmt.Sprintf("%d번째 줄의 기록 번호가 앞 줄보다 커지지 않습니다(순서 변경)", n)
		}
		out = append(out, parsedAnchor{anchorLine: l, raw: raw})
		prevRaw, prevSeq = raw, l.Seq
	}
	return out, ""
}

// read 는 파일을 공유 잠금으로 읽어 검사한다. 파일이 없으면 missing 이 true 다.
func (a *Anchor) read() (lines []parsedAnchor, fault string, missing bool) {
	f, err := os.Open(a.path)
	if err != nil {
		return nil, "앵커 파일을 찾을 수 없습니다(삭제되었거나 경로가 바뀌었습니다)", errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "앵커 파일을 읽을 수 없습니다: " + err.Error(), false
	}
	lines, fault = a.parse(data)
	return lines, fault, false
}

// lostStamps 는 DB 대장에는 있는데 파일에 없는 앵커 줄을 센다(마지막 재시작 이후만). B9.
// 근거가 프로세스 메모리가 아니라 DB 라서 재기동 뒤에도 같은 판정이 나온다.
func lostStamps(ctx context.Context, q contract.Tx, lines []parsedAnchor) (missing int, lastSeq int64, err error) {
	var base int64
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM audit_anchor_stamps WHERE kind='reset'`).Scan(&base); err != nil {
		return 0, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT seq, line_hash FROM audit_anchor_stamps WHERE kind='stamp' AND id > ? ORDER BY id`, base)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	have := make(map[string]bool, len(lines))
	for _, l := range lines {
		have[hashLine(l.raw)] = true
	}
	for rows.Next() {
		var seq int64
		var h string
		if err = rows.Scan(&seq, &h); err != nil {
			return 0, 0, err
		}
		if !have[h] {
			missing++
			lastSeq = seq
		}
	}
	return missing, lastSeq, rows.Err()
}

func lostHistory(ctx context.Context, q contract.Tx) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_anchor_stamps WHERE kind='reset'`).Scan(&n)
	return n > 0, err
}

func lostStatus(missing int, lastSeq int64, lines []parsedAnchor) caudit.AnchorStatus {
	return caudit.AnchorStatus{State: caudit.AnchorLost, Lines: len(lines), Message: fmt.Sprintf(
		"외부 앵커 파일에서 앵커 %d줄이 사라졌습니다(마지막으로 사라진 앵커는 %d번 기록). 앵커 파일이 삭제되었거나 끝이 잘렸습니다. "+
			"조용히 새로 시작하지 않으며, 감사자가 원인을 확인한 뒤 앵커를 명시적으로 다시 시작해야 합니다.", missing, lastSeq)}
}

// compare 는 앵커 줄들을 DB 와 대조한다. 줄 자체의 서명·연결은 parse 가 이미 확인했다.
func (a *Anchor) compare(ctx context.Context, lines []parsedAnchor) (caudit.AnchorStatus, error) {
	st := caudit.AnchorStatus{Lines: len(lines)}
	if len(lines) == 0 {
		st.State = caudit.AnchorEmpty
		st.Message = "외부 앵커가 아직 기록되지 않아 외부 대조를 하지 못했습니다."
		return st, nil
	}
	headSeq, _, _, err := a.store.head(ctx)
	if err != nil {
		return st, err
	}
	last := lines[len(lines)-1]
	st.LastSeq = last.Seq
	st.LastAt, _ = time.Parse(timeLayout, last.At)
	for _, l := range lines {
		hash, found, err := a.store.hashAt(ctx, l.Seq)
		if err != nil {
			return st, err
		}
		switch {
		case !found && l.Seq > headSeq:
			st.State = caudit.AnchorTailDeleted
			st.Message = fmt.Sprintf("마지막 기록이 삭제된 것으로 보입니다. 외부 앵커는 %d번 기록까지 있었는데 DB 의 마지막 기록은 %d번입니다.", l.Seq, headSeq)
			return st, nil
		case !found:
			st.State = caudit.AnchorRecomputed
			st.Message = fmt.Sprintf("%d번 기록이 외부 앵커와 맞지 않습니다(기록이 지워졌거나 다시 계산되었습니다).", l.Seq)
			return st, nil
		case hash != l.Head:
			st.State = caudit.AnchorRecomputed
			st.Message = fmt.Sprintf("%d번 기록의 내용이 외부 앵커와 다릅니다. 기록이 처음부터 다시 계산되어 덮어써졌을 가능성이 있습니다.", l.Seq)
			return st, nil
		}
	}
	st.State = caudit.AnchorOK
	st.Message = fmt.Sprintf("외부 앵커 %d개와 대조했고 모두 일치합니다(마지막 앵커는 %d번 기록).", len(lines), last.Seq)
	return st, nil
}

// Status 는 앵커 파일 검사와 DB 대조 결과다. Store.Verify 가 부른다.
func (a *Anchor) Status(ctx context.Context) (caudit.AnchorStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, err := a.status(ctx)
	if err != nil {
		return st, err
	}
	hist, err := lostHistory(ctx, a.store.db)
	if err != nil {
		return st, err
	}
	if hist {
		st.LostHistory = true
		st.Message += " 앵커 유실 이력 있음: 과거에 앵커 파일이 유실되어 다시 시작한 적이 있습니다."
	}
	return st, nil
}

func (a *Anchor) status(ctx context.Context) (caudit.AnchorStatus, error) {
	lines, fault, missingFile := a.read()
	if fault != "" && !missingFile {
		return caudit.AnchorStatus{State: caudit.AnchorFileCorrupt, Message: "외부 앵커 파일이 손상되었습니다: " + fault}, nil
	}
	lost, lastSeq, err := lostStamps(ctx, a.store.db, lines)
	if err != nil {
		return caudit.AnchorStatus{}, err
	}
	if lost > 0 {
		return lostStatus(lost, lastSeq, lines), nil
	}
	if fault != "" { // 파일이 없는데 유실로 볼 근거(이전 앵커 기록)도 없다
		return caudit.AnchorStatus{State: caudit.AnchorFileCorrupt, Message: "외부 앵커 파일이 손상되었습니다: " + fault}, nil
	}
	return a.compare(ctx, lines)
}

// Restart 는 유실된 앵커를 명시적으로 다시 시작한다(B9). 유실 상태가 아니면 ErrInvalidState 다.
// 이후의 유실 검사는 이 시점부터 시작하고, 검증 결과의 "앵커 유실 이력 있음"은 계속 남는다.
// a.mu 를 잡지 않는다: tx 가 DB 연결을 쥔 채 잠금을 기다리면 Status 와 교착한다.
func (a *Anchor) Restart(ctx context.Context, tx contract.Tx) error {
	lines, fault, missingFile := a.read()
	if fault != "" && !missingFile {
		return fmt.Errorf("%w: 앵커 파일이 손상되어 있습니다. 파일을 치운 뒤 다시 시도하세요: %s", contract.ErrInvalidState, fault)
	}
	lost, _, err := lostStamps(ctx, tx, lines)
	if err != nil {
		return err
	}
	if lost == 0 {
		return fmt.Errorf("%w: 앵커가 유실된 상태가 아닙니다", contract.ErrInvalidState)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_anchor_stamps (kind, at) VALUES ('reset', ?)`, a.now().UTC().Format(timeLayout))
	return err
}

var _ caudit.AnchorRestarter = (*Anchor)(nil)

// Stamp 는 체인 머리를 서명해 파일 끝에 한 줄 덧붙인다. 새 기록이 없으면 아무것도 쓰지 않는다.
// 파일이 손상되었거나 DB 가 기존 앵커와 맞지 않으면 쓰지 않고 오류를 돌려준다(어긋난 DB 를 앵커로 굳히지 않는다).
func (a *Anchor) Stamp(ctx context.Context) (caudit.StampResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(a.path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return caudit.StampResult{}, fmt.Errorf("앵커 파일을 열 수 없습니다: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return caudit.StampResult{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return caudit.StampResult{}, err
	}
	lines, fault := a.parse(data)
	if fault != "" {
		return caudit.StampResult{}, errors.New("앵커 파일이 손상되어 기록하지 않습니다: " + fault)
	}
	// 파일이 사라졌다가 O_CREATE 로 다시 생기거나 줄이 잘렸으면, 새 앵커로 조용히 정상 복귀하지 않는다.
	lost, lostSeq, err := lostStamps(ctx, a.store.db, lines)
	if err != nil {
		return caudit.StampResult{}, err
	}
	if lost > 0 {
		return caudit.StampResult{}, fmt.Errorf("%w: 앵커 %d줄 유실(마지막 %d번 기록)", caudit.ErrAnchorLost, lost, lostSeq)
	}
	cmp, err := a.compare(ctx, lines)
	if err != nil {
		return caudit.StampResult{}, err
	}
	if cmp.State.Failed() {
		return caudit.StampResult{}, errors.New("DB 가 기존 외부 앵커와 맞지 않아 기록하지 않습니다: " + cmp.Message)
	}
	seq, hash, ok, err := a.store.head(ctx)
	if err != nil {
		return caudit.StampResult{}, err
	}
	if !ok || (len(lines) > 0 && lines[len(lines)-1].Seq == seq) {
		return caudit.StampResult{}, nil // 새 기록 없음: 줄을 늘리지 않는다
	}
	l := anchorLine{V: anchorVersion, At: a.now().UTC().Format(timeLayout), Seq: seq, Head: hash}
	if n := len(lines); n > 0 {
		l.Prev = hashLine(lines[n-1].raw)
	}
	l.Sig = a.sign(l)
	b, err := json.Marshal(l)
	if err != nil {
		return caudit.StampResult{}, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return caudit.StampResult{}, fmt.Errorf("앵커 기록 실패: %w", err)
	}
	if err := f.Sync(); err != nil {
		return caudit.StampResult{}, fmt.Errorf("앵커 기록 실패: %w", err)
	}
	// 파일에 쓴 뒤에 대장에 남긴다. 반대 순서면 쓰기 실패가 가짜 유실로 보인다.
	if _, err := a.store.db.ExecContext(ctx, `INSERT INTO audit_anchor_stamps (kind, seq, line_hash, at) VALUES ('stamp', ?, ?, ?)`,
		seq, hashLine(string(b)), l.At); err != nil {
		return caudit.StampResult{}, fmt.Errorf("앵커 대장 기록 실패: %w", err)
	}
	at, _ := time.Parse(timeLayout, l.At)
	return caudit.StampResult{Written: true, Seq: seq, Hash: hash, At: at}, nil
}

// Run 은 interval 마다 Stamp 를 부른다. ctx 가 끝나면 돌아온다(종료 시 마지막 한 번은 호출자가 Stamp 로 찍는다).
func (a *Anchor) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := a.Stamp(ctx); err != nil {
				log.Printf("audit anchor: %v", err)
			}
		}
	}
}

// head 는 DB 체인의 마지막 기록이다. 기록이 없으면 ok=false.
func (s *Store) head(ctx context.Context) (seq int64, hash string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT seq, hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	return seq, hash, err == nil, err
}

func (s *Store) hashAt(ctx context.Context, seq int64) (hash string, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT hash FROM audit_logs WHERE seq = ?`, seq).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

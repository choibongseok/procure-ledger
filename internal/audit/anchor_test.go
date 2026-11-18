package audit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

var anchorKey = []byte("test-anchor-key-0123456789")

func setupAnchor(t *testing.T) (*audit.Store, contract.SQLTxManager, *audit.Anchor, string) {
	t.Helper()
	s, txs := setup(t)
	path := filepath.Join(t.TempDir(), "anchor.log")
	a, err := audit.NewAnchor(s, path, anchorKey)
	if err != nil {
		t.Fatal(err)
	}
	return s, txs, a, path
}

func dropTriggers(t *testing.T, txs contract.SQLTxManager) {
	t.Helper()
	for _, tr := range []string{"trg_audit_no_update", "trg_audit_no_delete"} {
		dbtest.DropTrigger(t, txs.DB, tr)
	}
}

func stamp(t *testing.T, a *audit.Anchor) caudit.StampResult {
	t.Helper()
	r, err := a.Stamp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func verify(t *testing.T, s *audit.Store) caudit.VerifyResult {
	t.Helper()
	r, err := s.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func anchorLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAnchorOKWhenUntouched(t *testing.T) {
	s, txs, a, _ := setupAnchor(t)
	fill(t, s, txs, 3)
	if r := stamp(t, a); !r.Written || r.Seq != 3 {
		t.Fatalf("stamp: %+v", r)
	}
	fill(t, s, txs, 2)
	stamp(t, a)
	res := verify(t, s)
	if !res.OK || !res.ChainOK || res.Anchor.State != caudit.AnchorOK || res.Anchor.Lines != 2 || res.Anchor.LastSeq != 5 {
		t.Fatalf("%+v", res)
	}
}

// 변조 시나리오 1: 꼬리(마지막 기록)를 지우면 체인 검사는 통과하지만 앵커 대조가 잡는다.
func TestAnchorDetectsTailDeletion(t *testing.T) {
	s, txs, a, _ := setupAnchor(t)
	fill(t, s, txs, 5)
	stamp(t, a)
	dropTriggers(t, txs)
	if _, err := txs.DB.Exec(`DELETE FROM audit_logs WHERE seq > 3`); err != nil {
		t.Fatal(err)
	}
	res := verify(t, s)
	if !res.ChainOK {
		t.Fatalf("체인 자체는 이어져 있어야 함(체인만으로는 꼬리 삭제를 못 잡는다): %+v", res)
	}
	if res.OK || res.Anchor.State != caudit.AnchorTailDeleted || !strings.Contains(res.Anchor.Message, "삭제") {
		t.Fatalf("꼬리 삭제를 짚어야 함: %+v", res)
	}
}

// 변조 시나리오 2: 한 행을 바꾸고 체인 전체를 다시 계산하면 체인 검사는 통과하지만 앵커 해시와 달라진다.
func TestAnchorDetectsFullChainRecomputation(t *testing.T) {
	s, txs, a, _ := setupAnchor(t)
	fill(t, s, txs, 5)
	stamp(t, a)
	dropTriggers(t, txs)
	recs, err := s.List(context.Background(), caudit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	for _, r := range recs {
		r.PrevHash = prev
		if r.Seq == 2 {
			r.Detail = "조작된 사유"
		}
		r.Hash = caudit.HashRecord(r)
		if _, err := txs.DB.Exec(`UPDATE audit_logs SET detail = ?, prev_hash = ?, hash = ? WHERE seq = ?`, r.Detail, r.PrevHash, r.Hash, r.Seq); err != nil {
			t.Fatal(err)
		}
		prev = r.Hash
	}
	res := verify(t, s)
	if !res.ChainOK {
		t.Fatalf("재계산한 체인은 자체 검사를 통과해야 함: %+v", res)
	}
	if res.OK || res.Anchor.State != caudit.AnchorRecomputed || !strings.Contains(res.Anchor.Message, "5번") {
		t.Fatalf("내용 재계산을 짚어야 함: %+v", res)
	}
}

func TestAnchorDisabledIsExplicit(t *testing.T) {
	s, txs := setup(t)
	fill(t, s, txs, 2)
	res := verify(t, s)
	if !res.OK || res.Anchor.State != caudit.AnchorDisabled || !strings.Contains(res.Anchor.Message, "외부 대조를 하지 않았습니다") {
		t.Fatalf("키가 없으면 외부 대조 없음을 분명히 알려야 함: %+v", res)
	}
}

func TestAnchorEmptyIsNotAPass(t *testing.T) {
	s, txs, _, _ := setupAnchor(t)
	fill(t, s, txs, 2)
	res := verify(t, s)
	if res.Anchor.State != caudit.AnchorEmpty || !strings.Contains(res.Anchor.Message, "하지 못했습니다") {
		t.Fatalf("%+v", res)
	}
}

func TestAnchorForgedSignatureDetected(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	fill(t, s, txs, 2)
	stamp(t, a)
	fill(t, s, txs, 2)
	stamp(t, a)
	lines := anchorLines(t, path)
	// 키 없이 내용을 바꾼 줄(서명은 그대로).
	lines[1] = strings.Replace(lines[1], `"seq":4`, `"seq":3`, 1)
	writeLines(t, path, lines)
	res := verify(t, s)
	if res.OK || res.Anchor.State != caudit.AnchorFileCorrupt || !strings.Contains(res.Anchor.Message, "서명") {
		t.Fatalf("서명 위조를 짚어야 함: %+v", res)
	}
}

func TestAnchorLineDeletionAndReorderDetected(t *testing.T) {
	build := func(t *testing.T) (*audit.Store, string, []string) {
		s, txs, a, path := setupAnchor(t)
		for i := 0; i < 3; i++ {
			fill(t, s, txs, 1)
			stamp(t, a)
		}
		lines := anchorLines(t, path)
		if len(lines) != 3 {
			t.Fatalf("줄 수 %d", len(lines))
		}
		return s, path, lines
	}
	cases := map[string]func([]string) []string{
		"중간 줄 삭제":   func(l []string) []string { return []string{l[0], l[2]} },
		"첫 줄 삭제":    func(l []string) []string { return l[1:] },
		"순서 바꿈":     func(l []string) []string { return []string{l[0], l[2], l[1]} },
		"줄 복제":      func(l []string) []string { return []string{l[0], l[1], l[1], l[2]} },
		"빈 줄 끼워 넣기": func(l []string) []string { return []string{l[0], "", l[1], l[2]} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, path, lines := build(t)
			writeLines(t, path, mutate(lines))
			res := verify(t, s)
			if res.OK || res.Anchor.State != caudit.AnchorFileCorrupt {
				t.Fatalf("앵커 파일 손상을 짚어야 함: %+v", res)
			}
		})
	}
	t.Run("파일 삭제", func(t *testing.T) {
		s, path, _ := build(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if res := verify(t, s); res.OK || res.Anchor.State != caudit.AnchorLost {
			t.Fatalf("대장에 앵커 기록이 있으니 유실로 보여야 함: %+v", res)
		}
	})
	t.Run("끊긴 마지막 줄", func(t *testing.T) {
		s, path, lines := build(t)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+`{"v":1,"at"`), 0o600); err != nil {
			t.Fatal(err)
		}
		if res := verify(t, s); res.OK || res.Anchor.State != caudit.AnchorFileCorrupt {
			t.Fatalf("%+v", res)
		}
	})
}

func TestAnchorStampWithoutNewRecordsAddsNoLine(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	if r := stamp(t, a); r.Written {
		t.Fatal("기록이 하나도 없으면 줄을 쓰지 않아야 함")
	}
	fill(t, s, txs, 3)
	if r := stamp(t, a); !r.Written {
		t.Fatal("새 기록이 있으면 써야 함")
	}
	for i := 0; i < 3; i++ {
		if r := stamp(t, a); r.Written {
			t.Fatal("새 기록이 없으면 중복 기록하지 않아야 함")
		}
	}
	if n := len(anchorLines(t, path)); n != 1 {
		t.Fatalf("줄 수 %d, 기대 1", n)
	}
	fill(t, s, txs, 1)
	stamp(t, a)
	if n := len(anchorLines(t, path)); n != 2 {
		t.Fatalf("줄 수 %d, 기대 2", n)
	}
}

// 어긋난 DB 를 새 앵커로 굳히지 않는다.
func TestAnchorStampRefusesWhenDBDiverged(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	fill(t, s, txs, 4)
	stamp(t, a)
	dropTriggers(t, txs)
	if _, err := txs.DB.Exec(`DELETE FROM audit_logs WHERE seq > 2`); err != nil {
		t.Fatal(err)
	}
	fill(t, s, txs, 5) // 지운 뒤 새 기록이 쌓여 seq 가 앞서 간다
	before := anchorLines(t, path)
	if _, err := a.Stamp(context.Background()); err == nil {
		t.Fatal("앵커와 맞지 않는 DB 는 기록을 거부해야 함")
	}
	if len(anchorLines(t, path)) != len(before) {
		t.Fatal("거부했는데 줄이 늘었음")
	}
}

func TestNewAnchorRejectsBadConfig(t *testing.T) {
	s, _ := setup(t)
	dir := t.TempDir()
	if _, err := audit.NewAnchor(s, filepath.Join(dir, "a.log"), []byte("short")); err == nil {
		t.Error("짧은 키는 거부되어야 함")
	}
	if _, err := audit.NewAnchor(s, "", anchorKey); err == nil {
		t.Error("경로가 없으면 거부되어야 함")
	}
	if _, err := audit.NewAnchor(s, filepath.Join(dir, "없는디렉터리", "a.log"), anchorKey); err == nil {
		t.Error("쓸 수 없는 경로는 거부되어야 함")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "ok.log")
	if _, err := audit.NewAnchor(s, p, anchorKey); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("앵커 파일 권한: %v %v", info, err)
	}
}

// 키가 다르면(예: 키가 바뀐 채 재기동) 기존 줄을 위조로 본다.
func TestAnchorWrongKeyIsCorrupt(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	fill(t, s, txs, 2)
	stamp(t, a)
	if _, err := audit.NewAnchor(s, path, []byte("different-key-0123456789")); err != nil {
		t.Fatal(err)
	}
	if res := verify(t, s); res.OK || res.Anchor.State != caudit.AnchorFileCorrupt {
		t.Fatalf("%+v", res)
	}
}

// ---- B9 ③ 앵커가 꺼졌거나 비어 있으면 최상위 결과가 완전 통과와 구분된다 ----

func TestVerifyLevelDistinguishesChainOnlyFromFullPass(t *testing.T) {
	// 앵커 꺼짐: OK 는 true 지만 외부 대조는 아니다.
	s, txs := setup(t)
	fill(t, s, txs, 2)
	res := verify(t, s)
	if !res.OK || res.ExternallyVerified() || res.Level() != caudit.LevelChainOnly || !strings.Contains(res.Summary(), "완전 통과가 아닙니다") {
		t.Fatalf("꺼짐: %+v level=%s", res, res.Level())
	}
	// 앵커 비어 있음.
	s2, txs2, a, _ := setupAnchor(t)
	fill(t, s2, txs2, 2)
	if res := verify(t, s2); !res.OK || res.ExternallyVerified() || res.Level() != caudit.LevelChainOnly {
		t.Fatalf("비어 있음: %+v", res)
	}
	// 앵커 일치: 완전 통과.
	stamp(t, a)
	if res := verify(t, s2); !res.ExternallyVerified() || res.Level() != caudit.LevelVerified {
		t.Fatalf("일치: %+v", res)
	}
}

// ---- B9 ④ 앵커 파일 유실 ----

func TestAnchorFileRemovedThenManualStampDoesNotReturnToOK(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	fill(t, s, txs, 3)
	stamp(t, a)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fill(t, s, txs, 1)
	_, err := a.Stamp(context.Background())
	if !errors.Is(err, caudit.ErrAnchorLost) {
		t.Fatalf("유실을 거부해야 함: %v", err)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("조용히 새 앵커를 쓰면 안 됨: %q", b)
	}
	if res := verify(t, s); res.OK || res.Anchor.State != caudit.AnchorLost || res.Level() != caudit.LevelFailed {
		t.Fatalf("%+v", res)
	}
}

func TestAnchorTruncatedTailIsLost(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	for i := 0; i < 3; i++ {
		fill(t, s, txs, 1)
		stamp(t, a)
	}
	writeLines(t, path, anchorLines(t, path)[:2])
	if res := verify(t, s); res.OK || res.Anchor.State != caudit.AnchorLost {
		t.Fatalf("끝 줄 잘림은 유실: %+v", res)
	}
}

// 재기동(새 Anchor)해도 근거가 DB 라서 같은 판정이다.
func TestAnchorLossSurvivesRestartAndRestartRequiresExplicitStep(t *testing.T) {
	s, txs, a, path := setupAnchor(t)
	fill(t, s, txs, 3)
	stamp(t, a)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a2, err := audit.NewAnchor(s, path, anchorKey) // 프로세스 재기동: 빈 파일이 새로 생긴다
	if err != nil {
		t.Fatal(err)
	}
	if res := verify(t, s); res.Anchor.State != caudit.AnchorLost || res.OK {
		t.Fatalf("재기동 뒤에도 유실: %+v", res)
	}
	if _, err := a2.Stamp(context.Background()); !errors.Is(err, caudit.ErrAnchorLost) {
		t.Fatalf("재기동 뒤에도 조용한 복귀 금지: %v", err)
	}
	// 명시적 재시작.
	if err := txs.WithTx(context.Background(), func(tx contract.Tx) error { return a2.Restart(context.Background(), tx) }); err != nil {
		t.Fatal(err)
	}
	fill(t, s, txs, 1)
	stamp(t, a2)
	res := verify(t, s)
	if !res.OK || res.Anchor.State != caudit.AnchorOK || !res.Anchor.LostHistory || res.ExternallyVerified() || res.Level() != caudit.LevelLostHistory {
		t.Fatalf("재시작 뒤 복귀하되 유실 이력이 남아야 함: %+v", res)
	}
	if !strings.Contains(res.Anchor.Message, "앵커 유실 이력 있음") {
		t.Fatalf("메시지에 이력 표시: %s", res.Anchor.Message)
	}
	// 다시 재기동해도 이력이 유지된다.
	if _, err := audit.NewAnchor(s, path, anchorKey); err != nil {
		t.Fatal(err)
	}
	if res := verify(t, s); !res.Anchor.LostHistory {
		t.Fatalf("재기동 뒤에도 이력 유지: %+v", res)
	}
}

func TestAnchorRestartRefusedWhenNotLost(t *testing.T) {
	s, txs, a, _ := setupAnchor(t)
	fill(t, s, txs, 2)
	stamp(t, a)
	err := txs.WithTx(context.Background(), func(tx contract.Tx) error { return a.Restart(context.Background(), tx) })
	if !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("유실이 아니면 재시작 거부: %v", err)
	}
	if res := verify(t, s); res.Anchor.LostHistory {
		t.Fatal("거부된 재시작이 이력을 남기면 안 됨")
	}
}

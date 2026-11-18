package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// legacyHash 는 B44 이전의 체인 해시 계산을 그대로 옮긴 것이다. 이 함수가 바뀌면 이관 전 모든 기록의 검증이 깨진다.
func legacyHash(rec Record) string {
	diffJSON, _ := json.Marshal(rec.Diff)
	payload, _ := json.Marshal([]any{
		rec.PrevHash, rec.Seq, rec.At.UTC().UnixNano(), rec.ActorID, string(rec.ActorRole),
		string(rec.Channel), rec.Action, string(rec.TargetType), rec.TargetID, string(rec.Result),
		string(diffJSON), rec.Detail,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func sampleRecord() Record {
	return Record{Seq: 41, At: time.Date(2026, 11, 13, 1, 2, 3, 456, time.UTC), ActorID: 7, ActorRole: contract.RoleFinance, Channel: contract.ChannelWeb,
		Action: "journal.approve", TargetType: TargetJournal, TargetID: "12", Result: ResultSuccess, Diff: []DiffItem{{Key: "status", Old: "requested", New: "approved"}}, Detail: "x", PrevHash: "abc"}
}

// 법인 0(이관 전·법인과 무관한 사건)의 해시는 이전 계산과 같고, 법인이 있으면 달라지며, 법인 값을 바꾸면(2→3, 2→0) 달라진다(B44 13.1, AC-4415).
func TestHashRecordEntityIsConditional(t *testing.T) {
	r := sampleRecord()
	if HashRecord(r) != legacyHash(r) {
		t.Fatal("법인 0 의 해시가 이전 계산과 다르다: 이관 전 기록의 검증이 깨진다")
	}
	r2 := r
	r2.EntityID = 2
	r3 := r
	r3.EntityID = 3
	if HashRecord(r2) == HashRecord(r) || HashRecord(r2) == HashRecord(r3) {
		t.Fatal("법인 값이 해시에 묶이지 않는다")
	}
	if HashRecord(r2) != HashRecord(r2) {
		t.Fatal("해시가 결정적이지 않다")
	}
}

func TestEntityOf(t *testing.T) {
	ctx := contract.WithActiveEntity(context.Background(), contract.ActiveEntity{ID: 5})
	all := contract.WithActiveEntity(context.Background(), contract.ActiveEntity{All: true})
	cases := []struct {
		name string
		ctx  context.Context
		e    Entry
		want int64
	}{
		{"명시값 우선", ctx, Entry{EntityID: 9}, 9},
		{"현재 법인", ctx, Entry{}, 5},
		{"법인과 무관 강제", ctx, Entry{EntityID: NoEntity}, 0},
		{"전체 모드는 0", all, Entry{}, 0},
		{"컨텍스트에 법인 없음", context.Background(), Entry{}, 0},
	}
	for _, c := range cases {
		if got := EntityOf(c.ctx, c.e); got != c.want {
			t.Errorf("%s: %d, 기대 %d", c.name, got, c.want)
		}
	}
}

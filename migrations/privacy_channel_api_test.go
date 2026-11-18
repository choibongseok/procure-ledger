package migrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0061(B41·B42): 조회 기록 경로에 'api' 를 더하려고 두 표를 다시 만든다. 기존 행·정보주체·해시가 그대로여서 체인 검증이 통과하고,
// 추가 전용 트리거와 색인이 살아 있으며 이후 api 경로 기록이 같은 체인에 붙어야 한다(SQLite 재작성 시험, PG 는 제약 교체).
func TestPrivacyChannelAPIRebuildKeepsChain(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0060_privacy_access_log.sql"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	txs := dbutil.NewTxManager(db, dbutil.DialectOf(db))
	rec, err := privacy.New(privacy.Options{DB: db, Txs: txs, Audit: audit.New(db), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	web := contract.Principal{UserID: 7, Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	note := func(p contract.Principal, ids ...int64) {
		t.Helper()
		a := privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Fields: []string{privacy.FieldEmail}}
		for _, id := range ids {
			a.Subjects = append(a.Subjects, privacy.Subject{Type: privacy.SubjectUser, ID: id})
		}
		if err := rec.Note(ctx, p, a); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	for i := 0; i < 4; i++ {
		note(web, 1, 2, 3)
	}
	if _, err := db.Exec(`INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('t',0,'x','api','contact','s',0,'','','h-old')`); err == nil {
		t.Fatal("0061 전에 api 경로가 들어감")
	}
	var before int
	var headBefore string
	_ = db.QueryRow(`SELECT COUNT(*) FROM privacy_access_log`).Scan(&before)
	_ = db.QueryRow(`SELECT hash FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&headBefore)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var after, subs int
	var headAfter string
	_ = db.QueryRow(`SELECT COUNT(*) FROM privacy_access_log`).Scan(&after)
	_ = db.QueryRow(`SELECT COUNT(*) FROM privacy_access_subjects`).Scan(&subs)
	_ = db.QueryRow(`SELECT hash FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&headAfter)
	if before != after || headBefore != headAfter || subs != 12 {
		t.Fatalf("행·정보주체·체인 머리가 바뀜: %d→%d %s→%s 정보주체 %d", before, after, headBefore, headAfter, subs)
	}
	res, err := rec.Verify(ctx)
	if err != nil || !res.OK || res.Checked != 4 {
		t.Fatalf("이관 직후 체인 검증: %+v %v", res, err)
	}
	// 이어서 api 경로 기록이 같은 체인에 붙고, 알 수 없는 경로는 여전히 거부된다.
	note(contract.Principal{UserID: 9, Role: "api", Channel: contract.ChannelAPI}, 5)
	if res, err := rec.Verify(ctx); err != nil || !res.OK || res.Checked != 5 {
		t.Fatalf("api 기록 뒤 체인 검증: %+v %v", res, err)
	}
	var ch string
	if err := db.QueryRow(`SELECT channel FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&ch); err != nil || ch != "api" {
		t.Fatalf("마지막 행의 경로 %q %v", ch, err)
	}
	if _, err := db.Exec(`INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('t',0,'x','ssh','contact','s',0,'','','h-bad')`); err == nil {
		t.Fatal("알 수 없는 경로가 들어감")
	}
	// 추가 전용 트리거·정보주체 FK·색인이 다시 만들어졌다.
	if _, err := db.Exec(`UPDATE privacy_access_log SET screen = 'x' WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("수정이 막히지 않음: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id = 1`); err == nil {
		t.Fatal("정보주체 삭제가 막히지 않음")
	}
	for _, idx := range []string{"idx_privacy_log_at", "idx_privacy_log_actor", "idx_privacy_subjects_subject"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("색인 %s 가 없음", idx)
		}
	}
}

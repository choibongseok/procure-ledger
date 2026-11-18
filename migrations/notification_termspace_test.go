package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0063: 알림 웹훅 대상 종류에 termspace 를 더하려고 notification_targets·notification_deliveries 를 다시 만든다.
// 기존 행·번호·시퀀스·색인·트리거가 그대로 살아 있고, 새 종류만 더 받아야 한다.
func TestNotificationTermspaceRebuildKeepsRowsIndexesAndTriggers(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0062_sod.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO notification_targets (name, type, secret_sealed, hint, kinds_json, created_at, updated_at) VALUES ('슬랙방','slack','sealed-1','h1','["approval_pending"]','t','t'), ('팀즈방','teams','sealed-2','h2','[]','t','t')`)
	mustExec(`INSERT INTO notification_deliveries (target_id, channel, kind, status, next_try_at, created_at) VALUES (1,'slack','approval_pending','sent','t','t'), (2,'teams','approval_pending','queued','t','t'), (1,'slack','connection_test','sent','t','t')`)
	// 옛 제약에서는 termspace 가 들어가지 않는다.
	if _, err := db.Exec(`INSERT INTO notification_targets (name, type, secret_sealed, created_at, updated_at) VALUES ('ts','termspace','s','t','t')`); err == nil {
		t.Fatal("0063 전에 termspace 대상이 들어감")
	}
	// 지워서 시퀀스가 최대 id 보다 앞서 가게 한다(보낸 건만 지울 수 있다).
	mustExec(`DELETE FROM notification_deliveries WHERE id = 3`)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if count(`SELECT COUNT(*) FROM notification_targets`) != 2 || count(`SELECT COUNT(*) FROM notification_deliveries`) != 2 {
		t.Fatal("행 수가 바뀜")
	}
	var name, sealed, hint string
	if err := db.QueryRow(`SELECT name, secret_sealed, hint FROM notification_targets WHERE id = 2`).Scan(&name, &sealed, &hint); err != nil || name != "팀즈방" || sealed != "sealed-2" || hint != "h2" {
		t.Fatalf("행 내용이 바뀜: %q %q %q %v", name, sealed, hint, err)
	}
	// 새 종류는 받고 알 수 없는 종류는 여전히 거부한다.
	mustExec(`INSERT INTO notification_targets (name, type, secret_sealed, created_at, updated_at) VALUES ('termspace방','termspace','s','t','t')`)
	if _, err := db.Exec(`INSERT INTO notification_targets (name, type, secret_sealed, created_at, updated_at) VALUES ('x','discord','s','t','t')`); err == nil {
		t.Fatal("알 수 없는 종류가 들어감")
	}
	mustExec(`INSERT INTO notification_deliveries (target_id, channel, kind, next_try_at, created_at) VALUES (3,'termspace','approval_pending','t','t')`)
	if _, err := db.Exec(`INSERT INTO notification_deliveries (target_id, channel, kind, next_try_at, created_at) VALUES (3,'discord','approval_pending','t','t')`); err == nil {
		t.Fatal("알 수 없는 채널이 들어감")
	}
	// 시퀀스가 이어진다: 새 발송 건 번호는 지운 3번보다 뒤다(번호 재사용 없음).
	var maxID int
	_ = db.QueryRow(`SELECT MAX(id) FROM notification_deliveries`).Scan(&maxID)
	if maxID < 4 {
		t.Fatalf("AUTOINCREMENT 시퀀스가 이어지지 않음: 새 발송 건 번호 %d", maxID)
	}
	// 트리거: 종류 고정·삭제 금지(대상), 상태 전이 표·고정 칸·처리 중 삭제 금지(발송 건).
	expectFail := func(q, want string) {
		t.Helper()
		_, err := db.Exec(q)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: 오류 %v (기대 %q)", q, err, want)
		}
	}
	expectFail(`UPDATE notification_targets SET type = 'slack' WHERE id = 3`, "종류는 바꿀 수 없습니다")
	expectFail(`DELETE FROM notification_targets WHERE id = 1`, "지울 수 없습니다")
	expectFail(`UPDATE notification_deliveries SET status = 'sent' WHERE id = 2`, "상태 전이가 올바르지 않습니다") // queued → sent 는 표에 없다
	expectFail(`UPDATE notification_deliveries SET channel = 'slack' WHERE id = 4`, "바꿀 수 없습니다")
	expectFail(`DELETE FROM notification_deliveries WHERE id = 2`, "처리 중인 발송 건")
	mustExec(`UPDATE notification_deliveries SET status = 'sending' WHERE id = 2`) // 표 안의 전이는 된다
	// 색인: 시험 발송을 뺀 부분 유일 색인이 살아 있어 같은 (알림·사람·대상·채널) 중복은 막고 시험 발송은 여러 번 된다.
	expectFail(`INSERT INTO notification_deliveries (target_id, channel, kind, next_try_at, created_at) VALUES (1,'slack','approval_pending','t','t')`, "UNIQUE")
	mustExec(`INSERT INTO notification_deliveries (target_id, channel, kind, next_try_at, created_at) VALUES (1,'slack','connection_test','t','t')`)
	mustExec(`INSERT INTO notification_deliveries (target_id, channel, kind, next_try_at, created_at) VALUES (1,'slack','connection_test','t','t')`)
	for _, ix := range []string{"idx_notification_deliveries_once", "idx_notification_deliveries_due", "idx_notification_deliveries_user", "idx_notification_deliveries_created",
		"idx_notification_deliveries_target", "idx_notification_deliveries_held"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = '`+ix+`'`) != 1 {
			t.Errorf("색인 %s 가 없다", ix)
		}
	}
	for _, tg := range []string{"trg_notification_targets_type_fixed", "trg_notification_targets_no_delete", "trg_notification_deliveries_status", "trg_notification_deliveries_fixed", "trg_notification_deliveries_no_delete"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = '`+tg+`'`) != 1 {
			t.Errorf("트리거 %s 가 없다", tg)
		}
	}
	if count(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\'`) != 0 {
		t.Error("임시 표가 남음")
	}
}

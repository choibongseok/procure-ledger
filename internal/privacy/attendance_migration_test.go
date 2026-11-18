package privacy_test

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

// B50 0099: 열람 기록 범주 'attendance' 추가는 SQLite 에서 표를 다시 만든다. 기존 설치(0095 까지 적용, 기록이 쌓인 DB)에 적용해도
// 해시 체인·정보주체 행·추가 전용 트리거가 그대로이고, 새 범주가 기록되며 체인이 이어지는지 본다(SQLite 전용. PG 는 제약만 바꾸는 짝 파일이고 verify-pg 의 일반 시험이 지난다).
func TestAttendanceCategoryMigrationKeepsChainAndGuards(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.ApplyThrough(ctx, db, "0095_mcp_tool_enable.sql"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO departments (name) VALUES ('운영')`)
	user := func(email string) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, 'staff', (SELECT id FROM departments WHERE name = '운영')) RETURNING id`, email, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	viewer, s1, s2 := user("v@x.test"), user("s1@x.test"), user("s2@x.test")
	clock := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	txs := dbutil.NewTxManager(db, dbutil.DialectOf(db))
	rec, err := privacy.New(privacy.Options{DB: db, Txs: txs, Audit: audit.New(db), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	actor := contract.Principal{UserID: viewer, Name: "열람자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	// 이전 범주 기록 여섯 건(정보주체 여러 명, 거래 상대방 유형 포함).
	for i := 0; i < 6; i++ {
		clock = clock.Add(time.Minute)
		subs := []privacy.Subject{{Type: privacy.SubjectUser, ID: s1}, {Type: privacy.SubjectUser, ID: s2}}
		a := privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Fields: []string{privacy.FieldEmail}, Subjects: subs}
		if i == 5 {
			a = privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenTaxInvoiceDetail, Fields: []string{privacy.FieldEmail}, Subjects: []privacy.Subject{{Type: privacy.SubjectCounterparty, ID: 7}}}
		}
		if err := rec.Note(ctx, actor, a); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	lastHash := func() string {
		var h string
		if err := db.QueryRow(`SELECT hash FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	before := struct {
		logs, subs int
		last       string
	}{count(`SELECT COUNT(*) FROM privacy_access_log`), count(`SELECT COUNT(*) FROM privacy_access_subjects`), lastHash()}
	if before.logs != 6 || before.subs != 11 {
		t.Fatalf("시드 수: %+v", before)
	}
	if res, err := rec.Verify(ctx); err != nil || !res.OK || res.Checked != 6 {
		t.Fatalf("적용 전 체인: %+v %v", res, err)
	}
	// 새 범주는 아직 거부된다(범주 열거가 옛 값).
	if _, err := db.Exec(`INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('x', 1, 'admin', 'web', 'attendance', '근태 사유 보기', 0, 'h', 'p', 'zz')`); err == nil {
		t.Fatal("적용 전에는 attendance 범주가 거부돼야 함")
	}

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("0099 적용: %v", err)
	}

	after := struct {
		logs, subs int
		last       string
	}{count(`SELECT COUNT(*) FROM privacy_access_log`), count(`SELECT COUNT(*) FROM privacy_access_subjects`), lastHash()}
	if after != before {
		t.Fatalf("행·정보주체·마지막 해시가 바뀜: %+v → %+v", before, after)
	}
	if res, err := rec.Verify(ctx); err != nil || !res.OK || res.Checked != 6 {
		t.Fatalf("적용 뒤 체인: %+v %v", res, err)
	}
	if n := count(`SELECT COUNT(*) FROM privacy_access_subjects WHERE subject_type = 'counterparty'`); n != 1 {
		t.Fatalf("거래 상대방 정보주체가 보존되지 않음: %d", n)
	}
	// 추가 전용 트리거와 색인이 새 표에도 있다.
	if _, err := db.Exec(`UPDATE privacy_access_log SET screen = '바꿈' WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("수정 거부 트리거 없음: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM privacy_access_log WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "파기") {
		t.Fatalf("삭제 거부 트리거 없음: %v", err)
	}
	if _, err := db.Exec(`UPDATE privacy_access_subjects SET subject_id = 1 WHERE log_id = 1`); err == nil {
		t.Fatal("정보주체 수정 거부 트리거 없음")
	}
	if _, err := db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id = 1`); err == nil {
		t.Fatal("정보주체 삭제 거부 트리거 없음")
	}
	for _, idx := range []string{"idx_privacy_log_at", "idx_privacy_log_actor", "idx_privacy_subjects_subject"} {
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = '` + idx + `'`); n != 1 {
			t.Fatalf("색인 %s 가 없음", idx)
		}
	}
	// 새 범주 기록: 체인이 이어지고(직전 해시 = 옛 마지막 해시), id 가 이어서 늘고, 정보주체가 붙는다. 사유 원문은 어디에도 담기지 않는다.
	clock = clock.Add(time.Minute)
	if err := rec.Note(ctx, actor, privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason, Fields: []string{privacy.FieldLeaveReason, privacy.FieldAbsenceReason},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: s1}}}); err != nil {
		t.Fatalf("attendance 기록: %v", err)
	}
	clock = clock.Add(time.Minute)
	if err := rec.Note(ctx, actor, privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceProof, Fields: []string{privacy.FieldProofFile},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: s2}}}); err != nil {
		t.Fatalf("증빙 기록: %v", err)
	}
	var prev string
	var id int64
	if err := db.QueryRow(`SELECT id, prev_hash FROM privacy_access_log WHERE category = 'attendance' ORDER BY id LIMIT 1`).Scan(&id, &prev); err != nil {
		t.Fatal(err)
	}
	if id != 7 || prev != before.last {
		t.Fatalf("체인이 이어지지 않음: id=%d prev=%s (기대 7, %s)", id, prev, before.last)
	}
	if res, err := rec.Verify(ctx); err != nil || !res.OK || res.Checked != 8 {
		t.Fatalf("새 범주 뒤 체인: %+v %v", res, err)
	}
	// 알 수 없는 범주·필드·화면은 코드가 거부한다.
	for name, a := range map[string]privacy.Access{
		"범주":   {Category: "health", Screen: privacy.ScreenAttendanceReason, Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: s1}}},
		"필드":   {Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason, Fields: []string{"diagnosis"}, Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: s1}}},
		"화면":   {Category: privacy.CatAttendance, Screen: "임의 화면", Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: s1}}},
		"정보주체": {Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason, Subjects: []privacy.Subject{{Type: "pet", ID: 1}}},
	} {
		if err := rec.Note(ctx, actor, a); err == nil {
			t.Errorf("알 수 없는 %s 가 통과함", name)
		}
	}
	// DB 직접 접근으로도 열거 밖 범주는 거부된다(CHECK).
	if _, err := db.Exec(`INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('x', 1, 'admin', 'web', 'health', '근태 사유 보기', 0, 'h', 'p', 'yy')`); err == nil {
		t.Fatal("열거 밖 범주가 DB 에 들어감")
	}
	// 변조 탐지는 새 표에서도 같다: 한 행을 고치면 그 행을 짚는다(트리거를 우회한 접근자 가정).
	for _, trg := range []string{"trg_privacy_log_no_update"} {
		exec(`DROP TRIGGER ` + trg)
	}
	exec(`UPDATE privacy_access_log SET actor_id = 999 WHERE id = 3`)
	if res, _ := rec.Verify(ctx); res.OK || res.BrokenID != 3 {
		t.Fatalf("변조를 짚지 못함: %+v", res)
	}
}

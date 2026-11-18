package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t                           *testing.T
	db                          *sql.DB
	rec                         *privacy.Recorder
	clock                       *time.Time
	ctx                         context.Context
	viewer, other, subj1, subj2 int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC) // 한국 시간 10시(업무 시간)
	e := &env{t: t, db: db, clock: &now, ctx: ctx}
	txs := dbutil.NewTxManager(db, dbutil.DialectOf(db))
	rec, err := privacy.New(privacy.Options{DB: db, Txs: txs, Audit: audit.New(db), Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.rec = rec
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	user := func(email, name, role string) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, ?, (SELECT id FROM departments WHERE name = '운영')) RETURNING id`, email, name, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.viewer, e.other = user("admin@x.test", "열람 관리자", "admin"), user("aud@x.test", "감사", "auditor")
	e.subj1, e.subj2 = user("s1@x.test", "정보주체 하나", "staff"), user("s2@x.test", "정보주체 둘", "staff")
	return e
}

func (e *env) actor() contract.Principal {
	return contract.Principal{UserID: e.viewer, Name: "열람 관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

func (e *env) auditor() contract.Principal {
	return contract.Principal{UserID: e.other, Name: "감사", Role: contract.RoleAuditor, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) note(a privacy.Access) {
	e.t.Helper()
	if err := e.rec.Note(e.ctx, e.actor(), a); err != nil {
		e.t.Fatal(err)
	}
}

func contactAccess(subs ...int64) privacy.Access {
	a := privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Fields: []string{privacy.FieldEmail, privacy.FieldPhone}}
	for _, s := range subs {
		a.Subjects = append(a.Subjects, privacy.Subject{Type: privacy.SubjectUser, ID: s})
	}
	return a
}

// disableGuards 는 시험이 "DB 접근자가 트리거를 우회해 고치는" 상황을 만들 때 쓴다(트리거 제거).
func (e *env) disableGuards() {
	e.t.Helper()
	if dbutil.DialectOf(e.db).HasExplicitLocks() {
		for _, tbl := range []string{"privacy_access_log", "privacy_access_subjects"} {
			if _, err := e.db.Exec(`ALTER TABLE ` + tbl + ` DISABLE TRIGGER USER`); err != nil {
				e.t.Fatal(err)
			}
		}
		return
	}
	for _, trg := range []string{"trg_privacy_log_no_update", "trg_privacy_log_no_delete", "trg_privacy_subjects_no_update", "trg_privacy_subjects_no_delete"} {
		if _, err := e.db.Exec(`DROP TRIGGER ` + trg); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestNoteWritesRowAndSubjects(t *testing.T) {
	e := newEnv(t)
	e.note(contactAccess(e.subj1, e.subj2, e.subj1)) // 중복은 합친다
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 1 || e.count(`SELECT COUNT(*) FROM privacy_access_subjects`) != 2 {
		t.Fatal("이벤트 1행 + 정보주체 2행이어야 함")
	}
	var actor int64
	var channel, category, screen, fields, exposure string
	var count int
	if err := e.db.QueryRow(`SELECT actor_id, channel, category, screen, fields, subject_count, exposure FROM privacy_access_log`).Scan(&actor, &channel, &category, &screen, &fields, &count, &exposure); err != nil {
		t.Fatal(err)
	}
	if actor != e.viewer || channel != "web" || category != privacy.CatContact || screen != privacy.ScreenPersonCard || fields != `["email","phone"]` || count != 2 || exposure != "raw" {
		t.Errorf("행 내용: %d %s %s %s %s %d %s", actor, channel, category, screen, fields, count, exposure)
	}
	// 내보내기: 식별하지 못한 정보주체는 건수에만 더해진다.
	e.note(privacy.Access{Category: privacy.CatContact, Screen: "내보내기 조직 구성원", Dataset: "org_people", Export: true, Fields: []string{privacy.FieldEmail}, Subjects: []privacy.Subject{{privacy.SubjectUser, e.subj1}}, UnknownSubjects: 4})
	var export, n int
	_ = e.db.QueryRow(`SELECT is_export, subject_count FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&export, &n)
	if export != 1 || n != 5 {
		t.Errorf("내보내기 행: %d %d", export, n)
	}
	// 정보주체가 없으면 기록하지 않는다. 본인 제외 도우미.
	e.note(contactAccess())
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 2 {
		t.Error("정보주체 없는 호출이 행을 만듦")
	}
	// MCP 이상 상태는 정보주체 없이도 행을 남긴다(exposure=mcp_anomaly).
	e.note(privacy.Access{Category: privacy.CatAccount, Screen: "MCP get_payables", Fields: []string{privacy.FieldAccountNo}, Anomaly: true})
	var anomExposure string
	var cnt int
	_ = e.db.QueryRow(`SELECT exposure, subject_count FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&anomExposure, &cnt)
	if anomExposure != "mcp_anomaly" || cnt != 0 || e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 3 {
		t.Errorf("이상 상태 행: %s %d", anomExposure, cnt)
	}
	subs := privacy.ExcludeSelf(e.actor(), []privacy.Subject{{privacy.SubjectUser, e.viewer}, {privacy.SubjectUser, e.subj1}, {privacy.SubjectVendor, e.viewer}})
	if len(subs) != 2 {
		t.Errorf("본인 제외: %v", subs)
	}
}

func TestNoteRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	for name, a := range map[string]privacy.Access{
		"범주":   {Category: "x", Screen: privacy.ScreenPersonCard, Subjects: []privacy.Subject{{privacy.SubjectUser, 1}}},
		"화면":   {Category: privacy.CatContact, Screen: "임의 문자열 <script>", Subjects: []privacy.Subject{{privacy.SubjectUser, 1}}},
		"필드":   {Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Fields: []string{"password"}, Subjects: []privacy.Subject{{privacy.SubjectUser, 1}}},
		"정보주체": {Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Subjects: []privacy.Subject{{"robot", 1}}},
		"번호":   {Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Subjects: []privacy.Subject{{privacy.SubjectUser, 0}}},
	} {
		if err := e.rec.Note(e.ctx, e.actor(), a); err == nil {
			t.Errorf("%s: 잘못된 입력이 통과함", name)
		}
	}
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 0 {
		t.Error("거부된 입력이 행을 남김")
	}
	for _, s := range []string{privacy.ScreenPersonCard, "내보내기 거래처", "MCP get_payables"} {
		if !privacy.ValidScreen(s) {
			t.Errorf("%q 는 허용되어야 함", s)
		}
	}
}

// 값(원문·검색어)은 어떤 열에도 없다: 표의 모든 텍스트를 이어 붙여 심어 둔 값이 없는지 본다.
func TestNoRawValuesStored(t *testing.T) {
	e := newEnv(t)
	e.note(contactAccess(e.subj1))
	rows, err := e.db.Query(`SELECT at, actor_role, channel, category, screen, dataset, fields, subjects_hash, exposure, prev_hash, hash FROM privacy_access_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		cols := make([]string, 11)
		ptrs := make([]any, 11)
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		_ = rows.Scan(ptrs...)
		all := strings.Join(cols, "|")
		for _, bad := range []string{"@x.test", "010-", "s1@"} {
			if strings.Contains(all, bad) {
				t.Errorf("조회 기록에 값 조각 %q 이 있음: %s", bad, all)
			}
		}
	}
}

func TestAppendOnlyAndChainVerify(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		*e.clock = e.clock.Add(time.Minute)
		e.note(contactAccess(e.subj1, e.subj2))
	}
	if _, err := e.db.Exec(`UPDATE privacy_access_log SET screen = '바꿈' WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("수정이 막히지 않음: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM privacy_access_log WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "파기") {
		t.Fatalf("삭제가 막히지 않음: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id = 1`); err == nil {
		t.Fatal("정보주체 삭제가 막히지 않음")
	}
	res, err := e.rec.Verify(e.ctx)
	if err != nil || !res.OK || res.Checked != 5 {
		t.Fatalf("정상 체인 검증: %+v %v", res, err)
	}
	// 한 행의 내용을 고치면 그 행을 짚는다.
	e.disableGuards()
	if _, err := e.db.Exec(`UPDATE privacy_access_log SET actor_id = 999 WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	res, _ = e.rec.Verify(e.ctx)
	if res.OK || res.BrokenID != 3 {
		t.Fatalf("행 변조를 짚지 못함: %+v", res)
	}
	if _, err := e.db.Exec(`UPDATE privacy_access_log SET actor_id = ? WHERE id = 3`, e.viewer); err != nil {
		t.Fatal(err)
	}
	// 정보주체 행 하나를 지우면(체인에 묶인 정보주체 목록 해시) 짚는다.
	if _, err := e.db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id = 4 AND subject_id = ?`, e.subj2); err != nil {
		t.Fatal(err)
	}
	res, _ = e.rec.Verify(e.ctx)
	if res.OK || res.BrokenID != 4 {
		t.Fatalf("정보주체 변조를 짚지 못함: %+v", res)
	}
}

func TestMiddleRowDeletionBreaksChain(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		*e.clock = e.clock.Add(time.Minute)
		e.note(contactAccess(e.subj1))
	}
	e.disableGuards()
	if _, err := e.db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`DELETE FROM privacy_access_log WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	res, _ := e.rec.Verify(e.ctx)
	if res.OK || res.BrokenID != 3 {
		t.Fatalf("중간 행 삭제를 짚지 못함: %+v", res)
	}
}

func TestDigestCatchesTailDeletion(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		*e.clock = e.clock.Add(time.Minute)
		e.note(contactAccess(e.subj1))
	}
	day := e.clock.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")
	if did, err := e.rec.Digest(e.ctx, day); err != nil || !did {
		t.Fatalf("하루 요약: %v %v", did, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_digest' AND channel = 'cli'`) != 1 {
		t.Fatal("감사 체인에 하루 요약이 없음")
	}
	if res, _ := e.rec.Verify(e.ctx); !res.OK || res.DigestsChecked != 1 {
		t.Fatalf("요약 대조: %+v", res)
	}
	// 마지막 행 둘을 지운다: 남은 체인은 멀쩡하지만 요약이 가리키는 행이 없다.
	e.disableGuards()
	if _, err := e.db.Exec(`DELETE FROM privacy_access_subjects WHERE log_id >= 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`DELETE FROM privacy_access_log WHERE id >= 3`); err != nil {
		t.Fatal(err)
	}
	res, _ := e.rec.Verify(e.ctx)
	if !res.ChainOK || res.DigestOK || res.OK || !strings.Contains(res.DigestWhy, "끝부분 삭제") {
		t.Fatalf("끝부분 삭제를 짚지 못함: %+v", res)
	}
	// 같은 날 요약은 한 번만 남는다(DigestIfDue 는 어제 요약이 있으면 아무것도 하지 않는다).
	*e.clock = e.clock.Add(24 * time.Hour)
	if err := e.rec.DigestIfDue(e.ctx, *e.clock); err != nil {
		t.Fatal(err)
	}
	if err := e.rec.DigestIfDue(e.ctx, *e.clock); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_digest'`) != 2 {
		t.Errorf("요약이 중복됨: %d", e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_digest'`))
	}
}

func TestRetentionSettingAndPurge(t *testing.T) {
	e := newEnv(t)
	admin := e.actor()
	if d, _ := e.rec.RetentionDays(e.ctx); d != 730 {
		t.Fatalf("기본 보관 기간 %d", d)
	}
	for _, bad := range []int{0, 364, 3651} {
		if err := e.rec.SetRetention(e.ctx, admin, bad); err == nil {
			t.Errorf("범위 밖 보관 기간 %d 가 저장됨", bad)
		}
	}
	tok := admin
	tok.Auth = contract.AuthToken
	if err := e.rec.SetRetention(e.ctx, tok, 400); err == nil {
		t.Error("토큰으로 보관 기간이 바뀜")
	}
	if err := e.rec.SetRetention(e.ctx, e.auditor(), 400); err == nil {
		t.Error("감사가 보관 기간을 바꿈(privacy:log_manage 없음)")
	}
	if err := e.rec.SetRetention(e.ctx, admin, 365); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.retention_set'`) != 1 {
		t.Error("보관 기간 변경 감사가 없음")
	}
	// 2024-12, 2025-01 에 기록 3건, 최근에 1건.
	for _, at := range []time.Time{time.Date(2024, 12, 20, 1, 0, 0, 0, time.UTC), time.Date(2024, 12, 21, 1, 0, 0, 0, time.UTC), time.Date(2025, 1, 5, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)} {
		*e.clock = at
		e.note(contactAccess(e.subj1, e.subj2))
	}
	*e.clock = time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	pending, err := e.rec.PendingPurge(e.ctx, *e.clock)
	if err != nil || len(pending) != 2 || pending[0].Month != "2024-12" || pending[0].Rows != 2 || pending[1].Month != "2025-01" {
		t.Fatalf("파기 대상: %+v %v", pending, err)
	}
	dry, err := e.rec.Purge(e.ctx, *e.clock, true)
	if err != nil || len(dry) != 2 || e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 4 {
		t.Fatalf("dry-run 이 지움: %+v %v", dry, err)
	}
	done, err := e.rec.Purge(e.ctx, *e.clock, false)
	if err != nil || len(done) != 2 {
		t.Fatalf("파기: %+v %v", done, err)
	}
	if e.count(`SELECT COUNT(*) FROM privacy_access_log`) != 1 || e.count(`SELECT COUNT(*) FROM privacy_access_subjects`) != 2 || e.count(`SELECT COUNT(*) FROM privacy_log_segments`) != 2 {
		t.Fatal("파기 뒤 행·정보주체·세그먼트 수가 맞지 않음")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_purge'`) != 2 {
		t.Error("파기 감사가 달마다 남아야 함")
	}
	// 파기 뒤에도 남은 구간의 체인이 이어서 검증된다. 이어 붙인 새 행도 같은 체인이다.
	if res, _ := e.rec.Verify(e.ctx); !res.OK || !res.Purged || res.Checked != 1 {
		t.Fatalf("파기 뒤 검증: %+v", res)
	}
	e.note(contactAccess(e.subj1))
	if res, _ := e.rec.Verify(e.ctx); !res.OK || res.Checked != 2 {
		t.Fatalf("파기 뒤 추가 기록 검증: %+v", res)
	}
	// 다시 불러도 할 일이 없다. 보관 기간 안의 기록은 지워지지 않는다.
	if again, _ := e.rec.Purge(e.ctx, *e.clock, false); len(again) != 0 {
		t.Errorf("두 번째 파기: %+v", again)
	}
}

func TestPurgedHeadStillMatchesDigest(t *testing.T) {
	e := newEnv(t)
	if err := e.rec.SetRetention(e.ctx, e.actor(), 365); err != nil {
		t.Fatal(err)
	}
	*e.clock = time.Date(2024, 12, 20, 1, 0, 0, 0, time.UTC)
	e.note(contactAccess(e.subj1))
	if _, err := e.rec.Digest(e.ctx, "2024-12-20"); err != nil {
		t.Fatal(err)
	}
	*e.clock = time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	e.note(contactAccess(e.subj1))
	if _, err := e.rec.Purge(e.ctx, *e.clock, false); err != nil {
		t.Fatal(err)
	}
	res, _ := e.rec.Verify(e.ctx)
	if !res.OK || res.DigestsChecked != 1 {
		t.Fatalf("파기된 구간을 가리키는 요약은 세그먼트로 대조되어야 함: %+v", res)
	}
}

func TestReadNeedsPermissionAndIsAudited(t *testing.T) {
	e := newEnv(t)
	e.note(contactAccess(e.subj1, e.subj2))
	*e.clock = e.clock.Add(time.Hour)
	e.note(privacy.Access{Category: privacy.CatAccount, Screen: privacy.ScreenVendorList, Fields: []string{privacy.FieldAccountNo}, Subjects: []privacy.Subject{{privacy.SubjectVendor, 7}}})
	// 관리자는 조회 권한이 없다(감사만).
	if _, err := e.rec.List(e.ctx, e.actor(), privacy.Filter{}); err == nil {
		t.Fatal("관리자가 조회 기록을 읽음")
	}
	anon := contract.Principal{}
	if _, err := e.rec.List(e.ctx, anon, privacy.Filter{}); err == nil {
		t.Fatal("미인증이 조회 기록을 읽음")
	}
	items, err := e.rec.List(e.ctx, e.auditor(), privacy.Filter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("목록: %d %v", len(items), err)
	}
	if items[0].Category != privacy.CatAccount || items[1].ActorName != "열람 관리자" || items[1].SubjectCount != 2 {
		t.Errorf("목록 내용: %+v", items)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_read' AND actor_id = ?`, e.other) != 1 {
		t.Error("조회가 감사 privacy.log_read 로 남지 않음")
	}
	var diff string
	_ = e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action = 'privacy.log_read'`).Scan(&diff)
	if strings.Contains(diff, "정보주체 하나") {
		t.Errorf("감사에 조건 값이 남음: %s", diff)
	}
	// 필터: 범주·정보주체 이름·내보내기.
	if got, _ := e.rec.List(e.ctx, e.auditor(), privacy.Filter{Category: privacy.CatContact}); len(got) != 1 {
		t.Errorf("범주 필터: %d", len(got))
	}
	if got, _ := e.rec.List(e.ctx, e.auditor(), privacy.Filter{SubjectName: "정보주체 둘"}); len(got) != 1 || got[0].Category != privacy.CatContact {
		t.Errorf("정보주체 이름 필터: %+v", got)
	}
	if got, _ := e.rec.List(e.ctx, e.auditor(), privacy.Filter{ExportOnly: true}); len(got) != 0 {
		t.Errorf("내보내기 필터: %d", len(got))
	}
	if got, _ := e.rec.List(e.ctx, e.auditor(), privacy.Filter{Before: items[0].ID}); len(got) != 1 {
		t.Errorf("커서: %d", len(got))
	}
	subs, total, err := e.rec.Subjects(e.ctx, e.auditor(), items[1].ID, 0, 10)
	if err != nil || total != 2 || len(subs) != 2 || subs[0].Name == "" {
		t.Fatalf("정보주체 목록: %+v %d %v", subs, total, err)
	}
	if _, _, err := e.rec.Subjects(e.ctx, e.auditor(), 99999, 0, 10); err == nil {
		t.Error("없는 요청의 정보주체")
	}
	st, err := e.rec.Stats(e.ctx, e.auditor(), 30)
	if err != nil || st.Total != 2 || len(st.ByCategory) != 2 || len(st.ByActor) != 1 || st.ByActor[0].Subjects != 3 {
		t.Fatalf("통계: %+v %v", st, err)
	}
	// 업무시간 밖(한국 시간 새벽 3시) 열람은 통계에 표시된다.
	*e.clock = time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC) // KST 03:00
	e.note(contactAccess(e.subj1))
	if st, _ := e.rec.Stats(e.ctx, e.auditor(), 30); st.OffHours != 1 {
		t.Errorf("업무시간 밖 건수 %d", st.OffHours)
	}
	if !privacy.OffHours(time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)) { // 토요일 정오(KST)
		t.Error("주말은 업무시간 밖")
	}
}

func TestRegisterDeclaresPermissions(t *testing.T) {
	e := newEnv(t)
	rt := contract.NewRouteTable()
	e.rec.Register(rt)
	want := map[string]contract.Permission{
		"GET /api/admin/privacy/log":                    contract.PermPrivacyLogRead,
		"GET /api/admin/privacy/log/{id}/subjects":      contract.PermPrivacyLogRead,
		"GET /api/admin/privacy/stats":                  contract.PermPrivacyLogRead,
		"POST /api/admin/privacy/verify":                contract.PermPrivacyLogRead,
		"GET /api/admin/privacy/retention":              contract.PermPrivacyLogManage,
		"PUT /api/admin/privacy/retention":              contract.PermPrivacyLogManage,
		"POST /api/privacy/requests":                    contract.PermAuthenticated,
		"GET /api/privacy/requests":                     contract.PermAuthenticated,
		"GET /api/privacy/requests/{id}/result":         contract.PermAuthenticated,
		"GET /api/admin/privacy/requests":               contract.PermAuthenticated,
		"POST /api/admin/privacy/requests/{id}/approve": contract.PermAuthenticated,
		"POST /api/admin/privacy/requests/{id}/reject":  contract.PermAuthenticated,
	}
	got := map[string]contract.Permission{}
	for _, r := range rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
		if r.Method != "GET" && r.Method != "POST" && r.Method != "PUT" {
			t.Errorf("조회 기록에 %s 라우트가 있음", r.Method)
		}
	}
	for k, p := range want {
		if got[k] != p {
			t.Errorf("%s: %q(기대 %q)", k, got[k], p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("예상하지 못한 라우트: %v", got)
	}
	_ = caudit.TargetPrivacyLog
	_ = json.Marshal
	_ = fmt.Sprint
}

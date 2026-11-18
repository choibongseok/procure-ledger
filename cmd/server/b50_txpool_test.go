package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B50 판정 후속: "트랜잭션 안에서 풀 연결로 읽기" 가 운영 SQLite(연결 1개)에서 영구 대기를 만든 사고의 재발 방지 시험.
// 같은 흐름(신청 → 최종 승인 → 원장, 반려·회수·승인 뒤 취소, 휴직 발령 복원, 근태 정정·마감)을 실제 HTTP 로 끝까지 두 번 돈다.
//   1) 운영과 같은 SQLite 파일 DB(MaxOpenConns 1): 멈추면 요청 제한 시간(15초)에 걸려 시험이 실패한다.
//   2) 감시 DB(dbtest.OpenGuarded): 트랜잭션이 열린 동안 그 밖의 연결에서 일어난 질의를 위반으로 모아 끝에서 실패시킨다.

// flowEnv 는 지정한 DB 로 플랫폼+업무 서비스를 조립한 서버다(조직 백필 포함).
func newFlowEnv(t *testing.T, db *sql.DB) *orgEnv {
	t.Helper()
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := config.Config{DevIDP: true, BaseURL: base}
	d := deps.Deps{DB: db}
	if err := bootstrap.WirePlatform(cfg, &d); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.WireERP(cfg, &d); err != nil {
		t.Fatal(err)
	}
	rt := buildRoutes(cfg, d)
	ts.Config.Handler = handlerFor(rt, d)
	ts.Start()
	t.Cleanup(ts.Close)
	e := &orgEnv{platformEnv: &platformEnv{t: t, ts: ts, d: d, rt: rt, base: base}, clients: map[string]*http.Client{}, csrfs: map[string]string{}}
	if _, err := db.Exec(`UPDATE users SET created_at = '2020-02-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if did, err := d.Org.(*org.Service).Backfill(context.Background()); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	return e
}

func openProdLikeSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.Open(filepath.Join(t.TempDir(), "erp.db")) // 운영과 같은 Open: 연결 1개, WAL
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestB50LeaveFlowDoesNotUsePoolInsideTx_ProdSQLite(t *testing.T) {
	dbtest.SkipPG(t, "SQLite 연결 1개 제약을 시험한다")
	runB50LeaveFlow(t, openProdLikeSQLite(t))
}

func TestB50LeaveFlowDoesNotUsePoolInsideTx_Guarded(t *testing.T) {
	dbtest.SkipPG(t, "트랜잭션 안 풀 연결 감시는 SQLite 연결 1개 제약을 모사한다")
	db, g := dbtest.OpenGuarded(t)
	runB50LeaveFlow(t, db)
	g.Check(t)
}

func runB50LeaveFlow(t *testing.T, db *sql.DB) {
	t.Helper()
	e := newFlowEnv(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = ctx
	staff := e.userID("staff@pl.test")
	tz0 := time.Now().In(contract.KST)
	// 인사 담당: 사용자 정의 역할 "attendance:hr" 을 관리자 계정에 더한다.
	for _, q := range []string{
		`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_hr','인사 담당','custom','a','a')`,
		`INSERT INTO role_permissions (role_id, permission) SELECT id, 'attendance:hr' FROM roles WHERE key='r_hr'`,
		fmt.Sprintf(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) SELECT id, 'user', %d, '', 0, 'manual', '시험', 'a' FROM roles WHERE key = 'r_hr'`, e.userID("admin@pl.test")),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// 규칙·공휴일·모듈·초기 잔여
	y := time.Now().In(contract.KST).Year()
	for _, d := range []string{fmt.Sprintf("%d-12-25", y), fmt.Sprintf("%d-12-25", y+1)} {
		e.do("admin", "POST", "/api/company/holidays", `{"items":[{"date":"`+d+`","name":"휴일"}]}`)
	}
	if code, b := e.do("admin", "POST", "/api/admin/attendance/policies", goodPolicy); code != 201 {
		t.Fatalf("규칙 저장: %d %s", code, b)
	}
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	e.must("admin", "POST", "/api/attendance/leave/adjust", fmt.Sprintf(`{"userId":%d,"balance":"annual","kind":"opening","minutes":%d,"reason":"초기 이관"}`, staff, 20*480), 204)

	// 공휴일이 없는 내년 월~금 주를 찾는다.
	mon := time.Date(y+1, 3, 1, 0, 0, 0, 0, contract.KST)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	day := func(i int) string { return mon.AddDate(0, 0, i).Format("2006-01-02") }
	ledger := func(id int64) (sum int64, lines int) {
		if err := db.QueryRow(`SELECT COALESCE(SUM(minutes),0), COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_request' AND ref_id = ?`, id).Scan(&sum, &lines); err != nil {
			t.Fatal(err)
		}
		return
	}
	status := func(id int64) string {
		var s string
		if err := db.QueryRow(`SELECT status FROM leave_requests WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	requestBy := func(who, from, to string, extra string) (id, doc int64) {
		out := e.must(who, "POST", "/api/attendance/leave/requests", fmt.Sprintf(`{"leaveCode":"annual","startDate":"%s","endDate":"%s","part":"full"%s}`, from, to, extra), 201)
		id = num(out["id"])
		if err := db.QueryRow(`SELECT doc_id FROM leave_requests WHERE id = ?`, id).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		return
	}
	request := func(from, to string, extra string) (int64, int64) { return requestBy("staff", from, to, extra) }
	decide := func(who string, doc int64, approve bool, comment string) {
		b, _ := json.Marshal(map[string]any{"approve": approve, "comment": comment})
		e.must(who, "POST", fmt.Sprintf("/api/approvals/%d/decision", doc), string(b), 200)
	}

	// 1) 신청 → 최종 승인: 원장에 예약 해제(+)와 사용(-)이 붙는다. (사고 지점: 승인 트랜잭션 안의 모듈 확인)
	id1, doc1 := request(day(0), day(1), "")
	if s, n := ledger(id1); s != -960 || n != 1 {
		t.Fatalf("신청 직후 원장 합 %d 줄 %d", s, n)
	}
	decide("head", doc1, true, "")
	if status(id1) != "approved" {
		t.Fatalf("승인 뒤 상태 %s", status(id1))
	}
	if s, n := ledger(id1); s != -960 || n != 3 {
		t.Fatalf("승인 뒤 원장 합 %d 줄 %d(신청 예약 1 + 해제 1 + 사용 1)", s, n)
	}
	var rel, use int
	_ = db.QueryRow(`SELECT COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_request' AND ref_id = ? AND kind = 'release'`, id1).Scan(&rel)
	_ = db.QueryRow(`SELECT COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_request' AND ref_id = ? AND kind = 'use'`, id1).Scan(&use)
	if rel != 1 || use != 1 {
		t.Fatalf("release %d use %d", rel, use)
	}

	// 2) 승인 뒤 취소(시작 전 본인): 원장 합 0
	e.must("staff", "POST", fmt.Sprintf("/api/attendance/leave/requests/%d/cancel", id1), `{"reason":"일정 변경"}`, 200)
	if status(id1) != "canceled" {
		t.Fatalf("취소 뒤 상태 %s", status(id1))
	}
	if s, _ := ledger(id1); s != 0 {
		t.Fatalf("취소 뒤 원장 합 %d", s)
	}

	// 3) 반려
	id2, doc2 := request(day(2), day(2), "")
	decide("head", doc2, false, "시기를 옮겨 주세요")
	if status(id2) != "rejected" {
		t.Fatalf("반려 뒤 상태 %s", status(id2))
	}
	if s, _ := ledger(id2); s != 0 {
		t.Fatalf("반려 뒤 원장 합 %d", s)
	}

	// 4) 회수
	id3, _ := request(day(3), day(3), "")
	e.must("staff", "POST", fmt.Sprintf("/api/attendance/leave/requests/%d/cancel", id3), `{}`, 200)
	if status(id3) != "recalled" {
		t.Fatalf("회수 뒤 상태 %s", status(id3))
	}
	if s, _ := ledger(id3); s != 0 {
		t.Fatalf("회수 뒤 원장 합 %d", s)
	}

	// 5c) 모듈이 꺼진 동안의 승인(승인 트랜잭션에서 모듈 상태를 다시 읽는 줄), 결재 대기 중 모듈 끄기
	idM, docM := request(day(10), day(10), "")
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":false}`, 200)
	decide("head", docM, true, "")
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	if status(idM) != "approved" {
		t.Fatalf("모듈이 꺼진 동안 승인 뒤 상태 %s", status(idM))
	}

	// 4b) 결재자(팀장)의 휴가: 대결자를 적으면 승인 트랜잭션에서 부재 설정(B29)이 만들어진다.
	e.must("admin", "POST", "/api/attendance/leave/adjust", fmt.Sprintf(`{"userId":%d,"balance":"annual","kind":"opening","minutes":%d,"reason":"초기 이관"}`, e.userID("lead@pl.test"), 20*480), 204)
	idL, docL := requestBy("lead", day(7), day(8), `,"delegateUserId":`+fmt.Sprint(e.userID("lead2@pl.test")))
	decide("head", docL, true, "")
	if status(idL) != "approved" {
		t.Fatalf("팀장 휴가 승인 뒤 상태 %s", status(idL))
	}

	// 4c) 마감된 지난달에 걸친 휴가의 승인: 인사 담당에게 알림(승인 트랜잭션 안의 마감 조회·수신자 조회)
	pm := time.Date(tz0.Year(), tz0.Month(), 1, 0, 0, 0, 0, contract.KST).AddDate(0, 0, -1) // 지난달 마지막 평일(30일 안이라 신청된다)
	for pm.Weekday() == time.Saturday || pm.Weekday() == time.Sunday {
		pm = pm.AddDate(0, 0, -1)
	}
	pd := pm.Format("2006-01-02")
	code4c, b4c := e.do("lead", "POST", "/api/attendance/leave/requests", fmt.Sprintf(`{"leaveCode":"annual","startDate":"%s","endDate":"%s","part":"full","reason":"소급 신청"}`, pd, pd))
	if code4c == 201 {
		var o map[string]any
		_ = json.Unmarshal([]byte(b4c), &o)
		var d int64
		_ = db.QueryRow(`SELECT doc_id FROM leave_requests WHERE id = ?`, num(o["id"])).Scan(&d)
		e.must("admin", "POST", "/api/attendance/admin/closes", `{"month":"`+pm.Format("2006-01")+`"}`, 204)
		decide("head", d, true, "")
	} else {
		t.Logf("지난달 휴가 신청 거부(%d %s): 마감 달 승인 알림 경로는 시험하지 못함", code4c, b4c)
	}

	// 5) 승인된 휴가 기간에 휴직 발령: 복원 줄(조직 서비스 트랜잭션 안의 휴가 연동)
	id4, doc4 := request(day(0), day(4), "")
	decide("head", doc4, true, "")
	if status(id4) != "approved" {
		t.Fatalf("승인 뒤 상태 %s", status(id4))
	}
	body, _ := json.Marshal(map[string]any{"kind": "absence_start", "userId": staff, "effectiveDate": day(2), "reason": "휴직 발령", "confirm": true, "absenceKind": "illness", "absenceReason": "회복", "returnDay": day(30)})
	ev := e.must("admin", "POST", "/api/org/events", string(body), 201)
	var restore int
	_ = db.QueryRow(`SELECT COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_absence' AND kind = 'release'`).Scan(&restore)
	if restore != 2 { // 휴직 기간에 걸친 승인 휴가 두 건(id4·idM)

		t.Fatalf("복원 줄 %d", restore)
	}

	// 6) 근태 정정 요청 → 승인, 직접 정정, 마감 → 마감 달 정정 거부
	past := time.Now().In(contract.KST).AddDate(0, 0, -3).Format("2006-01-02")
	c := e.must("staff2", "POST", "/api/attendance/corrections", `{"workDate":"`+past+`","checkIn":"09:00","checkOut":"18:00","reason":"퇴근 누락"}`, 201)
	_ = c
	c2 := e.must("staff", "POST", "/api/attendance/corrections", `{"workDate":"`+past+`","checkIn":"09:00","checkOut":"18:00","reason":"퇴근 누락"}`, 201)
	e.must("head", "POST", fmt.Sprintf("/api/attendance/corrections/%v/decide", c2["id"]), `{"approve":true}`, 200)
	e.must("admin", "POST", "/api/attendance/admin/correct", fmt.Sprintf(`{"userId":%d,"workDate":"%s","checkIn":"09:10","checkOut":"18:00","reason":"인사 정정"}`, staff, past), 201)
	m := time.Now().In(contract.KST).AddDate(0, -2, 0).Format("2006-01")
	e.must("admin", "POST", "/api/attendance/admin/closes", `{"month":"`+m+`"}`, 204)
	if code, _ := e.do("admin", "POST", "/api/attendance/admin/closes/"+m+"/reopen", `{"reason":"재정산"}`); code != 204 {
		t.Fatalf("마감 해제 %d", code)
	}

	// 5b) 예정 복직일 경과 알림(인사 담당·직속 조직장), 복직 발령, 발령 취소
	staff2 := e.userID("staff2@pl.test")
	tz := time.Now().In(contract.KST)
	b2, _ := json.Marshal(map[string]any{"kind": "absence_start", "userId": staff2, "effectiveDate": tz.AddDate(0, 0, -10).Format("2006-01-02"), "reason": "휴직 발령", "confirm": true, "absenceKind": "illness", "absenceReason": "회복", "returnDay": tz.AddDate(0, 0, -2).Format("2006-01-02")})
	ev2 := e.must("admin", "POST", "/api/org/events", string(b2), 201)
	if n, err := e.d.Leave.(interface {
		RunOnce(context.Context) (int, error)
	}).RunOnce(ctx); err != nil {
		t.Fatalf("휴가 발생·복직 경과 작업: %v (%d)", err, n)
	}
	if n := e.count(`SELECT COUNT(*) FROM notifications WHERE kind LIKE 'absence%'`); n == 0 {
		t.Log("복직 경과 알림이 만들어지지 않았다(조건 미충족일 수 있음)")
	}
	b3, _ := json.Marshal(map[string]any{"kind": "absence_end", "userId": staff2, "effectiveDate": tz.AddDate(0, 0, -1).Format("2006-01-02"), "reason": "복직", "confirm": true})
	ev3 := e.must("admin", "POST", "/api/org/events", string(b3), 201)
	if ids, _ := ev3["eventIds"].([]any); len(ids) == 1 {
		e.must("admin", "POST", fmt.Sprintf("/api/org/events/%d/cancel", num(ids[0])), `{"reason":"복직 일정 오류","confirm":true}`, 200)
	}
	if ids, _ := ev2["eventIds"].([]any); len(ids) == 1 {
		e.must("admin", "POST", fmt.Sprintf("/api/org/events/%d/cancel", num(ids[0])), `{"reason":"발령 오류","confirm":true}`, 200)
	}

	// 7) 출퇴근과 주 기록, 휴가 발생 작업(서버 작업과 같은 서비스)
	e.must("staff", "POST", "/api/attendance/check-in", `{}`, 200)
	e.must("staff", "POST", "/api/attendance/check-out", `{}`, 200)
	if _, err := e.d.Leave.(interface {
		RunOnce(context.Context) (int, error)
	}).RunOnce(ctx); err != nil {
		t.Fatalf("휴가 발생 작업: %v", err)
	}
	if n, err := e.d.Attendance.(interface {
		RunWarnings(context.Context) (int, error)
	}).RunWarnings(ctx); err != nil {
		t.Fatalf("주 근로시간 경고 작업: %v (%d)", err, n)
	}
	// 개인 열람 기록: 휴직 사유 원문 열람(인사 담당 = 관리자 계정에 더한 역할)과 결재자의 휴가 사유 열람
	if ids, _ := ev["eventIds"].([]any); len(ids) == 1 {
		if code, b := e.do("admin", "GET", fmt.Sprintf("/api/org/events/%d/absence-reason", num(ids[0])), ""); code != 200 {
			t.Fatalf("휴직 사유 열람 %d %s", code, b)
		}
	} else {
		t.Fatalf("휴직 발령 응답: %v", ev)
	}
	e.must("head", "GET", fmt.Sprintf("/api/attendance/leave/requests/%d", id4), "", 200)
	e.do("head", "GET", "/api/approvals/pending", "")
	e.do("staff", "GET", "/api/notifications", "")
	_ = strings.TrimSpace
}

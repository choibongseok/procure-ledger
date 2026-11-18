package privacy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func (e *env) staffP(id int64) contract.Principal {
	return contract.Principal{UserID: id, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

func (e *env) withPerms() {
	e.t.Helper()
	txs := dbutil.NewTxManager(e.db, dbutil.DialectOf(e.db))
	if _, err := access.SyncTemplates(e.ctx, e.db); err != nil {
		e.t.Fatal(err)
	}
	now := *e.clock
	if _, err := access.New(e.db, txs, audit.New(e.db), func() time.Time { return now }).Reconcile(e.ctx, contract.Principal{Role: "system", Channel: contract.ChannelWeb}, true); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) adminCtx() context.Context { return contract.WithPrincipal(e.ctx, e.actor()) }

func TestSubjectRequestFlow(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	staff := e.staffP(e.subj1)
	// 열람 기록: 관리자가 정보주체 1 과 2 를, 감사가 1 을, 본인이 자기 것을 봤다.
	e.note(contactAccess(e.subj1, e.subj2))
	*e.clock = e.clock.Add(time.Minute)
	if err := e.rec.Note(e.ctx, e.auditor(), contactAccess(e.subj1)); err != nil {
		t.Fatal(err)
	}
	if err := e.rec.Note(e.ctx, staff, contactAccess(e.subj1)); err != nil {
		t.Fatal(err)
	}
	day := e.clock.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")

	if _, err := e.rec.CreateRequest(e.ctx, contract.Principal{}, day, day, ""); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("로그인 없이: %v", err)
	}
	for _, c := range [][2]string{{"2026-13-01", day}, {day, "2026-01-01"}, {"2024-01-01", day}, {day, "2099-01-01"}} {
		if _, err := e.rec.CreateRequest(e.ctx, staff, c[0], c[1], ""); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("잘못된 기간 %v: %v", c, err)
		}
	}
	id, err := e.rec.CreateRequest(e.ctx, staff, day, day, "내 정보 확인")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.rec.CreateRequest(e.ctx, staff, day, day, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("열린 요청은 한 건: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_request_pending' AND user_id = ?`, e.viewer) != 1 {
		t.Fatal("관리자에게 대기 알림이 가야 한다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.request_create'`) != 1 {
		t.Fatal("요청 감사가 없다")
	}
	if _, err := e.rec.MyResult(e.ctx, staff, id); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("승인 전 결과: %v", err)
	}
	// 승인 권한이 없으면 처리할 수 없다.
	if err := e.rec.DecideRequest(contract.WithPrincipal(e.ctx, staff), staff, id, true, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 처리: %v", err)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, true, "확인함"); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_request_done' AND user_id = ?`, e.subj1) != 1 {
		t.Fatal("요청자에게 결과 알림이 가야 한다")
	}
	rows, err := e.rec.MyResult(e.ctx, staff, id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("결과는 관리자·감사 2건(본인 열람 제외): %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.ViewerUnit != "" && r.ViewerUnit != "운영" && !r.Summary {
			t.Errorf("부서가 이상함: %+v", r)
		}
		if strings.Contains(r.ViewerRole+r.ViewerUnit+r.Screen, "열람 관리자") || strings.Contains(r.ViewerRole, "감사 ") {
			t.Errorf("열람자 이름이 노출됨: %+v", r)
		}
	}
	if e.count(`SELECT COUNT(*) FROM privacy_request_results WHERE request_id = ?`, id) != 2 {
		t.Fatal("스냅샷 행이 2개여야 한다")
	}
	// 다른 사람의 요청 결과는 볼 수 없다.
	if _, err := e.rec.MyResult(e.ctx, e.staffP(e.subj2), id); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 결과: %v", err)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, false, "사유"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 처리: %v", err)
	}
	// 보관 기간이 지나면 결과가 닫히고 행이 지워진다.
	*e.clock = e.clock.AddDate(0, 0, 31)
	if _, err := e.rec.MyResult(e.ctx, staff, id); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("만료 뒤 결과: %v", err)
	}
	if n, err := e.rec.ExpireRequests(e.ctx); err != nil || n != 1 {
		t.Fatalf("만료 처리 %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM privacy_request_results WHERE request_id = ?`, id) != 0 {
		t.Fatal("만료된 결과 행이 남음")
	}
	mine, _ := e.rec.MyRequests(e.ctx, staff)
	if len(mine) != 1 || mine[0].Status != "expired" {
		t.Fatalf("내 목록 %+v", mine)
	}
}

func TestSubjectRequestOwnCannotBeProcessedByRequester(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	day := e.clock.Format("2006-01-02")
	id, err := e.rec.CreateRequest(e.ctx, e.actor(), day, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, true, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("본인 요청을 본인이 처리할 수 없다(D9): %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_request_pending' AND user_id = ?`, e.viewer) != 0 {
		t.Fatal("요청자 본인에게는 대기 알림을 보내지 않는다")
	}
	// 다른 관리자는 처리할 수 있고, 반려하면 결과가 없다.
	other := contract.Principal{UserID: e.other, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	if err := e.rec.DecideRequest(contract.WithPrincipal(e.ctx, other), other, id, false, "범위 불명확"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rec.MyResult(e.ctx, e.actor(), id); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("반려된 요청 결과: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.request_reject'`) != 1 {
		t.Fatal("반려 감사가 없다")
	}
}

func TestSubjectRequestSnapshotCapsRows(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	for i := 0; i < 503; i++ {
		e.note(contactAccess(e.subj1))
	}
	day := e.clock.Format("2006-01-02")
	staff := e.staffP(e.subj1)
	id, err := e.rec.CreateRequest(e.ctx, staff, day, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, true, ""); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.rec.MyResult(e.ctx, staff, id)
	if len(rows) != 501 || !rows[500].Summary || !strings.Contains(rows[500].Screen, "3건") {
		t.Fatalf("상한 500행 뒤 요약 한 줄이어야 한다: %d %+v", len(rows), rows[len(rows)-1])
	}
}

func (e *env) otherAdmin() contract.Principal {
	return contract.Principal{UserID: e.other, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

func TestSubjectRequestRejectNeedsReason(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	day := e.clock.Format("2006-01-02")
	staff := e.staffP(e.subj1)
	id, err := e.rec.CreateRequest(e.ctx, staff, day, day, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{"", "   ", "\t"} {
		if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, false, note); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("사유 없는 반려(%q): %v", note, err)
		}
	}
	if e.count(`SELECT COUNT(*) FROM privacy_subject_requests WHERE id = ? AND status = 'requested'`, id) != 1 {
		t.Fatal("거부된 반려가 상태를 바꿈")
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, false, "범위가 불명확합니다"); err != nil {
		t.Fatal(err)
	}
	mine, _ := e.rec.MyRequests(e.ctx, staff)
	if len(mine) != 1 || mine[0].Status != "rejected" || mine[0].DecisionNote != "범위가 불명확합니다" {
		t.Fatalf("반려 사유가 요청자에게 보여야 한다: %+v", mine)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, true, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("승인은 사유 없이 가능하나 이미 처리됨: %v", err)
	}
}

func TestSubjectRequestOncePerMonth(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	day := e.clock.Format("2006-01-02")
	staff := e.staffP(e.subj1)
	id, err := e.rec.CreateRequest(e.ctx, staff, day, day, "")
	if err != nil {
		t.Fatal(err)
	}
	// 반려된 요청은 한 달 한도에 세지 않는다.
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, false, "다시 요청하세요"); err != nil {
		t.Fatal(err)
	}
	id, err = e.rec.CreateRequest(e.ctx, staff, day, day, "")
	if err != nil {
		t.Fatalf("반려 뒤 같은 달 재요청: %v", err)
	}
	if err := e.rec.DecideRequest(e.adminCtx(), e.actor(), id, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rec.CreateRequest(e.ctx, staff, day, day, ""); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "한 달에 한 번") {
		t.Fatalf("승인된 요청이 있는 같은 달에는 다시 요청할 수 없다: %v", err)
	}
	// 다른 사람은 영향이 없고, 다음 달(KST)에는 다시 요청할 수 있다.
	if _, err := e.rec.CreateRequest(e.ctx, e.staffP(e.subj2), day, day, ""); err != nil {
		t.Fatalf("다른 사람의 요청: %v", err)
	}
	*e.clock = e.clock.AddDate(0, 1, 0)
	next := e.clock.Format("2006-01-02")
	if _, err := e.rec.CreateRequest(e.ctx, staff, next, next, ""); err != nil {
		t.Fatalf("다음 달 재요청: %v", err)
	}
}

// D9: 요청자가 유일한 관리자이면 감사(privacy:log_read)가 대신 처리한다. 다른 관리자가 있으면 감사는 처리할 수 없다.
func TestSubjectRequestAuditorFallbackWhenSoleManager(t *testing.T) {
	e := newEnv(t)
	e.withPerms() // admin(e.viewer) 한 명만 privacy:request_manage 를 가진다. 감사(e.other 아님: e.auditor() 의 사용자)가 log_read 를 가진다.
	day := e.clock.Format("2006-01-02")
	id, err := e.rec.CreateRequest(e.ctx, e.actor(), day, day, "관리자 본인 열람")
	if err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_request_pending' AND user_id = ?`, e.other) != 1 {
		t.Fatal("처리할 관리자가 없으면 감사에게 대기 알림이 가야 한다")
	}
	aud := e.auditor()
	audCtx := contract.WithPrincipal(e.ctx, aud)
	list, err := e.rec.ListRequests(audCtx, aud)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("감사는 처리할 관리자가 없는 요청만 본다: %+v %v", list, err)
	}
	if err := e.rec.DecideRequest(audCtx, aud, id, true, ""); err != nil {
		t.Fatalf("유일한 관리자의 요청은 감사가 처리: %v", err)
	}
	if _, err := e.rec.MyResult(e.ctx, e.actor(), id); err != nil {
		t.Fatalf("승인된 결과: %v", err)
	}
	// 일반 직원의 요청은 감사가 처리할 수 없다(관리자가 처리).
	sid, _ := e.rec.CreateRequest(e.ctx, e.staffP(e.subj1), day, day, "")
	if err := e.rec.DecideRequest(audCtx, aud, sid, true, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 요청을 감사가 처리: %v", err)
	}
	if l, _ := e.rec.ListRequests(audCtx, aud); len(l) != 1 {
		t.Fatalf("직원 요청은 감사 목록에 없다: %d건", len(l))
	}
	// 관리자가 두 명이면 감사 대행은 없다.
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, created_at) VALUES ((SELECT id FROM roles WHERE key = 'admin'), 'user', ?, '', 0, 'manual', 't')`, e.subj2); err != nil {
		t.Fatal(err)
	}
	id2 := func() int64 {
		*e.clock = e.clock.AddDate(0, 1, 0)
		d := e.clock.Format("2006-01-02")
		id, err := e.rec.CreateRequest(e.ctx, e.actor(), d, d, "")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}()
	if err := e.rec.DecideRequest(audCtx, aud, id2, true, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("관리자가 둘이면 감사는 대행할 수 없다: %v", err)
	}
	if l, _ := e.rec.ListRequests(audCtx, aud); len(l) != 0 {
		t.Fatalf("관리자가 둘이면 감사 목록은 비어야 한다: %d건", len(l))
	}
	// 권한이 전혀 없는 직원은 목록·처리 모두 거부.
	st := e.staffP(e.subj1)
	if _, err := e.rec.ListRequests(contract.WithPrincipal(e.ctx, st), st); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 목록: %v", err)
	}
}

// 서비스가 열람 요청 처리를 거부해도 route 계층과 같은 형태의 거부 감사(access.denied, 대상=라우트)가 한 건씩 남는다.
func TestRequestRoutesAuditDeniedCalls(t *testing.T) {
	e := newEnv(t)
	e.withPerms()
	day := e.clock.Format("2006-01-02")
	sid, err := e.rec.CreateRequest(e.ctx, e.staffP(e.subj1), day, day, "")
	if err != nil {
		t.Fatal(err)
	}
	rt := contract.NewRouteTable()
	e.rec.Register(rt)
	call := func(p contract.Principal, method, path string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req = req.WithContext(contract.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, req)
		return w.Code
	}
	denied := func(target string) int {
		return e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND result = 'denied' AND target_type = 'route' AND target_id = ?`, target)
	}
	idPath := "/api/admin/privacy/requests/" + strconv.FormatInt(sid, 10)
	st := e.staffP(e.subj2)
	for _, c := range []struct{ method, path, pattern string }{
		{"GET", "/api/admin/privacy/requests", "/api/admin/privacy/requests"},
		{"POST", idPath + "/approve", "/api/admin/privacy/requests/{id}/approve"},
		{"POST", idPath + "/reject", "/api/admin/privacy/requests/{id}/reject"},
	} {
		if code := call(st, c.method, c.path); code != http.StatusForbidden {
			t.Fatalf("직원 %s %s: %d", c.method, c.path, code)
		}
		if n := denied("GET " + c.pattern); c.method == "GET" && n != 1 {
			// 패턴 기록 형식은 rbac 와 같다(r.Pattern, 없으면 메서드+경로)
			t.Fatalf("직원 목록 거부 감사 %d건", n)
		}
	}
	var total int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND result = 'denied' AND target_type = 'route' AND actor_id = ?`, e.subj2).Scan(&total); err != nil || total != 3 {
		t.Fatalf("직원 호출 3건에 거부 감사 3건이어야 한다: %d %v", total, err)
	}
	// 감사 대행 조건 밖(관리자가 한 명뿐이 아님, 요청자는 직원)의 감사: 승인·반려 거부 + 감사.
	aud := e.auditor()
	before := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND actor_id = ?`, aud.UserID)
	if code := call(aud, "POST", idPath+"/approve"); code != http.StatusForbidden {
		t.Fatalf("감사의 직원 요청 승인: %d", code)
	}
	if code := call(aud, "POST", idPath+"/reject"); code != http.StatusForbidden && code != http.StatusBadRequest {
		t.Fatalf("감사의 직원 요청 반려: %d", code)
	}
	after := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND actor_id = ?`, aud.UserID)
	if after < before+1 {
		t.Fatalf("감사 대행 조건 밖 호출에 거부 감사가 남아야 한다: %d→%d", before, after)
	}
	// 정상 처리(관리자)는 거부 감사를 남기지 않는다.
	adminBefore := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND actor_id = ?`, e.viewer)
	req := httptest.NewRequest("POST", idPath+"/approve", strings.NewReader("{}"))
	req = req.WithContext(contract.WithPrincipal(req.Context(), e.actor()))
	w := httptest.NewRecorder()
	rt.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND actor_id = ?`, e.viewer) != adminBefore {
		t.Fatalf("관리자 승인 %d", w.Code)
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// newERPOrgEnv 는 플랫폼에 업무 서비스(WireERP)까지 조립한 서버다(보존 만료 폐기 API 처럼 ERP 라우트가 필요한 시험용).
func newERPOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	db := dbtest.Open(t)
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
	return &orgEnv{platformEnv: &platformEnv{t: t, ts: ts, d: d, rt: rt, base: base}, clients: map[string]*http.Client{}, csrfs: map[string]string{}}
}

// B29: 보존 만료 문서 폐기 API — 권한(관리자·감사 조회, 관리자만 폐기), 폐기 결과, 감사 기록.
func TestRetentionHTTP(t *testing.T) {
	e := newERPOrgEnv(t)
	req := e.userID("staff@pl.test")
	var dept int64
	if err := e.d.DB.QueryRow(`SELECT department_id FROM users WHERE id = ?`, req).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	insert := func(no, formCode string) int64 {
		return dbtest.InsertID(t, e.d.DB, `INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, fields, retention_until, completed_at, created_at, updated_at, legal_hold)
			VALUES (?, ?, ?, '기밀 제목', 'approved', (SELECT id FROM approval_forms WHERE code = ?), '{"content":"본문"}', '2025-12-31', '2020-12-31T00:00:00Z', '2020-12-31T00:00:00Z', '2020-12-31T00:00:00Z', ?)`,
			no, req, dept, formCode, map[string]int{"GEN": 0, "EXP": 1}[formCode])
	}
	gen := insert("GEN-9001", "GEN")
	legal := insert("EXP-9001", "EXP")

	if code, _ := e.do("staff", "GET", "/api/retention/candidates", ""); code != 403 {
		t.Errorf("직원의 후보 조회 %d(기대 403)", code)
	}
	code, body := e.do("auditor", "GET", "/api/retention/candidates", "")
	if code != 200 {
		t.Fatalf("감사의 후보 조회 %d: %s", code, body)
	}
	var list struct {
		Rows []struct {
			ID       int64  `json:"id"`
			No       string `json:"no"`
			DaysOver int    `json:"daysOver"`
		} `json:"rows"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Rows) != 1 || list.Rows[0].ID != gen || list.Rows[0].DaysOver < 1 {
		t.Fatalf("후보 %+v (법정 보존 문서는 나오지 않아야 함)", list)
	}
	if code, _ := e.do("auditor", "POST", "/api/retention/dispose", fmt.Sprintf(`{"ids":[%d],"reason":"정리"}`, gen)); code != 403 {
		t.Errorf("감사의 폐기 %d(기대 403)", code)
	}
	if code, body := e.do("admin", "POST", "/api/retention/dispose", fmt.Sprintf(`{"ids":[%d,%d],"reason":"정리"}`, gen, legal)); code != 400 || !strings.Contains(body, "EXP-9001") {
		t.Errorf("법정 보존 문서가 섞인 폐기 %d %s", code, body)
	}
	e.must("admin", "POST", "/api/retention/hold", fmt.Sprintf(`{"id":%d,"reason":"감사 진행 중"}`, gen), 200)
	if code, _ := e.do("admin", "POST", "/api/retention/dispose", fmt.Sprintf(`{"ids":[%d],"reason":"정리"}`, gen)); code != 400 {
		t.Errorf("보류 문서 폐기 %d(기대 400)", code)
	}
	_, hb := e.do("auditor", "GET", "/api/retention/holds", "")
	if !strings.Contains(hb, "GEN-9001") || !strings.Contains(hb, "감사 진행 중") {
		t.Errorf("보류 목록 %s", hb)
	}
	e.must("admin", "POST", fmt.Sprintf("/api/retention/hold/%d/clear", gen), `{}`, 200)
	out := e.must("admin", "POST", "/api/retention/dispose", fmt.Sprintf(`{"ids":[%d],"reason":"보존 연한 만료 정기 정리"}`, gen), 200)
	if out["disposed"].(float64) != 1 {
		t.Fatalf("폐기 결과 %v", out)
	}
	if code, body := e.do("admin", "GET", "/api/retention/count", ""); code != 200 || !strings.Contains(body, `"count":0`) {
		t.Errorf("폐기 뒤 건수 %d %s", code, body)
	}
	var title, fields string
	if err := e.d.DB.QueryRow(`SELECT title, fields FROM purchase_requests WHERE id = ?`, gen).Scan(&title, &fields); err != nil || title != "폐기된 문서" || fields != "{}" {
		t.Errorf("폐기 결과 title=%q fields=%q %v", title, fields, err)
	}
	_, ab := e.do("auditor", "GET", "/api/audit-logs?targetType=approval_doc", "")
	for _, act := range []string{"doc.dispose", "doc.dispose_hold", "doc.dispose_hold_clear"} {
		if !strings.Contains(ab, act) {
			t.Errorf("감사 기록에 %s 가 없음", act)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// B50 7단계: 근태·휴가 MCP 도구 2개를 실제 서버 조립(HTTP /mcp, 개인 토큰, 실제 조직·휴가 서비스) 위에서 시험한다.
//   - 모듈이 꺼져 있으면 목록에서 빠지고 직접 불러도 거부된다(감사에는 남는다).
//   - 모든 역할 x 도구 권한 표(스윕): 어느 결과에도 사유·증빙·사번·병가·휴가 종류 영문 코드가 없다.
//   - 열람 범위는 웹 달력과 같은 함수다: 같은 팀 동료는 보이고 다른 팀은 목록에도 없다.
const (
	secretReason = "비밀사유-허리디스크수술"
	secretNote   = "비밀메모-진단서원본"
)

func TestMCPAttendanceToolsModuleGateScopeAndSweep(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if did, err := d.Org.(*org.Service).SeedDev(ctx); err != nil || !did {
		t.Fatalf("개발용 조직 시드: %v %v", did, err)
	}
	h := handlerFor(buildRoutes(cfg, d), d)

	uid := func(email string) int64 {
		var id int64
		if err := d.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tokens := map[string]string{}
	token := func(email string) string {
		if tok, ok := tokens[email]; ok {
			return tok
		}
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "근태 시험-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		tokens[email] = tok
		return tok
	}
	rpc := func(tok, method string, params any) (map[string]any, string) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		var out struct {
			Result map[string]any `json:"result"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.Result, string(raw)
	}
	listTools := func(email string) []string {
		res, raw := rpc(token(email), "tools/list", map[string]any{})
		tools, _ := res["tools"].([]any)
		if res == nil {
			t.Fatalf("tools/list: %s", raw)
		}
		var names []string
		for _, x := range tools {
			names = append(names, x.(map[string]any)["name"].(string))
		}
		sort.Strings(names)
		return names
	}
	has := func(names []string, n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	call := func(email, tool string, args any) (bool, map[string]any, string) {
		res, raw := rpc(token(email), "tools/call", map[string]any{"name": tool, "arguments": args})
		isErr, _ := res["isError"].(bool)
		text := ""
		if cs, ok := res["content"].([]any); ok {
			for _, c := range cs {
				text += c.(map[string]any)["text"].(string)
			}
		} else {
			text = raw
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(text), &m)
		return isErr, m, text
	}

	// ---- 모듈이 꺼져 있으면: 목록에 없고, 불러도 거부되며 감사에 남는다 ----
	for _, who := range []string{"staff@pl.test", "admin@pl.test"} {
		names := listTools(who)
		if has(names, contract.ToolGetMyLeaveBalance) || has(names, contract.ToolListTeamAbsence) {
			t.Fatalf("%s: 모듈이 꺼졌는데 근태 도구가 목록에 있다: %v", who, names)
		}
	}
	for _, tool := range []string{contract.ToolGetMyLeaveBalance, contract.ToolListTeamAbsence} {
		isErr, _, text := call("staff@pl.test", tool, map[string]any{})
		if !isErr || !strings.Contains(text, "켜져 있지 않습니다") {
			t.Fatalf("%s: 꺼진 모듈의 호출은 거부여야 한다: %v %s", tool, isErr, text)
		}
		var c int
		if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = ? AND channel = 'mcp' AND result = 'error'`, "mcp."+tool).Scan(&c); err != nil || c != 1 {
			t.Fatalf("%s: 거부 호출 감사 %d건(기대 1): %v", tool, c, err)
		}
	}

	// ---- 시드: 잔여 원장과 승인된 휴가(사유·메모는 민감 표식) ----
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES ('module.attendance', 'on') ON CONFLICT(key) DO UPDATE SET value = 'on'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(contract.KST)
	soon := now.AddDate(0, 0, 10).Format("2006-01-02")
	far := now.AddDate(1, 0, 0).Format("2006-01-02")
	stamp := now.UTC().Format(time.RFC3339)
	staff, lead, staff2 := uid("staff@pl.test"), uid("lead@pl.test"), uid("staff2@pl.test")
	grant := func(user int64, kind string, minutes int64, period, expires string) {
		t.Helper()
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO leave_ledger (user_id, balance, kind, minutes, period_key, expires_on, created_at) VALUES (?, 'annual', ?, ?, ?, ?, ?)`,
			user, kind, minutes, period, expires, stamp); err != nil {
			t.Fatal(err)
		}
	}
	grant(staff, "grant", 4800, "시험-1", soon) // 10일 안에 소멸하는 10일분
	grant(staff, "grant", 4800, "시험-2", far)  // 10일분
	grant(staff, "use", -480, "", "")         // 1일 사용
	seq := 0
	leave := func(user int64, code, balance, start, end, reason string) {
		t.Helper()
		seq++
		var dept int64
		if err := d.DB.QueryRowContext(ctx, `SELECT COALESCE(department_id,0) FROM users WHERE id = ?`, user).Scan(&dept); err != nil {
			t.Fatal(err)
		}
		var doc int64
		if err := d.DB.QueryRowContext(ctx, `INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, fields, created_at, updated_at)
			VALUES (?, ?, ?, '휴가 신청서', 'approved', (SELECT id FROM approval_forms WHERE code = 'LEAVE'), '{}', ?, ?) RETURNING id`, fmt.Sprintf("LV-MCP-%d", seq), user, dept, stamp, stamp).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO leave_requests (user_id, doc_id, leave_code, start_date, end_date, part, minutes, balance, status, reason, note, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'full', 480, ?, 'approved', ?, ?, ?, ?)`, user, doc, code, start, end, balance, reason, secretNote, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	leave(staff, "annual", "annual", "2026-12-07", "2026-12-07", secretReason)  // 휴가
	leave(lead, "sick", "", "2026-12-08", "2026-12-08", secretReason)           // 부재(병가)
	leave(staff2, "annual", "annual", "2026-12-09", "2026-12-09", secretReason) // 다른 팀(영업1팀)
	if _, err := d.DB.ExecContext(ctx, `UPDATE org_people SET employee_no = 'EMP-비밀-' || user_id`); err != nil {
		t.Fatal(err)
	}
	rawMarkers := []string{secretReason, secretNote, "EMP-비밀", "진단", "병가", "sick", "annual", "compensatory", `"reason"`, `"proof`, "employee", "@pl.test", "010-"}

	// ---- 모듈이 켜지면 모든 역할의 목록에 둘 다 있다(권한은 전 역할) ----
	roles := []string{"staff@pl.test", "lead@pl.test", "finance@pl.test", "auditor@pl.test", "admin@pl.test"}
	for _, who := range roles {
		names := listTools(who)
		if !has(names, contract.ToolGetMyLeaveBalance) || !has(names, contract.ToolListTeamAbsence) {
			t.Fatalf("%s: 모듈이 켜졌는데 근태 도구가 없다: %v", who, names)
		}
	}

	// ---- 잔여: 본인 것만, 일·분·예약·소멸 예정 ----
	isErr, m, text := call("staff@pl.test", contract.ToolGetMyLeaveBalance, map[string]any{})
	if isErr {
		t.Fatalf("내 잔여: %s", text)
	}
	bal, _ := m["balances"].([]any)
	if len(bal) == 0 {
		t.Fatalf("잔여 줄이 없다: %s", text)
	}
	annual := bal[0].(map[string]any)
	if annual["name"] != "연차" || annual["available_days"] != float64(19) || annual["available_minutes"] != float64(9120) || annual["expiring_minutes"] != float64(4320) || annual["expiring_on"] != soon {
		t.Fatalf("연차 줄: %s", text)
	}
	// 본인 외 인자는 받지 않는다: 다른 사람 번호를 넘기면 스키마가 거부하고 값은 나가지 않는다.
	if e, _, tx := call("lead@pl.test", contract.ToolGetMyLeaveBalance, map[string]any{"user_id": staff}); !e || strings.Contains(tx, "9120") {
		t.Fatalf("남의 잔여 인자가 받아들여졌다: %v %s", e, tx)
	}
	// 팀장 본인의 잔여는 본인 원장(없음)이다.
	if _, lm, tx := call("lead@pl.test", contract.ToolGetMyLeaveBalance, map[string]any{}); strings.Contains(tx, "9120") || lm["balances"] == nil {
		t.Fatalf("팀장의 내 잔여: %s", tx)
	}

	// ---- 팀 부재: 같은 팀 동료는 휴가/부재로만, 다른 팀은 목록에도 없다 ----
	win := map[string]any{"from": "2026-12-01", "to": "2026-12-31"}
	isErr, m, text = call("staff@pl.test", contract.ToolListTeamAbsence, win)
	if isErr {
		t.Fatalf("팀 부재: %s", text)
	}
	kinds := map[string]string{}
	for _, p := range m["people"].([]any) {
		pm := p.(map[string]any)
		for _, a := range pm["absences"].([]any) {
			kinds[pm["name"].(string)] = a.(map[string]any)["kind"].(string)
		}
	}
	if len(kinds) != 2 || kinds["구매직원"] != "휴가" || kinds["구매팀장"] != "부재" {
		t.Fatalf("같은 팀 일정(구매직원 휴가·구매팀장 부재 둘뿐): %v %s", kinds, text)
	}
	if strings.Contains(text, "영업직원") {
		t.Fatalf("다른 팀 사람이 보인다: %s", text)
	}
	// 기간이 31일을 넘거나 형식이 틀리면 한국어 오류다.
	for _, bad := range []map[string]any{{"from": "2026-12-01", "to": "2027-02-01"}, {"from": "12/01", "to": "12/05"}, {"from": "2026-12-05", "to": "2026-12-01"}} {
		if e, _, tx := call("staff@pl.test", contract.ToolListTeamAbsence, bad); !e || !strings.Contains(tx, "조회") && !strings.Contains(tx, "형식") && !strings.Contains(tx, "앞섭니다") {
			t.Fatalf("잘못된 기간 %v: %v %s", bad, e, tx)
		}
	}
	// 법인 인자: 고를 수 없는 법인은 없는 것과 같다.
	if e, _, tx := call("staff@pl.test", contract.ToolListTeamAbsence, map[string]any{"entity": "ZZZ", "from": "2026-12-01", "to": "2026-12-02"}); !e || !strings.Contains(tx, "찾을 수 없") {
		t.Fatalf("없는 법인: %v %s", e, tx)
	}

	// ---- 스윕: 모든 역할 x 두 도구. 어떤 결과에도 사유·메모·사번·종류 코드가 없다 ----
	for _, who := range roles {
		for _, tool := range []string{contract.ToolGetMyLeaveBalance, contract.ToolListTeamAbsence} {
			args := map[string]any{}
			if tool == contract.ToolListTeamAbsence {
				args = win
			}
			isErr, _, tx := call(who, tool, args)
			if isErr && !strings.Contains(tx, "권한") {
				t.Fatalf("%s/%s: 예상 밖 오류 %s", who, tool, tx)
			}
			for _, bad := range rawMarkers {
				if strings.Contains(tx, bad) {
					t.Errorf("%s/%s: 결과에 %q 가 있다: %s", who, tool, bad, tx)
				}
			}
			if strings.Contains(tx, "mcp_anomaly") {
				t.Errorf("%s/%s: 원문 감시가 발동했다: %s", who, tool, tx)
			}
		}
	}
	// 인사 담당 권한이 없는 관리자·재무·감사도 본인 팀 밖은 목록에 없다(staff2 는 영업1팀).
	for _, who := range []string{"finance@pl.test", "auditor@pl.test"} {
		_, _, tx := call(who, contract.ToolListTeamAbsence, win)
		if strings.Contains(tx, "구매직원") || strings.Contains(tx, "영업직원") {
			t.Errorf("%s: 다른 팀 일정이 보인다: %s", who, tx)
		}
	}

	// ---- 감사: 호출마다 mcp.<도구> 기록이 channel=mcp 로 남고, 값(기간·사람)은 남기지 않는다 ----
	var okCalls int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE channel = 'mcp' AND action IN ('mcp.get_my_leave_balance', 'mcp.list_team_absence') AND result = 'success'`).Scan(&okCalls); err != nil || okCalls < 10 {
		t.Fatalf("성공 호출 감사 %d건: %v", okCalls, err)
	}
	var leaked int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action IN ('mcp.get_my_leave_balance', 'mcp.list_team_absence') AND (detail LIKE '%2026-12%' OR detail LIKE '%비밀%')`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("감사 기록에 기간·표식이 남았다: %d %v", leaked, err)
	}

	// ---- 모듈을 다시 끄면 목록에서 다시 빠진다 ----
	if _, err := d.DB.ExecContext(ctx, `UPDATE app_settings SET value = 'off' WHERE key = 'module.attendance'`); err != nil {
		t.Fatal(err)
	}
	if names := listTools("staff@pl.test"); has(names, contract.ToolGetMyLeaveBalance) || has(names, contract.ToolListTeamAbsence) {
		t.Fatalf("끈 뒤에도 목록에 있다: %v", names)
	}
}

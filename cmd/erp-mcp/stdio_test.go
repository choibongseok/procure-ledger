package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func cleanEnv(extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOROOT=") && !strings.HasPrefix(e, "PL_") {
			env = append(env, e)
		}
	}
	return append(env, extra...)
}

// 빌드한 바이너리를 임시 DB 로 stdio 실행해 initialize → tools/list → 권한 없는 역할의 approve_or_reject 거부까지
// 실제로 주고받고, 거부가 감사 로그(channel=mcp, denied)에 남았는지 같은 DB 에서 확인한다.
func TestBuiltBinaryStdioEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("바이너리 빌드가 필요해 -short 에서는 건너뜀")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "erp.db")
	bin := filepath.Join(dir, "erp-mcp-test")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = cleanEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("빌드 실패: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := config.Config{DBPath: dbPath, DevIDP: true}
	cfg.LLM.Provider = "mock"
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(email string) string {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "stdio-test", 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	staffTok, leadTok := issue("staff@pl.test"), issue("lead@pl.test")
	closeDB()

	connect := func(token string) *sdk.ClientSession {
		cmd := exec.Command(bin, "--transport", "stdio")
		cmd.Env = cleanEnv("PL_DB_PATH="+dbPath, "PL_MCP_TOKEN="+token, "PL_LLM_PROVIDER=mock")
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "stdio-test", Version: "0"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil) // initialize
		if err != nil {
			t.Fatalf("initialize: %v", err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}

	cs := connect(staffTok)
	var names []string
	for tool, err := range cs.Tools(ctx, nil) { // tools/list
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	// 근태·휴가 모듈이 꺼진 설치(기본)에서는 그 도구 2개가 목록에서 빠져 24개다. 켜면 26개가 된다(아래).
	if len(names) != 24 {
		t.Fatalf("도구 24개여야 함(근태·휴가 모듈 꺼짐): %v", names)
	}
	for _, n := range names {
		if n == contract.ToolGetMyLeaveBalance || n == contract.ToolListTeamAbsence {
			t.Fatalf("모듈이 꺼졌는데 %s 가 목록에 있다", n)
		}
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: contract.ToolApproveOrReject, Arguments: map[string]any{"pr_id": 1, "approve": true}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text += tc.Text
		}
	}
	if !res.IsError || !strings.Contains(text, "권한이 없습니다") {
		t.Fatalf("staff 의 approve_or_reject 는 거부여야 함: isError=%v %q", res.IsError, text)
	}
	t.Logf("tools/list=%v\napprove_or_reject(staff) → isError=%v %s", names, res.IsError, text)

	// 권한 있는 역할은 같은 바이너리에서 권한 검사를 통과한다(대상이 없어 서비스 오류가 날 뿐 거부가 아니다).
	csLead := connect(leadTok)
	res, err = csLead.CallTool(ctx, &sdk.CallToolParams{Name: contract.ToolApproveOrReject, Arguments: map[string]any{"pr_id": 999, "approve": true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok && strings.Contains(tc.Text, "권한이 없습니다") {
			t.Fatalf("manager 는 권한 거부가 아니어야 함: %s", tc.Text)
		}
	}
	_ = cs.Close()
	_ = csLead.Close()

	// 같은 DB 의 감사 로그에 거부가 channel=mcp 로 남았는지.
	d2, close2, err := bootstrap.Build(ctx, config.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer close2()
	recs, err := d2.AuditReader.List(ctx, audit.Filter{Action: "mcp.approve_or_reject", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	denied, other := 0, 0
	for _, r := range recs {
		if r.Channel != contract.ChannelMCP {
			t.Fatalf("채널은 mcp 여야 함: %+v", r)
		}
		if r.Result == audit.ResultDenied {
			denied++
		} else {
			other++
		}
	}
	if denied != 1 || other != 1 {
		t.Fatalf("거부 1건과 권한 통과 호출 1건이 기록되어야 함: denied=%d other=%d %+v", denied, other, recs)
	}

	// 모듈을 켜면(같은 DB) 새 연결의 도구 목록에 근태·휴가 도구 2개가 더해져 26개다. 호출마다 켜짐 상태를 읽는다.
	if _, err := d2.DB.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES ('module.attendance', 'on') ON CONFLICT(key) DO UPDATE SET value = 'on'`); err != nil {
		t.Fatal(err)
	}
	csOn := connect(staffTok)
	var onNames []string
	for tool, err := range csOn.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		onNames = append(onNames, tool.Name)
	}
	if len(onNames) != 26 {
		t.Fatalf("모듈을 켜면 도구 26개여야 함: %v", onNames)
	}
	_ = csOn.Close()
}

func TestStdioWithoutTokenFails(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "erp-mcp-test")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = cleanEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("빌드 실패: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "--transport", "stdio")
	cmd.Env = cleanEnv("PL_DB_PATH=" + filepath.Join(dir, "x.db"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "토큰") {
		t.Fatalf("토큰이 없으면 실패해야 함: %v %s", err, out)
	}
}

// 단독 실행(stdio)에도 서버와 같은 환경변수의 분당 한도가 적용된다: 한도 1 이면 두 번째 호출이 거부되고,
// 감사에는 첫 거부 1건이 남는다. 꺼져 있을 때의 경고는 stderr 로만 나간다.
func TestBuiltBinaryStdioAppliesRateLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("바이너리 빌드가 필요해 -short 에서는 건너뜀")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "erp.db")
	bin := filepath.Join(dir, "erp-mcp-test")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = cleanEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("빌드 실패: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := config.Config{DBPath: dbPath, DevIDP: true}
	cfg.LLM.Provider = "mock"
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var p contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, "staff@pl.test").Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role, p.Name = contract.Role(role), "staff"
	tok, _, err := d.Tokens.Issue(ctx, p, "rate-test", 0)
	if err != nil {
		t.Fatal(err)
	}
	closeDB()

	cmd := exec.Command(bin, "--transport", "stdio")
	cmd.Env = cleanEnv("PL_DB_PATH="+dbPath, "PL_MCP_TOKEN="+tok, "PL_LLM_PROVIDER=mock", "PL_MCP_RATE_PER_MIN=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "rate-test", Version: "0"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer cs.Close()
	call := func() (bool, string) {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: contract.ToolSearchItems, Arguments: map[string]any{"query": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		var text string
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				text += tc.Text
			}
		}
		return res.IsError, text
	}
	if isErr, text := call(); isErr {
		t.Fatalf("첫 호출은 통과해야 함: %s", text)
	}
	if isErr, text := call(); !isErr || !strings.Contains(text, "호출 한도") {
		t.Fatalf("두 번째 호출은 한도 거부여야 함: %v %q", isErr, text)
	}
	_ = cs.Close() // 프로세스가 끝난 뒤에 stderr 를 읽는다(쓰는 고루틴과 경합 방지)
	if strings.Contains(stderr.String(), "한도가 꺼져") {
		t.Fatalf("한도가 켜져 있으면 경고가 없어야 함: %s", stderr.String())
	}
}

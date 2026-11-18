package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/mcp"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// AC-55·AC-56: 퇴사일 다음 날이 된 사람은 투영 갱신이 돌지 않아 users.active 가 1 로 남아 있어도
// 세션·개인 토큰·dev IdP 로그인·erp-mcp 기동이 모두 거부된다.
func TestRetiredUserDeniedWithoutProjection(t *testing.T) {
	e := newPlatformEnv(t, true)
	ctx := context.Background()
	var adminID, staffID int64
	if err := e.d.DB.QueryRow(`SELECT id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := e.d.DB.QueryRow(`SELECT id FROM users WHERE email = 'staff@pl.test'`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}
	orgSvc := e.d.Org.(*org.Service)
	admin := contract.Principal{UserID: adminID, Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	reg := func(c org.Command) org.Outcome {
		t.Helper()
		if c.Effective != "" && c.Reason == "" {
			c.Reason, c.Confirm = "시험 준비", true
		}
		o, err := orgSvc.Register(ctx, admin, c)
		if err != nil {
			t.Fatalf("%s: %v", org.KindLabel(c.Kind), err)
		}
		return o
	}
	// 기존 dev 시드 사용자를 입사일이 과거인 조직 구성원으로 옮긴다(서버가 기동할 때 하는 백필).
	if _, err := e.d.DB.Exec(`UPDATE users SET created_at = '2020-02-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if did, err := orgSvc.Backfill(ctx); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}

	c := e.browser()
	e.login(c, "staff@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(c)}
	code, body := e.call(c, "POST", "/api/tokens", `{"name":"mcp","ttlDays":30}`, h)
	if code != http.StatusCreated {
		t.Fatalf("토큰 발급: %d %s", code, body)
	}
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &issued)
	if _, err := e.d.Authenticator.AuthenticateToken(ctx, issued.Token); err != nil {
		t.Fatalf("재직 중에는 토큰 인증이 되어야 함: %v", err)
	}
	if _, err := mcp.NewServerForToken(ctx, e.d, issued.Token); err != nil {
		t.Fatalf("재직 중에는 erp-mcp 기동이 되어야 함: %v", err)
	}

	// 어제가 마지막 근무일이면 오늘부터 퇴사다. 등록은 투영(계정 끔·세션·토큰 폐기)까지 같이 하므로,
	// 서버가 꺼져 있어 투영이 돌지 않은 상황을 만들려고 투영 결과를 되돌린다(계정 켬, 세션·토큰 폐기 취소).
	yesterday := contract.DayOf(time.Now().Add(-24 * time.Hour))
	reg(org.Command{Kind: org.KindLeave, UserID: staffID, LastDay: yesterday, LeaveKind: "자진", Reason: "퇴사 처리"})
	for _, q := range []string{
		`UPDATE users SET active = 1 WHERE id = ` + "?",
		`UPDATE sessions SET revoked_at = NULL WHERE user_id = ` + "?",
		`UPDATE personal_tokens SET revoked_at = NULL WHERE user_id = ` + "?",
	} {
		if _, err := e.d.DB.Exec(q, staffID); err != nil {
			t.Fatal(err)
		}
	}
	var active int
	_ = e.d.DB.QueryRow(`SELECT active FROM users WHERE id = ?`, staffID).Scan(&active)
	if active != 1 {
		t.Fatal("시험 전제: 투영이 돌지 않아 계정이 아직 켜져 있어야 함")
	}

	// 1) 세션
	if code, _ := e.call(c, "GET", "/api/me", "", nil); code != http.StatusUnauthorized {
		t.Errorf("퇴사자의 세션: %d, want 401", code)
	}
	// 2) 개인 토큰
	if _, err := e.d.Authenticator.AuthenticateToken(ctx, issued.Token); err == nil {
		t.Error("퇴사자의 토큰이 인증됨")
	}
	// 3) erp-mcp 기동(같은 인증기를 쓴다)
	if _, err := mcp.NewServerForToken(ctx, e.d, issued.Token); err == nil {
		t.Error("퇴사자의 토큰으로 erp-mcp 가 기동됨")
	}
	// 4) dev IdP 로그인
	fresh := e.browser()
	res, err := fresh.Get(e.base + "/api/auth/login?login_hint=staff%40pl.test")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !strings.Contains(res.Request.URL.String(), "error=") {
		t.Errorf("퇴사자의 로그인이 거부되지 않음: %s", res.Request.URL)
	}
	if code, _ := e.call(fresh, "GET", "/api/me", "", nil); code != http.StatusUnauthorized {
		t.Errorf("퇴사자 로그인 뒤 /api/me: %d", code)
	}
	// 5) 재직 중인 다른 조직 구성원은 영향이 없다(조직에 등록되지 않은 사용자의 허용은 org.EmploymentDenied 단위 테스트가 확인한다).
	other := e.browser()
	e.login(other, "lead@pl.test")
	if code, _ := e.call(other, "GET", "/api/me", "", nil); code != http.StatusOK {
		t.Errorf("재직 중인 사용자: %d", code)
	}
}

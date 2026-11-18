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

// B50 4단계(AC-5035·5041): 휴직 중인 사람도 로그인·토큰·erp-mcp 기동이 되고(퇴사와 다르다), 휴직 사유 원문은 전용 경로로만 나가며
// 본인이 아니면 열람 기록을 남긴다. 범위 밖(관리자 등)은 404, 이력 목록에는 사유가 없다.
func TestAbsenceKeepsLoginAndGuardsReasonRoute(t *testing.T) {
	e := newPlatformEnv(t, true)
	ctx := context.Background()
	var staffID int64
	if err := e.d.DB.QueryRow(`SELECT id FROM users WHERE email = 'staff@pl.test'`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}
	orgSvc := e.d.Org.(*org.Service)
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

	// 어제부터 휴직(사유 있음). 관리자는 HTTP 로 등록한다(권한 선언·감사·CSRF 경로).
	ac := e.browser()
	e.login(ac, "admin@pl.test")
	ah := map[string]string{"X-CSRF-Token": e.csrf(ac)}
	yesterday := string(contract.DayOf(time.Now().Add(-24 * time.Hour)))
	reqBody, _ := json.Marshal(map[string]any{"kind": "absence_start", "userId": staffID, "effectiveDate": yesterday, "reason": "휴직 발령", "confirm": true,
		"absenceKind": "illness", "absenceReason": "수술 후 회복", "returnDay": "2099-01-01"})
	code, body = e.call(ac, "POST", "/api/org/events", string(reqBody), ah)
	if code != http.StatusCreated {
		t.Fatalf("휴직 등록: %d %s", code, body)
	}
	var created struct {
		EventIDs []int64 `json:"eventIds"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if len(created.EventIDs) != 1 {
		t.Fatalf("이벤트 번호: %s", body)
	}
	ev := created.EventIDs[0]

	// 휴직은 퇴사가 아니다: 세션·토큰·erp-mcp 모두 그대로다.
	if code, _ := e.call(c, "GET", "/api/me", "", nil); code != http.StatusOK {
		t.Errorf("휴직 중에도 로그인은 된다: %d", code)
	}
	if _, err := e.d.Authenticator.AuthenticateToken(ctx, issued.Token); err != nil {
		t.Errorf("휴직 중에도 토큰 인증은 된다: %v", err)
	}
	if _, err := mcp.NewServerForToken(ctx, e.d, issued.Token); err != nil {
		t.Errorf("휴직 중에도 erp-mcp 기동은 된다: %v", err)
	}
	p, err := orgSvc.Person(ctx, staffID, "")
	if err != nil || !p.Active || !p.OnLeave {
		t.Fatalf("조직 조회: %+v %v", p, err)
	}

	// 이력 목록: 사유 원문 없이 "사유 있음" 표시만.
	code, body = e.call(ac, "GET", "/api/org/events?kind=absence_start", "", nil)
	if code != http.StatusOK || strings.Contains(body, "수술") || !strings.Contains(body, `"hasAbsenceReason":true`) {
		t.Fatalf("이력 목록: %d %s", code, body)
	}
	// 사람 카드: 휴직 중 표시만 있고 구분(질병)은 없다.
	code, body = e.call(ac, "GET", "/api/org/people?q="+"%EC%A7%81%EC%9B%90", "", nil)
	if code != http.StatusOK || strings.Contains(body, "illness") || strings.Contains(body, "질병") {
		t.Fatalf("사람 목록: %d %s", code, body)
	}

	// 사유 원문: 본인은 읽는다. 관리자(근태 팀·인사 권한 없음)는 없는 것과 같다.
	path := "/api/org/events/" + jsonNum(ev) + "/absence-reason"
	code, body = e.call(c, "GET", path, "", nil)
	if code != http.StatusOK || !strings.Contains(body, "수술 후 회복") {
		t.Fatalf("본인 열람: %d %s", code, body)
	}
	if code, body = e.call(ac, "GET", path, "", nil); code != http.StatusNotFound || strings.Contains(body, "수술") {
		t.Fatalf("관리자는 사유를 볼 수 없다: %d %s", code, body)
	}
	// 감사: 휴직 발령에 사유 원문이 없다.
	var diff string
	if err := e.d.DB.QueryRow(`SELECT COALESCE(detail,'') || COALESCE(diff,'') FROM audit_logs WHERE action = 'org.absence_start' ORDER BY seq DESC LIMIT 1`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, "수술") {
		t.Fatalf("감사에 사유 원문: %s", diff)
	}
}

func jsonNum(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

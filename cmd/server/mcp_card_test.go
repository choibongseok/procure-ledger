package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B32: 실제 카드 서비스 위에서 get_card_unreconciled 를 HTTP /mcp 로 부른다.
// 본인 소지 카드의 내역만 나오고(재무도 본인 소지분만), 카드번호 조각(끝 4자리)은 나가지 않으며, card:explain 이 없는 감사는 거부된다.
func TestMCPCardUnreconciledOverRealCardService(t *testing.T) {
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
	h := handlerFor(buildRoutes(cfg, d), d)

	userID := func(email string) (int64, int64, string) {
		var id, dept int64
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id, role FROM users WHERE email = ?`, email).Scan(&id, &dept, &role); err != nil {
			t.Fatal(err)
		}
		return id, dept, role
	}
	token := func(email string) string {
		id, dept, role := userID(email)
		tok, _, err := d.Tokens.Issue(ctx, contract.Principal{UserID: id, Name: email, Role: contract.Role(role), DepartmentID: dept}, "카드 시험-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	mcpCall := func(tok, tool string, args any) (bool, map[string]any, string) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &out)
		text := ""
		for _, c := range out.Result.Content {
			text += c.Text
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(text), &m)
		return out.Result.IsError, m, text
	}

	staffID, staffDept, _ := userID("staff@pl.test")
	staff2ID, _, _ := userID("staff2@pl.test")
	finID, finDept, _ := userID("finance@pl.test")
	fctx := contract.WithPrincipal(ctx, contract.Principal{UserID: finID, Name: "재무", Role: contract.RoleFinance, DepartmentID: finDept, Channel: contract.ChannelWeb})
	_ = staffDept
	// 카드 두 장(소지자가 다르다)을 등록하고, 오늘 날짜 근처의 사용 내역을 올린다.
	for _, c := range []domain.CardInput{
		{Alias: "구매팀 법인카드", Issuer: domain.CardFormatA, Last4: "7890", HolderID: staffID},
		{Alias: "영업팀 법인카드", Issuer: domain.CardFormatA, Last4: "1111", HolderID: staff2ID},
	} {
		if _, err := d.CardSvc.SaveCard(fctx, 0, c); err != nil {
			t.Fatalf("카드 등록: %v", err)
		}
	}
	day := time.Now().In(contract.KST).AddDate(0, 0, -2).Format("2006-01-02")
	csv := "승인일시,카드번호,가맹점명,승인금액,승인번호,할부개월,승인구분\n" +
		day + " 12:00:00,1234-56**-****-7890,스타벅스 강남점,12500,A1,0,승인\n" +
		day + " 13:00:00,1234-56**-****-7890,대한문구,45000,A2,0,승인\n" +
		day + " 14:00:00,1234-56**-****-1111,영업팀 회식,300000,A3,0,승인\n"
	res, err := d.CardSvc.ConfirmImport(fctx, domain.CardFormatA, []byte(csv))
	if err != nil || res.New != 3 {
		t.Fatalf("내역 업로드: %+v %v", res, err)
	}

	staffTok, staff2Tok, finTok, audTok := token("staff@pl.test"), token("staff2@pl.test"), token("finance@pl.test"), token("auditor@pl.test")

	// 본인 소지분만: staff 는 구매팀 카드 2건, staff2 는 영업팀 카드 1건. 카드번호 조각은 어느 응답에도 없다.
	isErr, m, text := mcpCall(staffTok, "get_card_unreconciled", map[string]any{})
	if isErr || m["count"] != float64(2) || m["total_amount"] != float64(57500) {
		t.Fatalf("staff: %s", text)
	}
	for _, bad := range []string{"7890", "1111", "1234-56", "last4", "영업팀 회식", "300000"} {
		if strings.Contains(text, bad) {
			t.Fatalf("staff 응답에 나가면 안 되는 값 %q: %s", bad, text)
		}
	}
	for _, r := range m["items"].([]any) {
		if r.(map[string]any)["card"] != "구매팀 법인카드" {
			t.Fatalf("다른 소지자의 카드 내역이 섞임: %s", text)
		}
	}
	isErr, m, text = mcpCall(staff2Tok, "get_card_unreconciled", map[string]any{})
	if isErr || m["count"] != float64(1) || !strings.Contains(text, "영업팀 회식") || strings.Contains(text, "스타벅스") || strings.Contains(text, "1111") {
		t.Fatalf("staff2: %s", text)
	}
	// 재무도 본인 소지분만(소지 카드가 없으면 빈 목록): 전체 현황은 화면에서 본다.
	isErr, m, text = mcpCall(finTok, "get_card_unreconciled", map[string]any{})
	if isErr || m["count"] != float64(0) || strings.Contains(text, "스타벅스") || strings.Contains(text, "영업팀 회식") {
		t.Fatalf("재무는 본인 소지분만: %s", text)
	}
	// limit: 1건만.
	if isErr, m, text = mcpCall(staffTok, "get_card_unreconciled", map[string]any{"limit": 1}); isErr || m["count"] != float64(1) || m["truncated"] != true {
		t.Fatalf("limit 1: %s", text)
	}
	// card:explain 이 없는 감사는 거부되고 감사 로그에 denied 로 남는다.
	if isErr, _, text = mcpCall(audTok, "get_card_unreconciled", map[string]any{}); !isErr {
		t.Fatalf("감사는 거부되어야 함: %s", text)
	}
	var denied int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.get_card_unreconciled' AND result = 'denied'`).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("거부 감사 %d건: %v", denied, err)
	}
	// 감사 로그 어디에도 카드번호 조각이 없다(호출 기록의 대상·내용).
	var leaked int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.get_card_unreconciled' AND (detail LIKE '%7890%' OR detail LIKE '%1111%' OR target_id LIKE '%7890%')`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("감사 로그에 카드번호 조각 %d건: %v", leaked, err)
	}
}

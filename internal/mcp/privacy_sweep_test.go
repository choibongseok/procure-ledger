package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/demo"
	"github.com/choibongseok/procure-ledger/internal/mcp"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// B41 D6·AC-4120: 실제 서버 조립(bootstrap.Build)과 데모 세트(원문 사업자번호·전화·이메일이 든 거래처·사람) 위에서,
// mask:bypass 를 가진 역할의 MCP 토큰으로 모든 도구를 호출해도 어떤 결과에도 원문이 없고 열람 기록(privacy_access_log)이 생기지 않는다.

var rawMarkers = []string{
	"000-00-00001", "000-00-00002", "000-00-00003", // 데모 거래처 사업자번호
	"02-555-0101", "02-555-0102", "02-555-0103", // 거래처 전화
	"sales@hanbit.demo.invalid", "order@daesung.demo.invalid", "mirae@office.demo.invalid", // 거래처 이메일
	"010-9999-8888", "finance.a@demo.invalid", "purchase.lead@demo.invalid", // 사람 연락처(시험이 넣은 전화, 사람 이메일)
}

func sweepWorld(t *testing.T) (deps.Deps, map[string]contract.Principal) {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	d, closeDB, err := bootstrap.Build(context.Background(), config.Load())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	ctx := context.Background()
	if err := demo.Seed(ctx, d); err != nil {
		t.Fatalf("데모 데이터: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE org_people SET phone = '010-9999-8888'`); err != nil {
		t.Fatal(err)
	}
	who := map[string]contract.Principal{}
	for key, email := range map[string]string{"finance": "finance.a@demo.invalid", "lead": "purchase.lead@demo.invalid", "auditor": "auditor@demo.invalid", "ceo": "ceo@demo.invalid"} {
		var id, dept int64
		var name, role string
		if err := d.DB.QueryRow(`SELECT id, name, role, COALESCE(department_id,0) FROM users WHERE email = ?`, email).Scan(&id, &name, &role, &dept); err != nil {
			t.Fatal(err)
		}
		who[key] = contract.Principal{UserID: id, Name: name, Role: contract.Role(role), DepartmentID: dept, Channel: contract.ChannelMCP}
	}
	return d, who
}

func sweepConnect(t *testing.T, d deps.Deps, p contract.Principal) *sdk.ClientSession {
	t.Helper()
	srv, _ := mcp.NewServer(d, p)
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "sweep", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func scalarInt(t *testing.T, d deps.Deps, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// toolArgs 는 도구 이름별 시험 인자다. 시드의 실제 번호를 쓴다.
func toolArgs(t *testing.T, d deps.Deps, name string) map[string]any {
	item := scalarInt(t, d, `SELECT MIN(id) FROM items`)
	pr := scalarInt(t, d, `SELECT MIN(id) FROM purchase_requests WHERE status = 'ordered'`)
	po := scalarInt(t, d, `SELECT MIN(id) FROM purchase_orders`)
	switch name {
	case contract.ToolSearchItems:
		return map[string]any{"query": "모니터"}
	case contract.ToolGetStock, contract.ToolGetItemLedger:
		return map[string]any{"item_id": item}
	case contract.ToolGetPurchaseRequest:
		return map[string]any{"pr_id": pr}
	case contract.ToolWhoApproves:
		return map[string]any{"form_code": "PUR", "amount": 1000000}
	case contract.ToolGetApprovalDoc:
		return map[string]any{"doc_id": pr}
	case contract.ToolSearchPolicy:
		return map[string]any{"query": "결재선"}
	case contract.ToolSearchPeople:
		return map[string]any{"query": "구매"}
	case contract.ToolGetOrgUnit:
		return map[string]any{"unit": "DEP-PU", "include_members": true}
	case contract.ToolVerifyDocument:
		// 증빙 본문에 원문이 들어 있어도 결과(근거 항목·원문 인용)는 가려서 나간다.
		return map[string]any{"po_id": po, "text": "거래명세서\n공급자: 한빛상사\n사업자등록번호: 000-00-00001\n전화: 02-555-0101\n합계: 1000"}
	case contract.ToolGetPayables:
		return map[string]any{}
	}
	return map[string]any{}
}

func TestEveryMCPToolResultHasNoRawPersonalData(t *testing.T) {
	d, who := sweepWorld(t)
	masker := rbac.Masker{}
	// 대조군: 같은 사람·같은 데이터를 웹 채널로 보면 원문이 나간다(위 검사가 의미 있는 시험이 되게).
	web := who["finance"]
	web.Channel = contract.ChannelWeb
	if card, err := d.Org.Card(contract.WithPrincipal(context.Background(), web), web, who["lead"].UserID, ""); err != nil || card.Phone != "010-9999-8888" {
		t.Fatalf("대조군: 웹 채널은 원문을 봐야 함: %q %v", card.Phone, err)
	}
	called, succeeded := map[string]bool{}, map[string]bool{}
	for role, p := range who {
		cs := sweepConnect(t, d, p)
		tools, err := cs.ListTools(context.Background(), nil)
		if err != nil || len(tools.Tools) == 0 {
			t.Fatalf("도구 목록: %v", err)
		}
		for _, tool := range tools.Tools {
			switch tool.Name {
			case contract.ToolCreatePRDraft, contract.ToolDraftReorder, contract.ToolApproveOrReject:
				continue // 쓰기 도구는 아래의 별도 시험이 본다(데이터를 바꾸므로 순회에서 뺀다)
			}
			res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool.Name, Arguments: toolArgs(t, d, tool.Name)})
			if err != nil {
				t.Fatalf("%s/%s: 프로토콜 오류 %v", role, tool.Name, err)
			}
			called[tool.Name] = true
			if !res.IsError {
				succeeded[tool.Name] = true
			}
			var sb strings.Builder
			for _, c := range res.Content {
				if tc, ok := c.(*sdk.TextContent); ok {
					sb.WriteString(tc.Text)
				}
			}
			text := sb.String()
			if strings.Contains(text, mcp.ErrExposure.Error()) {
				t.Errorf("%s/%s: 이상 감지가 발동했다(도구가 원문을 낸 것): %s", role, tool.Name, text)
			}
			for _, m := range rawMarkers {
				if strings.Contains(text, m) {
					t.Errorf("%s/%s: 결과에 원문 %q", role, tool.Name, m)
				}
			}
			if masker.MaskText(text) != text {
				t.Errorf("%s/%s: 마스킹되지 않은 패턴이 남음: %s", role, tool.Name, text)
			}
		}
	}
	// 모든 읽기 도구를 실제로 불렀다(선언된 도구 중 쓰기 3종 외 전부).
	for _, name := range []string{contract.ToolSearchItems, contract.ToolGetStock, contract.ToolListPendingApprovals, contract.ToolGetPurchaseRequest, contract.ToolVerifyDocument,
		contract.ToolListStockAlerts, contract.ToolSearchPolicy, contract.ToolListApprovalForms, contract.ToolGetApprovalDoc, contract.ToolGetPayables, contract.ToolGetTaxInvoices, contract.ToolGetCardUnreconciled,
		contract.ToolGetMonthCloseStatus, contract.ToolGetBudgetStatus, contract.ToolGetItemLedger, contract.ToolWhoApproves, contract.ToolSearchPeople, contract.ToolGetOrgUnit, contract.ToolListMyApprovalDocs} {
		if !called[name] {
			t.Errorf("도구 %s 를 순회에서 부르지 못했다(권한 없는 역할만 있었거나 목록에 없음)", name)
		}
	}
	// 원문을 가진 데이터를 다루는 도구는 실제로 성공 응답을 냈다(오류 응답만 보고 통과하는 시험이 되지 않게).
	for _, name := range []string{contract.ToolSearchPeople, contract.ToolGetOrgUnit, contract.ToolGetPayables, contract.ToolGetPurchaseRequest, contract.ToolVerifyDocument, contract.ToolWhoApproves} {
		if !succeeded[name] {
			t.Errorf("도구 %s 가 어느 역할에서도 성공하지 못했다(시험 인자·시드 점검)", name)
		}
	}
	// MCP 채널은 원문이 나갈 수 없으므로 열람 기록(원문·이상 상태 모두)이 하나도 생기지 않는다.
	if n := scalarInt(t, d, `SELECT COUNT(*) FROM privacy_access_log WHERE channel = 'mcp'`); n != 0 {
		t.Fatalf("MCP 경로에 열람 기록이 %d건 생김", n)
	}
	if n := scalarInt(t, d, `SELECT COUNT(*) FROM privacy_access_log WHERE exposure = 'mcp_anomaly'`); n != 0 {
		t.Fatalf("이상 상태 기록이 %d건 생김", n)
	}
}

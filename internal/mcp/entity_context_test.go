package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

// B44 1단계(ai): MCP 호출은 도구 실행과 호출 감사가 같은 현재 법인 컨텍스트로 일한다. 토큰 주인의 소속 법인(없으면 기본 법인)이고,
// 법인을 바꾸는 입력 인자와 업무 데이터의 법인 거름은 2~3단계다. 이 시험은 그 틀(컨텍스트가 실리는지, 권한 거부 때는 도구가 안 불리는지)을 고정한다.

type entityProbe struct {
	called bool
	active contract.ActiveEntity
	have   bool
}

func runWithEntity(t *testing.T, p contract.Principal, tool string, probe *entityProbe) sdk.Result {
	t.Helper()
	b := fake.NewBundle()
	d := b.Deps()
	table := &contract.ToolTable{}
	table.Declare(tool)
	h := guard(d, fixed(p), table)(func(ctx context.Context, _ string, _ sdk.Request) (sdk.Result, error) {
		probe.called = true
		probe.active, probe.have = contract.ActiveEntityFrom(ctx)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `{"ok":true}`}}}, nil
	})
	args, _ := json.Marshal(map[string]any{})
	res, err := h(context.Background(), "tools/call", &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMCPCallCarriesOwnEntityOrDefault(t *testing.T) {
	fin := contract.Principal{UserID: 3, Name: "재무", Role: contract.RoleFinance, Channel: contract.ChannelMCP}
	// 소속 법인이 없으면(법인이 하나뿐인 설치) 기본 법인이다.
	var probe entityProbe
	runWithEntity(t, fin, contract.ToolGetPayables, &probe)
	if !probe.called || !probe.have || probe.active.All || probe.active.ID != contract.DefaultEntityID {
		t.Fatalf("기본 법인: %+v", probe)
	}
	// 소속 법인이 있으면 그 첫째 법인이다(겸직이 있어도 한 법인으로 고정, 전체 모드는 MCP 에 없다).
	fin.OwnEntities = []int64{4, 2}
	probe = entityProbe{}
	runWithEntity(t, fin, contract.ToolGetPayables, &probe)
	if !probe.have || probe.active.ID != 4 || probe.active.All {
		t.Fatalf("소속 법인: %+v", probe)
	}
}

func TestMCPDeniedCallNeverReachesToolEvenWithEntity(t *testing.T) {
	staff := contract.Principal{UserID: 5, Name: "직원", Role: contract.RoleStaff, Channel: contract.ChannelMCP, OwnEntities: []int64{2}}
	var probe entityProbe
	res := runWithEntity(t, staff, contract.ToolGetPayables, &probe) // 직원은 ledger:read 가 없다
	if probe.called {
		t.Fatal("권한이 없으면 도구가 불리면 안 된다")
	}
	if cr, ok := res.(*sdk.CallToolResult); !ok || !cr.IsError {
		t.Fatalf("거부 응답이어야 한다: %+v", res)
	}
}

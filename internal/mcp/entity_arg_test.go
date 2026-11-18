package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 법인이 둘 이상인 설치: entity 인자는 호출자가 고를 수 있는 법인일 때만 되고, 범위 밖·없는 코드는 같은 거부이며, 응답에 법인 코드가 붙고 tools/list 가 인자를 알린다.
func TestMCPEntityArgumentScopeAndTag(t *testing.T) {
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entities (code, name, business_no, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', '', 0, 1, 1, 'x', 'x'), ('XYZ', '다른 계열사', '', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	var abc int64
	_ = db.QueryRow(`SELECT id FROM entities WHERE code = 'ABC'`).Scan(&abc)
	b := fake.NewBundle()
	d := b.Deps()
	d.DB = db
	table := &contract.ToolTable{}
	table.Declare(contract.ToolGetPayables)
	var seen []contract.ActiveEntity
	p := contract.Principal{UserID: 3, Name: "재무", Role: contract.RoleFinance, Channel: contract.ChannelMCP, OwnEntities: []int64{abc},
		EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermLedgerRead: contract.NewEntitySet(contract.DefaultEntityID, abc)}}
	h := guard(d, fixed(p), table)(func(ctx context.Context, m string, r sdk.Request) (sdk.Result, error) {
		if m == "tools/list" {
			return &sdk.ListToolsResult{Tools: []*sdk.Tool{{Name: contract.ToolGetPayables, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"vendor": map[string]any{"type": "string"}}}}}}, nil
		}
		a, _ := contract.ActiveEntityFrom(ctx)
		seen = append(seen, a)
		var m2 map[string]any
		_ = json.Unmarshal(r.GetParams().(*sdk.CallToolParamsRaw).Arguments, &m2)
		if _, has := m2["entity"]; has {
			t.Error("도구는 entity 인자를 받으면 안 된다(가드가 걷어 낸다)")
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `{"ok":true}`}}}, nil
	})
	call := func(args map[string]any) *sdk.CallToolResult {
		raw, _ := json.Marshal(args)
		res, err := h(context.Background(), "tools/call", &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{Name: contract.ToolGetPayables, Arguments: raw}})
		if err != nil {
			t.Fatal(err)
		}
		return res.(*sdk.CallToolResult)
	}
	text := func(r *sdk.CallToolResult) string { return r.Content[0].(*sdk.TextContent).Text }
	// 인자 없음: 소속 법인, 응답에 법인 코드.
	if r := call(map[string]any{}); r.IsError || !strings.Contains(text(r), `"entity":"ABC"`) || seen[0].ID != abc {
		t.Fatalf("기본: %+v %+v", r, seen)
	}
	// 고를 수 있는 다른 법인(기본 법인).
	if r := call(map[string]any{"entity": "main"}); r.IsError || seen[1].ID != contract.DefaultEntityID || !strings.Contains(text(r), `"entity":"MAIN"`) {
		t.Fatalf("범위 안 법인: %+v %+v", r, seen)
	}
	// 범위 밖(존재하는 법인)과 없는 코드는 같은 거부이고 도구는 불리지 않는다.
	n := len(seen)
	for _, code := range []string{"XYZ", "NOPE"} {
		r := call(map[string]any{"entity": code})
		if !r.IsError || !strings.Contains(text(r), "찾을 수 없는 법인") || len(seen) != n {
			t.Fatalf("%s: %+v", code, r)
		}
	}
	// tools/list 는 entity 인자를 알린다.
	res, err := h(context.Background(), "tools/list", &sdk.ServerRequest[*sdk.ListToolsParams]{Params: &sdk.ListToolsParams{}})
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(res.(*sdk.ListToolsResult).Tools[0].InputSchema)
	if !strings.Contains(string(b2), `"entity"`) || !strings.Contains(string(b2), `"vendor"`) {
		t.Fatalf("스키마: %s", b2)
	}
}

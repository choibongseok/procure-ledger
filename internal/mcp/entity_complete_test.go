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

// B44 3단계 마무리: 입력 스키마가 없는 도구에도 entity 인자가 알려지고, 배열 응답에도 법인 코드가 붙는다(법인이 둘 이상인 설치만).
func TestMCPEntityCompletion(t *testing.T) {
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entities (code, name, business_no, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', '', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	d := fake.NewBundle().Deps()
	d.DB = db
	p := contract.Principal{UserID: 3, Name: "재무", Role: contract.RoleFinance, Channel: contract.ChannelMCP,
		EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermLedgerRead: {All: true}}}
	ctx := contract.WithPrincipal(context.Background(), p)

	lr := &sdk.ListToolsResult{Tools: []*sdk.Tool{{Name: "no_schema"}}}
	addEntityProperty(lr) // 스키마가 없어도 패닉 없이 entity 를 더한다
	b, _ := json.Marshal(lr.Tools[0].InputSchema)
	if !strings.Contains(string(b), `"entity"`) {
		t.Fatalf("스키마: %s", b)
	}

	cr := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `[{"a":1},{"a":2}]`}}}
	tagEntity(ctx, d, cr, contract.ActiveEntity{ID: contract.DefaultEntityID})
	var out struct {
		Entity string           `json:"entity"`
		Items  []map[string]int `json:"items"`
	}
	if err := json.Unmarshal([]byte(cr.Content[0].(*sdk.TextContent).Text), &out); err != nil || out.Entity != "MAIN" || len(out.Items) != 2 {
		t.Fatalf("배열 응답: %s %v", cr.Content[0].(*sdk.TextContent).Text, err)
	}
	plain := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `재고가 없습니다`}}}
	tagEntity(ctx, d, plain, contract.ActiveEntity{ID: contract.DefaultEntityID})
	if plain.Content[0].(*sdk.TextContent).Text != `재고가 없습니다` {
		t.Fatalf("일반 글은 그대로여야 한다: %s", plain.Content[0].(*sdk.TextContent).Text)
	}
}

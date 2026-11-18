package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

// B48 판정 후속 4: 상대방·품목 접두 조건은 서비스의 "포함" 검색 결과를 후처리로 좁힌다. 서비스의 한 쪽 상한이 접두 일치를 가리면 안 된다.
// 세금계산서는 쪽을 넘겨 가져오므로 "포함"만 하는 210건이 더 최근이어도 오래된 접두 일치 3건이 나온다. 품목은 서비스가 쪽 넘김이 없어 한 번에 200건까지다(한계, 명세 22절).
func TestPrefixNarrowingIsNotHiddenByServicePageLimit(t *testing.T) {
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
	var admin contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&admin.UserID, &role, &admin.DepartmentID); err != nil {
		t.Fatal(err)
	}
	admin.Role, admin.Name, admin.Channel = contract.Role(role), "admin", contract.ChannelMCP
	admin.Perms = contract.NewPermSet(contract.AllPermissions...)
	actx := contract.WithPrincipal(ctx, admin)

	seed := func(seq int, name, date string) {
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO tax_invoices (seq_no, no, direction, tax_type, status, approval_no, issue_date, buyer_name, created_by, created_at, updated_at) VALUES (?,?, 'sale', 'taxable', 'draft', '', ?, ?, ?, 't', 't')`,
			seq, fmt.Sprintf("TI-P-%04d", seq), date, name, admin.UserID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 210; i++ { // 더 최근(작성일 내림차순에서 앞): 포함은 하지만 접두는 아니다
		seed(1000+i, fmt.Sprintf("제XACME-%03d", i), "2026-11-10")
	}
	for i := 0; i < 3; i++ { // 더 오래됨: 접두 일치
		seed(2000+i, fmt.Sprintf("XACME-%d", i), "2026-01-05")
	}
	res, ok := mcptool.BuiltinCatalog(d).Get("tax_invoices")
	if !ok {
		t.Fatal("세금계산서 자원이 없음")
	}
	rows, err := res.List(actx, mcptool.Query{Limit: 50, Conds: []mcptool.Cond{{Field: "counterparty", Op: mcptool.OpPrefix, Value: "XACME-"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("접두 일치 %d건(3건이어야 함, 서비스 쪽 상한 뒤에 가려짐): %v", len(rows), rows)
	}
	for _, r := range rows {
		if n, _ := r["counterparty"].(string); !strings.HasPrefix(n, "XACME-") {
			t.Fatalf("접두 일치가 아닌 행: %v", r)
		}
	}
}

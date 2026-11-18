package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

func setup(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func wantErr(t *testing.T, db *sql.DB, contains, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("%q 오류를 기대했으나 %v (쿼리: %s)", contains, err, q)
	}
}

func seed(t *testing.T, db *sql.DB) {
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (1, '구매팀')`)
	mustExec(t, db, `INSERT INTO users (id, email, name, role, department_id) VALUES
		(1,'staff@x','직원','staff',1),(2,'mgr@x','팀장','manager',1),(3,'fin@x','재무','finance',1)`)
	mustExec(t, db, `INSERT INTO items (id, sku, name) VALUES (1,'A','복사용지')`)
}

func TestApplyIsIdempotent(t *testing.T) {
	db := setup(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 105 { // 0001~0005, 0006_approval_history_withdraw, 0007_delegated_requester_rank, 0008_mcp_rate_and_audit_index, 0009_mcp_user_rate_and_denied_count, 0010_org_chart, 0011_org_approval_line, 0012_snapshot_guards, 0013_approval_forms, 0014_document_numbering, 0015_expense_form_v2, 0016_inbox_receipts, 0017_authority_rules, 0018_doc_revision, 0019_inventory_depth, 0020_stock_count, 0021_stock_count_guards, 0022_purchase_budget, 0023_purchase_quotes, 0024_vendor_rating_indexes, 0025_inventory_valuation, 0026_month_close, 0027_month_close_form, 0028_month_close_guards, 0029_retention_disposal … 0063_notification_termspace, 0064_api_tokens, 0065_change_events, 0066_webhooks, 0067_webhook_coalesce, 0068_connector_runs, 0069_tax_invoices, 0070_tax_asp, 0071_tax_invoice_match, 0072_tax_invoice_feed, 0073_entities, 0074_entity_scope, 0075_entity_columns, 0076_entity_counters_close, 0077_entity_tax_asp, 0078_entity_immutable, 0079_scim_group_scope, 0080_llm_providers, 0081_llm_routes, 0082_llm_usage_route, 0083_eval_runs_suite, 0084_eval_sets, 0085_eval_items, 0086_eval_runs_sets, 0087_knowledge_index, 0088_knowledge_queue, 0089_forms_searchable, 0090_knowledge_uploads, 0091_eval_followup, 0093_mcp_tools, 0094_mcp_tool_rate, 0095_mcp_tool_enable, 0096_ai_cost_base, 0097_ai_cost_rules, 0098_ai_cost_runs, 0099_privacy_attendance_category, 0100_leave_settings_ledger, 0101_leave_requests, 0102_attendance, 0103_org_absence, 0104_stock_valuation_entity, 0105_stock_transfer_entity, 0106_month_close_lines_entity_key
		t.Fatalf("적용 기록 %d", n)
	}
	var rules int
	_ = db.QueryRow(`SELECT COUNT(*) FROM approval_line_rules WHERE form_id = 1`).Scan(&rules)
	if rules != 10 { // 0001 의 3구간 6행 + D10 의 1억 원 이상 구간 4행(0011)
		t.Fatalf("결재선 규칙 시드 %d", rules)
	}
}

func TestAuditLogsAppendOnly(t *testing.T) {
	db := setup(t)
	mustExec(t, db, `INSERT INTO audit_logs (at,actor_id,actor_role,channel,action,target_type,target_id,result,prev_hash,hash)
		VALUES ('2026-01-01T00:00:00Z',1,'staff','web','x','item','1','success','','h1')`)
	wantErr(t, db, "추가 전용", `UPDATE audit_logs SET detail='변조' WHERE seq=1`)
	wantErr(t, db, "추가 전용", `DELETE FROM audit_logs WHERE seq=1`)
}

func TestLedgerSumsAndImmutable(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id) VALUES (1, 10, 'receipt', 3), (1, -3, 'issue', 3)`)
	var onHand int
	if err := db.QueryRow(`SELECT on_hand FROM v_item_stock WHERE item_id=1`).Scan(&onHand); err != nil || onHand != 7 {
		t.Fatalf("원장 합 %d %v", onHand, err)
	}
	wantErr(t, db, "추가 전용", `UPDATE stock_ledger SET qty_delta=100`)
	wantErr(t, db, "추가 전용", `DELETE FROM stock_ledger`)
	wantErr(t, db, "CHECK", `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id) VALUES (1, 0, 'adjust', 3)`)
}

func TestApprovalHistoryImmutableAndNoSelfApproval(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','in_review','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,1,0,1,'submit','2026-01-01T00:00:00Z')`)
	wantErr(t, db, "본인이 올린 요청", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,1,1,1,'approve','x')`)
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,1,1,2,'approve','2026-01-01T01:00:00Z')`)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET comment='x'`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_history`)
}

func TestReceiptCannotExceedPO(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','ordered','x','x')`)
	mustExec(t, db, `INSERT INTO purchase_orders (id,no,pr_id,status,total_amount,created_at) VALUES (1,'PO-1',1,'open',100,'x')`)
	mustExec(t, db, `INSERT INTO po_lines (id,po_id,item_id,quantity,unit_price) VALUES (1,1,1,10,10)`)
	mustExec(t, db, `INSERT INTO receipts (id,po_id,received_by,received_at) VALUES (1,1,3,'x')`)
	mustExec(t, db, `INSERT INTO receipt_lines (receipt_id,po_line_id,item_id,quantity) VALUES (1,1,1,6)`)
	wantErr(t, db, "PO 수량을 넘는", `INSERT INTO receipt_lines (receipt_id,po_line_id,item_id,quantity) VALUES (1,1,1,5)`)
	mustExec(t, db, `INSERT INTO receipt_lines (receipt_id,po_line_id,item_id,quantity) VALUES (1,1,1,4)`)
}

func TestEvalRunsAppendOnly(t *testing.T) {
	db := setup(t)
	mustExec(t, db, `INSERT INTO eval_runs (at,provider,total,passed,pass_rate_pct,threshold_pct,gate_passed)
		VALUES ('2026-01-01T00:00:00Z','mock',10,9,90,80,1)`)
	wantErr(t, db, "추가 전용", `UPDATE eval_runs SET passed=10 WHERE id=1`)
	wantErr(t, db, "추가 전용", `DELETE FROM eval_runs WHERE id=1`)
	var n int
	if err := db.QueryRow(`SELECT passed FROM eval_runs WHERE id=1`).Scan(&n); err != nil || n != 9 {
		t.Fatalf("행이 그대로여야 함: %d %v", n, err)
	}
}

func TestAnchorStampsAppendOnly(t *testing.T) {
	db := setup(t)
	mustExec(t, db, `INSERT INTO audit_anchor_stamps (kind,seq,line_hash,at) VALUES ('stamp',1,'h','2026-01-01T00:00:00Z')`)
	wantErr(t, db, "추가 전용", `UPDATE audit_anchor_stamps SET seq=2 WHERE id=1`)
	wantErr(t, db, "추가 전용", `DELETE FROM audit_anchor_stamps WHERE id=1`)
}

func TestSecretsStoredAsHashesAndRolesChecked(t *testing.T) {
	db := setup(t)
	seed(t, db)
	// B33: users.role 의 CHECK 는 풀렸다(사용자 정의 역할). 알 수 없는 역할 키는 기본 템플릿이 아니라 호환 부여가 생기지 않고 권한이 없다.
	mustExec(t, db, `INSERT INTO users (email,name,role,department_id) VALUES ('g@x','g','ghost',1)`)
	var grants int
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants g JOIN users u ON u.id = g.target_id AND g.target_type = 'user' WHERE u.email = 'g@x'`).Scan(&grants)
	if grants != 0 {
		t.Fatalf("알 수 없는 역할 키에 호환 부여가 생김: %d", grants)
	}
	mustExec(t, db, `INSERT INTO personal_tokens (user_id,name,token_prefix,token_hash,created_at,expires_at) VALUES (1,'t','plp_ab','hash1','a','b')`)
	wantErr(t, db, "UNIQUE", `INSERT INTO personal_tokens (user_id,name,token_prefix,token_hash,created_at,expires_at) VALUES (1,'t2','plp_ab','hash1','a','b')`)
	// 토큰 원문·세션 ID 원문을 담는 컬럼이 없어야 한다.
	for _, table := range []string{"personal_tokens", "sessions"} {
		rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			if c == "token" || c == "raw_token" || c == "session_id" {
				t.Errorf("%s.%s: 원문 저장 컬럼 금지", table, c)
			}
		}
		rows.Close()
	}
}

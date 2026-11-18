-- 0075 (B44 2단계 platform, 5·8.1·15.3절): 법인 소유 표에 entity_id 열을 더하고 조회 색인을 만든다.
--  - 기존 행은 모두 기본 법인(1)이다. ADD COLUMN 은 행을 훑지 않아 큰 표(전표·피드)도 바로 끝난다. 이 기본값 1 은 이관을 위한 것이고
--    새 행을 만드는 코드는 법인을 반드시 명시한다(래칫과 시험이 지킨다). SQLite 는 기본값이 NULL 이 아닌 REFERENCES 열을 더할 수 없어 열만 두고,
--    정합성은 시험(TestEntityColumnsPointAtEntities)이 표 전체로 본다. PG 는 같은 열에 외래 키를 건다.
--  - 재고 원장·전표 줄처럼 큰 표에는 열을 더하지 않는다(창고·부모 전표로 법인을 안다).
--  - 법인 값이 0 인 표가 둘 있다: authority_rules(0 = 모든 법인 공통 규칙), webhook_subscriptions(0 = 모든 법인).
--  - vendors.linked_entity_id: 이 거래처가 그룹의 다른 법인이면 그 법인(0 = 아님). journals.site_id·warehouses.site_id: 사업장(0 = 주사업장).
--  - 감사 로그 색인은 만들지 않는다(erp-admin audit-entity-index, 0074 머리말).
ALTER TABLE journals ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE journals ADD COLUMN site_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE payables ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE payment_requests ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE bank_accounts ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE corporate_cards ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE card_imports ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE card_transactions ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE budgets ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE budget_revisions ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE purchase_requests ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE purchase_orders ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE receipts ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE tax_invoices ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE company_assets ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE import_batches ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE warehouses ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE warehouses ADD COLUMN site_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vendors ADD COLUMN linked_entity_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE authority_rules ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE change_events ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE api_tokens ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE webhook_subscriptions ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_journals_entity_posting ON journals(entity_id, posting_date, status);
CREATE INDEX idx_payables_entity_due ON payables(entity_id, due_date);
CREATE INDEX idx_payment_requests_entity ON payment_requests(entity_id, status);
CREATE INDEX idx_purchase_requests_entity ON purchase_requests(entity_id, status, created_at);
CREATE INDEX idx_purchase_orders_entity ON purchase_orders(entity_id, status);
CREATE INDEX idx_tax_invoices_entity ON tax_invoices(entity_id, direction, issue_date);
CREATE INDEX idx_change_events_entity ON change_events(entity_id, seq);
CREATE INDEX idx_warehouses_entity ON warehouses(entity_id);

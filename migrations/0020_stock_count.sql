-- 0020: 재고 기능 깊이 단계 2(B24 ③, 6절): 재고 실사와 조정 전표(ADJ).
--  1) stock_counts·stock_count_lines: 한 창고·한 기준일의 실사 표. 전산 수량은 만들 때 스냅샷으로 저장한다.
--  2) 조정 전표는 B23 양식 문서 "재고 조정 전표"(ADJ, 시스템 양식)다. 결재선은 구매팀장(조직 코드 DEP-PU 의 조직장) → 재무 2단계 고정.
--     조직 코드는 개발 시드 기준이고 운영은 결재 양식 관리 화면에서 실제 구매팀 코드로 바꾼다.
CREATE TABLE stock_counts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    no           TEXT NOT NULL UNIQUE,
    warehouse_id INTEGER NOT NULL REFERENCES warehouses(id),
    base_date    TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('draft','approving','applied','rejected','no_diff','discarded')),
    doc_id       INTEGER REFERENCES purchase_requests(id),
    reject_reason TEXT NOT NULL DEFAULT '',
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    applied_at   TEXT,
    snapshot_ledger_id INTEGER NOT NULL DEFAULT 0  -- 전산 수량을 잡은 시점의 원장 마지막 번호(기준일 이후 변동 경고 계산용)
);
-- 한 창고에 진행 중(작성 중·결재 중) 실사는 하나다(E6).
CREATE UNIQUE INDEX idx_stock_counts_open ON stock_counts(warehouse_id) WHERE status IN ('draft','approving');
CREATE INDEX idx_stock_counts_doc ON stock_counts(doc_id);

CREATE TABLE stock_count_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    count_id    INTEGER NOT NULL REFERENCES stock_counts(id),
    item_id     INTEGER NOT NULL REFERENCES items(id),
    lot_id      INTEGER REFERENCES lots(id),
    lot_key     INTEGER NOT NULL DEFAULT 0,
    system_qty  INTEGER NOT NULL,
    counted_qty INTEGER CHECK (counted_qty IS NULL OR counted_qty >= 0),
    reason      TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    UNIQUE (count_id, item_id, lot_key)
);

-- 처리가 끝난 실사 표(반영 완료·차이 없음·폐기)는 고칠 수 없고, 반영 완료된 줄도 바뀌지 않는다.
CREATE TRIGGER trg_stock_count_final BEFORE UPDATE ON stock_counts
WHEN OLD.status IN ('applied','no_diff','discarded')
BEGIN SELECT RAISE(ABORT, '끝난 실사 표는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_stock_count_no_delete BEFORE DELETE ON stock_counts
BEGIN SELECT RAISE(ABORT, '실사 표는 지울 수 없습니다'); END;
CREATE TRIGGER trg_stock_count_lines_final BEFORE UPDATE ON stock_count_lines
WHEN (SELECT status FROM stock_counts WHERE id = OLD.count_id) IN ('applied','no_diff','discarded','approving')
BEGIN SELECT RAISE(ABORT, '결재 중이거나 끝난 실사 표의 줄은 고칠 수 없습니다'); END;

INSERT INTO approval_forms (code, category, name, kind, active, required_permission, created_at)
VALUES ('ADJ', 'other', '재고 조정 전표', 'general', 1, 'inventory:manage', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT id, 1, 'published',
 '{"system":true,"fields":[{"key":"count_no","label":"실사 번호","type":"text","required":true,"maxLength":40},{"key":"warehouse","label":"창고","type":"text","required":true,"maxLength":60},{"key":"base_date","label":"기준일","type":"text","required":true,"maxLength":10},{"key":"summary","label":"조정 내용","type":"textarea","required":false,"maxLength":5000}],"body":"","line":{"steps":[{"role":"approve","source":"unit_head","unitCode":"DEP-PU","required":true},{"role":"approve","kind":"finance","source":"org_level","required":true}]},"recipients":{"cc":[],"receive":[]},"numbering":{"pattern":"ADJ-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":5,"dept_limit":[],"settings":{"line_edit":"none","objection_action":"record","amount_field":"","dept_visible":false,"urgent_allowed":false}}',
 strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
 FROM approval_forms WHERE code = 'ADJ';

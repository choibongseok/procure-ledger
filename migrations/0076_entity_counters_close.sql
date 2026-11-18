-- 0076 (B44 2단계 platform, 명세 6·8.3절): 번호 카운터와 월 마감을 법인별로 만든다(키에 entity_id 를 넣어 표를 다시 만든다. 기존 행은 모두 기본 법인 1).
--  - inventory_counters (prefix, year) → (entity_id, prefix, year), document_counters (form_id, scope_unit_id, year) → (entity_id, form_id, scope_unit_id, year).
--    번호 발급은 지금처럼 UPDATE … RETURNING 한 문장이고 행이 법인마다 달라 법인 사이 번호 경합이 없다.
--  - month_closes month → (entity_id, month): 법인마다 따로 마감한다(AC-4408). 마감 사건·스냅샷에도 법인 열을 더한다.
--  - 마감 월 쓰기 잠금 트리거 8개는 법인을 본다: 전표는 자기 entity_id, 재고 쓰기(원장·출고·실사·평가)는 창고의 법인이다. A 법인의 마감이 B 법인의 쓰기를 막지 않는다.
--    (PG 의 월 단위 자문 잠금 키는 법인과 상관없이 월이라 한 달 안에서는 법인끼리 조금 더 기다릴 수 있지만 틀리지는 않는다.)
DROP TRIGGER trg_journals_month_open_ins;
DROP TRIGGER trg_journals_month_open_upd;
DROP TRIGGER trg_ledger_month_open;
DROP TRIGGER trg_issue_month_open;
DROP TRIGGER trg_count_month_open_ins;
DROP TRIGGER trg_count_month_open_upd;
DROP TRIGGER trg_valuation_month_open_upd;
DROP TRIGGER trg_valuation_month_open_del;

CREATE TABLE inventory_counters_new (
    prefix    TEXT NOT NULL,
    year      INTEGER NOT NULL,
    last      INTEGER NOT NULL DEFAULT 0,
    entity_id INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (entity_id, prefix, year)
);
INSERT INTO inventory_counters_new (prefix, year, last, entity_id) SELECT prefix, year, last, 1 FROM inventory_counters;
DROP TABLE inventory_counters;
ALTER TABLE inventory_counters_new RENAME TO inventory_counters;

CREATE TABLE document_counters_new (
    form_id       INTEGER NOT NULL REFERENCES approval_forms(id),
    scope_unit_id INTEGER NOT NULL DEFAULT 0,
    year          INTEGER NOT NULL DEFAULT 0,
    last_seq      INTEGER NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    entity_id     INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (entity_id, form_id, scope_unit_id, year)
);
INSERT INTO document_counters_new (form_id, scope_unit_id, year, last_seq, entity_id) SELECT form_id, scope_unit_id, year, last_seq, 1 FROM document_counters;
DROP TABLE document_counters;
ALTER TABLE document_counters_new RENAME TO document_counters;

CREATE TABLE month_closes_new (
    month           TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    status          TEXT NOT NULL CHECK (status IN ('open','closed')),
    version         INTEGER NOT NULL CHECK (version >= 1),
    closed_by       INTEGER REFERENCES users(id),
    closed_at       TEXT,
    released_at     TEXT,
    released_doc_id INTEGER,
    entity_id       INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (entity_id, month)
);
INSERT INTO month_closes_new (month, status, version, closed_by, closed_at, released_at, released_doc_id, entity_id)
    SELECT month, status, version, closed_by, closed_at, released_at, released_doc_id, 1 FROM month_closes;
DROP TABLE month_closes;
ALTER TABLE month_closes_new RENAME TO month_closes;

ALTER TABLE month_close_events ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE month_close_lines ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;

CREATE TRIGGER trg_month_closes_no_delete BEFORE DELETE ON month_closes
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 지울 수 없습니다'); END;

CREATE TRIGGER trg_month_closes_insert_guard BEFORE INSERT ON month_closes
WHEN NOT (NEW.status = 'closed' AND NEW.version = 1 AND NEW.closed_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 처음 마감(버전 1)으로만 만들 수 있습니다'); END;

CREATE TRIGGER trg_month_closes_update_guard BEFORE UPDATE ON month_closes
WHEN NOT (
  NEW.entity_id = OLD.entity_id AND NEW.month = OLD.month AND (
    (OLD.status = 'open' AND NEW.status = 'closed' AND NEW.version = OLD.version + 1 AND NEW.closed_at IS NOT NULL)
    OR
    (OLD.status = 'closed' AND NEW.status = 'open' AND NEW.version = OLD.version AND NEW.closed_by IS OLD.closed_by AND NEW.closed_at IS OLD.closed_at AND NEW.released_at IS NOT NULL)
  )
)
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 마감·해제 절차로만 바꿀 수 있습니다'); END;

-- 마감된 월 쓰기 잠금(방어선): 그 쓰기의 법인이 마감한 월이면 거부한다.
CREATE TRIGGER trg_journals_month_open_ins BEFORE INSERT ON journals
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_journals_month_open_upd BEFORE UPDATE OF status, posting_date ON journals
WHEN (NEW.status IN ('requested','approved') OR NEW.posting_date <> OLD.posting_date)
 AND EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_ledger_month_open BEFORE INSERT ON stock_ledger
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(COALESCE(NEW.book_date, date(NEW.created_at, '+9 hours')), 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_issue_month_open BEFORE INSERT ON issue_slips
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.issue_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_count_month_open_ins BEFORE INSERT ON stock_counts
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.base_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_count_month_open_upd BEFORE UPDATE OF base_date ON stock_counts
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.base_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_valuation_month_open_upd BEFORE UPDATE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = OLD.ledger_id), 1) AND status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_valuation_month_open_del BEFORE DELETE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = OLD.ledger_id), 1) AND status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

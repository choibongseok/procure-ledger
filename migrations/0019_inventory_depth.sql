-- 0019: 재고 기능 깊이 단계 1(B24 ①②, 6절): 창고, 출고, 창고 이동.
--  1) warehouses: 재고가 놓이는 곳. 시드 WH-01 본사 창고. 기존 원장 행은 전부 여기에 속한다.
--  2) stock_ledger 재구성: warehouse_id(기본 1) 추가, reason 에 transfer_out·transfer_in 추가. 원장은 계속 추가 전용이다.
--  3) 출고 전표(issue_slips·lines), 이동 전표(stock_transfers·lines), 전표 번호 카운터(inventory_counters, 연도별 순번).
--  4) v_wh_lot_stock: 창고별 로트 재고. v_item_stock·v_lot_stock 은 창고 무관 합계로 그대로 유지한다(경보·MCP 도구 숫자 불변).
CREATE TABLE warehouses (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT NOT NULL UNIQUE CHECK (length(code) BETWEEN 2 AND 20),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),
    location   TEXT NOT NULL DEFAULT '' CHECK (length(location) <= 100),
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO warehouses (id, code, name, location, active, created_at, updated_at)
VALUES (1, 'WH-01', '본사 창고', '', 1, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));

DROP VIEW v_item_stock;
DROP VIEW v_lot_stock;

CREATE TABLE stock_ledger_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id      INTEGER NOT NULL REFERENCES items(id),
    lot_id       INTEGER REFERENCES lots(id),
    qty_delta    INTEGER NOT NULL CHECK (qty_delta <> 0),
    reason       TEXT NOT NULL CHECK (reason IN ('receipt','issue','adjust','transfer_out','transfer_in')),
    ref_type     TEXT NOT NULL DEFAULT '',
    ref_id       INTEGER NOT NULL DEFAULT 0,
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    warehouse_id INTEGER NOT NULL DEFAULT 1 REFERENCES warehouses(id)
);
INSERT INTO stock_ledger_new (id, item_id, lot_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id)
SELECT id, item_id, lot_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, 1 FROM stock_ledger;
DROP TABLE stock_ledger;
ALTER TABLE stock_ledger_new RENAME TO stock_ledger;
CREATE INDEX idx_ledger_item ON stock_ledger(item_id);
CREATE INDEX idx_ledger_wh_item ON stock_ledger(warehouse_id, item_id, lot_id);

CREATE TRIGGER trg_ledger_no_update BEFORE UPDATE ON stock_ledger
BEGIN SELECT RAISE(ABORT, 'stock_ledger 는 추가 전용입니다'); END;
CREATE TRIGGER trg_ledger_no_delete BEFORE DELETE ON stock_ledger
BEGIN SELECT RAISE(ABORT, 'stock_ledger 는 추가 전용입니다'); END;
-- 사용 중지된 창고에는 입고·출고 외의 들어오는 기록(입고·이동 입고)을 넣을 수 없다.
CREATE TRIGGER trg_ledger_warehouse_active BEFORE INSERT ON stock_ledger
WHEN NEW.reason IN ('receipt','transfer_in') AND (SELECT active FROM warehouses WHERE id = NEW.warehouse_id) <> 1
BEGIN SELECT RAISE(ABORT, '사용 중지된 창고에는 들일 수 없습니다'); END;

CREATE VIEW v_item_stock AS
SELECT i.id AS item_id, COALESCE(SUM(l.qty_delta), 0) AS on_hand
FROM items i LEFT JOIN stock_ledger l ON l.item_id = i.id
GROUP BY i.id;

CREATE VIEW v_lot_stock AS
SELECT l.item_id, l.id AS lot_id, l.lot_no, l.expiry_date, COALESCE(SUM(s.qty_delta), 0) AS on_hand
FROM lots l LEFT JOIN stock_ledger s ON s.lot_id = l.id
GROUP BY l.id;

-- 창고별 재고(로트 없는 품목은 lot_id 0).
CREATE VIEW v_wh_lot_stock AS
SELECT warehouse_id, item_id, COALESCE(lot_id, 0) AS lot_id, SUM(qty_delta) AS on_hand
FROM stock_ledger
GROUP BY warehouse_id, item_id, COALESCE(lot_id, 0);

CREATE TABLE inventory_counters (
    prefix TEXT NOT NULL,
    year   INTEGER NOT NULL,
    last   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (prefix, year)
);

CREATE TABLE issue_slips (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    no           TEXT NOT NULL UNIQUE,
    department_id INTEGER NOT NULL REFERENCES departments(id),
    warehouse_id INTEGER NOT NULL REFERENCES warehouses(id),
    issue_date   TEXT NOT NULL,
    method       TEXT NOT NULL CHECK (method IN ('fifo','expiry','manual')),
    memo         TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL
);
CREATE TABLE issue_slip_lines (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    slip_id INTEGER NOT NULL REFERENCES issue_slips(id),
    item_id INTEGER NOT NULL REFERENCES items(id),
    lot_id  INTEGER REFERENCES lots(id),
    qty     INTEGER NOT NULL CHECK (qty > 0)
);
CREATE INDEX idx_issue_lines_slip ON issue_slip_lines(slip_id);

CREATE TABLE stock_transfers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    no         TEXT NOT NULL UNIQUE,
    from_wh_id INTEGER NOT NULL REFERENCES warehouses(id),
    to_wh_id   INTEGER NOT NULL REFERENCES warehouses(id),
    move_date  TEXT NOT NULL,
    memo       TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    actor_id   INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    CHECK (from_wh_id <> to_wh_id)
);
CREATE TABLE stock_transfer_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    transfer_id INTEGER NOT NULL REFERENCES stock_transfers(id),
    item_id     INTEGER NOT NULL REFERENCES items(id),
    lot_id      INTEGER REFERENCES lots(id),
    qty         INTEGER NOT NULL CHECK (qty > 0)
);
CREATE INDEX idx_transfer_lines_tr ON stock_transfer_lines(transfer_id);

-- 전표는 추가 전용이다(수정·삭제 없음).
CREATE TRIGGER trg_issue_slips_fixed BEFORE UPDATE ON issue_slips BEGIN SELECT RAISE(ABORT, '출고 전표는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_issue_slips_nodel BEFORE DELETE ON issue_slips BEGIN SELECT RAISE(ABORT, '출고 전표는 지울 수 없습니다'); END;
CREATE TRIGGER trg_transfers_fixed BEFORE UPDATE ON stock_transfers BEGIN SELECT RAISE(ABORT, '이동 전표는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_transfers_nodel BEFORE DELETE ON stock_transfers BEGIN SELECT RAISE(ABORT, '이동 전표는 지울 수 없습니다'); END;

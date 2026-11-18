-- 0106 (B51 판정 후속): 월말 스냅샷 줄의 유일 키에 법인을 넣는다. 옛 키 (월, 버전, 품목, 창고)는 법인을 몰라서,
-- B44 시기에 다른 법인 창고 줄이 마감한 법인의 스냅샷에 섞여 들어간 옛 스냅샷이 있으면 그 창고의 법인이 같은 달을 마감할 때 "이미 등록된 값입니다" 로 거부됐다.
-- SQLite 는 제약을 바꿀 수 없어 표를 다시 만든다. 기존 행(번호 포함)·추가 전용 트리거를 그대로 옮긴다.
CREATE TABLE month_close_lines_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    month           TEXT NOT NULL,
    version         INTEGER NOT NULL,
    item_id         INTEGER NOT NULL REFERENCES items(id),
    warehouse_id    INTEGER NOT NULL REFERENCES warehouses(id),
    open_qty        INTEGER NOT NULL, open_amount     INTEGER NOT NULL,
    in_qty          INTEGER NOT NULL, in_amount       INTEGER NOT NULL,
    out_qty         INTEGER NOT NULL, out_amount      INTEGER NOT NULL,
    transfer_qty    INTEGER NOT NULL, transfer_amount INTEGER NOT NULL,
    adjust_qty      INTEGER NOT NULL, adjust_amount   INTEGER NOT NULL,
    close_qty       INTEGER NOT NULL, close_amount    INTEGER NOT NULL,
    avg_cost        INTEGER NOT NULL,
    entity_id       INTEGER NOT NULL DEFAULT 1,
    UNIQUE (entity_id, month, version, item_id, warehouse_id)
);
INSERT INTO month_close_lines_new (id, month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id)
    SELECT id, month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id FROM month_close_lines;
DROP TABLE month_close_lines;
ALTER TABLE month_close_lines_new RENAME TO month_close_lines;
CREATE TRIGGER trg_month_close_lines_no_update BEFORE UPDATE ON month_close_lines BEGIN SELECT RAISE(ABORT, '월말 스냅샷은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_month_close_lines_no_delete BEFORE DELETE ON month_close_lines BEGIN SELECT RAISE(ABORT, '월말 스냅샷은 지울 수 없습니다'); END;

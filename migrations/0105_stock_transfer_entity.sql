-- 0105 (B51 4단계, 명세 7·11절): 법인 간 재고 이동(내부 거래)의 구조.
--  - stock_transfers.from_entity_id / to_entity_id: 이동 전표의 출발·도착 법인. 두 값이 다르면 법인 간 이동이다. 기존 전표는 두 창고의 법인으로 채운다(대부분 기본 법인 1).
--  - stock_transfer_lines.cost_amount: 법인 간 이동 줄의 동결 금액(원, 절대값). 이동을 기록하는 시점의 출발 법인 이동평균으로 한 번 계산해 저장하고(D5) 이후 소급 재계산이 바꾸지 않는다. 같은 법인 이동은 0.
--  - warehouses.entity_id 는 만든 뒤 바꿀 수 없다(D15). 평가·마감 이력이 창고의 법인에 매여 있다.
-- 열을 채우는 UPDATE 가 이동 전표 수정 금지 트리거(0019)에 걸리므로 트리거를 지웠다가 다시 만든다.
DROP TRIGGER trg_transfers_fixed;

ALTER TABLE stock_transfers ADD COLUMN from_entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE stock_transfers ADD COLUMN to_entity_id INTEGER NOT NULL DEFAULT 1;

-- 기본 법인이 아닌 창고가 낀 전표만 고친다(보통 0 건).
UPDATE stock_transfers
   SET from_entity_id = (SELECT w.entity_id FROM warehouses w WHERE w.id = stock_transfers.from_wh_id),
       to_entity_id   = (SELECT w.entity_id FROM warehouses w WHERE w.id = stock_transfers.to_wh_id)
 WHERE from_wh_id IN (SELECT id FROM warehouses WHERE entity_id <> 1)
    OR to_wh_id IN (SELECT id FROM warehouses WHERE entity_id <> 1);

CREATE TRIGGER trg_transfers_fixed BEFORE UPDATE ON stock_transfers BEGIN SELECT RAISE(ABORT, '이동 전표는 고칠 수 없습니다'); END;

CREATE INDEX idx_transfers_from_entity ON stock_transfers(from_entity_id, id);
CREATE INDEX idx_transfers_to_entity ON stock_transfers(to_entity_id, id);

ALTER TABLE stock_transfer_lines ADD COLUMN cost_amount INTEGER NOT NULL DEFAULT 0 CHECK (cost_amount >= 0);

CREATE TRIGGER trg_warehouses_entity_immutable BEFORE UPDATE OF entity_id ON warehouses
WHEN NEW.entity_id <> OLD.entity_id
BEGIN SELECT RAISE(ABORT, '창고의 법인은 바꿀 수 없습니다'); END;

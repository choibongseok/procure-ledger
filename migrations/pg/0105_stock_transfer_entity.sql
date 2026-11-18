-- 0105 (B51 4단계): SQLite 0105_stock_transfer_entity.sql 의 짝이다. 이동 전표에 출발·도착 법인, 이동 줄에 동결 금액, 창고의 법인 변경 금지.
-- 열을 채우는 UPDATE 동안만 이동 전표 수정 금지 트리거를 끈다(같은 트랜잭션 안이라 밖에서는 끈 순간이 보이지 않는다).
ALTER TABLE stock_transfers DISABLE TRIGGER trg_transfers_fixed;

ALTER TABLE stock_transfers ADD COLUMN from_entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE stock_transfers ADD COLUMN to_entity_id BIGINT NOT NULL DEFAULT 1;

UPDATE stock_transfers
   SET from_entity_id = (SELECT w.entity_id FROM warehouses w WHERE w.id = stock_transfers.from_wh_id),
       to_entity_id   = (SELECT w.entity_id FROM warehouses w WHERE w.id = stock_transfers.to_wh_id)
 WHERE from_wh_id IN (SELECT id FROM warehouses WHERE entity_id <> 1)
    OR to_wh_id IN (SELECT id FROM warehouses WHERE entity_id <> 1);

ALTER TABLE stock_transfers ENABLE TRIGGER trg_transfers_fixed;

CREATE INDEX idx_transfers_from_entity ON stock_transfers(from_entity_id, id);
CREATE INDEX idx_transfers_to_entity ON stock_transfers(to_entity_id, id);

ALTER TABLE stock_transfer_lines ADD COLUMN cost_amount BIGINT NOT NULL DEFAULT 0 CHECK (cost_amount >= 0);

CREATE FUNCTION trg_warehouses_entity_immutable_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.entity_id <> OLD.entity_id THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '창고의 법인은 바꿀 수 없습니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_warehouses_entity_immutable BEFORE UPDATE OF entity_id ON warehouses FOR EACH ROW EXECUTE FUNCTION trg_warehouses_entity_immutable_fn();

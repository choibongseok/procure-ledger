-- 0104 (B51 1단계 platform): SQLite 0104_stock_valuation_entity.sql 의 짝이다. 평가 줄에 법인(entity_id)·기준 표시(entity_basis)를 더하고 마감 월 잠금을 평가 줄의 법인으로 판정한다.
-- 열을 채우는 UPDATE 가 닫힌 달 줄에도 닿으므로 그동안만 갱신 트리거를 끈다(같은 트랜잭션 안이라 밖에서는 끈 순간이 보이지 않는다).
ALTER TABLE stock_valuation DISABLE TRIGGER trg_valuation_month_open_upd;

ALTER TABLE stock_valuation ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE stock_valuation ADD COLUMN entity_basis BIGINT NOT NULL DEFAULT 1 CHECK (entity_basis IN (0,1));

UPDATE stock_valuation
   SET entity_id = (SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = stock_valuation.ledger_id)
 WHERE ledger_id IN (SELECT l.id FROM stock_ledger l WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE entity_id <> 1));

UPDATE stock_valuation
   SET entity_basis = 0
 WHERE item_id IN (
        SELECT x.item_id
          FROM (SELECT DISTINCT l.item_id FROM stock_ledger l WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE entity_id <> 1)) x
         WHERE (SELECT COUNT(DISTINCT w.entity_id) FROM stock_ledger l2 JOIN warehouses w ON w.id = l2.warehouse_id WHERE l2.item_id = x.item_id) > 1);

ALTER TABLE stock_valuation ENABLE TRIGGER trg_valuation_month_open_upd;

CREATE INDEX idx_valuation_entity_item_book ON stock_valuation(entity_id, item_id, book_date, ledger_id);

CREATE OR REPLACE FUNCTION trg_valuation_month_open_upd_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(OLD.book_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = OLD.entity_id AND status = 'closed' AND month = substr(OLD.book_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_valuation_month_open_del_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(OLD.book_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = OLD.entity_id AND status = 'closed' AND month = substr(OLD.book_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN OLD;
END
$fn$;

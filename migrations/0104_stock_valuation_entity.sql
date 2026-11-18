-- 0104 (B51 1단계 platform, 명세 4·5·11절): 재고 평가 줄(stock_valuation)에 법인을 붙인다. 평가 키는 (법인, 품목)이다(D1·D2).
--  - entity_id: 그 평가 줄의 법인. 원장 줄의 창고가 속한 법인이다. 기본값 1 은 이관용이고 새 줄은 법인을 명시해 쓴다(외래 키는 0075 와 같은 이유로 두지 않는다).
--  - entity_basis: 1 = 잔량·잔액·평균 열이 (법인, 품목) 기준, 0 = 이관 전 품목 전체 기준 값(여러 법인에 원장이 걸친 품목의 기존 줄만 0).
--    단일 법인에서 일어난 기록은 값 열을 한 글자도 바꾸지 않는다. 다법인 품목의 법인 기준 재계산은 2단계(부팅 때 한 번)가 한다.
--  - 마감 월 쓰기 잠금 트리거 2개는 평가 줄의 entity_id 로 판정한다(0076 은 원장→창고 조인이었다). 열을 채우는 UPDATE 가 닫힌 달 줄에도 닿으므로 트리거를 지웠다가 다시 만든다.
DROP TRIGGER trg_valuation_month_open_upd;
DROP TRIGGER trg_valuation_month_open_del;

ALTER TABLE stock_valuation ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE stock_valuation ADD COLUMN entity_basis INTEGER NOT NULL DEFAULT 1 CHECK (entity_basis IN (0,1));

-- 기본 법인이 아닌 창고의 줄만 고친다(보통 0 건이라 큰 표를 훑지 않는다).
UPDATE stock_valuation
   SET entity_id = (SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = stock_valuation.ledger_id)
 WHERE ledger_id IN (SELECT l.id FROM stock_ledger l WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE entity_id <> 1));

-- 원장이 둘 이상의 법인 창고에 걸친 품목의 기존 줄은 품목 전체 기준 값이다.
UPDATE stock_valuation
   SET entity_basis = 0
 WHERE item_id IN (
        SELECT x.item_id
          FROM (SELECT DISTINCT l.item_id FROM stock_ledger l WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE entity_id <> 1)) x
         WHERE (SELECT COUNT(DISTINCT w.entity_id) FROM stock_ledger l2 JOIN warehouses w ON w.id = l2.warehouse_id WHERE l2.item_id = x.item_id) > 1);

CREATE INDEX idx_valuation_entity_item_book ON stock_valuation(entity_id, item_id, book_date, ledger_id);

CREATE TRIGGER trg_valuation_month_open_upd BEFORE UPDATE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = OLD.entity_id AND status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_valuation_month_open_del BEFORE DELETE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = OLD.entity_id AND status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

-- 0025: 월 마감과 재고 평가 단계 1(B27 ①, 3.1·6절): 이동평균 단가와 재고 금액, 원장 행의 장부일.
--  1) stock_ledger.book_date: 장부일(KST 날짜). 새 행만 채우고(출고 전표는 출고일, 나머지는 기록한 날) 기존 행은 NULL 이며 date(created_at,'+9 hours') 로 읽는다.
--     원장은 계속 추가 전용이다(수정·삭제 트리거는 그대로, 칸 추가는 기존 행을 바꾸지 않는다).
--  2) stock_valuation: 원장 행당 한 줄의 평가(파생 표). 단가·금액 증감·잔량·잔액 금액·평균 단가. 소급 출고가 들어오면 뒤쪽 줄을 다시 계산하므로 갱신할 수 있다
--     (마감된 달의 줄을 막는 트리거는 월 마감 단계에서 더한다). 원장 번호당 한 줄이고 평가 줄이 없는 새 원장 행은 만들어지지 않는다.
ALTER TABLE stock_ledger ADD COLUMN book_date TEXT CHECK (book_date IS NULL OR book_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]');

CREATE TABLE stock_valuation (
    ledger_id    INTEGER PRIMARY KEY REFERENCES stock_ledger(id),
    item_id      INTEGER NOT NULL REFERENCES items(id),
    book_date    TEXT NOT NULL,
    in_unit_cost INTEGER,                       -- 입고 행의 발주 단가(원). 소급 재계산에 다시 쓴다
    unit_cost    INTEGER NOT NULL DEFAULT 0,    -- 이 줄의 단가(금액 ÷ 수량, 반올림)
    amount       INTEGER NOT NULL,              -- 금액 증감(부호 있음, 원)
    bal_qty      INTEGER NOT NULL,              -- 이 줄 뒤 품목 전체 잔량
    bal_amount   INTEGER NOT NULL,              -- 이 줄 뒤 품목 전체 잔액 금액(정본)
    avg_cost     INTEGER NOT NULL DEFAULT 0,    -- 이 줄 뒤 평균 단가(잔량이 0 이면 직전 값을 이어 쓴다)
    warned       INTEGER NOT NULL DEFAULT 0 CHECK (warned IN (0,1)) -- 평균 단가를 몰라 0 원으로 평가한 조정
);
CREATE INDEX idx_valuation_item_book ON stock_valuation(item_id, book_date, ledger_id);

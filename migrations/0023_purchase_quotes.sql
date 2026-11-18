-- 0023: 구매 기능 깊이 단계 2(B25 ②, 3.2·6절): 견적서 첨부와 거래처별 비교.
--  1) purchase_quotes: 구매요청에 붙는 견적서(요청당 10건, 거래처 연결된 건은 거래처당 1건). 증빙 documents 와 분리한다(D8).
--     원문 텍스트(text_content)는 응답에 내보내지 않는다. 요청이 작성 중·반려일 때만 바꿀 수 있고 상신한 요청은 트리거가 막는다.
--  2) purchase_requests: 선택한 견적(quote_id)과 최저가가 아닌 선택의 사유(quote_reason).
CREATE TABLE purchase_quotes (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id            INTEGER NOT NULL REFERENCES purchase_requests(id),
    vendor_id        INTEGER REFERENCES vendors(id),
    supplier         TEXT NOT NULL DEFAULT '',
    filename         TEXT NOT NULL,
    content_type     TEXT NOT NULL DEFAULT '',
    sha256           TEXT NOT NULL DEFAULT '',
    text_content     TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL CHECK (status IN ('read','unreadable')),
    edited           INTEGER NOT NULL DEFAULT 0 CHECK (edited IN (0,1)),
    lines_json       TEXT NOT NULL DEFAULT '[]',
    total_amount     INTEGER NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
    doc_total_amount INTEGER,
    lead_days        INTEGER CHECK (lead_days IS NULL OR lead_days >= 0),
    lead_text        TEXT NOT NULL DEFAULT '',
    warnings_json    TEXT NOT NULL DEFAULT '[]',
    extraction_json  TEXT NOT NULL DEFAULT '',
    uploaded_by      INTEGER NOT NULL REFERENCES users(id),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX uq_purchase_quotes_vendor ON purchase_quotes(pr_id, vendor_id) WHERE vendor_id IS NOT NULL;
CREATE INDEX idx_purchase_quotes_pr ON purchase_quotes(pr_id, id);

CREATE TRIGGER trg_purchase_quotes_editable_ins BEFORE INSERT ON purchase_quotes
WHEN (SELECT status FROM purchase_requests WHERE id = NEW.pr_id) NOT IN ('draft','rejected')
BEGIN SELECT RAISE(ABORT, '상신한 구매요청의 견적서는 더할 수 없습니다'); END;
CREATE TRIGGER trg_purchase_quotes_editable_upd BEFORE UPDATE ON purchase_quotes
WHEN (SELECT status FROM purchase_requests WHERE id = OLD.pr_id) NOT IN ('draft','rejected')
BEGIN SELECT RAISE(ABORT, '상신한 구매요청의 견적서는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_purchase_quotes_editable_del BEFORE DELETE ON purchase_quotes
WHEN (SELECT status FROM purchase_requests WHERE id = OLD.pr_id) NOT IN ('draft','rejected')
BEGIN SELECT RAISE(ABORT, '상신한 구매요청의 견적서는 지울 수 없습니다'); END;

ALTER TABLE purchase_requests ADD COLUMN quote_id INTEGER REFERENCES purchase_quotes(id);
ALTER TABLE purchase_requests ADD COLUMN quote_reason TEXT NOT NULL DEFAULT '';

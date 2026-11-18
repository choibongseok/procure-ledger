-- B31 단계 2: 전표 대기(journal_queue). 자동 전표를 만들지 못한 증빙 확정·지출 결의서 승인을 사유와 함께 남기고, 재무가 "다시 만들기·수동 입력·전표 불필요"로 처리한다.
--  - 같은 출처(source_type, source_id)에는 대기 행이 하나다. 처리된 행(created·manual·skipped)은 고칠 수 없고 지울 수 없다.
--  - 이 마이그레이션은 전표를 만들지 않는다. 도입 전에 일치로 확정된 판정은 "도입 전 확정" 사유의 대기 행으로만 올린다(D7).
-- 금액은 정수 원이고 시각은 RFC3339 UTC 문자열이다.

CREATE TABLE journal_queue (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT NOT NULL CHECK (source_type IN ('verdict','approval_doc')),
    source_id   INTEGER NOT NULL,
    reason      TEXT NOT NULL,
    reason_text TEXT NOT NULL,
    po_id       INTEGER,
    receipt_id  INTEGER,
    vendor_id   INTEGER,
    amount      INTEGER,            -- 입고 금액(공급가액) 또는 결의서 금액 후보. 모르면 NULL
    doc_total   INTEGER,            -- 증빙 문서 합계(추출 값이 없으면 NULL: 다시 만들 때 합계 비교를 건너뛴다)
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','created','manual','skipped')),
    journal_id  INTEGER REFERENCES journals(id),
    handled_by  INTEGER REFERENCES users(id),
    handled_at  TEXT,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    UNIQUE (source_type, source_id)
);
CREATE INDEX idx_journal_queue_status ON journal_queue(status, id);

CREATE TRIGGER trg_journal_queue_no_delete BEFORE DELETE ON journal_queue BEGIN SELECT RAISE(ABORT, '전표 대기 기록은 지울 수 없습니다'); END;
CREATE TRIGGER trg_journal_queue_final BEFORE UPDATE ON journal_queue
WHEN OLD.status <> 'pending'
BEGIN SELECT RAISE(ABORT, '처리된 전표 대기 기록은 고칠 수 없습니다'); END;

-- 도입 전에 일치로 확정된 판정은 전표 대기에 올린다. 전표는 만들지 않는다.
INSERT INTO journal_queue (source_type, source_id, reason, reason_text, po_id, receipt_id, status, created_at)
SELECT 'verdict', v.id, 'pre_adoption', '도입 전 확정된 증빙입니다. 전기일을 정해 전표를 만들거나 기초 전표로 처리하세요.', v.po_id, v.receipt_id, 'pending', strftime('%Y-%m-%dT%H:%M:%SZ','now')
  FROM document_verdicts v
 WHERE v.confirmed_outcome = 'match';

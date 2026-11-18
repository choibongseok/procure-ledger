-- migrate:fk-off
-- 0072 (B43 3단계, 4절·13절): 변경 피드 자원 'tax_invoice', 전표 출처 'tax_invoice'(D7).
--  - change_events.resource CHECK 에 'tax_invoice' 추가: CHECK 는 바꿀 수 없어 표를 새로 만들어 옮긴다(0058·0064 와 같은 방식). 행·seq 는 그대로 복사하고
--    AUTOINCREMENT 시퀀스는 복사한 최대 seq 로 이어진다. 추가 전용 트리거와 색인은 새 표에 다시 만든다.
--  - journals.source_type CHECK 에 'tax_invoice' 추가(설정 taxinvoice.auto_journal 을 켰을 때 전자 세금계산서 확정이 만드는 임시 매입 전표의 출처): 0064 와 같은 방식으로
--    표를 새로 만들고(줄 표 트리거를 내렸다 다시 만든다) fk-off 지시로 외래키 검사를 끈 채 적용한 뒤 foreign_key_check 로 확인한다. 정의는 0064 와 같고 CHECK 한 값만 늘었다.

-- ---- change_events ----
CREATE TABLE change_events_new (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT NOT NULL,
    resource      TEXT NOT NULL CHECK (resource IN ('item','vendor','purchase_order','receipt','journal','payable','tax_invoice')),
    object_id     INTEGER NOT NULL,
    op            TEXT NOT NULL CHECK (op IN ('created','updated','status_changed','canceled')),
    status        TEXT NOT NULL DEFAULT '',
    actor_channel TEXT NOT NULL DEFAULT 'web' CHECK (actor_channel IN ('web','mcp','llmgw','cli','scim','api'))
);
INSERT INTO change_events_new (seq, at, resource, object_id, op, status, actor_channel) SELECT seq, at, resource, object_id, op, status, actor_channel FROM change_events;
UPDATE sqlite_sequence SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'change_events') WHERE name = 'change_events_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'change_events');
DROP TABLE change_events;
ALTER TABLE change_events_new RENAME TO change_events;
CREATE INDEX idx_change_events_object ON change_events(resource, object_id, seq);
CREATE TRIGGER trg_change_events_no_update BEFORE UPDATE ON change_events
BEGIN SELECT RAISE(ABORT, 'change_events 는 추가 전용입니다'); END;
CREATE TRIGGER trg_change_events_no_delete BEFORE DELETE ON change_events
BEGIN SELECT RAISE(ABORT, 'change_events 는 추가 전용입니다'); END;

-- ---- journals ----
-- 줄 표의 트리거가 journals 를 참조하므로 표 이름을 바꾸는 동안(옛 표를 지운 뒤 새 표 이름을 바꾸는 사이) 이름 해석이 실패한다: 먼저 지우고 끝에 같은 정의로 다시 만든다.
DROP TRIGGER trg_journal_lines_del;
DROP TRIGGER trg_journal_lines_ins;
DROP TRIGGER trg_journal_lines_upd;
CREATE TABLE journals_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    no            TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL CHECK (kind IN ('general','auto_purchase','auto_expense','auto_payment','auto_card','reversal','opening')),
    status        TEXT NOT NULL CHECK (status IN ('draft','requested','approved','canceled')),
    posting_date  TEXT NOT NULL CHECK (posting_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    trade_date    TEXT NOT NULL CHECK (trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    memo          TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    source_type   TEXT NOT NULL DEFAULT '' CHECK (source_type IN ('','verdict','approval_doc','payment','card_txn','api','tax_invoice')),
    source_id     INTEGER NOT NULL DEFAULT 0,
    reverses_id   INTEGER REFERENCES journals(id),
    doc_id        INTEGER REFERENCES purchase_requests(id),
    total_amount  INTEGER NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
    created_by    INTEGER NOT NULL REFERENCES users(id),
    created_at    TEXT NOT NULL,
    approved_by   INTEGER REFERENCES users(id),
    approved_at   TEXT,
    canceled_by   INTEGER REFERENCES users(id),
    canceled_at   TEXT,
    cancel_reason TEXT NOT NULL DEFAULT ''
);
INSERT INTO journals_new (id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, reverses_id, doc_id, total_amount, created_by, created_at, approved_by, approved_at, canceled_by, canceled_at, cancel_reason)
    SELECT id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, reverses_id, doc_id, total_amount, created_by, created_at, approved_by, approved_at, canceled_by, canceled_at, cancel_reason FROM journals ORDER BY id;
UPDATE sqlite_sequence SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'journals') WHERE name = 'journals_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'journals');
DROP TABLE journals;
ALTER TABLE journals_new RENAME TO journals;
CREATE UNIQUE INDEX idx_journals_source ON journals(source_type, source_id) WHERE source_type <> '' AND kind IN ('auto_purchase','auto_expense','auto_payment','auto_card') AND status <> 'canceled';
CREATE UNIQUE INDEX idx_journals_reverses ON journals(reverses_id) WHERE reverses_id IS NOT NULL AND status <> 'canceled';
CREATE INDEX idx_journals_posting ON journals(posting_date, status);
CREATE INDEX idx_journals_doc ON journals(doc_id);
CREATE TRIGGER trg_journals_balanced BEFORE UPDATE OF status ON journals
WHEN NEW.status IN ('requested','approved') AND NEW.status <> OLD.status AND (
     (SELECT COALESCE(SUM(CASE side WHEN 'D' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  <> (SELECT COALESCE(SUM(CASE side WHEN 'C' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  OR (SELECT COUNT(*) FROM journal_lines WHERE journal_id = NEW.id) < 2)
BEGIN SELECT RAISE(ABORT, '차변 합계와 대변 합계가 같지 않습니다'); END;
CREATE TRIGGER trg_journals_final BEFORE UPDATE ON journals
WHEN OLD.status = 'approved' OR OLD.status = 'canceled'
BEGIN SELECT RAISE(ABORT, '승인된 전표는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_journals_month_open_ins BEFORE INSERT ON journals
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_journals_month_open_upd BEFORE UPDATE OF status, posting_date ON journals
WHEN (NEW.status IN ('requested','approved') OR NEW.posting_date <> OLD.posting_date)
 AND EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_journals_no_delete BEFORE DELETE ON journals BEGIN SELECT RAISE(ABORT, '전표는 지울 수 없습니다. 취소하거나 역분개하세요'); END;
CREATE TRIGGER trg_journals_transition BEFORE UPDATE OF status ON journals
WHEN NEW.status <> OLD.status AND NOT (
     (OLD.status = 'draft' AND NEW.status IN ('requested','canceled'))
  OR (OLD.status = 'draft' AND NEW.status = 'approved' AND NEW.kind = 'auto_payment')
  OR (OLD.status = 'requested' AND NEW.status IN ('approved','draft','canceled')))
BEGIN SELECT RAISE(ABORT, '허용되지 않는 전표 상태 변경입니다'); END;

CREATE TRIGGER trg_journal_lines_del BEFORE DELETE ON journal_lines
WHEN (SELECT status FROM journals WHERE id = OLD.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_journal_lines_ins BEFORE INSERT ON journal_lines
WHEN (SELECT status FROM journals WHERE id = NEW.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_journal_lines_upd BEFORE UPDATE ON journal_lines
WHEN (SELECT status FROM journals WHERE id = OLD.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;


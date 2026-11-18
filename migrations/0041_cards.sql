-- 0041: B32 단계 2 — 법인카드( 3.3, 6절).
--  - corporate_cards: 카드 별칭·카드사 형식·끝 4자리·소지자. 전체 카드번호를 담는 칸은 없다(끝 4자리만).
--  - card_holder_history: 소지자 변경 이력(추가 전용).
--  - card_imports: 업로드 한 번의 요약(파일 해시·건수). 파일 본문은 저장하지 않는다.
--  - card_transactions: 카드 내역 한 행. dedup_key UNIQUE 로 같은 행은 한 번만 들어온다. 사용 일시·가맹점·금액은 올린 뒤 고칠 수 없다.
--  - card_explanations: 소명(제출·반려·확인 이력은 행으로 남는다). 증빙은 document_attachments 의 card_txn_id 로 연결한다.
--  - card_explain_accounts: 소명에 쓸 수 있는 비용 계정 목록. card_reminders: 미소명 알림의 하루 한 번 표시.
--  - journal_queue 에 출처 card_txn 을 더한다(표를 다시 만들어 CHECK 를 바꾼다).
-- 금액은 정수 원, 시각은 RFC3339 UTC 문자열, 날짜는 KST YYYY-MM-DD 다.

CREATE TABLE corporate_cards (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alias      TEXT NOT NULL CHECK (length(alias) BETWEEN 1 AND 40),
    issuer     TEXT NOT NULL CHECK (issuer IN ('format_a','format_b')),
    last4      TEXT NOT NULL CHECK (last4 GLOB '[0-9][0-9][0-9][0-9]'),
    holder_id  INTEGER REFERENCES users(id),
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    UNIQUE (issuer, last4)
);
CREATE TRIGGER trg_corporate_cards_no_delete BEFORE DELETE ON corporate_cards BEGIN SELECT RAISE(ABORT, '카드는 지울 수 없습니다. 사용 중지하세요'); END;

CREATE TABLE card_holder_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    card_id    INTEGER NOT NULL REFERENCES corporate_cards(id),
    holder_id  INTEGER REFERENCES users(id),
    from_date  TEXT NOT NULL,
    changed_by INTEGER REFERENCES users(id),
    created_at TEXT NOT NULL
);
CREATE TRIGGER trg_card_holder_history_no_update BEFORE UPDATE ON card_holder_history BEGIN SELECT RAISE(ABORT, '소지자 이력은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_card_holder_history_no_delete BEFORE DELETE ON card_holder_history BEGIN SELECT RAISE(ABORT, '소지자 이력은 지울 수 없습니다'); END;

CREATE TABLE card_imports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issuer      TEXT NOT NULL,
    file_sha256 TEXT NOT NULL,
    rows_total  INTEGER NOT NULL,
    rows_new    INTEGER NOT NULL,
    rows_dup    INTEGER NOT NULL,
    rows_error  INTEGER NOT NULL,
    imported_by INTEGER NOT NULL REFERENCES users(id),
    imported_at TEXT NOT NULL
);

CREATE TABLE card_transactions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    card_id     INTEGER NOT NULL REFERENCES corporate_cards(id),
    holder_id   INTEGER REFERENCES users(id),
    used_at     TEXT NOT NULL,
    merchant    TEXT NOT NULL CHECK (length(merchant) <= 200),
    amount      INTEGER NOT NULL CHECK (amount <> 0),
    supply      INTEGER,
    vat         INTEGER,
    tip         INTEGER,
    approval_no TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('approve','cancel')),
    dedup_key   TEXT NOT NULL UNIQUE,
    import_id   INTEGER NOT NULL REFERENCES card_imports(id),
    due_date    TEXT NOT NULL CHECK (due_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    status      TEXT NOT NULL CHECK (status IN ('unreconciled','submitted','confirmed','needs_fix','not_required','orphan_cancel','refund_requested','refunded')),
    pair_id     INTEGER REFERENCES card_transactions(id),
    refund_note TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_card_txn_holder ON card_transactions(holder_id, status, due_date);
CREATE INDEX idx_card_txn_card ON card_transactions(card_id, used_at);
CREATE TRIGGER trg_card_txn_no_delete BEFORE DELETE ON card_transactions BEGIN SELECT RAISE(ABORT, '카드 내역은 지울 수 없습니다'); END;
CREATE TRIGGER trg_card_txn_fixed BEFORE UPDATE ON card_transactions
WHEN NEW.used_at <> OLD.used_at OR NEW.merchant <> OLD.merchant OR NEW.amount <> OLD.amount OR NEW.card_id <> OLD.card_id OR NEW.kind <> OLD.kind OR NEW.dedup_key <> OLD.dedup_key
BEGIN SELECT RAISE(ABORT, '카드 내역의 사용 일시·가맹점·금액은 고칠 수 없습니다'); END;

CREATE TABLE card_explanations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    card_txn_id   INTEGER NOT NULL REFERENCES card_transactions(id),
    purpose       TEXT NOT NULL CHECK (length(purpose) <= 200),
    account_id    INTEGER REFERENCES accounts(id),
    vat_deduct    INTEGER NOT NULL DEFAULT 1 CHECK (vat_deduct IN (0,1)),
    vat_reason    TEXT NOT NULL DEFAULT '',
    supply        INTEGER NOT NULL DEFAULT 0,
    vat           INTEGER NOT NULL DEFAULT 0,
    personal      INTEGER NOT NULL DEFAULT 0 CHECK (personal IN (0,1)),
    status        TEXT NOT NULL CHECK (status IN ('submitted','returned','confirmed','withdrawn')),
    submitted_at  TEXT NOT NULL,
    submitted_by  INTEGER NOT NULL REFERENCES users(id),
    confirmed_by  INTEGER REFERENCES users(id),
    confirmed_at  TEXT,
    return_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_card_expl_txn ON card_explanations(card_txn_id, id);
CREATE TRIGGER trg_card_expl_no_delete BEFORE DELETE ON card_explanations BEGIN SELECT RAISE(ABORT, '소명 이력은 지울 수 없습니다'); END;
CREATE TRIGGER trg_card_expl_fixed BEFORE UPDATE ON card_explanations
WHEN OLD.status IN ('confirmed','returned','withdrawn') OR NEW.purpose <> OLD.purpose OR NEW.account_id IS NOT OLD.account_id OR NEW.supply <> OLD.supply OR NEW.vat <> OLD.vat OR NEW.personal <> OLD.personal
BEGIN SELECT RAISE(ABORT, '처리된 소명은 고칠 수 없습니다'); END;

CREATE TABLE card_explain_accounts (
    account_id INTEGER PRIMARY KEY REFERENCES accounts(id)
);
INSERT INTO card_explain_accounts (account_id) SELECT id FROM accounts WHERE code IN ('5210','5220','5230','5240','5250','5260','5290');

CREATE TABLE card_reminders (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    holder_id INTEGER NOT NULL,
    day       TEXT NOT NULL,
    kind      TEXT NOT NULL CHECK (kind IN ('holder','head')),
    UNIQUE (holder_id, day, kind)
);

-- 소명 증빙: 문서에 붙지 않아도 24시간 정리 대상이 되지 않도록 카드 내역 연결 칸을 둔다.
ALTER TABLE document_attachments ADD COLUMN card_txn_id INTEGER REFERENCES card_transactions(id);
CREATE INDEX idx_doc_attachments_card ON document_attachments(card_txn_id);

INSERT OR IGNORE INTO app_settings (key, value) VALUES ('card.explain_days', '7');
INSERT OR IGNORE INTO app_settings (key, value) VALUES ('card.receipt_required_over', '30000');

-- journal_queue 의 출처에 card_txn 을 더한다(CHECK 를 바꾸려면 표를 다시 만든다).
CREATE TABLE journal_queue_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT NOT NULL CHECK (source_type IN ('verdict','approval_doc','card_txn')),
    source_id   INTEGER NOT NULL,
    reason      TEXT NOT NULL,
    reason_text TEXT NOT NULL,
    po_id       INTEGER,
    receipt_id  INTEGER,
    vendor_id   INTEGER,
    amount      INTEGER,
    doc_total   INTEGER,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','created','manual','skipped')),
    journal_id  INTEGER REFERENCES journals(id),
    handled_by  INTEGER REFERENCES users(id),
    handled_at  TEXT,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    UNIQUE (source_type, source_id)
);
INSERT INTO journal_queue_new (id, source_type, source_id, reason, reason_text, po_id, receipt_id, vendor_id, amount, doc_total, status, journal_id, handled_by, handled_at, note, created_at)
SELECT id, source_type, source_id, reason, reason_text, po_id, receipt_id, vendor_id, amount, doc_total, status, journal_id, handled_by, handled_at, note, created_at FROM journal_queue;
DROP TABLE journal_queue;
ALTER TABLE journal_queue_new RENAME TO journal_queue;
CREATE INDEX idx_journal_queue_status ON journal_queue(status, id);
CREATE TRIGGER trg_journal_queue_no_delete BEFORE DELETE ON journal_queue BEGIN SELECT RAISE(ABORT, '전표 대기 기록은 지울 수 없습니다'); END;
CREATE TRIGGER trg_journal_queue_final BEFORE UPDATE ON journal_queue
WHEN OLD.status <> 'pending'
BEGIN SELECT RAISE(ABORT, '처리된 전표 대기 기록은 고칠 수 없습니다'); END;

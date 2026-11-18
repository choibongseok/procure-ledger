-- 0037: 재무 1 계정과목·전표·매입 채무(B31 단계 1, 3·6절).
--  - accounts: 국내 표준 약식 계정 21개. 코드·구분은 바꿀 수 없고 삭제할 수 없으며 시스템 계정은 중지할 수 없다.
--  - account_rules: 자동 전표 규칙(이벤트 → 계정). 규칙 키는 코드가 쓰는 값이고 B32 가 쓰는 키(payment.bank·card.*)도 미리 둔다.
--  - journals·journal_lines·journal_evidence: 전표. 상태 전이는 트리거가 한 번 더 막고, 승인된 전표와 승인 요청 이후의 줄은 고칠 수 없으며,
--    승인 요청 이상으로 가는 전표는 차변·대변 합이 같아야 하고, 마감된 달의 전기일로는 입력·요청·승인할 수 없다(B27 과 같은 마감).
--  - payables: 승인된 매입 전표의 외상매입금 대변 줄에서 생기는 채무 항목. paid_amount + reserved_amount ≤ amount 를 CHECK 가 지킨다.
--  - vendors.payment_days: 결제 조건 일수(NULL 이면 설정 기본 30일).
-- 금액은 모두 정수 원이다. 시각은 RFC3339 UTC 문자열이고 전기일·거래일은 KST 날짜(YYYY-MM-DD)다.

CREATE TABLE accounts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT NOT NULL UNIQUE CHECK (code GLOB '[1-5][0-9][0-9][0-9]'),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    kind       TEXT NOT NULL CHECK (kind IN ('asset','liability','equity','revenue','expense')),
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    system     INTEGER NOT NULL DEFAULT 0 CHECK (system IN (0,1)),
    created_at TEXT NOT NULL,
    CHECK ((kind = 'asset' AND code GLOB '1*') OR (kind = 'liability' AND code GLOB '2*') OR (kind = 'equity' AND code GLOB '3*')
        OR (kind = 'revenue' AND code GLOB '4*') OR (kind = 'expense' AND code GLOB '5*'))
);
INSERT INTO accounts (code, name, kind, system, created_at) VALUES
 ('1010','현금','asset',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('1020','보통예금','asset',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('1210','상품','asset',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('1220','원재료','asset',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('1310','부가세대급금','asset',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('2110','외상매입금','liability',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('2120','미지급금','liability',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('2130','부가세예수금','liability',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('2140','예수금','liability',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('3110','자본금','equity',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('3210','이익잉여금','equity',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('4110','매출액','revenue',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('4910','기타수익','revenue',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5110','매출원가','expense',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5210','복리후생비','expense',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5220','소모품비','expense',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5230','여비교통비','expense',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5240','교육훈련비','expense',1,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5250','접대비','expense',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5260','지급수수료','expense',0,strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('5290','기타비용','expense',1,strftime('%Y-%m-%dT%H:%M:%SZ','now'));
CREATE TRIGGER trg_accounts_no_delete BEFORE DELETE ON accounts BEGIN SELECT RAISE(ABORT, '계정과목은 지울 수 없습니다. 중지만 할 수 있습니다'); END;
CREATE TRIGGER trg_accounts_fixed BEFORE UPDATE ON accounts
WHEN NEW.code <> OLD.code OR NEW.kind <> OLD.kind OR NEW.system <> OLD.system OR (OLD.system = 1 AND NEW.active = 0)
BEGIN SELECT RAISE(ABORT, '계정과목의 코드·구분은 바꿀 수 없고 시스템 계정은 중지할 수 없습니다'); END;

CREATE TABLE account_rules (
    key        TEXT PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    updated_by INTEGER REFERENCES users(id),
    updated_at TEXT NOT NULL
);
INSERT INTO account_rules (key, account_id, updated_at)
SELECT k, (SELECT id FROM accounts WHERE code = c), strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM (
 SELECT 'purchase.inventory' AS k, '1210' AS c UNION ALL SELECT 'purchase.vat', '1310' UNION ALL SELECT 'purchase.payable', '2110'
 UNION ALL SELECT 'expense.welfare', '5210' UNION ALL SELECT 'expense.trip', '5230' UNION ALL SELECT 'expense.supplies', '5220'
 UNION ALL SELECT 'expense.training', '5240' UNION ALL SELECT 'expense.other', '5290' UNION ALL SELECT 'expense.payable', '2120'
 UNION ALL SELECT 'payment.bank', '1020' UNION ALL SELECT 'card.payable', '2120' UNION ALL SELECT 'card.vat', '1310');

CREATE TABLE journals (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    no            TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL CHECK (kind IN ('general','auto_purchase','auto_expense','auto_payment','auto_card','reversal','opening')),
    status        TEXT NOT NULL CHECK (status IN ('draft','requested','approved','canceled')),
    posting_date  TEXT NOT NULL CHECK (posting_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    trade_date    TEXT NOT NULL CHECK (trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    memo          TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    source_type   TEXT NOT NULL DEFAULT '' CHECK (source_type IN ('','verdict','approval_doc','payment','card_txn')),
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
-- 같은 출처의 취소되지 않은 자동 전표는 하나(AC-1111). 역분개는 원 전표당 한 번만(E9).
CREATE UNIQUE INDEX idx_journals_source ON journals(source_type, source_id) WHERE source_type <> '' AND kind IN ('auto_purchase','auto_expense','auto_payment','auto_card') AND status <> 'canceled';
CREATE UNIQUE INDEX idx_journals_reverses ON journals(reverses_id) WHERE reverses_id IS NOT NULL AND status <> 'canceled';
CREATE INDEX idx_journals_posting ON journals(posting_date, status);
CREATE INDEX idx_journals_doc ON journals(doc_id);

CREATE TABLE journal_lines (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id),
    seq        INTEGER NOT NULL,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    side       TEXT NOT NULL CHECK (side IN ('D','C')),
    amount     INTEGER NOT NULL CHECK (amount > 0),
    vendor_id  INTEGER REFERENCES vendors(id),
    memo       TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    UNIQUE (journal_id, seq)
);
CREATE INDEX idx_journal_lines_account ON journal_lines(account_id, journal_id);

CREATE TABLE journal_evidence (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id),
    kind       TEXT NOT NULL CHECK (kind IN ('verdict','document','attachment')),
    ref_id     INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (journal_id, kind, ref_id)
);
CREATE TRIGGER trg_journal_evidence_no_update BEFORE UPDATE ON journal_evidence BEGIN SELECT RAISE(ABORT, '증빙 연결은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_journal_evidence_no_delete BEFORE DELETE ON journal_evidence BEGIN SELECT RAISE(ABORT, '증빙 연결은 지울 수 없습니다'); END;

-- 전표 삭제 금지, 승인·취소된 전표는 고칠 수 없고, 상태는 허용된 전이로만 바뀐다.
CREATE TRIGGER trg_journals_no_delete BEFORE DELETE ON journals BEGIN SELECT RAISE(ABORT, '전표는 지울 수 없습니다. 취소하거나 역분개하세요'); END;
CREATE TRIGGER trg_journals_final BEFORE UPDATE ON journals
WHEN OLD.status = 'approved' OR OLD.status = 'canceled'
BEGIN SELECT RAISE(ABORT, '승인된 전표는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_journals_transition BEFORE UPDATE OF status ON journals
WHEN NEW.status <> OLD.status AND NOT (
     (OLD.status = 'draft' AND NEW.status IN ('requested','canceled'))
  OR (OLD.status = 'draft' AND NEW.status = 'approved' AND NEW.kind = 'auto_payment')
  OR (OLD.status = 'requested' AND NEW.status IN ('approved','draft','canceled')))
BEGIN SELECT RAISE(ABORT, '허용되지 않는 전표 상태 변경입니다'); END;
-- 승인 요청 이상으로 가려면 차변 합계와 대변 합계가 같고 줄이 2개 이상이어야 한다(DB 가 서버 검사를 우회하는 경우를 막는다).
CREATE TRIGGER trg_journals_balanced BEFORE UPDATE OF status ON journals
WHEN NEW.status IN ('requested','approved') AND NEW.status <> OLD.status AND (
     (SELECT COALESCE(SUM(CASE side WHEN 'D' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  <> (SELECT COALESCE(SUM(CASE side WHEN 'C' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  OR (SELECT COUNT(*) FROM journal_lines WHERE journal_id = NEW.id) < 2)
BEGIN SELECT RAISE(ABORT, '차변 합계와 대변 합계가 같지 않습니다'); END;
-- 마감된 달(B27)의 전기일로는 입력·승인 요청·승인할 수 없다(서버 검사의 이중 방어).
CREATE TRIGGER trg_journals_month_open_ins BEFORE INSERT ON journals
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_journals_month_open_upd BEFORE UPDATE OF status, posting_date ON journals
WHEN (NEW.status IN ('requested','approved') OR NEW.posting_date <> OLD.posting_date)
 AND EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

-- 승인 요청 이후(임시가 아닌) 전표의 줄은 넣거나 고치거나 지울 수 없다.
CREATE TRIGGER trg_journal_lines_ins BEFORE INSERT ON journal_lines
WHEN (SELECT status FROM journals WHERE id = NEW.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_journal_lines_upd BEFORE UPDATE ON journal_lines
WHEN (SELECT status FROM journals WHERE id = OLD.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_journal_lines_del BEFORE DELETE ON journal_lines
WHEN (SELECT status FROM journals WHERE id = OLD.journal_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 중이거나 끝난 전표의 줄은 고칠 수 없습니다'); END;

-- 매입 채무 항목: 승인된 전표의 외상매입금 대변 줄마다 하나. 지급 누계 + 예약 ≤ 금액(B32 가 올린다).
CREATE TABLE payables (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_id       INTEGER NOT NULL REFERENCES vendors(id),
    journal_id      INTEGER NOT NULL REFERENCES journals(id),
    line_id         INTEGER NOT NULL UNIQUE REFERENCES journal_lines(id),
    trade_date      TEXT NOT NULL,
    due_date        TEXT NOT NULL,
    amount          INTEGER NOT NULL CHECK (amount > 0),
    paid_amount     INTEGER NOT NULL DEFAULT 0,
    reserved_amount INTEGER NOT NULL DEFAULT 0,
    canceled        INTEGER NOT NULL DEFAULT 0 CHECK (canceled IN (0,1)),
    CHECK (paid_amount >= 0 AND reserved_amount >= 0 AND paid_amount + reserved_amount <= amount)
);
CREATE INDEX idx_payables_vendor ON payables(vendor_id, due_date);
CREATE TRIGGER trg_payables_no_delete BEFORE DELETE ON payables BEGIN SELECT RAISE(ABORT, '채무 항목은 지울 수 없습니다'); END;
CREATE TRIGGER trg_payables_fixed BEFORE UPDATE ON payables
WHEN NEW.vendor_id <> OLD.vendor_id OR NEW.journal_id <> OLD.journal_id OR NEW.line_id <> OLD.line_id OR NEW.amount <> OLD.amount
  OR NEW.trade_date <> OLD.trade_date OR (OLD.canceled = 1 AND NEW.canceled = 0)
BEGIN SELECT RAISE(ABORT, '채무 항목의 거래처·금액·발생일은 바꿀 수 없습니다'); END;

ALTER TABLE vendors ADD COLUMN payment_days INTEGER CHECK (payment_days IS NULL OR payment_days BETWEEN 0 AND 365);
INSERT OR IGNORE INTO app_settings (key, value) VALUES ('ledger.default_payment_days', '30');

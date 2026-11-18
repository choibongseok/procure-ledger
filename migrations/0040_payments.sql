-- 0040: B32 단계 1 — 자금 계좌·입출금 기록·지급 요청·지급·지급 요청서 양식 PAY( 6절).
--  - bank_accounts: 회사 계좌. 삭제 불가, 기초 잔액·기준일은 입력 뒤 수정 불가. 계좌번호는 구분(민감) 필드로 저장한다.
--  - fund_movements: 입출금 기록(입금 +, 출금 −). 추가 전용(수정·삭제 트리거). 정정은 반대 행이다.
--  - payment_requests·payment_request_items: 지급 요청(한 거래처의 채무 항목 여럿). 승인 요청 이후 거래처·합계·받는 계좌·줄은 고칠 수 없다.
--  - payments: 지급 행. payment_request_id UNIQUE(한 요청에 한 번), (bank_account_id, transfer_ref) UNIQUE(참조번호 중복 방지). 추가 전용.
--  - payables 의 paid_amount + reserved_amount ≤ amount 는 B31 의 CHECK 가 지킨다(0037).
-- 금액은 정수 원, 시각은 RFC3339 UTC 문자열, 날짜는 KST YYYY-MM-DD 다.

CREATE TABLE bank_accounts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    alias           TEXT NOT NULL UNIQUE CHECK (length(alias) BETWEEN 1 AND 40),
    bank_name       TEXT NOT NULL CHECK (length(bank_name) BETWEEN 1 AND 40),
    account_no      TEXT NOT NULL CHECK (length(account_no) BETWEEN 4 AND 40),
    account_id      INTEGER NOT NULL REFERENCES accounts(id),
    opening_balance INTEGER NOT NULL DEFAULT 0,
    opening_date    TEXT NOT NULL CHECK (opening_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    active          INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at      TEXT NOT NULL
);
CREATE TRIGGER trg_bank_accounts_no_delete BEFORE DELETE ON bank_accounts BEGIN SELECT RAISE(ABORT, '자금 계좌는 지울 수 없습니다. 사용 중지하세요'); END;
CREATE TRIGGER trg_bank_accounts_opening_fixed BEFORE UPDATE ON bank_accounts
WHEN NEW.opening_balance <> OLD.opening_balance OR NEW.opening_date <> OLD.opening_date OR NEW.account_id <> OLD.account_id
BEGIN SELECT RAISE(ABORT, '기초 잔액·기초 기준일·연결 계정은 입력 뒤 바꿀 수 없습니다'); END;

CREATE TABLE payment_requests (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    no                   TEXT NOT NULL UNIQUE,
    vendor_id            INTEGER NOT NULL REFERENCES vendors(id),
    status               TEXT NOT NULL CHECK (status IN ('draft','requested','approved','paid','canceled','paid_canceled')),
    total                INTEGER NOT NULL CHECK (total > 0),
    due_date             TEXT NOT NULL CHECK (due_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    bank_account_id      INTEGER NOT NULL REFERENCES bank_accounts(id),
    payee_account_masked TEXT NOT NULL,
    payee_account_enc    TEXT NOT NULL,
    memo                 TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    doc_id               INTEGER REFERENCES purchase_requests(id),
    created_by           INTEGER NOT NULL REFERENCES users(id),
    created_at           TEXT NOT NULL,
    approved_by          INTEGER REFERENCES users(id),
    approved_at          TEXT,
    cancel_reason        TEXT NOT NULL DEFAULT '',
    reverse_reason       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_payment_requests_status ON payment_requests(status, due_date);
CREATE INDEX idx_payment_requests_vendor ON payment_requests(vendor_id, status);
CREATE TRIGGER trg_payment_requests_no_delete BEFORE DELETE ON payment_requests BEGIN SELECT RAISE(ABORT, '지급 요청은 지울 수 없습니다. 취소하세요'); END;
CREATE TRIGGER trg_payment_requests_transition BEFORE UPDATE OF status ON payment_requests
WHEN NEW.status <> OLD.status AND NOT (
     (OLD.status = 'draft' AND NEW.status IN ('requested','canceled'))
  OR (OLD.status = 'requested' AND NEW.status IN ('approved','draft','canceled'))
  OR (OLD.status = 'approved' AND NEW.status IN ('paid','canceled'))
  OR (OLD.status = 'paid' AND NEW.status = 'paid_canceled'))
BEGIN SELECT RAISE(ABORT, '허용되지 않는 지급 요청 상태 변경입니다'); END;
CREATE TRIGGER trg_payment_requests_fixed BEFORE UPDATE ON payment_requests
WHEN OLD.status <> 'draft' AND (NEW.vendor_id <> OLD.vendor_id OR NEW.total <> OLD.total OR NEW.payee_account_enc <> OLD.payee_account_enc OR NEW.no <> OLD.no)
BEGIN SELECT RAISE(ABORT, '승인 요청 이후에는 지급 요청의 거래처·금액·받는 계좌를 바꿀 수 없습니다'); END;

CREATE TABLE payment_request_items (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    payment_request_id INTEGER NOT NULL REFERENCES payment_requests(id),
    payable_id         INTEGER NOT NULL REFERENCES payables(id),
    amount             INTEGER NOT NULL CHECK (amount > 0),
    UNIQUE (payment_request_id, payable_id)
);
CREATE TRIGGER trg_payment_items_ins BEFORE INSERT ON payment_request_items
WHEN (SELECT status FROM payment_requests WHERE id = NEW.payment_request_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 이후에는 지급 항목을 고칠 수 없습니다'); END;
CREATE TRIGGER trg_payment_items_upd BEFORE UPDATE ON payment_request_items
WHEN (SELECT status FROM payment_requests WHERE id = OLD.payment_request_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 이후에는 지급 항목을 고칠 수 없습니다'); END;
CREATE TRIGGER trg_payment_items_del BEFORE DELETE ON payment_request_items
WHEN (SELECT status FROM payment_requests WHERE id = OLD.payment_request_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '승인 요청 이후에는 지급 항목을 고칠 수 없습니다'); END;

CREATE TABLE payments (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    payment_request_id INTEGER NOT NULL UNIQUE REFERENCES payment_requests(id),
    paid_date          TEXT NOT NULL CHECK (paid_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    bank_account_id    INTEGER NOT NULL REFERENCES bank_accounts(id),
    transfer_ref       TEXT CHECK (transfer_ref IS NULL OR length(transfer_ref) BETWEEN 1 AND 40),
    paid_by            INTEGER NOT NULL REFERENCES users(id),
    paid_at            TEXT NOT NULL,
    journal_id         INTEGER NOT NULL REFERENCES journals(id),
    account_changed    INTEGER NOT NULL DEFAULT 0 CHECK (account_changed IN (0,1)),
    memo               TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200)
);
CREATE UNIQUE INDEX idx_payments_transfer_ref ON payments(bank_account_id, transfer_ref) WHERE transfer_ref IS NOT NULL;
CREATE TRIGGER trg_payments_no_update BEFORE UPDATE ON payments BEGIN SELECT RAISE(ABORT, '지급 기록은 고칠 수 없습니다. 지급 취소로 정정하세요'); END;
CREATE TRIGGER trg_payments_no_delete BEFORE DELETE ON payments BEGIN SELECT RAISE(ABORT, '지급 기록은 지울 수 없습니다. 지급 취소로 정정하세요'); END;

CREATE TABLE fund_movements (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    bank_account_id        INTEGER NOT NULL REFERENCES bank_accounts(id),
    move_date              TEXT NOT NULL CHECK (move_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    kind                   TEXT NOT NULL CHECK (kind IN ('payment','payment_cancel','deposit')),
    amount                 INTEGER NOT NULL CHECK (amount <> 0),
    payment_id             INTEGER REFERENCES payments(id),
    journal_id             INTEGER REFERENCES journals(id),
    counterpart_account_id INTEGER REFERENCES accounts(id),
    vendor_id              INTEGER REFERENCES vendors(id),
    memo                   TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    created_by             INTEGER NOT NULL REFERENCES users(id),
    created_at             TEXT NOT NULL
);
CREATE INDEX idx_fund_movements_acc ON fund_movements(bank_account_id, move_date);
CREATE TRIGGER trg_fund_movements_no_update BEFORE UPDATE ON fund_movements BEGIN SELECT RAISE(ABORT, '입출금 기록은 고칠 수 없습니다. 반대 기록으로 정정하세요'); END;
CREATE TRIGGER trg_fund_movements_no_delete BEFORE DELETE ON fund_movements BEGIN SELECT RAISE(ABORT, '입출금 기록은 지울 수 없습니다. 반대 기록으로 정정하세요'); END;

-- 한 채무 항목에 지급 완료 또는 진행 중 요청이 겹쳐도 합계가 잔액을 넘을 수 없다(0037 의 CHECK). 조회 속도용 색인.
CREATE INDEX idx_payables_open ON payables(vendor_id, due_date) WHERE canceled = 0;

-- 기본 계좌: 개발·시험에서 바로 쓰도록 보통예금(1020)에 연결한 회사 계좌 하나를 두지 않는다(운영 데이터는 재무가 입력한다).

-- 지급 요청서 시스템 양식 PAY. 결재선: 재무팀장 단독, 합계 3천만원 이상이면 재무본부장, 1억원 이상이면 대표(회사 조직 CO)까지.
INSERT INTO approval_forms (code, category, name, kind, active, required_permission, created_at)
VALUES ('PAY', 'finance', '지급 요청서', 'general', 1, 'payment:write', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT id, 1, 'published',
 '{"system":true,"fields":[{"key":"payment_no","label":"지급 요청 번호","type":"text","required":true,"maxLength":20},{"key":"vendor","label":"거래처","type":"text","required":true,"maxLength":200},{"key":"total","label":"지급 합계(원)","type":"amount","required":true},{"key":"due_date","label":"지급 예정일","type":"text","required":true,"maxLength":10},{"key":"bank","label":"출금 계좌","type":"text","required":false,"maxLength":100},{"key":"summary","label":"항목 요약","type":"textarea","required":false,"maxLength":5000}],"body":"","line":{"steps":[{"role":"approve","source":"unit_head","unitCode":"T-FIN","required":true},{"role":"approve","source":"unit_head","unitCode":"DIV-MG","required":true,"minAmount":30000000},{"role":"approve","source":"unit_head","unitCode":"CO","required":true,"minAmount":100000000}]},"recipients":{"cc":[{"unit_code":"T-AUD"}],"receive":[]},"numbering":{"pattern":"PYD-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":10,"dept_limit":[],"settings":{"line_edit":"none","objection_action":"record","amount_field":"total","dept_visible":false,"urgent_allowed":false}}',
 strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
 FROM approval_forms WHERE code = 'PAY';

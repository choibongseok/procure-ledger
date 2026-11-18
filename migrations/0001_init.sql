-- ProcureLedger 초기 스키마. 전 영역 테이블을 한 번에 둔다. (이후 변경은 platform 에게 요청).
-- 전제: 연결 시 PRAGMA foreign_keys=ON (internal/dbutil.Open 이 설정). 시각은 UTC RFC3339 문자열.
-- 표기: [platform] 플랫폼, [erp] 업무(구매·결재·재고), [ai] AI(문서·LLM).

-- ============================================================
-- [platform] 조직·사용자·세션·토큰
-- ============================================================
CREATE TABLE departments (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    oidc_sub      TEXT UNIQUE,                       -- IdP 의 sub (dev IdP 도 같은 경로)
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('admin','finance','manager','staff','auditor')),
    position      TEXT CHECK (position IN ('team_lead','dept_head')), -- manager 의 결재선 단계 구분
    department_id INTEGER NOT NULL REFERENCES departments(id),
    active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX idx_users_department ON users(department_id);

-- 서버 측 세션. 쿠키에는 무작위 세션 ID 만 담고 DB 에는 해시만 저장한다.
CREATE TABLE sessions (
    id_hash      TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    csrf_hash    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    revoked_at   TEXT
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

-- OIDC Authorization Code + PKCE 진행 중 요청(state·nonce·code_verifier 검증용, 1회용).
CREATE TABLE oidc_auth_requests (
    state         TEXT PRIMARY KEY,
    nonce         TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    redirect_to   TEXT NOT NULL DEFAULT '/',
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL
);

-- 개인 토큰(MCP·API). 원문은 발급 시 한 번만 보여 주고 해시만 저장한다.
CREATE TABLE personal_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,                       -- 목록 표시용 앞 몇 글자
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,                       -- 만료 필수
    revoked_at   TEXT,
    last_used_at TEXT
);
CREATE INDEX idx_tokens_user ON personal_tokens(user_id);

-- ============================================================
-- [platform] 감사 로그: 추가 전용 + 해시 체인
-- ============================================================
CREATE TABLE audit_logs (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    actor_id    INTEGER NOT NULL,                     -- 사용자 없는 사건은 0 (FK 없음: 기록은 사용자 삭제와 무관)
    actor_role  TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw')),
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('success','denied','error')),
    diff        TEXT NOT NULL DEFAULT '[]',           -- JSON [{key,old,new}]
    detail      TEXT NOT NULL DEFAULT '',
    prev_hash   TEXT NOT NULL,                        -- 첫 레코드는 빈 문자열
    hash        TEXT NOT NULL UNIQUE
);
CREATE INDEX idx_audit_target ON audit_logs(target_type, target_id);
CREATE INDEX idx_audit_actor  ON audit_logs(actor_id);
CREATE INDEX idx_audit_action ON audit_logs(action);

CREATE TRIGGER trg_audit_no_update BEFORE UPDATE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;
CREATE TRIGGER trg_audit_no_delete BEFORE DELETE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;

-- ============================================================
-- [erp] 거래처·품목·재고 원장
-- ============================================================
-- 사업자번호·계좌·연락처는 구분 컬럼으로 저장한다. 응답·LLM 프롬프트에서 역할에 따라 마스킹(contract.Masker).
CREATE TABLE vendors (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    business_no TEXT NOT NULL DEFAULT '',
    account_no  TEXT NOT NULL DEFAULT '',
    phone       TEXT NOT NULL DEFAULT '',
    email       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE items (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    sku          TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    unit         TEXT NOT NULL DEFAULT 'EA',
    safety_stock INTEGER NOT NULL DEFAULT 0 CHECK (safety_stock >= 0),
    track_lot    INTEGER NOT NULL DEFAULT 0 CHECK (track_lot IN (0,1)),
    active       INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1))
);

CREATE TABLE lots (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id     INTEGER NOT NULL REFERENCES items(id),
    lot_no      TEXT NOT NULL,
    expiry_date TEXT,                                 -- YYYY-MM-DD, 없으면 NULL
    UNIQUE (item_id, lot_no)
);

-- 재고 수량은 이 원장의 합으로만 계산한다. 수량 컬럼·직접 수정 경로는 없고 행은 수정·삭제할 수 없다.
CREATE TABLE stock_ledger (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id    INTEGER NOT NULL REFERENCES items(id),
    lot_id     INTEGER REFERENCES lots(id),
    qty_delta  INTEGER NOT NULL CHECK (qty_delta <> 0),
    reason     TEXT NOT NULL CHECK (reason IN ('receipt','issue','adjust')),
    ref_type   TEXT NOT NULL DEFAULT '',
    ref_id     INTEGER NOT NULL DEFAULT 0,
    actor_id   INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX idx_ledger_item ON stock_ledger(item_id);

CREATE TRIGGER trg_ledger_no_update BEFORE UPDATE ON stock_ledger
BEGIN SELECT RAISE(ABORT, 'stock_ledger 는 추가 전용입니다'); END;
CREATE TRIGGER trg_ledger_no_delete BEFORE DELETE ON stock_ledger
BEGIN SELECT RAISE(ABORT, 'stock_ledger 는 추가 전용입니다'); END;

CREATE VIEW v_item_stock AS
SELECT i.id AS item_id, COALESCE(SUM(l.qty_delta), 0) AS on_hand
FROM items i LEFT JOIN stock_ledger l ON l.item_id = i.id
GROUP BY i.id;

CREATE VIEW v_lot_stock AS
SELECT l.item_id, l.id AS lot_id, l.lot_no, l.expiry_date, COALESCE(SUM(s.qty_delta), 0) AS on_hand
FROM lots l LEFT JOIN stock_ledger s ON s.lot_id = l.id
GROUP BY l.id;

-- ============================================================
-- [erp] 구매요청·결재
-- ============================================================
-- 금액 구간별 결재선 규칙. min_amount <= 금액 < max_amount(NULL 이면 상한 없음) 구간의 단계들이 seq 순으로 적용된다.
CREATE TABLE approval_line_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    min_amount INTEGER NOT NULL,
    max_amount INTEGER,
    seq        INTEGER NOT NULL,
    step_kind  TEXT NOT NULL CHECK (step_kind IN ('team_lead','dept_head','finance')),
    UNIQUE (min_amount, seq)
);
INSERT INTO approval_line_rules (min_amount, max_amount, seq, step_kind) VALUES
    (0,        1000000,  1, 'team_lead'),
    (1000000,  10000000, 1, 'team_lead'),
    (1000000,  10000000, 2, 'dept_head'),
    (10000000, NULL,     1, 'team_lead'),
    (10000000, NULL,     2, 'dept_head'),
    (10000000, NULL,     3, 'finance');

CREATE TABLE purchase_requests (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    no                TEXT NOT NULL UNIQUE,
    requester_id      INTEGER NOT NULL REFERENCES users(id),
    department_id     INTEGER NOT NULL REFERENCES departments(id),   -- 부서 격리 기준
    vendor_id         INTEGER REFERENCES vendors(id),
    title             TEXT NOT NULL,
    reason            TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL CHECK (status IN ('draft','submitted','in_review','rejected','approved','ordered')),
    total_amount      INTEGER NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
    reject_reason     TEXT NOT NULL DEFAULT '',
    round             INTEGER NOT NULL DEFAULT 1,                    -- 재상신마다 증가
    purchase_order_id INTEGER,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
CREATE INDEX idx_pr_department ON purchase_requests(department_id, status);
CREATE INDEX idx_pr_requester  ON purchase_requests(requester_id);

CREATE TABLE purchase_request_lines (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id      INTEGER NOT NULL REFERENCES purchase_requests(id),
    item_id    INTEGER NOT NULL REFERENCES items(id),
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    unit_price INTEGER NOT NULL CHECK (unit_price >= 0),
    amount     INTEGER NOT NULL CHECK (amount >= 0)
);
CREATE INDEX idx_prl_pr ON purchase_request_lines(pr_id);

-- 현재 결재 진행 상태(차수별). 결정 이력은 아래 approval_history 에 불변으로 남는다.
CREATE TABLE approval_steps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id       INTEGER NOT NULL REFERENCES purchase_requests(id),
    round       INTEGER NOT NULL,
    seq         INTEGER NOT NULL,
    step_kind   TEXT NOT NULL CHECK (step_kind IN ('team_lead','dept_head','finance')),
    status      TEXT NOT NULL CHECK (status IN ('waiting','pending','approved','rejected')),
    approver_id INTEGER REFERENCES users(id),
    comment     TEXT NOT NULL DEFAULT '',
    decided_at  TEXT,
    UNIQUE (pr_id, round, seq)
);

-- 결재 이력: 불변. 수정·삭제 불가(트리거). 반려 사유는 comment.
CREATE TABLE approval_history (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id    INTEGER NOT NULL REFERENCES purchase_requests(id),
    round    INTEGER NOT NULL,
    seq      INTEGER NOT NULL DEFAULT 0,
    actor_id INTEGER NOT NULL REFERENCES users(id),
    action   TEXT NOT NULL CHECK (action IN ('submit','resubmit','approve','reject')),
    comment  TEXT NOT NULL DEFAULT '',
    at       TEXT NOT NULL
);
CREATE INDEX idx_ah_pr ON approval_history(pr_id);

CREATE TRIGGER trg_approval_history_no_update BEFORE UPDATE ON approval_history
BEGIN SELECT RAISE(ABORT, 'approval_history 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_approval_history_no_delete BEFORE DELETE ON approval_history
BEGIN SELECT RAISE(ABORT, 'approval_history 는 삭제할 수 없습니다'); END;

-- 자기 결재 금지: 요청자가 같은 요청의 승인·반려 이력을 남길 수 없다(서비스 검사의 최후 방어선).
CREATE TRIGGER trg_approval_no_self BEFORE INSERT ON approval_history
WHEN NEW.action IN ('approve','reject')
BEGIN
    SELECT RAISE(ABORT, '본인이 올린 요청은 결재할 수 없습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.actor_id;
END;

-- ============================================================
-- [erp] 발주·입고
-- ============================================================
CREATE TABLE purchase_orders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    no           TEXT NOT NULL UNIQUE,
    pr_id        INTEGER NOT NULL UNIQUE REFERENCES purchase_requests(id),
    vendor_id    INTEGER REFERENCES vendors(id),
    status       TEXT NOT NULL CHECK (status IN ('open','partial','received')),
    total_amount INTEGER NOT NULL CHECK (total_amount >= 0),
    created_at   TEXT NOT NULL
);

CREATE TABLE po_lines (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    po_id      INTEGER NOT NULL REFERENCES purchase_orders(id),
    item_id    INTEGER NOT NULL REFERENCES items(id),
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    unit_price INTEGER NOT NULL CHECK (unit_price >= 0)
);
CREATE INDEX idx_pol_po ON po_lines(po_id);

CREATE TABLE receipts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    po_id       INTEGER NOT NULL REFERENCES purchase_orders(id),
    received_by INTEGER NOT NULL REFERENCES users(id),
    received_at TEXT NOT NULL
);

CREATE TABLE receipt_lines (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    receipt_id INTEGER NOT NULL REFERENCES receipts(id),
    po_line_id INTEGER NOT NULL REFERENCES po_lines(id),
    item_id    INTEGER NOT NULL REFERENCES items(id),
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    lot_id     INTEGER REFERENCES lots(id)
);
CREATE INDEX idx_rl_po_line ON receipt_lines(po_line_id);

-- PO 수량 초과 입고 금지(서비스 검사의 최후 방어선). 입고 누계 = receipt_lines 합.
CREATE TRIGGER trg_receipt_no_over BEFORE INSERT ON receipt_lines
BEGIN
    SELECT RAISE(ABORT, 'PO 수량을 넘는 입고입니다')
    WHERE (SELECT COALESCE(SUM(quantity), 0) FROM receipt_lines WHERE po_line_id = NEW.po_line_id) + NEW.quantity
        > (SELECT quantity FROM po_lines WHERE id = NEW.po_line_id);
END;

-- ============================================================
-- [ai] 증빙 문서·판정
-- ============================================================
CREATE TABLE documents (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT NOT NULL CHECK (kind IN ('transaction_statement','tax_invoice')),
    filename      TEXT NOT NULL,
    content_type  TEXT NOT NULL,
    text_content  TEXT,                               -- PDF 텍스트
    image         BLOB,                               -- 이미지 증빙
    sha256        TEXT NOT NULL,
    uploaded_by   INTEGER NOT NULL REFERENCES users(id),
    department_id INTEGER NOT NULL REFERENCES departments(id),
    po_id         INTEGER REFERENCES purchase_orders(id),
    receipt_id    INTEGER REFERENCES receipts(id),
    created_at    TEXT NOT NULL
);

CREATE TABLE document_extractions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id     INTEGER NOT NULL UNIQUE REFERENCES documents(id),
    extraction_json TEXT NOT NULL,                    -- 공급자·품목·수량·단가·합계·사업자번호
    llm_usage_id    INTEGER,                          -- llm_usage.id (FK 없음: 사용량은 별도 보관 정책)
    created_at      TEXT NOT NULL
);

-- AI 판정은 제안(outcome). 최종 확정은 재무가 confirmed_* 에 기록한다.
CREATE TABLE document_verdicts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id       INTEGER NOT NULL REFERENCES documents(id),
    po_id             INTEGER NOT NULL REFERENCES purchase_orders(id),
    receipt_id        INTEGER REFERENCES receipts(id),
    outcome           TEXT NOT NULL CHECK (outcome IN ('match','mismatch','hold')),
    items_json        TEXT NOT NULL,                  -- 근거 항목 [{field,expected,actual,result,note}]
    created_at        TEXT NOT NULL,
    confirmed_by      INTEGER REFERENCES users(id),
    confirmed_outcome TEXT CHECK (confirmed_outcome IN ('match','mismatch','hold')),
    confirmed_at      TEXT,
    confirm_comment   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_verdict_doc ON document_verdicts(document_id);

-- ============================================================
-- [ai] LLM 게이트웨이: 사용량·정책
-- ============================================================
CREATE TABLE llm_usage (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    at             TEXT NOT NULL,
    user_id        INTEGER NOT NULL DEFAULT 0,
    department_id  INTEGER NOT NULL DEFAULT 0,
    role           TEXT NOT NULL DEFAULT '',
    provider       TEXT NOT NULL,
    model          TEXT NOT NULL,
    purpose        TEXT NOT NULL DEFAULT '',
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_micro_usd INTEGER NOT NULL DEFAULT 0,        -- 1e-6 USD 단위 정수
    masked_count   INTEGER NOT NULL DEFAULT 0,        -- 전송 전 마스킹된 항목 수
    status         TEXT NOT NULL CHECK (status IN ('ok','blocked_quota','blocked_model','error'))
);
CREATE INDEX idx_llm_usage_user_day ON llm_usage(user_id, at);
CREATE INDEX idx_llm_usage_dept_day ON llm_usage(department_id, at);

-- 역할·부서별 모델 허용 목록. role·department_id 가 모두 NULL 이면 전체 기본 규칙.
-- 규칙이 하나도 없으면 전부 거부(fail-closed). model_pattern 은 접두 일치(끝에 * 허용).
CREATE TABLE llm_model_policies (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    role          TEXT,
    department_id INTEGER REFERENCES departments(id),
    model_pattern TEXT NOT NULL,
    allowed       INTEGER NOT NULL DEFAULT 1 CHECK (allowed IN (0,1))
);

-- 일일 토큰 한도. scope 별(role|department|user) 한도이며 가장 좁은 규칙이 우선한다.
CREATE TABLE llm_daily_quotas (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_type        TEXT NOT NULL CHECK (scope_type IN ('role','department','user')),
    scope_value       TEXT NOT NULL,
    daily_token_limit INTEGER NOT NULL CHECK (daily_token_limit >= 0),
    UNIQUE (scope_type, scope_value)
);

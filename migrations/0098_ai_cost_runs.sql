-- migrate:fk-off
-- 0098 (B49 3단계, 4·8·9절): 월 배부 실행·해제 요청·AI 비용 예산·한도 알림 표, 전표 쪽 확장.
--  1) ai_cost_runs(월·법인마다 확정 하나, 해제되면 released) · ai_cost_run_lines(확정 때 고정, 수정·삭제 불가) · ai_cost_release_requests(요청자≠확인자는 DB CHECK).
--  2) 확정된 달은 환율·청구액을 바꿀 수 없다(트리거). 예산 ai_cost_budgets(+이력)와 한도 알림 중복 방지 ai_cost_limit_notices.
--  3) journal_lines.department_id: 부서 차원(NULL 허용, 기존 응답은 달라지지 않는다).
--  4) journals.kind 에 'auto_aicost', journals.source_type 에 'aicost_run': CHECK 는 바꿀 수 없어 0064·0072 와 같은 방식으로 표를 새로 만든다
--     (줄 표 트리거를 내렸다 다시 만들고, fk-off 지시로 외래키 검사를 끈 채 적용한 뒤 foreign_key_check 로 확인한다). 정의는 0072 이후 열(entity_id·site_id)까지 같고 CHECK 두 곳과 자동 전표 출처 색인만 늘었다.

CREATE TABLE ai_cost_runs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    month            TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    entity_id        INTEGER NOT NULL,
    rule_version     INTEGER NOT NULL CHECK (rule_version >= 1),
    status           TEXT NOT NULL CHECK (status IN ('confirmed','released')),
    fx_usd_krw_x1000 INTEGER NOT NULL CHECK (fx_usd_krw_x1000 > 0),
    total_krw        INTEGER NOT NULL CHECK (total_krw >= 0),
    direct_krw       INTEGER NOT NULL CHECK (direct_krw >= 0),
    shared_krw       INTEGER NOT NULL CHECK (shared_krw >= 0),
    journal_no       TEXT NOT NULL DEFAULT '',
    confirmed_by     INTEGER NOT NULL REFERENCES users(id),
    confirmed_at     TEXT NOT NULL,
    released_by      INTEGER REFERENCES users(id),
    released_at      TEXT,
    detail_json      TEXT NOT NULL DEFAULT '' CHECK (length(detail_json) <= 200000)
);
CREATE UNIQUE INDEX idx_ai_cost_runs_confirmed ON ai_cost_runs(month, entity_id) WHERE status = 'confirmed';
CREATE INDEX idx_ai_cost_runs_month ON ai_cost_runs(month, entity_id, id);
CREATE TRIGGER trg_ai_cost_runs_no_delete BEFORE DELETE ON ai_cost_runs BEGIN SELECT RAISE(ABORT, '배부 실행은 지울 수 없습니다'); END;
CREATE TRIGGER trg_ai_cost_runs_fixed BEFORE UPDATE ON ai_cost_runs
WHEN NEW.month <> OLD.month OR NEW.entity_id <> OLD.entity_id OR NEW.rule_version <> OLD.rule_version OR NEW.total_krw <> OLD.total_krw OR NEW.direct_krw <> OLD.direct_krw
  OR NEW.shared_krw <> OLD.shared_krw OR NEW.fx_usd_krw_x1000 <> OLD.fx_usd_krw_x1000 OR NEW.confirmed_by <> OLD.confirmed_by OR NEW.confirmed_at <> OLD.confirmed_at
  OR (OLD.status = 'released' AND NEW.status <> 'released')
BEGIN SELECT RAISE(ABORT, '확정한 배부 결과는 고칠 수 없습니다. 해제한 뒤 다시 확정하세요'); END;

CREATE TABLE ai_cost_run_lines (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id          INTEGER NOT NULL REFERENCES ai_cost_runs(id),
    department_id   INTEGER NOT NULL,
    department_name TEXT NOT NULL DEFAULT '',
    headcount       INTEGER NOT NULL DEFAULT 0,
    calls           INTEGER NOT NULL DEFAULT 0,
    direct_krw      INTEGER NOT NULL DEFAULT 0,
    shared_krw      INTEGER NOT NULL DEFAULT 0,
    total_krw       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (run_id, department_id)
);
CREATE TRIGGER trg_ai_cost_run_lines_no_update BEFORE UPDATE ON ai_cost_run_lines BEGIN SELECT RAISE(ABORT, '확정한 배부 결과는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_ai_cost_run_lines_no_delete BEFORE DELETE ON ai_cost_run_lines BEGIN SELECT RAISE(ABORT, '확정한 배부 결과는 지울 수 없습니다'); END;

CREATE TABLE ai_cost_release_requests (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       INTEGER NOT NULL REFERENCES ai_cost_runs(id),
    requested_by INTEGER NOT NULL REFERENCES users(id),
    requested_at TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    confirmed_by INTEGER REFERENCES users(id),
    resolved_at  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('pending','confirmed','rejected','canceled')),
    CHECK (confirmed_by IS NULL OR confirmed_by <> requested_by)
);
CREATE UNIQUE INDEX idx_ai_cost_release_one_pending ON ai_cost_release_requests(run_id) WHERE status = 'pending';

CREATE TRIGGER trg_ai_cost_fx_locked_ins BEFORE INSERT ON ai_cost_fx
WHEN EXISTS (SELECT 1 FROM ai_cost_runs WHERE month = NEW.month AND status = 'confirmed')
BEGIN SELECT RAISE(ABORT, '확정된 달은 다시 계산할 수 없습니다. 확정 해제를 요청하세요.'); END;
CREATE TRIGGER trg_ai_cost_fx_locked_upd BEFORE UPDATE ON ai_cost_fx
WHEN EXISTS (SELECT 1 FROM ai_cost_runs WHERE month = NEW.month AND status = 'confirmed')
BEGIN SELECT RAISE(ABORT, '확정된 달은 다시 계산할 수 없습니다. 확정 해제를 요청하세요.'); END;
CREATE TRIGGER trg_ai_cost_adjust_locked BEFORE INSERT ON ai_cost_adjustments
WHEN EXISTS (SELECT 1 FROM ai_cost_runs WHERE month = NEW.month AND status = 'confirmed')
BEGIN SELECT RAISE(ABORT, '확정된 달은 다시 계산할 수 없습니다. 확정 해제를 요청하세요.'); END;

CREATE TABLE ai_cost_budgets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id     INTEGER NOT NULL,
    department_id INTEGER NOT NULL,
    month         TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    amount_krw    INTEGER NOT NULL CHECK (amount_krw >= 0),
    updated_by    INTEGER NOT NULL REFERENCES users(id),
    updated_at    TEXT NOT NULL,
    UNIQUE (entity_id, department_id, month)
);
CREATE TRIGGER trg_ai_cost_budgets_no_delete BEFORE DELETE ON ai_cost_budgets BEGIN SELECT RAISE(ABORT, 'AI 비용 예산은 지울 수 없습니다(0원으로 바꾸세요)'); END;

CREATE TABLE ai_cost_budget_revisions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id     INTEGER NOT NULL,
    department_id INTEGER NOT NULL,
    month         TEXT NOT NULL,
    old_amount    INTEGER,
    new_amount    INTEGER NOT NULL,
    changed_by    INTEGER NOT NULL REFERENCES users(id),
    changed_at    TEXT NOT NULL
);
CREATE TRIGGER trg_ai_cost_budget_rev_no_update BEFORE UPDATE ON ai_cost_budget_revisions BEGIN SELECT RAISE(ABORT, '예산 이력은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_ai_cost_budget_rev_no_delete BEFORE DELETE ON ai_cost_budget_revisions BEGIN SELECT RAISE(ABORT, '예산 이력은 지울 수 없습니다'); END;

CREATE TABLE ai_cost_limit_notices (
    month         TEXT NOT NULL,
    entity_id     INTEGER NOT NULL,
    department_id INTEGER NOT NULL,
    threshold     INTEGER NOT NULL CHECK (threshold IN (80, 100)),
    notified_at   TEXT NOT NULL,
    PRIMARY KEY (month, entity_id, department_id, threshold)
);

-- ---- journal_lines: 부서 차원 ----
ALTER TABLE journal_lines ADD COLUMN department_id INTEGER;

-- ---- journals ----
-- 줄 표의 트리거가 journals 를 참조하므로 표 이름을 바꾸는 동안 이름 해석이 실패한다: 먼저 지우고 끝에 같은 정의로 다시 만든다(0072 와 같다).
DROP TRIGGER trg_journal_lines_del;
DROP TRIGGER trg_journal_lines_ins;
DROP TRIGGER trg_journal_lines_upd;
CREATE TABLE journals_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    no            TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL CHECK (kind IN ('general','auto_purchase','auto_expense','auto_payment','auto_card','auto_aicost','reversal','opening')),
    status        TEXT NOT NULL CHECK (status IN ('draft','requested','approved','canceled')),
    posting_date  TEXT NOT NULL CHECK (posting_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    trade_date    TEXT NOT NULL CHECK (trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    memo          TEXT NOT NULL DEFAULT '' CHECK (length(memo) <= 200),
    source_type   TEXT NOT NULL DEFAULT '' CHECK (source_type IN ('','verdict','approval_doc','payment','card_txn','api','tax_invoice','aicost_run')),
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
    cancel_reason TEXT NOT NULL DEFAULT '',
    entity_id     INTEGER NOT NULL DEFAULT 1,
    site_id       INTEGER NOT NULL DEFAULT 0
);
INSERT INTO journals_new (id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, reverses_id, doc_id, total_amount, created_by, created_at, approved_by, approved_at, canceled_by, canceled_at, cancel_reason, entity_id, site_id)
    SELECT id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, reverses_id, doc_id, total_amount, created_by, created_at, approved_by, approved_at, canceled_by, canceled_at, cancel_reason, entity_id, site_id FROM journals ORDER BY id;
UPDATE sqlite_sequence SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'journals') WHERE name = 'journals_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'journals');
DROP TABLE journals;
ALTER TABLE journals_new RENAME TO journals;
CREATE UNIQUE INDEX idx_journals_source ON journals(source_type, source_id) WHERE source_type <> '' AND kind IN ('auto_purchase','auto_expense','auto_payment','auto_card','auto_aicost') AND status <> 'canceled';
CREATE UNIQUE INDEX idx_journals_reverses ON journals(reverses_id) WHERE reverses_id IS NOT NULL AND status <> 'canceled';
CREATE INDEX idx_journals_posting ON journals(posting_date, status);
CREATE INDEX idx_journals_doc ON journals(doc_id);
CREATE INDEX idx_journals_entity_posting ON journals(entity_id, posting_date, status);
CREATE TRIGGER trg_journals_balanced BEFORE UPDATE OF status ON journals
WHEN NEW.status IN ('requested','approved') AND NEW.status <> OLD.status AND (
     (SELECT COALESCE(SUM(CASE side WHEN 'D' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  <> (SELECT COALESCE(SUM(CASE side WHEN 'C' THEN amount END),0) FROM journal_lines WHERE journal_id = NEW.id)
  OR (SELECT COUNT(*) FROM journal_lines WHERE journal_id = NEW.id) < 2)
BEGIN SELECT RAISE(ABORT, '차변 합계와 대변 합계가 같지 않습니다'); END;
CREATE TRIGGER trg_journals_entity_immutable BEFORE UPDATE OF entity_id ON journals
WHEN NEW.entity_id <> OLD.entity_id
BEGIN SELECT RAISE(ABORT, '전표의 법인은 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_journals_final BEFORE UPDATE ON journals
WHEN OLD.status = 'approved' OR OLD.status = 'canceled'
BEGIN SELECT RAISE(ABORT, '승인된 전표는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_journals_month_open_ins BEFORE INSERT ON journals
WHEN EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_journals_month_open_upd BEFORE UPDATE OF status, posting_date ON journals
WHEN (NEW.status IN ('requested','approved') OR NEW.posting_date <> OLD.posting_date)
 AND EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7))
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

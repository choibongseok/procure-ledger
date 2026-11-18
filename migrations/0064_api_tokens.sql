-- migrate:fk-off
-- 0064 (B42 1단계, 5절): 연동 API 토큰·속도 제한 분 창·멱등 표, 감사 기록 경로 'api', 전표 출처 'api'.
--  - audit_logs.channel CHECK 에 'api' 추가: CHECK 는 바꿀 수 없어 표를 새로 만들어 옮긴다(0053·0058 과 같은 방식). 행 내용·seq·해시는 그대로 복사하므로
--    해시 체인은 영향이 없고 AUTOINCREMENT 시퀀스는 복사한 최대 seq 로 이어진다. 추가 전용 트리거와 색인은 새 표에 다시 만든다.
--  - journals.source_type CHECK 에 'api' 추가(3단계 전표 초안 제출이 쓴다): 같은 이유로 표를 새로 만든다. 전표는 다른 표(줄·근거·지급·채무·자금 이동·전표 대기)가 참조하므로
--    첫 줄의 fk-off 지시로 외래키 검사를 끄고 적용한 뒤 foreign_key_check 로 위반이 없음을 확인한다. 색인·트리거는 모두 새 표에 다시 만든다(내용은 옛 정의와 같다).
--  - api_tokens: pli_ 토큰(해시만 저장). 발급·인증·속도 제한·화면은 platform 몫이고 이 단계는 표·제약만 둔다.

-- ---- audit_logs ----
CREATE TABLE audit_logs_new (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    actor_id    INTEGER NOT NULL,                     -- 사용자 없는 사건은 0 (FK 없음: 기록은 사용자 삭제와 무관)
    actor_role  TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw','cli','scim','api')),
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('success','denied','error')),
    diff        TEXT NOT NULL DEFAULT '[]',           -- JSON [{key,old,new}]
    detail      TEXT NOT NULL DEFAULT '',
    prev_hash   TEXT NOT NULL,                        -- 첫 레코드는 빈 문자열
    hash        TEXT NOT NULL UNIQUE
);
INSERT INTO audit_logs_new (seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash)
    SELECT seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash FROM audit_logs ORDER BY seq;
DROP TABLE audit_logs;
ALTER TABLE audit_logs_new RENAME TO audit_logs;
CREATE INDEX idx_audit_target ON audit_logs(target_type, target_id);
CREATE INDEX idx_audit_actor  ON audit_logs(actor_id);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE INDEX idx_audit_channel_at ON audit_logs(channel, at);
CREATE TRIGGER trg_audit_no_update BEFORE UPDATE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;
CREATE TRIGGER trg_audit_no_delete BEFORE DELETE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;

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
    source_type   TEXT NOT NULL DEFAULT '' CHECK (source_type IN ('','verdict','approval_doc','payment','card_txn','api')),
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

-- ---- API 토큰·속도 제한·멱등 ----
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    token_prefix TEXT NOT NULL,                       -- 목록 표시용 앞 8자(pli_ + 4자)
    token_hash   TEXT NOT NULL UNIQUE,                -- SHA-256, 원문은 저장하지 않는다
    scopes       TEXT NOT NULL,                       -- JSON 문자열 배열(범위 이름, 정렬·중복 없음)
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,                       -- 만료 필수
    revoked_at   TEXT,
    last_used_at TEXT,
    allow_cidr   TEXT NOT NULL DEFAULT ''             -- 쉼표 구분 CIDR/IP. 비면 전역 PL_API_ALLOW_CIDR 만 적용
);
CREATE INDEX idx_api_tokens_creator ON api_tokens(created_by);
CREATE TABLE api_rate_windows (                       -- 분 창 카운터(mcp_usage 와 같은 방식: 다중 프로세스·재시작에도 맞다)
    token_id INTEGER NOT NULL REFERENCES api_tokens(id),
    minute   INTEGER NOT NULL,
    calls    INTEGER NOT NULL DEFAULT 0,
    denied   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_id, minute)
);
CREATE TABLE api_idempotency (                        -- 쓰기 POST 의 Idempotency-Key(3단계). 첫 응답의 상태와 결과 참조만 둔다(본문 값 없음)
    token_id     INTEGER NOT NULL REFERENCES api_tokens(id),
    idem_key     TEXT NOT NULL CHECK (length(idem_key) BETWEEN 1 AND 64),
    request_hash TEXT NOT NULL,
    status_code  INTEGER NOT NULL,
    result_ref   TEXT NOT NULL DEFAULT '',            -- 예: journal:<번호>
    created_at   TEXT NOT NULL,
    PRIMARY KEY (token_id, idem_key)
);

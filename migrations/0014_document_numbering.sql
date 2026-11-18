-- 0014: 전자결재 표준 단계 1 (b)~(d) (B23, 명세 4절). 이 파일은 단계 1 의 구현이 진행되는 동안 표를 더해 간다.
-- (b) 문서 번호 일련번호: 범위 (양식, 기안 부서 조직 번호, 연도)마다 마지막 번호. 채번은 상신 트랜잭션 안에서 UPDATE 로 올리므로
--     쓰기 연결이 하나인 환경에서 같은 번호가 두 건에 주어지지 않는다(문서 번호 유일 제약이 최후 방어선).
CREATE TABLE document_counters (
    form_id       INTEGER NOT NULL REFERENCES approval_forms(id),
    scope_unit_id INTEGER NOT NULL DEFAULT 0, -- 0 = 부서 범위 없음({unit} 이 없는 규칙)
    year          INTEGER NOT NULL DEFAULT 0, -- 0 = 연도 범위 없음(reset: never)
    last_seq      INTEGER NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    PRIMARY KEY (form_id, scope_unit_id, year)
);

-- 문서 번호는 이미 purchase_requests.no 의 UNIQUE 제약이 막는다(임시저장 문서는 TMP- 접두 임시 번호).

-- (c) 결재선 역할(결재·합의·참조·수신). 명세 3.6·3.7.
-- 1) approval_steps: 합의 단계 상태(agreed·objected)와 건너뜀(skipped)을 허용하고 단계 묶음(stage: 같은 번호의 합의는 병렬)·필수 여부·결재자 지정
--    방식(source)을 더한다. SQLite 는 CHECK 를 바꿀 수 없어 0011 처럼 표를 새로 만들어 복사한다(이 표를 참조하는 외래 키는 없다).
CREATE TABLE approval_steps_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL,
    step_kind    TEXT NOT NULL CHECK (length(step_kind) > 0),
    status       TEXT NOT NULL CHECK (status IN ('waiting','pending','approved','rejected','agreed','objected','skipped')),
    approver_id  INTEGER REFERENCES users(id),
    comment      TEXT NOT NULL DEFAULT '',
    decided_at   TEXT,
    on_behalf_of INTEGER REFERENCES users(id),
    role         TEXT NOT NULL DEFAULT 'approve' CHECK (role IN ('approve','agree')),
    unit_id      INTEGER,
    unit_path    TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    acting       INTEGER NOT NULL DEFAULT 0 CHECK (acting IN (0,1)),
    raised_note  TEXT NOT NULL DEFAULT '',
    layer_note   TEXT NOT NULL DEFAULT '',
    stage        INTEGER NOT NULL DEFAULT 0,
    required     INTEGER NOT NULL DEFAULT 0 CHECK (required IN (0,1)),
    source       TEXT NOT NULL DEFAULT '',
    UNIQUE (pr_id, round, seq)
);
INSERT INTO approval_steps_new (id, pr_id, round, seq, step_kind, status, approver_id, comment, decided_at, on_behalf_of, role, unit_id, unit_path, title, acting, raised_note, layer_note, stage, required, source)
SELECT id, pr_id, round, seq, step_kind, status, approver_id, comment, decided_at, on_behalf_of, role, unit_id, unit_path, title, acting, raised_note, layer_note, seq, 1, 'org_level' FROM approval_steps;
DROP TABLE approval_steps;
ALTER TABLE approval_steps_new RENAME TO approval_steps;
CREATE TRIGGER trg_approval_steps_snapshot_fixed BEFORE UPDATE OF step_kind, seq, role, unit_id, unit_path, title, acting, raised_note, layer_note, stage, required, source ON approval_steps
BEGIN SELECT RAISE(ABORT, '결재선 스냅샷은 수정할 수 없습니다')
    WHERE NEW.step_kind IS NOT OLD.step_kind OR NEW.seq IS NOT OLD.seq OR NEW.role IS NOT OLD.role OR NEW.unit_id IS NOT OLD.unit_id
       OR NEW.unit_path IS NOT OLD.unit_path OR NEW.title IS NOT OLD.title OR NEW.acting IS NOT OLD.acting
       OR NEW.raised_note IS NOT OLD.raised_note OR NEW.layer_note IS NOT OLD.layer_note
       OR NEW.stage IS NOT OLD.stage OR NEW.required IS NOT OLD.required OR NEW.source IS NOT OLD.source;
END;
CREATE TRIGGER trg_approval_steps_no_delete BEFORE DELETE ON approval_steps
BEGIN SELECT RAISE(ABORT, '결재 단계는 삭제할 수 없습니다'); END;

-- 2) approval_history: 합의 동의(agree)·이견(object). 자기 결재 금지 트리거는 합의에도 적용한다. 위임은 승인·반려에만 쓸 수 있다(합의 위임 불가, D11).
CREATE TABLE approval_history_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    action       TEXT NOT NULL CHECK (action IN ('submit','resubmit','approve','reject','withdraw','reassign','agree','object')),
    comment      TEXT NOT NULL DEFAULT '',
    at           TEXT NOT NULL,
    on_behalf_of INTEGER REFERENCES users(id)
);
INSERT INTO approval_history_new (id, pr_id, round, seq, actor_id, action, comment, at, on_behalf_of)
SELECT id, pr_id, round, seq, actor_id, action, comment, at, on_behalf_of FROM approval_history;
DROP TABLE approval_history;
ALTER TABLE approval_history_new RENAME TO approval_history;
CREATE INDEX idx_ah_pr ON approval_history(pr_id);
CREATE TRIGGER trg_approval_history_no_update BEFORE UPDATE ON approval_history
BEGIN SELECT RAISE(ABORT, 'approval_history 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_approval_history_no_delete BEFORE DELETE ON approval_history
BEGIN SELECT RAISE(ABORT, 'approval_history 는 삭제할 수 없습니다'); END;
CREATE TRIGGER trg_approval_no_self BEFORE INSERT ON approval_history
WHEN NEW.action IN ('approve','reject','agree','object')
BEGIN
    SELECT RAISE(ABORT, '본인이 올린 요청은 결재할 수 없습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.actor_id;
END;
CREATE TRIGGER trg_approval_delegated_rules BEFORE INSERT ON approval_history
WHEN NEW.on_behalf_of IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, '위임 결재는 승인·반려에만 쓸 수 있습니다')
    WHERE NEW.action NOT IN ('approve','reject');
    SELECT RAISE(ABORT, '본인을 대신해 결재할 수 없습니다')
    WHERE NEW.on_behalf_of = NEW.actor_id;
    SELECT RAISE(ABORT, '유효한 위임이 없습니다')
    WHERE NOT EXISTS (
        SELECT 1 FROM approval_delegations d
        WHERE d.delegator_id = NEW.on_behalf_of AND d.delegate_id = NEW.actor_id
          AND d.start_at <= NEW.at AND NEW.at < d.end_at
          AND (d.revoked_at IS NULL OR d.revoked_at > NEW.at));
END;
CREATE TRIGGER trg_approval_withdraw_by_requester BEFORE INSERT ON approval_history
WHEN NEW.action = 'withdraw'
BEGIN
    SELECT RAISE(ABORT, '회수는 요청자 본인만 할 수 있습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) <> NEW.actor_id;
END;

-- 3) 금액 구간 규칙은 양식별이다(form_id). 기존 행은 구매 품의서(1)이고, 지출 결의서(3)용 사본을 만든다(따로 바꿀 수 있다).
CREATE TABLE approval_line_rules_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    form_id    INTEGER NOT NULL DEFAULT 1 REFERENCES approval_forms(id),
    min_amount INTEGER NOT NULL,
    max_amount INTEGER,
    seq        INTEGER NOT NULL,
    step_kind  TEXT NOT NULL CHECK (length(step_kind) > 0),
    UNIQUE (form_id, min_amount, seq)
);
INSERT INTO approval_line_rules_new (id, form_id, min_amount, max_amount, seq, step_kind)
SELECT id, 1, min_amount, max_amount, seq, step_kind FROM approval_line_rules;
INSERT INTO approval_line_rules_new (form_id, min_amount, max_amount, seq, step_kind)
SELECT 3, min_amount, max_amount, seq, step_kind FROM approval_line_rules;
DROP TABLE approval_line_rules;
ALTER TABLE approval_line_rules_new RENAME TO approval_line_rules;

-- 4) 참조·수신 대상 스냅샷(상신 때 저장, 추가 전용). 수신 처리(접수 등)는 단계 2 의 표가 맡는다.
CREATE TABLE document_recipients (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id      INTEGER NOT NULL REFERENCES purchase_requests(id),
    round       INTEGER NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('cc','receive')),
    target_type TEXT NOT NULL CHECK (target_type IN ('user','unit')),
    target_id   INTEGER NOT NULL,
    name_at     TEXT NOT NULL,
    path_at     TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    UNIQUE (doc_id, round, kind, target_type, target_id)
);
CREATE INDEX idx_doc_recipients_target ON document_recipients(target_type, target_id);
CREATE TRIGGER trg_document_recipients_no_update BEFORE UPDATE ON document_recipients
BEGIN SELECT RAISE(ABORT, 'document_recipients 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_document_recipients_no_delete BEFORE DELETE ON document_recipients
BEGIN SELECT RAISE(ABORT, 'document_recipients 는 삭제할 수 없습니다'); END;

-- 5) 결재선 스냅샷에 양식 버전과 기안자 편집 내용(JSON)을 남긴다(approval_lines 는 추가 전용이라 INSERT 때만 채운다).
ALTER TABLE approval_lines ADD COLUMN form_version_id INTEGER REFERENCES approval_form_versions(id);
ALTER TABLE approval_lines ADD COLUMN edits_json TEXT NOT NULL DEFAULT '';

-- 6) 부서 동료 열람 여부(문서 단위 스냅샷). 구매 품의서는 1(기존 규칙), 양식 문서는 양식 설정(dept_visible)을 기안 때 복사한다.
ALTER TABLE purchase_requests ADD COLUMN dept_visible INTEGER NOT NULL DEFAULT 1 CHECK (dept_visible IN (0,1));

-- 7) 임시저장 문서의 결재선 편집(기안자가 더한 합의·참조·수신, JSON). 상신하면 approval_lines.edits_json 으로 스냅샷된다.
ALTER TABLE purchase_requests ADD COLUMN draft_edits TEXT NOT NULL DEFAULT '';

-- 8) 개인 결재선(명세 3.9): 기안자가 자주 쓰는 편집 부분(합의·참조·수신 대상, 병렬 묶음)을 이름 붙여 저장한다. 자동 단계는 저장하지 않는다.
CREATE TABLE personal_lines (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    name       TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 40),
    form_code  TEXT NOT NULL DEFAULT '',
    edits_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (user_id, name)
);

-- 9) 첨부(명세 3.15, D16): DB 에 BLOB 로 저장한다. 문서에 붙기 전(doc_id NULL)에는 올린 사람만 보고 24시간 뒤 정리한다. 상신 뒤에는 바뀌지 않는다.
CREATE TABLE document_attachments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id       INTEGER REFERENCES purchase_requests(id),
    uploaded_by  INTEGER NOT NULL REFERENCES users(id),
    filename     TEXT NOT NULL,
    size         INTEGER NOT NULL CHECK (size >= 0),
    sha256       TEXT NOT NULL,
    content_type TEXT NOT NULL,
    content      BLOB NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_doc_attachments_doc ON document_attachments(doc_id);
-- 문서에 붙은 첨부는 상신된 뒤(draft·rejected 가 아님) 문서와의 연결을 바꿀 수 없다.
CREATE TRIGGER trg_doc_attachments_fixed_after_submit BEFORE UPDATE OF doc_id ON document_attachments
WHEN OLD.doc_id IS NOT NULL AND NEW.doc_id IS NOT OLD.doc_id
 AND (SELECT status FROM purchase_requests WHERE id = OLD.doc_id) NOT IN ('draft', 'rejected')
BEGIN SELECT RAISE(ABORT, '상신된 문서의 첨부는 바꿀 수 없습니다'); END;

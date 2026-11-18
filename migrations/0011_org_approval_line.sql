-- 0011: 조직도 기반 결재선·열람 범위·결재자 재지정·앱 안 알림(B21). 작성: erp.
--
-- 1) approval_line_rules·approval_steps 의 step_kind CHECK 를 없앤다. 허용하는 종류(team_lead·dept_head·division_head·ceo·finance)와
--    속성은 코드의 종류 표(internal/service/approval_line.go)가 검증한다(B23 이 합의·참조·수신을 더하기 쉽게, 명세 4.1).
--    SQLite 는 CHECK 를 바꿀 수 없어 새 표에 기존 행을 id 까지 복사하고 교체한다. 이 표들을 참조하는 외래 키는 없다.
-- 2) 상신 시점 스냅샷: approval_lines(차수마다 1행), approval_steps 의 스냅샷 열, approval_step_assignees(직책 단계 배정자).
-- 3) 관리자 재지정 기록 approval_step_reassignments 와 "단계의 현재 결재자" 뷰 approval_step_current(후보 계산의 유일한 정의).
-- 4) 앱 안 알림 notifications(읽음 처리 외에는 바뀌지 않는다).
-- 5) approval_history.action 에 'reassign' 추가(0006 과 같은 표 재작성, 트리거 재생성).
-- 6) 요청 귀속 조직 purchase_requests.owner_unit_id(조직 서비스가 부서를 같은 번호의 부서 종류 조직으로 만든다: B20 11.4).
-- 7) 기존 진행 중 건의 결재 전 직책 단계는 지금 규칙(같은 부서·같은 직책의 재직 manager)의 후보를 배정자로 고정한다(동작 불변).
-- 8) 0007 의 요청자 서열 트리거는 없앤다. 서열이 users.position(재무 3·부서장 2·그 밖 1)에서 조직장의 층(팀 1~대표 4)으로 바뀌어
--    호환 투영 값(본부장·대표도 dept_head)만으로는 DB 가 같은 규칙을 계산할 수 없다. 규칙은 서비스(rankOf)가 지킨다(명세 3.6).

-- ── 1) 직책 종류 CHECK 제거 ─────────────────────────────────────────────
CREATE TABLE approval_line_rules_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    min_amount INTEGER NOT NULL,
    max_amount INTEGER,
    seq        INTEGER NOT NULL,
    step_kind  TEXT NOT NULL CHECK (length(step_kind) > 0),
    UNIQUE (min_amount, seq)
);
INSERT INTO approval_line_rules_new (id, min_amount, max_amount, seq, step_kind)
SELECT id, min_amount, max_amount, seq, step_kind FROM approval_line_rules;
DROP TABLE approval_line_rules;
ALTER TABLE approval_line_rules_new RENAME TO approval_line_rules;
-- D10(사용자 확정): 1억 원 이상 구간에 본부장 단계를 더한다. 기존 "1천만 원 이상" 구간은 상한을 1억 원으로 줄인다(경계값 1억 원은 위 구간).
UPDATE approval_line_rules SET max_amount = 100000000 WHERE min_amount = 10000000 AND max_amount IS NULL;
INSERT INTO approval_line_rules (min_amount, max_amount, seq, step_kind) VALUES
    (100000000, NULL, 1, 'team_lead'),
    (100000000, NULL, 2, 'dept_head'),
    (100000000, NULL, 3, 'division_head'),
    (100000000, NULL, 4, 'finance');

CREATE TABLE approval_steps_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id       INTEGER NOT NULL REFERENCES purchase_requests(id),
    round       INTEGER NOT NULL,
    seq         INTEGER NOT NULL,
    step_kind   TEXT NOT NULL CHECK (length(step_kind) > 0),
    status      TEXT NOT NULL CHECK (status IN ('waiting','pending','approved','rejected')),
    approver_id INTEGER REFERENCES users(id),
    comment     TEXT NOT NULL DEFAULT '',
    decided_at  TEXT,
    on_behalf_of INTEGER REFERENCES users(id),
    -- 스냅샷 열(상신 때만 쓰고 바뀌지 않는다). 도입 이전 건은 비어 있다.
    role        TEXT NOT NULL DEFAULT 'approve',
    unit_id     INTEGER,
    unit_path   TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    acting      INTEGER NOT NULL DEFAULT 0 CHECK (acting IN (0,1)),
    raised_note TEXT NOT NULL DEFAULT '',
    layer_note  TEXT NOT NULL DEFAULT '',
    UNIQUE (pr_id, round, seq)
);
INSERT INTO approval_steps_new (id, pr_id, round, seq, step_kind, status, approver_id, comment, decided_at, on_behalf_of)
SELECT id, pr_id, round, seq, step_kind, status, approver_id, comment, decided_at, on_behalf_of FROM approval_steps;
DROP TABLE approval_steps;
ALTER TABLE approval_steps_new RENAME TO approval_steps;

CREATE TRIGGER trg_approval_steps_snapshot_fixed BEFORE UPDATE OF step_kind, seq, role, unit_id, unit_path, title, acting, raised_note, layer_note ON approval_steps
BEGIN SELECT RAISE(ABORT, '결재선 스냅샷은 수정할 수 없습니다')
    WHERE NEW.step_kind IS NOT OLD.step_kind OR NEW.seq IS NOT OLD.seq OR NEW.role IS NOT OLD.role OR NEW.unit_id IS NOT OLD.unit_id
       OR NEW.unit_path IS NOT OLD.unit_path OR NEW.title IS NOT OLD.title OR NEW.acting IS NOT OLD.acting
       OR NEW.raised_note IS NOT OLD.raised_note OR NEW.layer_note IS NOT OLD.layer_note;
END;

-- ── 2) 스냅샷 ───────────────────────────────────────────────────────────
CREATE TABLE approval_lines (
    pr_id               INTEGER NOT NULL REFERENCES purchase_requests(id),
    round               INTEGER NOT NULL,
    as_of_day           TEXT NOT NULL,
    requester_unit_id   INTEGER NOT NULL,
    requester_unit_path TEXT NOT NULL,
    requester_rank      TEXT NOT NULL DEFAULT '',
    notes               TEXT NOT NULL DEFAULT '[]',
    created_at          TEXT NOT NULL,
    PRIMARY KEY (pr_id, round)
);
CREATE TRIGGER trg_approval_lines_no_update BEFORE UPDATE ON approval_lines
BEGIN SELECT RAISE(ABORT, 'approval_lines 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_approval_lines_no_delete BEFORE DELETE ON approval_lines
BEGIN SELECT RAISE(ABORT, 'approval_lines 는 삭제할 수 없습니다'); END;

-- 직책 단계의 배정자(재무 단계는 사람이 아니라 역할 풀이라 행이 없다). 도입 이전 건은 한 단계에 여러 명일 수 있다.
CREATE TABLE approval_step_assignees (
    pr_id   INTEGER NOT NULL REFERENCES purchase_requests(id),
    round   INTEGER NOT NULL,
    seq     INTEGER NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id),
    name_at TEXT NOT NULL,
    PRIMARY KEY (pr_id, round, seq, user_id)
);
CREATE INDEX idx_asa_user ON approval_step_assignees(user_id);
CREATE TRIGGER trg_approval_step_assignees_no_update BEFORE UPDATE ON approval_step_assignees
BEGIN SELECT RAISE(ABORT, 'approval_step_assignees 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_approval_step_assignees_no_delete BEFORE DELETE ON approval_step_assignees
BEGIN SELECT RAISE(ABORT, 'approval_step_assignees 는 삭제할 수 없습니다'); END;
-- 자기 결재 금지의 최후 방어선: 요청자가 자기 요청의 배정자가 될 수 없다.
CREATE TRIGGER trg_approval_step_assignees_not_requester BEFORE INSERT ON approval_step_assignees
BEGIN SELECT RAISE(ABORT, '요청자 본인은 결재자가 될 수 없습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.user_id;
END;

-- ── 3) 결재자 재지정 기록(추가 전용)과 단계의 현재 결재자 ───────────────────────
CREATE TABLE approval_step_reassignments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL,
    from_user_id INTEGER NOT NULL REFERENCES users(id),
    from_name_at TEXT NOT NULL,
    to_user_id   INTEGER NOT NULL REFERENCES users(id),
    to_name_at   TEXT NOT NULL,
    cause        TEXT NOT NULL CHECK (cause IN ('leave','transfer','absence','other')),
    reason       TEXT NOT NULL CHECK (length(trim(reason)) > 0 AND length(reason) <= 500),
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_asr_step ON approval_step_reassignments(pr_id, round, seq, id);
CREATE TRIGGER trg_approval_step_reassignments_no_update BEFORE UPDATE ON approval_step_reassignments
BEGIN SELECT RAISE(ABORT, 'approval_step_reassignments 는 수정할 수 없습니다'); END;
CREATE TRIGGER trg_approval_step_reassignments_no_delete BEFORE DELETE ON approval_step_reassignments
BEGIN SELECT RAISE(ABORT, 'approval_step_reassignments 는 삭제할 수 없습니다'); END;
CREATE TRIGGER trg_approval_step_reassignments_not_requester BEFORE INSERT ON approval_step_reassignments
BEGIN SELECT RAISE(ABORT, '요청자 본인은 결재자가 될 수 없습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.to_user_id;
END;

-- 단계의 현재 결재자: 재지정이 없으면 스냅샷 배정자(들), 있으면 마지막 재지정의 새 결재자 한 사람. 후보 계산(approversOf)·열람·결재함이 이 뷰만 쓴다.
CREATE VIEW approval_step_current AS
SELECT a.pr_id, a.round, a.seq, a.user_id
  FROM approval_step_assignees a
 WHERE NOT EXISTS (SELECT 1 FROM approval_step_reassignments r WHERE r.pr_id = a.pr_id AND r.round = a.round AND r.seq = a.seq)
UNION ALL
SELECT r.pr_id, r.round, r.seq, r.to_user_id
  FROM approval_step_reassignments r
 WHERE r.id = (SELECT MAX(r2.id) FROM approval_step_reassignments r2 WHERE r2.pr_id = r.pr_id AND r2.round = r.round AND r2.seq = r.seq);

-- ── 4) 앱 안 알림(명세 3.9). 종류는 열린 문자열이다. 읽음 처리(read_at)만 바뀐다. ───────────
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    link       TEXT NOT NULL DEFAULT '',
    ref_type   TEXT NOT NULL DEFAULT '',
    ref_id     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    read_at    TEXT
);
CREATE INDEX idx_notifications_user ON notifications(user_id, read_at, id);
CREATE TRIGGER trg_notifications_only_read BEFORE UPDATE ON notifications
BEGIN SELECT RAISE(ABORT, 'notifications 는 읽음 처리(read_at)만 바꿀 수 있습니다')
    WHERE NEW.id IS NOT OLD.id OR NEW.user_id IS NOT OLD.user_id OR NEW.kind IS NOT OLD.kind OR NEW.title IS NOT OLD.title
       OR NEW.body IS NOT OLD.body OR NEW.link IS NOT OLD.link OR NEW.ref_type IS NOT OLD.ref_type OR NEW.ref_id IS NOT OLD.ref_id
       OR NEW.created_at IS NOT OLD.created_at;
END;
CREATE TRIGGER trg_notifications_no_delete BEFORE DELETE ON notifications
BEGIN SELECT RAISE(ABORT, 'notifications 는 삭제할 수 없습니다'); END;

-- ── 5) 결재 이력에 결재자 변경('reassign') ──────────────────────────────────
CREATE TABLE approval_history_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    action       TEXT NOT NULL CHECK (action IN ('submit','resubmit','approve','reject','withdraw','reassign')),
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
WHEN NEW.action IN ('approve','reject')
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

-- ── 8) 요청자 서열 트리거 제거(위 설명) ─────────────────────────────────────
DROP TRIGGER IF EXISTS trg_approval_delegated_requester_rank;

-- ── 6) 요청 귀속 조직 ───────────────────────────────────────────────────
ALTER TABLE purchase_requests ADD COLUMN owner_unit_id INTEGER;
UPDATE purchase_requests SET owner_unit_id = department_id;
CREATE INDEX idx_pr_owner_unit ON purchase_requests(owner_unit_id);

-- ── 7) 기존 진행 중 건의 결재 전 직책 단계: 지금 규칙의 후보를 배정자로 고정 ──────────────
INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at)
SELECT s.pr_id, s.round, s.seq, u.id, u.name
  FROM approval_steps s
  JOIN purchase_requests p ON p.id = s.pr_id AND p.round = s.round AND p.status IN ('submitted','in_review')
  JOIN users u ON u.active = 1 AND u.role = 'manager' AND u.position = s.step_kind AND u.department_id = p.department_id
 WHERE s.status IN ('waiting','pending') AND s.step_kind IN ('team_lead','dept_head') AND u.id <> p.requester_id;

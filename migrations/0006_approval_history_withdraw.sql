-- 0006: 결재 이력에 회수('withdraw')를 한 줄로 남긴다(B11 ②).
-- SQLite 는 CHECK 제약을 바꾸려면 테이블을 다시 만들어야 한다. 새 테이블에 기존 행을 그대로(id 포함) 복사하고,
-- 옛 테이블을 지운 뒤 이름을 바꾸고, 인덱스와 트리거(불변 2개·자기 결재 금지·위임 규칙)를 같은 정의로 다시 만든다.
-- approval_history 를 참조하는 외래 키는 없다.
CREATE TABLE approval_history_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    action       TEXT NOT NULL CHECK (action IN ('submit','resubmit','approve','reject','withdraw')),
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

-- 자기 결재 금지(0001 과 같다): 요청자가 같은 요청의 승인·반려 이력을 남길 수 없다.
-- 회수('withdraw')는 요청자 본인이 행위자이므로 이 트리거의 대상이 아니다.
CREATE TRIGGER trg_approval_no_self BEFORE INSERT ON approval_history
WHEN NEW.action IN ('approve','reject')
BEGIN
    SELECT RAISE(ABORT, '본인이 올린 요청은 결재할 수 없습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.actor_id;
END;

-- 위임 규칙(0002 와 같다).
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

-- 새 규칙: 회수는 그 요청의 요청자 본인만 남길 수 있다(서비스 검사의 최후 방어선).
CREATE TRIGGER trg_approval_withdraw_by_requester BEFORE INSERT ON approval_history
WHEN NEW.action = 'withdraw'
BEGIN
    SELECT RAISE(ABORT, '회수는 요청자 본인만 할 수 있습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) <> NEW.actor_id;
END;

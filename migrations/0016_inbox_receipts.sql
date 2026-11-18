-- 0016: 전자결재 표준 단계 2 (B23, 명세 4절): 문서함·수신.
-- 1) document_reads: 문서 단위 읽음 상태. 안 읽음 = 한 번도 열지 않았거나 마지막으로 연 뒤에 문서가 갱신된 것.
-- 2) document_receipts: 결재 완료 때 수신처마다 생기는 수신 건(접수 대기 → 접수 → 담당자 지정 → 처리 완료, 반송). 처리 상태와 처리자·시각을 남긴다.
-- 3) unit_receivers: 조직장이 지정한 수신 담당자(그 조직의 접수·처리를 맡는다).
-- 4) approval_history: 상신 취소(cancel_submit, D9 확정). 0014 와 같은 방식으로 표를 다시 만들고 트리거를 되살린다.

CREATE TABLE document_reads (
    doc_id  INTEGER NOT NULL REFERENCES purchase_requests(id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    read_at TEXT NOT NULL,
    PRIMARY KEY (doc_id, user_id)
);
CREATE INDEX idx_document_reads_user ON document_reads(user_id);

CREATE TABLE document_receipts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id       INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    unit_id      INTEGER NOT NULL,
    unit_name    TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('pending','received','assigned','done','returned')),
    created_at   TEXT NOT NULL,
    received_by  INTEGER REFERENCES users(id),
    received_at  TEXT,
    assignee_id  INTEGER REFERENCES users(id),
    assigned_by  INTEGER REFERENCES users(id),
    assigned_at  TEXT,
    done_by      INTEGER REFERENCES users(id),
    done_at      TEXT,
    returned_by  INTEGER REFERENCES users(id),
    returned_at  TEXT,
    comment      TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    UNIQUE (doc_id, round, unit_id)
);
CREATE INDEX idx_document_receipts_unit ON document_receipts(unit_id, status);
CREATE INDEX idx_document_receipts_doc ON document_receipts(doc_id);
-- 처리가 끝난 수신 건(완료·반송)은 상태를 되돌릴 수 없다. 처리 시각·처리자는 한 번 채워지면 바뀌지 않는다.
CREATE TRIGGER trg_document_receipts_final BEFORE UPDATE ON document_receipts
WHEN OLD.status IN ('done','returned')
BEGIN SELECT RAISE(ABORT, '처리가 끝난 수신 건은 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_document_receipts_no_delete BEFORE DELETE ON document_receipts
BEGIN SELECT RAISE(ABORT, '수신 건은 삭제할 수 없습니다'); END;

CREATE TABLE unit_receivers (
    unit_id    INTEGER NOT NULL,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    added_by   INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (unit_id, user_id)
);
CREATE INDEX idx_unit_receivers_user ON unit_receivers(user_id);

-- ── 결재 이력에 상신 취소 ──────────────────────────────────────────────
CREATE TABLE approval_history_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES purchase_requests(id),
    round        INTEGER NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    actor_id     INTEGER NOT NULL REFERENCES users(id),
    action       TEXT NOT NULL CHECK (action IN ('submit','resubmit','approve','reject','withdraw','reassign','agree','object','cancel_submit')),
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
WHEN NEW.action IN ('withdraw', 'cancel_submit')
BEGIN
    SELECT RAISE(ABORT, '회수·상신 취소는 요청자 본인만 할 수 있습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) <> NEW.actor_id;
END;
-- 상신 취소는 현재 차수에 처리 이력(승인·반려·동의·이견)이 없을 때만 DB 도 받아 준다(서비스 검사의 최후 방어선).
CREATE TRIGGER trg_approval_cancel_untouched BEFORE INSERT ON approval_history
WHEN NEW.action = 'cancel_submit'
BEGIN
    SELECT RAISE(ABORT, '이미 처리된 문서의 상신은 취소할 수 없습니다')
    WHERE EXISTS (SELECT 1 FROM approval_history h WHERE h.pr_id = NEW.pr_id AND h.round = NEW.round AND h.action IN ('approve','reject','agree','object'));
END;

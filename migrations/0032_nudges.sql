-- 0032: 결재 독촉(B29 ④, 5·9절). 기안자가 지연된 단계(단계 시작 뒤 24시간)의 현재 처리자에게 보내는 독촉의 기록이다.
-- 같은 문서·차수·단계에 KST 달력일 하루 1회(nudge_day 는 KST 날짜): UNIQUE 가 동시 요청 중 한쪽을 막는다.
-- 추가 전용이다: 수정은 막고, 지우기는 문서가 폐기 처리된 뒤(disposed_at 이 채워진 뒤)에만 허용한다(폐기가 독촉 기록을 지운다, 6.3).
CREATE TABLE approval_nudges (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id          INTEGER NOT NULL REFERENCES purchase_requests(id),
    round           INTEGER NOT NULL,
    seq             INTEGER NOT NULL,
    step_title      TEXT NOT NULL DEFAULT '',
    nudged_by       INTEGER NOT NULL REFERENCES users(id),
    nudged_at       TEXT NOT NULL,
    nudge_day       TEXT NOT NULL CHECK (nudge_day GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    recipients      INTEGER NOT NULL CHECK (recipients >= 1),
    recipient_names TEXT NOT NULL DEFAULT '',
    UNIQUE (doc_id, round, seq, nudge_day)
);
CREATE INDEX idx_nudges_doc ON approval_nudges(doc_id, id);
CREATE TRIGGER trg_nudges_no_update BEFORE UPDATE ON approval_nudges
BEGIN SELECT RAISE(ABORT, '독촉 기록은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_nudges_delete_only_disposed BEFORE DELETE ON approval_nudges
WHEN NOT EXISTS (SELECT 1 FROM purchase_requests p WHERE p.id = OLD.doc_id AND p.disposed_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, '독촉 기록은 지울 수 없습니다'); END;

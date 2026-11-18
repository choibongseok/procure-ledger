-- 0029: 전자결재 보존 연한 만료 폐기(B29 ③, 6·9절).
--  1) purchase_requests: 법정 보존(legal_hold, 결재 완료 때 양식에서 스냅샷), 폐기 보류(disposal_hold·hold_reason), 폐기 기록(disposed_at·disposed_by·disposal_reason).
--     기존 완료 문서의 legal_hold 는 양식 코드(PUR·EXP·ADJ = 재무·회계 증빙)로 채운다. 그 밖의 양식은 0 이다.
--  2) 폐기는 본문·첨부·의견을 지우고 껍데기만 남기는 일이다. 결재 이력·알림·수신 건은 불변 트리거로 막혀 있으므로
--     "폐기 처리된 문서(disposed_at 이 채워짐)의 의견·제목·본문 칸을 빈 값으로 바꾸는 갱신"만 허용하도록 트리거를 좁게 다시 만든다.
--     그 밖의 갱신·삭제는 계속 거부한다(시험이 확인한다). 0013·0016 은 이미 적용된 파일이라 고치지 않는다.
ALTER TABLE purchase_requests ADD COLUMN legal_hold INTEGER NOT NULL DEFAULT 0 CHECK (legal_hold IN (0,1));
ALTER TABLE purchase_requests ADD COLUMN disposal_hold INTEGER NOT NULL DEFAULT 0 CHECK (disposal_hold IN (0,1));
ALTER TABLE purchase_requests ADD COLUMN hold_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase_requests ADD COLUMN disposed_at TEXT;
ALTER TABLE purchase_requests ADD COLUMN disposed_by INTEGER REFERENCES users(id);
ALTER TABLE purchase_requests ADD COLUMN disposal_reason TEXT NOT NULL DEFAULT '';

UPDATE purchase_requests SET legal_hold = 1
 WHERE form_id IN (SELECT id FROM approval_forms WHERE code IN ('PUR', 'EXP', 'ADJ'));

-- 폐기 후보 조회(결재 완료·보존 기한 경과·미폐기)를 빠르게 한다.
CREATE INDEX idx_pr_retention ON purchase_requests(retention_until) WHERE retention_until IS NOT NULL AND disposed_at IS NULL;

-- 결재 이력: 폐기된 문서의 의견(comment)을 빈 값으로 바꾸는 갱신만 허용한다. 누가·언제·무엇을 했는지는 바뀌지 않는다.
DROP TRIGGER trg_approval_history_no_update;
CREATE TRIGGER trg_approval_history_no_update BEFORE UPDATE ON approval_history
WHEN NOT (
    NEW.comment = '' AND OLD.comment <> ''
    AND (SELECT disposed_at FROM purchase_requests WHERE id = OLD.pr_id) IS NOT NULL
    AND NEW.id IS OLD.id AND NEW.pr_id IS OLD.pr_id AND NEW.round IS OLD.round AND NEW.seq IS OLD.seq
    AND NEW.actor_id IS OLD.actor_id AND NEW.action IS OLD.action AND NEW.at IS OLD.at AND NEW.on_behalf_of IS OLD.on_behalf_of
)
BEGIN SELECT RAISE(ABORT, 'approval_history 는 수정할 수 없습니다'); END;

-- 알림: 읽음 처리(read_at)와, 폐기된 문서에 딸린 알림의 제목·본문 비우기만 허용한다(본문에 문서 제목이 들어 있다).
DROP TRIGGER trg_notifications_only_read;
CREATE TRIGGER trg_notifications_only_read BEFORE UPDATE ON notifications
BEGIN SELECT RAISE(ABORT, 'notifications 는 읽음 처리(read_at)만 바꿀 수 있습니다')
    WHERE NEW.id IS NOT OLD.id OR NEW.user_id IS NOT OLD.user_id OR NEW.kind IS NOT OLD.kind OR NEW.link IS NOT OLD.link
       OR NEW.ref_type IS NOT OLD.ref_type OR NEW.ref_id IS NOT OLD.ref_id OR NEW.created_at IS NOT OLD.created_at
       OR ((NEW.title IS NOT OLD.title OR NEW.body IS NOT OLD.body)
           AND NOT (OLD.ref_type = 'purchase_request'
                    AND (SELECT disposed_at FROM purchase_requests WHERE id = OLD.ref_id) IS NOT NULL));
END;

-- 수신 건: 처리가 끝난 건은 바뀌지 않는다. 폐기된 문서의 수신 의견(comment·reason)을 비우는 갱신만 예외다.
DROP TRIGGER trg_document_receipts_final;
CREATE TRIGGER trg_document_receipts_final BEFORE UPDATE ON document_receipts
WHEN OLD.status IN ('done','returned') AND NOT (
    NEW.comment = '' AND NEW.reason = ''
    AND (SELECT disposed_at FROM purchase_requests WHERE id = OLD.doc_id) IS NOT NULL
    AND NEW.id IS OLD.id AND NEW.doc_id IS OLD.doc_id AND NEW.round IS OLD.round AND NEW.unit_id IS OLD.unit_id
    AND NEW.unit_name IS OLD.unit_name AND NEW.status IS OLD.status AND NEW.created_at IS OLD.created_at
    AND NEW.received_by IS OLD.received_by AND NEW.received_at IS OLD.received_at
    AND NEW.assignee_id IS OLD.assignee_id AND NEW.assigned_by IS OLD.assigned_by AND NEW.assigned_at IS OLD.assigned_at
    AND NEW.done_by IS OLD.done_by AND NEW.done_at IS OLD.done_at
    AND NEW.returned_by IS OLD.returned_by AND NEW.returned_at IS OLD.returned_at
)
BEGIN SELECT RAISE(ABORT, '처리가 끝난 수신 건은 바꿀 수 없습니다'); END;

-- 폐기 기록은 한 번 채워지면 지우거나 바꿀 수 없다.
CREATE TRIGGER trg_pr_disposal_fixed BEFORE UPDATE OF disposed_at, disposed_by, disposal_reason ON purchase_requests
WHEN OLD.disposed_at IS NOT NULL AND (NEW.disposed_at IS NOT OLD.disposed_at OR NEW.disposed_by IS NOT OLD.disposed_by OR NEW.disposal_reason IS NOT OLD.disposal_reason)
BEGIN SELECT RAISE(ABORT, '폐기 기록은 바꿀 수 없습니다'); END;

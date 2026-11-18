-- 0101: 휴가 신청(B50 12.2,). 신청은 결재 문서(양식 LEAVE) 위에 얹힌다: leave_requests.doc_id 가 문서 번호다.
--  - 상태는 문서 상태의 투영이고 허용 전이 표는 서비스(leaveTransitions)가 한 곳에서 정한다. 아래 트리거는 그 표를 DB 에서도 한 번 더 막는다
--    (끝난 상태 rejected·recalled·canceled 에서 되돌릴 수 없고, approved 는 canceled 로만 갈 수 있으며 신청 행은 지울 수 없다).
--  - reason(사유)은 민감한 개인정보다. 결재 문서 본문 필드에 넣지 않고 이 표에만 둔다(명세 11절). 휴가 종료 뒤 1년이 지나면 서비스가 사유 칸만 비운다.
--  - 시스템 양식 LEAVE: 일반 기안으로는 만들 수 없고(훅이 있는 양식) 휴가 서비스가 만든다. 결재선은 조직도 자동 팀장 한 단계(D9)이며 양식 설정으로 바꿀 수 있다.
CREATE TABLE leave_requests (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id             INTEGER NOT NULL DEFAULT 1,
    user_id               INTEGER NOT NULL REFERENCES users(id),
    doc_id                INTEGER NOT NULL UNIQUE REFERENCES purchase_requests(id),
    leave_code            TEXT NOT NULL,
    start_date            TEXT NOT NULL CHECK (start_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    end_date              TEXT NOT NULL CHECK (end_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    part                  TEXT NOT NULL CHECK (part IN ('full','am','pm','hours')),
    start_time            TEXT NOT NULL DEFAULT '',
    end_time              TEXT NOT NULL DEFAULT '',
    minutes               INTEGER NOT NULL CHECK (minutes > 0),
    balance               TEXT NOT NULL DEFAULT '' CHECK (balance IN ('','annual','compensatory')),
    status                TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','recalled','canceled')),
    reason                TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    delegate_user_id      INTEGER NOT NULL DEFAULT 0,
    absence_delegation_id INTEGER NOT NULL DEFAULT 0,
    proof_attachment_id   INTEGER NOT NULL DEFAULT 0,
    note                  TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);
CREATE INDEX idx_leave_requests_user ON leave_requests(entity_id, user_id, start_date);
CREATE INDEX idx_leave_requests_period ON leave_requests(entity_id, start_date, end_date);
CREATE TRIGGER trg_leave_requests_no_delete BEFORE DELETE ON leave_requests BEGIN SELECT RAISE(ABORT, '휴가 신청은 지울 수 없습니다'); END;
CREATE TRIGGER trg_leave_requests_transition BEFORE UPDATE ON leave_requests
WHEN NEW.status <> OLD.status AND (OLD.status IN ('rejected','recalled','canceled') OR (OLD.status = 'approved' AND NEW.status <> 'canceled'))
BEGIN SELECT RAISE(ABORT, '휴가 신청 상태를 이 방향으로 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_leave_requests_fixed BEFORE UPDATE ON leave_requests
WHEN NEW.user_id IS NOT OLD.user_id OR NEW.doc_id IS NOT OLD.doc_id OR NEW.leave_code IS NOT OLD.leave_code OR NEW.start_date IS NOT OLD.start_date OR NEW.end_date IS NOT OLD.end_date
  OR NEW.part IS NOT OLD.part OR NEW.minutes IS NOT OLD.minutes OR NEW.balance IS NOT OLD.balance OR NEW.entity_id IS NOT OLD.entity_id
BEGIN SELECT RAISE(ABORT, '휴가 신청의 신청 내용은 고칠 수 없습니다(취소 후 새로 신청하세요)'); END;

INSERT INTO approval_forms (code, category, name, kind, active, required_permission, created_at)
VALUES ('LEAVE', 'other', '휴가 신청서', 'general', 1, 'attendance:self', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT id, 1, 'published',
 '{"system":true,"fields":[{"key":"leave_name","label":"휴가 종류","type":"text","required":true,"maxLength":30},{"key":"period","label":"기간","type":"text","required":true,"maxLength":60},{"key":"amount","label":"사용 분량","type":"text","required":true,"maxLength":40}],"body":"휴가 종류: {{leave_name}}\n기간: {{period}}\n사용 분량: {{amount}}","line":{"steps":[{"role":"approve","kind":"team_lead","source":"org_level","level":1,"required":true}]},"recipients":{"cc":[],"receive":[]},"numbering":{"pattern":"{unit}-휴가-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":3,"dept_limit":[],"settings":{"line_edit":"add_only","objection_action":"record","amount_field":"","dept_visible":false,"urgent_allowed":false}}',
 strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
 FROM approval_forms WHERE code = 'LEAVE';
-- 휴가 신청서는 지식 검색(B47)에서 제외한다(사유가 민감하고 양식 설정으로 켤 수 없다: 서비스가 LEAVE 를 검색 대상에서 항상 뺀다).
UPDATE approval_forms SET searchable = 0 WHERE code = 'LEAVE';

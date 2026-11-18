-- 0030: 부재·대결(B29 ①, 3·9절). 부재 설정은 결재 위임(approval_delegations)의 한 종류다(kind='absence').
-- 후보 계산(approversOf)·자격 검사·자기 결재 금지·해제 검사는 위임과 한 곳이라 새 표를 만들지 않는다. 기존 행은 모두 'delegation'.
--  - reason: 부재 사유(vacation 휴가 | trip 출장 | training 교육 | other 기타, 위임은 빈 값). note: 선택 메모(100자 이하).
--  - start_notified_at·end_notified_at: 예정 부재의 시작·종료 알림을 서버 주기 작업이 한 번씩만 만들기 위한 표시(기간·당사자 불변 트리거는 이 열을 막지 않는다).
ALTER TABLE approval_delegations ADD COLUMN kind TEXT NOT NULL DEFAULT 'delegation' CHECK (kind IN ('absence','delegation'));
ALTER TABLE approval_delegations ADD COLUMN reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('','vacation','trip','training','other'));
ALTER TABLE approval_delegations ADD COLUMN note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 100);
ALTER TABLE approval_delegations ADD COLUMN start_notified_at TEXT;
ALTER TABLE approval_delegations ADD COLUMN end_notified_at TEXT;
CREATE INDEX idx_deleg_absence ON approval_delegations(delegator_id, kind, start_at, end_at);

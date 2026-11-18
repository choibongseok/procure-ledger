-- 0007: 요청자 본인이 위임자인 위임 결재의 직책 서열 규칙을 DB 에서도 막는다(B11 ①의 최후 방어선).
-- 서비스·저장소의 규칙과 같은 정의다(internal/repository/delegation_repo.go rankOf·requesterRule):
--   서열 = 재무 역할 3 > 부서장(position='dept_head') 2 > 그 밖의 결재자(팀장) 1.
--   요청자가 위임자(on_behalf_of)인 건은 대신 결재하는 사람(actor)의 서열이 위임자보다 높아야 한다.
--   요청자가 위임자가 아닌 위임 결재에는 아무 제약도 더하지 않는다.
-- 기존 테이블·트리거·행은 건드리지 않고 트리거만 하나 더한다. 서열은 결재 시각의 users 현재 값으로 계산한다(저장소와 같음).
CREATE TRIGGER trg_approval_delegated_requester_rank BEFORE INSERT ON approval_history
WHEN NEW.on_behalf_of IS NOT NULL AND NEW.action IN ('approve','reject')
BEGIN
    SELECT RAISE(ABORT, '요청자 본인의 위임은 상위 직책에게만 결재를 맡길 수 있습니다')
    WHERE (SELECT requester_id FROM purchase_requests WHERE id = NEW.pr_id) = NEW.on_behalf_of
      AND (SELECT CASE WHEN u.role = 'finance' THEN 3 WHEN u.position = 'dept_head' THEN 2 ELSE 1 END FROM users u WHERE u.id = NEW.actor_id)
       <= (SELECT CASE WHEN u.role = 'finance' THEN 3 WHEN u.position = 'dept_head' THEN 2 ELSE 1 END FROM users u WHERE u.id = NEW.on_behalf_of);
END;

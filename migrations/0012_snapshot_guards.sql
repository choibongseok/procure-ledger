-- 0012: 결재선 스냅샷 불변의 빈틈 보강(B21 검증자 지적). 작성: platform.
--
-- 1) approval_steps 는 행 삭제가 막혀 있지 않았다(수정은 trg_approval_steps_snapshot_fixed 가 막는다). 결재 단계는 상신 때 만들어지고
--    그 뒤에는 상태·결재자 기록만 바뀌므로 삭제 경로가 없다.
-- 2) purchase_requests.owner_unit_id(요청 귀속 조직)는 임시저장·반려 뒤 재상신 때만 바뀐다(SetOwner 는 상신 전의 건에만 부른다).
--    상신된 뒤(검토 중·승인·반려 확정 이후 발주 등)에는 직접 UPDATE 로 바꿀 수 없게 한다.
--    반려(rejected)는 재상신으로 새 차수를 시작할 수 있어 허용한다. 처음 값이 비어 있던 건(NULL)을 채우는 것도 허용한다.

CREATE TRIGGER trg_approval_steps_no_delete BEFORE DELETE ON approval_steps
BEGIN SELECT RAISE(ABORT, '결재 단계는 삭제할 수 없습니다'); END;

CREATE TRIGGER trg_pr_owner_unit_fixed BEFORE UPDATE OF owner_unit_id ON purchase_requests
WHEN OLD.status NOT IN ('draft', 'rejected') AND OLD.owner_unit_id IS NOT NULL AND NEW.owner_unit_id IS NOT OLD.owner_unit_id
BEGIN SELECT RAISE(ABORT, '상신된 구매요청의 귀속 조직은 바꿀 수 없습니다'); END;

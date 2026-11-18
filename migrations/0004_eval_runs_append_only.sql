-- 0004: 평가 이력 보호(B9).
-- eval_runs 는 추가 전용이다. 회귀 감지의 기준선이므로 행을 고치거나 지우지 못하게 한다(감사 로그·원장과 같은 방식).
CREATE TRIGGER trg_eval_runs_no_update BEFORE UPDATE ON eval_runs
BEGIN SELECT RAISE(ABORT, 'eval_runs 는 추가 전용입니다'); END;
CREATE TRIGGER trg_eval_runs_no_delete BEFORE DELETE ON eval_runs
BEGIN SELECT RAISE(ABORT, 'eval_runs 는 추가 전용입니다'); END;

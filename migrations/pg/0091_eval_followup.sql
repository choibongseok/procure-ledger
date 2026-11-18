-- 0091 (B46 판정 후속): SQLite 0091_eval_followup.sql 의 짝이다.
CREATE FUNCTION trg_eval_run_items_done_no_update_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF (SELECT status FROM eval_runs WHERE id = OLD.run_id) <> 'running' THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '끝난 실행의 문항 결과는 고칠 수 없습니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_eval_run_items_done_no_update BEFORE UPDATE ON eval_run_items FOR EACH ROW EXECUTE FUNCTION trg_eval_run_items_done_no_update_fn();

CREATE FUNCTION trg_eval_run_items_done_no_delete_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF (SELECT status FROM eval_runs WHERE id = OLD.run_id) <> 'running' THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '끝난 실행의 문항 결과는 고칠 수 없습니다';
  END IF;
  RETURN OLD;
END
$fn$;
CREATE TRIGGER trg_eval_run_items_done_no_delete BEFORE DELETE ON eval_run_items FOR EACH ROW EXECUTE FUNCTION trg_eval_run_items_done_no_delete_fn();

-- expected 의 최소 길이 1 을 0 으로(llm 채점 문항은 기대 답이 비어도 된다).
ALTER TABLE eval_items DROP CONSTRAINT eval_items_expected_check;
ALTER TABLE eval_items ADD CONSTRAINT eval_items_expected_check CHECK (length(expected) <= 4000);

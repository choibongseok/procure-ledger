-- 0054 (B38): 설치 상태. SQLite 0054_setup_state.sql 의 짝이다(초안은 pgbaseline -translate, 검토함).
CREATE TABLE setup_state (
    id BIGINT PRIMARY KEY CHECK (id = 1),
    status       TEXT NOT NULL CHECK (status IN ('pending','done')),
    completed_at TEXT,
    completed_by BIGINT,
    created_at   TEXT NOT NULL,
    CHECK ((status = 'done') = (completed_at IS NOT NULL))
);

INSERT INTO setup_state (id, status, completed_at, completed_by, created_at)
    SELECT 1,
           CASE WHEN EXISTS (SELECT 1 FROM users) THEN 'done' ELSE 'pending' END,
           CASE WHEN EXISTS (SELECT 1 FROM users) THEN to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') END,
           NULL,
           to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"');

CREATE FUNCTION trg_setup_state_one_way_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NOT (OLD.status = 'pending' AND NEW.status = 'done' AND NEW.id = OLD.id AND NEW.created_at = OLD.created_at) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '설치 상태는 pending 에서 done 으로만 바뀝니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_setup_state_one_way BEFORE UPDATE ON setup_state FOR EACH ROW EXECUTE FUNCTION trg_setup_state_one_way_fn();

CREATE FUNCTION trg_setup_state_no_delete_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '설치 상태는 지울 수 없습니다';
  RETURN OLD;
END
$fn$;
CREATE TRIGGER trg_setup_state_no_delete BEFORE DELETE ON setup_state FOR EACH ROW EXECUTE FUNCTION trg_setup_state_no_delete_fn();


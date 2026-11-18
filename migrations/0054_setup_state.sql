-- 0054: 설치 상태(B38, 4.1·11절).
-- setup_state 는 한 행(id=1)이다. 마이그레이션이 만들 때 사용자가 한 명이라도 있으면 done(기존 설치는 마법사가 열리지 않는다), 없으면 pending 이다.
-- pending → done 한 방향만 바뀐다. done 을 되돌리거나 행을 지우거나 다른 값으로 바꾸는 갱신은 트리거가 막는다(다른 표를 비우거나 복구해도 마법사는 다시 열리지 않는다).
CREATE TABLE setup_state (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    status       TEXT NOT NULL CHECK (status IN ('pending','done')),
    completed_at TEXT,
    completed_by INTEGER,
    created_at   TEXT NOT NULL,
    CHECK ((status = 'done') = (completed_at IS NOT NULL))
);
INSERT INTO setup_state (id, status, completed_at, completed_by, created_at)
    SELECT 1,
           CASE WHEN EXISTS (SELECT 1 FROM users) THEN 'done' ELSE 'pending' END,
           CASE WHEN EXISTS (SELECT 1 FROM users) THEN strftime('%Y-%m-%dT%H:%M:%SZ','now') END,
           NULL,
           strftime('%Y-%m-%dT%H:%M:%SZ','now');
CREATE TRIGGER trg_setup_state_one_way BEFORE UPDATE ON setup_state
WHEN NOT (OLD.status = 'pending' AND NEW.status = 'done' AND NEW.id = OLD.id AND NEW.created_at = OLD.created_at)
BEGIN SELECT RAISE(ABORT, '설치 상태는 pending 에서 done 으로만 바뀝니다'); END;
CREATE TRIGGER trg_setup_state_no_delete BEFORE DELETE ON setup_state
BEGIN SELECT RAISE(ABORT, '설치 상태는 지울 수 없습니다'); END;

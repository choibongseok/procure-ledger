-- 0086 (B46 2단계, 명세 4절): 평가 실행에 세트 연결 열·진행 상태, 문항별 결과, 게이트 지정.
-- 기본 골든셋 실행(set_id = 0)은 그대로이고 status 는 'done' 이다.
ALTER TABLE eval_runs ADD COLUMN set_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN set_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN prompt_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_runs ADD COLUMN route_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN mode TEXT NOT NULL DEFAULT 'cli';
ALTER TABLE eval_runs ADD COLUMN started_by INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN status TEXT NOT NULL DEFAULT 'done';
ALTER TABLE eval_runs ADD COLUMN done_items INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN fail_reason TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_eval_runs_set ON eval_runs(set_id, id);

-- 추가 전용(0004)은 끝난 행에 그대로 적용한다. 진행 중(running) 행만 마감 때 한 번 고칠 수 있다.
DROP TRIGGER trg_eval_runs_no_update;
CREATE TRIGGER trg_eval_runs_no_update BEFORE UPDATE ON eval_runs WHEN OLD.status <> 'running'
BEGIN SELECT RAISE(ABORT, 'eval_runs 는 추가 전용입니다'); END;

CREATE TABLE eval_run_items (
    run_id        INTEGER NOT NULL REFERENCES eval_runs(id),
    item_seq      INTEGER NOT NULL,
    pass          INTEGER NOT NULL CHECK (pass IN (0,1)),
    score         INTEGER NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    detail        TEXT NOT NULL DEFAULT '' CHECK (length(detail) <= 300),
    PRIMARY KEY (run_id, item_seq)
);

CREATE TABLE eval_gates (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    purpose_label TEXT NOT NULL UNIQUE CHECK (length(purpose_label) BETWEEN 1 AND 80),
    set_id        INTEGER NOT NULL REFERENCES eval_sets(id),
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    updated_by    INTEGER REFERENCES users(id),
    updated_at    TEXT NOT NULL
);

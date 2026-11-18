-- 0003: 평가 실행 이력(B6, 구현 ai).
-- 시각은 RFC3339 UTC 문자열로 저장한다.
CREATE TABLE eval_runs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    at                TEXT NOT NULL,
    provider          TEXT NOT NULL,
    model             TEXT NOT NULL DEFAULT '',
    total             INTEGER NOT NULL,
    passed            INTEGER NOT NULL,
    pass_rate_pct     INTEGER NOT NULL,
    threshold_pct     INTEGER NOT NULL,
    gate_passed       INTEGER NOT NULL,              -- 0/1
    new_failures_json TEXT NOT NULL DEFAULT '[]',
    new_passes_json   TEXT NOT NULL DEFAULT '[]',
    results_json      TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX idx_eval_runs_at ON eval_runs(at);

-- 0083 (B45 3단계): SQLite 0083_eval_runs_suite.sql 의 짝이다.
ALTER TABLE eval_runs ADD COLUMN suite_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_runs ADD COLUMN latency_ms_total BIGINT NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN cost_micro_usd_total BIGINT NOT NULL DEFAULT 0;

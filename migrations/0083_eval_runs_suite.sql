-- 0083 (B45 3단계, 명세 4·11절): 평가 실행의 비교용 열. suite_hash 는 케이스 모음(묶음·ID·치명 여부)의 해시라 같은 골든셋끼리만 한 표에 놓는다.
ALTER TABLE eval_runs ADD COLUMN suite_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_runs ADD COLUMN latency_ms_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN cost_micro_usd_total INTEGER NOT NULL DEFAULT 0;

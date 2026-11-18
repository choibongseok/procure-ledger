-- 0053 (B38 D7): 감사 기록 경로에 'cli' 추가. SQLite 0053_audit_channel_cli.sql 의 짝이다.
-- PG 는 제약만 바꾼다(표를 다시 만들지 않는다). 기존 행·해시 체인·추가 전용 트리거에 영향이 없다(제약 추가는 UPDATE 트리거를 실행하지 않는다).
ALTER TABLE audit_logs DROP CONSTRAINT audit_logs_channel_check;
ALTER TABLE audit_logs ADD CONSTRAINT audit_logs_channel_check CHECK (channel IN ('web','mcp','llmgw','cli'));

-- 0008: AI 연결(MCP) 사용 한도 창과 감사 조회 인덱스(B13).
-- mcp_rate_window 는 개인 토큰별 분당 호출 수를 DB 기준 고정 분 창(unix 분)으로 센다. 서버 /mcp 와 erp-mcp 가
-- 같은 DB 를 써도 한도가 새지 않게 하려고 메모리 대신 DB 에 둔다. 감사 체인과 무관한 운영 카운터라 정리(삭제)가 허용된다.
CREATE TABLE mcp_rate_window (
    token_id     INTEGER NOT NULL,
    window_start INTEGER NOT NULL,                    -- unix 분(초 / 60)
    calls        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_id, window_start)
);
-- 사용량 집계가 채널·기간으로 감사 로그를 읽는다. 조회 전용 인덱스이고 기존 행·해시에는 영향이 없다.
CREATE INDEX idx_audit_channel_at ON audit_logs(channel, at);

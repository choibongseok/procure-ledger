-- 0009: AI 연결(MCP) 사용자별 합산 한도 창과 거부 횟수 누적 칸(B17).
-- mcp_user_rate_window 는 사용자 한 명의 모든 개인 토큰을 합친 분당 호출 수를 DB 기준 고정 분 창(unix 분)으로 센다.
-- 토큰별 창(mcp_rate_window)과 같은 방식이고, 감사 체인과 무관한 운영 카운터라 정리(삭제)가 허용된다.
CREATE TABLE mcp_user_rate_window (
    user_id      INTEGER NOT NULL,
    window_start INTEGER NOT NULL,                    -- unix 분(초 / 60)
    calls        INTEGER NOT NULL DEFAULT 0,
    denied_count INTEGER NOT NULL DEFAULT 0,          -- 이 창에서 한도 초과로 거부한 횟수(감사 요약 1행의 근거)
    PRIMARY KEY (user_id, window_start)
);
-- 토큰·분 단위 거부 횟수. 감사 로그는 추가 전용이라 횟수를 감사 행에 누적하지 않고 한도 창 테이블에 둔다.
ALTER TABLE mcp_rate_window ADD COLUMN denied_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_rate_window ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;   -- 요약 행에 소유자를 남기기 위한 값(0 은 미상)
ALTER TABLE mcp_rate_window ADD COLUMN summarized INTEGER NOT NULL DEFAULT 0; -- 요약 감사 행을 이미 냈으면 1
-- 아직 요약하지 않은 창(거부 2회 이상)만 찾는 부분 인덱스.
CREATE INDEX idx_mcp_rate_unsettled ON mcp_rate_window(window_start) WHERE denied_count >= 2 AND summarized = 0;

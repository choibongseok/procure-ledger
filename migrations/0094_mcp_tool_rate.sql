-- 0094 (B48 2단계, 명세 48 4절): 도구별 사용자 분당 호출 창과 시험 실행 통과 표시. 켜기 전에 현재 정의의 시험 실행이 한 번 성공해야 한다.
-- 호출 창은 기존 mcp_rate_window 와 같은 방식이다: (도구, 사용자, 분) 한 행에 허용된 호출 수와 거부 수를 센다. 지난 창은 호출 때 정리한다.
CREATE TABLE mcp_tool_rate_window (
    tool_id INTEGER NOT NULL REFERENCES mcp_tools(id),
    user_id INTEGER NOT NULL,
    minute  TEXT NOT NULL,
    calls   INTEGER NOT NULL DEFAULT 0,
    denied  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tool_id, user_id, minute)
);

CREATE TABLE mcp_tool_tests (
    tool_id      INTEGER NOT NULL REFERENCES mcp_tools(id),
    version      INTEGER NOT NULL CHECK (version >= 1),
    tested_by    INTEGER REFERENCES users(id),
    tested_at    TEXT NOT NULL,
    result_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tool_id, version)
);

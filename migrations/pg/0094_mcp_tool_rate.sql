-- 0094 (B48 2단계): SQLite 0094_mcp_tool_rate.sql 의 짝이다.
CREATE TABLE mcp_tool_rate_window (
    tool_id BIGINT NOT NULL REFERENCES mcp_tools(id),
    user_id BIGINT NOT NULL,
    minute  TEXT NOT NULL,
    calls   BIGINT NOT NULL DEFAULT 0,
    denied  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tool_id, user_id, minute)
);

CREATE TABLE mcp_tool_tests (
    tool_id      BIGINT NOT NULL REFERENCES mcp_tools(id),
    version      BIGINT NOT NULL CHECK (version >= 1),
    tested_by    BIGINT REFERENCES users(id),
    tested_at    TEXT NOT NULL,
    result_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tool_id, version)
);

-- 0095 (B48 3단계, 명세 48 4절): 초안(쓰기) 도구 켜기의 2인 확인과 호출 중복 방지 키.
-- 요청자와 확인자는 달라야 한다(DB 검사까지). 도구마다 진행 중(pending) 요청은 하나다.
CREATE TABLE mcp_tool_enable_requests (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_id      INTEGER NOT NULL REFERENCES mcp_tools(id),
    version      INTEGER NOT NULL CHECK (version >= 1),
    requested_by INTEGER REFERENCES users(id),
    requested_at TEXT NOT NULL,
    confirmed_by INTEGER REFERENCES users(id),
    resolved_at  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('pending','confirmed','rejected','canceled')),
    CHECK (confirmed_by IS NULL OR requested_by IS NULL OR confirmed_by <> requested_by)
);
CREATE UNIQUE INDEX idx_mcp_enable_one_pending ON mcp_tool_enable_requests(tool_id) WHERE status = 'pending';

-- idempotency_key: 같은 (도구, 사용자, 키)는 24시간 동안 같은 초안을 돌려준다. 결과는 번호·상태 같은 값 없는 요약이다.
CREATE TABLE mcp_tool_idempotency (
    tool_id     INTEGER NOT NULL REFERENCES mcp_tools(id),
    user_id     INTEGER NOT NULL,
    idem_key    TEXT NOT NULL CHECK (length(idem_key) BETWEEN 1 AND 40),
    result_json TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (tool_id, user_id, idem_key)
);

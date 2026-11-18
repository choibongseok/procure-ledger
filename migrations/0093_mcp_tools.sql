-- 0093 (B48 1단계, 4절): 사용자 정의 MCP 도구와 불변 버전. 정의(JSON)는 코드 카탈로그 안에서 고른 선언뿐이다.
-- 버전은 만든 뒤 고치거나 지울 수 없고(트리거), 되돌리기는 옛 정의를 복사한 새 버전이다. 법인 열은 없다(정의는 설치 전체 하나, D11).
CREATE TABLE mcp_tools (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 10 AND 39 AND substr(name, 1, 7) = 'custom_' AND substr(name, 8) NOT GLOB '*[^a-z0-9_]*'),
    title           TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 60),
    kind            TEXT NOT NULL CHECK (kind IN ('read','draft')),
    current_version INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    rate_per_min    INTEGER NOT NULL DEFAULT 30 CHECK (rate_per_min BETWEEN 1 AND 60),
    eval_set_id     INTEGER NOT NULL DEFAULT 0,
    created_by      INTEGER REFERENCES users(id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE mcp_tool_versions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_id          INTEGER NOT NULL REFERENCES mcp_tools(id),
    version          INTEGER NOT NULL CHECK (version >= 1),
    definition_json  TEXT NOT NULL,
    def_hash         TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2400),
    note             TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_by       INTEGER REFERENCES users(id),
    created_at       TEXT NOT NULL,
    UNIQUE (tool_id, version)
);

CREATE TRIGGER trg_mcp_tool_versions_no_update BEFORE UPDATE ON mcp_tool_versions
BEGIN SELECT RAISE(ABORT, '도구 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_mcp_tool_versions_no_delete BEFORE DELETE ON mcp_tool_versions
BEGIN SELECT RAISE(ABORT, '도구 버전은 고칠 수 없습니다'); END;

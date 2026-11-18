-- 0057: SCIM 2.0 계정 자동 동기화(B40 1단계, 4절).
-- scim_tokens: IdP 가 쓰는 연동 토큰. 원문은 발급 응답에서 한 번만 보이고 해시만 저장한다(개인 토큰 personal_tokens 와 별개: 사용자 권한을 따르지 않는다).
-- scim_links: (출처, externalId) ↔ 사용자. 멱등의 기준이다.
-- scim_sync_log: 동기화 기록 화면용 운영 기록(정본 증거는 해시 체인 감사 로그). 요청 본문·이메일 원문은 담지 않는다.
CREATE TABLE scim_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    source       TEXT NOT NULL CHECK (source IN ('entra','okta','google','other')),
    scope        TEXT NOT NULL CHECK (scope IN ('users','users_groups')),
    token_prefix TEXT NOT NULL,                       -- 목록 표시용 앞 8자(pls_ + 4자)
    token_hash   TEXT NOT NULL UNIQUE,                -- SHA-256, 원문은 저장하지 않는다
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,                       -- 만료 필수
    revoked_at   TEXT,
    last_used_at TEXT
);
CREATE INDEX idx_scim_tokens_source ON scim_tokens(source, revoked_at);

CREATE TABLE scim_links (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    source         TEXT NOT NULL,
    external_id    TEXT NOT NULL CHECK (length(external_id) BETWEEN 1 AND 255),
    user_id        INTEGER NOT NULL REFERENCES users(id),
    linked_at      TEXT NOT NULL,
    last_synced_at TEXT NOT NULL,
    UNIQUE (source, external_id),
    UNIQUE (source, user_id)
);

CREATE TABLE scim_sync_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    token_id    INTEGER REFERENCES scim_tokens(id),
    source      TEXT NOT NULL,
    method      TEXT NOT NULL,                        -- GET POST PUT PATCH DELETE
    resource    TEXT NOT NULL,                        -- User | Group | Discovery
    op          TEXT NOT NULL,                        -- create update deactivate reactivate link get list rejected
    result      TEXT NOT NULL CHECK (result IN ('success','denied','conflict','error')),
    http_status INTEGER NOT NULL,
    scim_type   TEXT NOT NULL DEFAULT '',
    external_id TEXT NOT NULL DEFAULT '',
    user_id     INTEGER,                              -- FK 없음(대상이 없던 요청도 기록)
    summary     TEXT NOT NULL DEFAULT '',             -- 한국어 한 줄. 이메일은 가려서 쓴다
    warnings    TEXT NOT NULL DEFAULT '[]',           -- JSON 문자열 배열
    audit_seq   INTEGER                               -- 이 요청이 남긴 첫 감사 기록 번호(발령이 있었을 때)
);
CREATE INDEX idx_scim_sync_at ON scim_sync_log(at);
CREATE INDEX idx_scim_sync_result ON scim_sync_log(result, at);


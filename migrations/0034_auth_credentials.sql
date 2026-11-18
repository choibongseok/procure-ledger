-- B30 이메일+비밀번호: 사용자 자격, 비밀번호 이력, 일회용 링크, 인증 정책.
-- 계정(입사 발령)이 만들어진 시점에는 user_credentials 행이 없다 = 비밀번호가 없어 로그인할 수 없다.
CREATE TABLE user_credentials (
    user_id         INTEGER PRIMARY KEY REFERENCES users(id),
    password_hash   TEXT NOT NULL,                 -- argon2id PHC 문자열(원문은 어디에도 없다)
    must_change     INTEGER NOT NULL DEFAULT 0 CHECK (must_change IN (0,1)), -- 임시 비밀번호·최초 로그인: 제한 세션
    changed_at      TEXT NOT NULL,
    expires_at      TEXT,                          -- 정책의 만료일을 켠 경우만
    failed_count    INTEGER NOT NULL DEFAULT 0,    -- 연속 실패(성공·해제 때 0)
    locked_until    TEXT,
    temp_expires_at TEXT                           -- 임시 비밀번호의 유효 기한(지나면 그 비밀번호로는 들어올 수 없다)
);

-- 최근 사용한 비밀번호 해시(재사용 금지 검사). 사용자당 최근 몇 개만 남긴다.
CREATE TABLE password_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id),
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL
);
CREATE INDEX idx_password_history_user ON password_history(user_id, id DESC);

-- 재설정·최초 설정 링크. 원문은 메일로만 나가고 DB 에는 SHA-256 해시만 둔다.
CREATE TABLE password_reset_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL CHECK (kind IN ('reset','setup')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);
CREATE INDEX idx_password_reset_user ON password_reset_tokens(user_id, created_at);

-- 인증 정책(한 행). 비어 있는 항목은 코드의 기본값을 쓴다. 비밀은 담지 않는다.
CREATE TABLE auth_policy (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    config_json TEXT NOT NULL,
    updated_by  INTEGER REFERENCES users(id),
    updated_at  TEXT NOT NULL
);

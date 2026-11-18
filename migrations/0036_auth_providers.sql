-- B30 외부 로그인 방식 설정(구글·MS·SAML)과 SAML 재생 방지·진행 중 요청. 비밀값(클라이언트 비밀·SAML 키)은 DB 에 두지 않는다(환경변수).
CREATE TABLE auth_providers (
    provider    TEXT PRIMARY KEY CHECK (provider IN ('google','microsoft','saml')),
    enabled     INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    config_json TEXT NOT NULL DEFAULT '{}',    -- 허용 도메인·테넌트 ID·표시 이름·IdP 메타데이터(공개 정보)
    tested_at   TEXT,                          -- 마지막 연결 시험 성공 시각(설정이 바뀌면 지운다). 시험에 성공하기 전에는 켤 수 없다
    updated_by  INTEGER REFERENCES users(id),
    updated_at  TEXT NOT NULL
);
INSERT INTO auth_providers (provider, enabled, config_json, updated_at) VALUES
    ('google', 0, '{}', strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('microsoft', 0, '{}', strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('saml', 0, '{}', strftime('%Y-%m-%dT%H:%M:%SZ','now'));

-- 연결 시험: 외부 로그인을 새 창에서 시작하되 세션을 만들지 않고, 결과(클레임 요약)만 시험한 관리자에게 보여 준다.
ALTER TABLE oidc_auth_requests ADD COLUMN purpose TEXT NOT NULL DEFAULT 'login';   -- login | test
ALTER TABLE oidc_auth_requests ADD COLUMN test_user_id INTEGER;                      -- purpose=test 일 때 시험을 시작한 관리자

CREATE TABLE auth_test_results (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    provider     TEXT NOT NULL,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    ok           INTEGER NOT NULL,
    summary_json TEXT NOT NULL,                -- 가린 클레임 요약(이메일·도메인·테넌트). 비밀 없음
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_auth_test_results_user ON auth_test_results(user_id, id DESC);

-- SAML: 우리가 시작한 AuthnRequest(1회 소비, 5분)와 이미 받은 Assertion ID(재생 방지).
CREATE TABLE saml_requests (
    request_id TEXT PRIMARY KEY,               -- AuthnRequest ID = 응답의 InResponseTo
    relay_hash TEXT NOT NULL,                  -- RelayState(1회용 값)의 SHA-256
    redirect_to TEXT NOT NULL,
    purpose    TEXT NOT NULL DEFAULT 'login',
    test_user_id INTEGER,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE TABLE saml_assertions_seen (
    assertion_id TEXT PRIMARY KEY,
    expires_at   TEXT NOT NULL
);

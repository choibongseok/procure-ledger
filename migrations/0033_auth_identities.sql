-- B30 기반: 로그인 방식이 여러 개가 되므로 외부 신원(방식, 발급자, 주체)을 사용자와 따로 묶는다.
-- 기존 users.oidc_sub 는 SQLite 라 지우지 않고 사용 중지한다(코드는 이 표만 읽는다).
CREATE TABLE user_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id),
    provider      TEXT NOT NULL,                  -- oidc | dev | google | microsoft | saml
    issuer        TEXT NOT NULL DEFAULT '',       -- OIDC: 발급자 / Entra: 테넌트 ID / SAML: IdP entityID
    subject       TEXT NOT NULL,                  -- OIDC: sub / Entra: oid / SAML: NameID
    linked_at     TEXT NOT NULL,
    last_login_at TEXT
);
-- 같은 외부 신원은 한 사용자에게만, 한 사용자는 방식마다 신원 하나만(다른 주체가 묶인 이메일 거부의 근거).
CREATE UNIQUE INDEX idx_user_identities_subject ON user_identities(provider, issuer, subject);
CREATE UNIQUE INDEX idx_user_identities_user_provider ON user_identities(user_id, provider);

-- 기존 oidc_sub 를 범용 방식으로 복사한다.
INSERT INTO user_identities (user_id, provider, issuer, subject, linked_at)
SELECT id, 'oidc', '', oidc_sub, created_at FROM users WHERE oidc_sub IS NOT NULL AND oidc_sub <> '';

-- 세션이 어떤 방식으로 만들어졌는지, 제한 세션(비밀번호 변경·2단계 등록 전)인지.
ALTER TABLE sessions ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'oidc';
ALTER TABLE sessions ADD COLUMN setup TEXT;       -- NULL 이면 정상, 아니면 password_change | totp_enroll

-- 진행 중 외부 로그인 요청이 어느 방식인지(콜백이 저장된 요청에서 방식을 정한다).
ALTER TABLE oidc_auth_requests ADD COLUMN provider TEXT NOT NULL DEFAULT 'oidc';

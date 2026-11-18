-- B30 선택 2단계 인증(TOTP, RFC 6238). 비밀키는 AES-256-GCM 으로 암호화해 저장한다(키는 환경변수 PL_AUTH_SECRET_KEY, DB 에 없다).
CREATE TABLE user_totp (
    user_id            INTEGER PRIMARY KEY REFERENCES users(id),
    secret_enc         TEXT,                 -- 활성 비밀키(암호문). 확인 전(처음 등록)에는 NULL
    confirmed_at       TEXT,                 -- 코드 확인으로 활성화한 시각. NULL 이면 활성이 아니다
    pending_secret_enc TEXT,                 -- 등록 중(아직 확인 전) 새 비밀키. 확인하면 secret_enc 로 옮긴다
    last_step          INTEGER NOT NULL DEFAULT 0, -- 마지막으로 받은 30초 구간(같은 구간의 코드는 한 번만: 재생 방지)
    created_at         TEXT NOT NULL
);

-- 복구 코드(8개, 1회용). 원문은 활성화 응답에서 한 번만 나가고 해시만 저장한다.
CREATE TABLE totp_recovery_codes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id   INTEGER NOT NULL REFERENCES users(id),
    code_hash TEXT NOT NULL,
    used_at   TEXT
);
CREATE INDEX idx_totp_recovery_user ON totp_recovery_codes(user_id);

-- 비밀번호는 통과했고 2단계 코드가 남은 상태의 로그인 티켓(세션이 아니다). 5분·시도 5회, 해시만 저장.
CREATE TABLE login_tickets (
    ticket_hash TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    method      TEXT NOT NULL,
    prev_hash   TEXT,                        -- 로그인 때 요청에 있던 기존 세션(성공하면 폐기)
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0
);

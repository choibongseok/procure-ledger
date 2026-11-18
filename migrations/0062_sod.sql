-- 0062: 권한 분리(SoD) 규칙 모드·위반·예외(B41 2단계, 8절).
-- 규칙 자체(코드·종류·단계·초기 모드)는 코드(internal/sod.Rules)에 있고 이 표는 화면에서 바꾼 모드만 담는다(행이 없으면 코드의 초기 모드).
-- sod_violations 는 추가 전용이다: 위반의 정본 증거는 감사 체인(sod.violation·sod.blocked)이고 이 표는 조회·보고서를 위한 색인이다.
CREATE TABLE sod_rule_settings (
    rule_code  TEXT PRIMARY KEY CHECK (length(rule_code) BETWEEN 1 AND 40),
    mode       TEXT NOT NULL CHECK (mode IN ('off','warn','block')),
    updated_by INTEGER NOT NULL REFERENCES users(id),
    updated_at TEXT NOT NULL
);

CREATE TABLE sod_violations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           TEXT NOT NULL,
    rule_code    TEXT NOT NULL,
    result       TEXT NOT NULL CHECK (result IN ('allowed','blocked','excepted')),
    mode         TEXT NOT NULL CHECK (mode IN ('warn','block')),
    kind         TEXT NOT NULL CHECK (kind IN ('txn','hold')),
    subject_type TEXT NOT NULL,
    subject_id   INTEGER NOT NULL,
    subject_no   TEXT NOT NULL DEFAULT '',           -- 지급 번호·전표 번호 같은 사람이 읽는 번호
    actor_id     INTEGER NOT NULL,
    other_id     INTEGER NOT NULL DEFAULT 0,         -- 앞 단계를 한 사람(txn 은 actor 와 같은 사람, hold 는 0)
    exception_id INTEGER,
    note         TEXT NOT NULL DEFAULT ''            -- 한국어 한 줄(사람 이름·금액 같은 값 없음)
);
CREATE INDEX idx_sod_violations_at ON sod_violations(at);
CREATE INDEX idx_sod_violations_rule ON sod_violations(rule_code, at);
CREATE TRIGGER trg_sod_violations_no_update BEFORE UPDATE ON sod_violations
BEGIN SELECT RAISE(ABORT, 'sod_violations 는 추가 전용입니다'); END;
CREATE TRIGGER trg_sod_violations_no_delete BEFORE DELETE ON sod_violations
BEGIN SELECT RAISE(ABORT, 'sod_violations 는 지울 수 없습니다'); END;

CREATE TABLE sod_exceptions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_code    TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id   INTEGER NOT NULL,
    subject_no   TEXT NOT NULL DEFAULT '',
    user_id      INTEGER NOT NULL REFERENCES users(id),   -- 예외로 그 일을 할 수 있는 사람
    reason       TEXT NOT NULL CHECK (length(reason) BETWEEN 10 AND 300),
    requested_by INTEGER NOT NULL REFERENCES users(id),
    requested_at TEXT NOT NULL,
    approved_by  INTEGER REFERENCES users(id),
    approved_at  TEXT,
    status       TEXT NOT NULL CHECK (status IN ('requested','active','used','rejected','expired','revoked')),
    expires_at   TEXT,                                    -- 승인 시각 + 7일(승인 전에는 NULL)
    used_at      TEXT
);
CREATE INDEX idx_sod_exceptions_match ON sod_exceptions(rule_code, subject_type, subject_id, user_id, status);

-- 새 알림 종류의 메일 기본값(B36 종류 표 notify.Kinds 에 같은 코드를 더했다). 위반 알림은 필수(locked)다: 감사인이 끌 수 없다.
INSERT INTO notification_defaults (kind, channel, enabled, locked) VALUES
    ('sod_violation','email',1,1), ('privacy_request_pending','email',1,0), ('privacy_request_done','email',1,0), ('privacy_monthly_report','email',1,0);

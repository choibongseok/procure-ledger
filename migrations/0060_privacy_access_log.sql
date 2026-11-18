-- 0060: 개인정보 조회 기록(B41 1단계, 5절).
-- privacy_access_log: 원문 열람 한 번(응답 한 번)을 적은 행. 자체 해시 체인(prev_hash → hash)이고 하루 요약만 감사 체인에 남는다(privacy.log_digest).
-- privacy_access_subjects: 그 응답에 원문이 실린 정보주체. 두 표 모두 추가 전용이다(UPDATE 는 막고, DELETE 는 보관 기간 파기가
-- privacy_write_guard 행을 넣어 둔 트랜잭션 안에서만 한다). 값(연락처·계좌 원문, 검색어)은 어디에도 담지 않는다.
CREATE TABLE privacy_access_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT NOT NULL,                     -- 고정 폭 UTC(2006-01-02T15:04:05.000000000Z): 문자열 순서가 시간 순서
    actor_id      INTEGER NOT NULL,                  -- FK 없음(사용자 삭제와 무관). MCP 는 토큰 발급자
    actor_role    TEXT NOT NULL,
    channel       TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw','cli','scim')),
    token_id      INTEGER,
    category      TEXT NOT NULL CHECK (category IN ('contact','account','card','employee_no')),
    screen        TEXT NOT NULL CHECK (length(screen) BETWEEN 1 AND 80),
    dataset       TEXT NOT NULL DEFAULT '',
    is_export     INTEGER NOT NULL DEFAULT 0 CHECK (is_export IN (0,1)),
    fields        TEXT NOT NULL DEFAULT '[]',        -- JSON 문자열 배열(필드 이름만)
    subject_count INTEGER NOT NULL CHECK (subject_count >= 0),   -- 식별하지 못한 정보주체(내보내기 행 등)까지 센 수
    subjects_hash TEXT NOT NULL,                     -- 정렬한 정보주체 목록의 SHA-256(정보주체 행 변조도 체인이 짚는다)
    exposure      TEXT NOT NULL DEFAULT 'raw' CHECK (exposure IN ('raw','mcp_anomaly')),
    prev_hash     TEXT NOT NULL,                     -- 이 표의 직전 행 해시(첫 행은 빈 문자열)
    hash          TEXT NOT NULL UNIQUE
);
CREATE INDEX idx_privacy_log_at ON privacy_access_log(at);
CREATE INDEX idx_privacy_log_actor ON privacy_access_log(actor_id, at);

CREATE TABLE privacy_access_subjects (
    log_id       INTEGER NOT NULL REFERENCES privacy_access_log(id),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user','vendor','company','payment')),
    subject_id   INTEGER NOT NULL,
    PRIMARY KEY (log_id, subject_type, subject_id)
);
CREATE INDEX idx_privacy_subjects_subject ON privacy_access_subjects(subject_type, subject_id, log_id);

-- 파기한 달의 요약. 파기해도 남은 구간의 체인 검증이 이 줄의 머리 해시에서 이어 가게 한다.
CREATE TABLE privacy_log_segments (
    month     TEXT PRIMARY KEY CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    first_id  INTEGER NOT NULL,
    last_id   INTEGER NOT NULL,
    rows      INTEGER NOT NULL,
    head_hash TEXT NOT NULL,
    prev_hash TEXT NOT NULL,
    purged_at TEXT NOT NULL
);

-- 파기 때만 트랜잭션 안에 넣었다가 끝나기 전에 지우는 보호 행(org_write_guard 와 같은 방식). 롤백되면 함께 사라진다.
CREATE TABLE privacy_write_guard (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);

CREATE TRIGGER trg_privacy_log_no_update BEFORE UPDATE ON privacy_access_log
BEGIN SELECT RAISE(ABORT, 'privacy_access_log 는 추가 전용입니다'); END;
CREATE TRIGGER trg_privacy_log_no_delete BEFORE DELETE ON privacy_access_log
WHEN NOT EXISTS (SELECT 1 FROM privacy_write_guard)
BEGIN SELECT RAISE(ABORT, 'privacy_access_log 는 보관 기간 파기 외에는 지울 수 없습니다'); END;
CREATE TRIGGER trg_privacy_subjects_no_update BEFORE UPDATE ON privacy_access_subjects
BEGIN SELECT RAISE(ABORT, 'privacy_access_subjects 는 추가 전용입니다'); END;
CREATE TRIGGER trg_privacy_subjects_no_delete BEFORE DELETE ON privacy_access_subjects
WHEN NOT EXISTS (SELECT 1 FROM privacy_write_guard)
BEGIN SELECT RAISE(ABORT, 'privacy_access_subjects 는 보관 기간 파기 외에는 지울 수 없습니다'); END;

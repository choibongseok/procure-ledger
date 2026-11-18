-- 0061: 정보주체 열람 요청(B41 2단계, 7절)과 조회 기록 경로에 'api' 추가(B42).
-- (1) privacy_access_log.channel 의 CHECK 에 'api'(서버 간 API 키 요청)를 더한다. SQLite 는 CHECK 를 바꿀 수 없어 두 표를 새로 만들어 옮긴다:
--     행 내용(해시 포함)·id 를 그대로 복사하므로 체인 검증에 영향이 없고, 추가 전용 트리거와 색인은 새 표에 다시 만든다(DROP TABLE 은 삭제 트리거를 실행하지 않는다).
CREATE TABLE privacy_access_log_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT NOT NULL,
    actor_id      INTEGER NOT NULL,
    actor_role    TEXT NOT NULL,
    channel       TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw','cli','scim','api')),
    token_id      INTEGER,
    category      TEXT NOT NULL CHECK (category IN ('contact','account','card','employee_no')),
    screen        TEXT NOT NULL CHECK (length(screen) BETWEEN 1 AND 80),
    dataset       TEXT NOT NULL DEFAULT '',
    is_export     INTEGER NOT NULL DEFAULT 0 CHECK (is_export IN (0,1)),
    fields        TEXT NOT NULL DEFAULT '[]',
    subject_count INTEGER NOT NULL CHECK (subject_count >= 0),
    subjects_hash TEXT NOT NULL,
    exposure      TEXT NOT NULL DEFAULT 'raw' CHECK (exposure IN ('raw','mcp_anomaly')),
    prev_hash     TEXT NOT NULL,
    hash          TEXT NOT NULL UNIQUE
);
INSERT INTO privacy_access_log_new (id, at, actor_id, actor_role, channel, token_id, category, screen, dataset, is_export, fields, subject_count, subjects_hash, exposure, prev_hash, hash)
    SELECT id, at, actor_id, actor_role, channel, token_id, category, screen, dataset, is_export, fields, subject_count, subjects_hash, exposure, prev_hash, hash FROM privacy_access_log ORDER BY id;
CREATE TABLE privacy_access_subjects_new (
    log_id       INTEGER NOT NULL REFERENCES privacy_access_log_new(id),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user','vendor','company','payment')),
    subject_id   INTEGER NOT NULL,
    PRIMARY KEY (log_id, subject_type, subject_id)
);
INSERT INTO privacy_access_subjects_new (log_id, subject_type, subject_id) SELECT log_id, subject_type, subject_id FROM privacy_access_subjects;
DROP TABLE privacy_access_subjects;
DROP TABLE privacy_access_log;
ALTER TABLE privacy_access_log_new RENAME TO privacy_access_log;
ALTER TABLE privacy_access_subjects_new RENAME TO privacy_access_subjects;
CREATE INDEX idx_privacy_log_at ON privacy_access_log(at);
CREATE INDEX idx_privacy_log_actor ON privacy_access_log(actor_id, at);
CREATE INDEX idx_privacy_subjects_subject ON privacy_access_subjects(subject_type, subject_id, log_id);
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

-- (2) 정보주체 열람 요청: 직원이 "내 정보를 누가 봤는지"를 요청하면 관리자가 승인·반려하고, 승인 시점의 결과를 스냅샷으로 만든다.
CREATE TABLE privacy_subject_requests (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id),
    status        TEXT NOT NULL CHECK (status IN ('requested','approved','rejected','expired')),
    period_from   TEXT NOT NULL,                      -- 한국 시간 날짜(YYYY-MM-DD, 포함)
    period_to     TEXT NOT NULL,                      -- 한국 시간 날짜(포함)
    reason        TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    requested_at  TEXT NOT NULL,
    decided_by    INTEGER REFERENCES users(id),
    decided_at    TEXT,
    decision_note TEXT NOT NULL DEFAULT '' CHECK (length(decision_note) <= 200),
    expires_at    TEXT                                -- 승인한 뒤 결과 열람 만료(승인 시각 + 30일)
);
CREATE UNIQUE INDEX idx_privacy_req_open ON privacy_subject_requests(user_id) WHERE status = 'requested';
CREATE INDEX idx_privacy_req_user ON privacy_subject_requests(user_id, requested_at);
CREATE TABLE privacy_request_results (
    request_id  INTEGER NOT NULL REFERENCES privacy_subject_requests(id),
    seq         INTEGER NOT NULL,
    at          TEXT NOT NULL,
    viewer_unit TEXT NOT NULL,                        -- 열람자의 그 시점 소속 조직 이름
    viewer_role TEXT NOT NULL,                        -- 역할 이름(사용자 정의 역할 포함)
    category    TEXT NOT NULL,
    screen      TEXT NOT NULL,
    fields      TEXT NOT NULL,
    is_export   INTEGER NOT NULL,
    summary     INTEGER NOT NULL DEFAULT 0,           -- 1 이면 "이후 N건 생략" 요약 행(screen 에 문구)
    PRIMARY KEY (request_id, seq)
);

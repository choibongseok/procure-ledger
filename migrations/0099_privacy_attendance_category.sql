-- 0099: 개인정보 열람 기록 범주에 'attendance'(근태·휴가·휴직 사유 원문, B50 11절)를 더한다. 12.5.
-- privacy_access_log.category 의 CHECK 열거를 바꾸려면 SQLite 는 표를 다시 만들어야 한다(0061 과 같은 방식).
-- 행 내용(prev_hash·hash 포함)·id 를 그대로 복사하므로 해시 체인과 정보주체 목록 해시는 바뀌지 않는다. privacy_log_segments(파기 구간 머리 해시)는 이 표를 참조하지 않아 건드리지 않는다.
-- 추가 전용 트리거와 색인은 새 표에 다시 만든다(DROP TABLE 은 삭제 트리거를 실행하지 않는다). 정보주체 표는 0070 이 더한 'counterparty' 를 포함한 현재 열거 그대로 다시 만든다.
CREATE TABLE privacy_access_log_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT NOT NULL,
    actor_id      INTEGER NOT NULL,
    actor_role    TEXT NOT NULL,
    channel       TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw','cli','scim','api')),
    token_id      INTEGER,
    category      TEXT NOT NULL CHECK (category IN ('contact','account','card','employee_no','attendance')),
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
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user','vendor','company','payment','counterparty')),
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

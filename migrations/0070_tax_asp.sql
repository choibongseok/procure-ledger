-- 0070 (B43 1단계 platform, 4·7절): 전자세금계산서 대행사 설정·수신 동기화 상태, 알림 종류 기본값, 개인정보 정보주체 종류 counterparty.
--  - tax_asp_settings(행 1개): 대행사 이름·호스트 힌트·봉인 값(용도 tax-asp: 엔드포인트 URL·API 키·인증서·비밀번호를 JSON 한 덩어리로 봉인)·인증서 만료일(평문 날짜: 알림·점검표가 풀지 않고 읽는다)·
--    인증서 주체 끝 부분(화면용)·사용 여부. 값은 어디에도 평문으로 없고 힌트에는 호스트만 있다. 키 교체(keyrotate.AccountColumns)가 secret_sealed 를 다시 봉한다.
--  - tax_sync_state(행 1개): 수신 동기화의 불투명 커서와 마지막 실행 정보(코드·시각). 2단계 동기화가 쓴다.
--  - privacy_access_subjects.subject_type 에 'counterparty'(세금계산서 거래 상대방)를 더한다: CHECK 는 바꿀 수 없어 표를 다시 만든다(0061 과 같은 방식, 행·추가 전용 트리거·색인 보존).
CREATE TABLE tax_asp_settings (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    provider          TEXT NOT NULL CHECK (length(provider) BETWEEN 1 AND 40),
    endpoint_hint     TEXT NOT NULL DEFAULT '',
    secret_sealed     TEXT NOT NULL,
    cert_not_after    TEXT NOT NULL DEFAULT '' CHECK (cert_not_after = '' OR cert_not_after GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    cert_subject_hint TEXT NOT NULL DEFAULT '',
    active            INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
    updated_by        INTEGER REFERENCES users(id),
    updated_at        TEXT NOT NULL
);
CREATE TRIGGER trg_tax_asp_settings_no_delete BEFORE DELETE ON tax_asp_settings
BEGIN SELECT RAISE(ABORT, '대행사 설정은 지울 수 없습니다(사용 끄기만 됩니다)'); END;

CREATE TABLE tax_sync_state (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    cursor        TEXT NOT NULL DEFAULT '',
    last_from     TEXT NOT NULL DEFAULT '',
    last_to       TEXT NOT NULL DEFAULT '',
    last_run_at   TEXT NOT NULL DEFAULT '',
    last_result   TEXT NOT NULL DEFAULT '',
    last_error_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO tax_sync_state (id) VALUES (1);

INSERT INTO notification_defaults (kind, channel, enabled, locked) VALUES
    ('tax_invoice_failed','email',1,0), ('tax_invoice_sync','email',0,0), ('tax_cert_expiring','email',1,1);

CREATE TABLE privacy_access_subjects_new (
    log_id       INTEGER NOT NULL REFERENCES privacy_access_log(id),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user','vendor','company','payment','counterparty')),
    subject_id   INTEGER NOT NULL,
    PRIMARY KEY (log_id, subject_type, subject_id)
);
INSERT INTO privacy_access_subjects_new (log_id, subject_type, subject_id) SELECT log_id, subject_type, subject_id FROM privacy_access_subjects;
DROP TABLE privacy_access_subjects;
ALTER TABLE privacy_access_subjects_new RENAME TO privacy_access_subjects;
CREATE INDEX idx_privacy_subjects_subject ON privacy_access_subjects(subject_type, subject_id, log_id);
CREATE TRIGGER trg_privacy_subjects_no_update BEFORE UPDATE ON privacy_access_subjects
BEGIN SELECT RAISE(ABORT, 'privacy_access_subjects 는 추가 전용입니다'); END;
CREATE TRIGGER trg_privacy_subjects_no_delete BEFORE DELETE ON privacy_access_subjects
WHEN NOT EXISTS (SELECT 1 FROM privacy_write_guard)
BEGIN SELECT RAISE(ABORT, 'privacy_access_subjects 는 보관 기간 파기 외에는 지울 수 없습니다'); END;

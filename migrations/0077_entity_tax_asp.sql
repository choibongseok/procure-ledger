-- 0077 (B44 2단계 platform, 명세 14.1·8.3절): 전자세금계산서 대행사 설정·수신 동기화 상태·회사 직인의 현재 색인을 법인별로 만든다.
--  - tax_asp_settings / tax_sync_state: 행 하나(CHECK id = 1)였던 것을 법인마다 한 행(entity_id 유일)으로. 기존 행은 기본 법인(1)의 것이다.
--    인증서는 사업자번호 단위라 법인마다 따로 설정한다. id 는 그대로 행 번호(키 교체 도구가 읽는다).
--  - company_assets: 직인·로고 현재 한 행 유일 색인을 (entity_id, kind) 로.
CREATE TABLE tax_asp_settings_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    provider          TEXT NOT NULL CHECK (length(provider) BETWEEN 1 AND 40),
    endpoint_hint     TEXT NOT NULL DEFAULT '',
    secret_sealed     TEXT NOT NULL,
    cert_not_after    TEXT NOT NULL DEFAULT '' CHECK (cert_not_after = '' OR cert_not_after GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    cert_subject_hint TEXT NOT NULL DEFAULT '',
    active            INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
    updated_by        INTEGER REFERENCES users(id),
    updated_at        TEXT NOT NULL,
    entity_id         INTEGER NOT NULL DEFAULT 1 UNIQUE
);
INSERT INTO tax_asp_settings_new (id, provider, endpoint_hint, secret_sealed, cert_not_after, cert_subject_hint, active, updated_by, updated_at, entity_id)
    SELECT id, provider, endpoint_hint, secret_sealed, cert_not_after, cert_subject_hint, active, updated_by, updated_at, 1 FROM tax_asp_settings;
DROP TABLE tax_asp_settings;
ALTER TABLE tax_asp_settings_new RENAME TO tax_asp_settings;
CREATE TRIGGER trg_tax_asp_settings_no_delete BEFORE DELETE ON tax_asp_settings
BEGIN SELECT RAISE(ABORT, '대행사 설정은 지울 수 없습니다(사용 끄기만 됩니다)'); END;

CREATE TABLE tax_sync_state_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    cursor        TEXT NOT NULL DEFAULT '',
    last_from     TEXT NOT NULL DEFAULT '',
    last_to       TEXT NOT NULL DEFAULT '',
    last_run_at   TEXT NOT NULL DEFAULT '',
    last_result   TEXT NOT NULL DEFAULT '',
    last_error_at TEXT NOT NULL DEFAULT '',
    entity_id     INTEGER NOT NULL DEFAULT 1 UNIQUE
);
INSERT INTO tax_sync_state_new (id, cursor, last_from, last_to, last_run_at, last_result, last_error_at, entity_id)
    SELECT id, cursor, last_from, last_to, last_run_at, last_result, last_error_at, 1 FROM tax_sync_state;
DROP TABLE tax_sync_state;
ALTER TABLE tax_sync_state_new RENAME TO tax_sync_state;

DROP INDEX idx_company_assets_current;
CREATE UNIQUE INDEX idx_company_assets_current ON company_assets(entity_id, kind) WHERE current = 1;

-- 0070 (B43 1단계 platform): SQLite 0070_tax_asp.sql 의 짝이다(초안은 pgbaseline -translate, 검토함).
-- PG 는 정보주체 종류 CHECK 제약만 교체한다(표를 다시 만들지 않는다: 행·추가 전용 트리거에 영향이 없다).
CREATE TABLE tax_asp_settings (
    id BIGINT PRIMARY KEY CHECK (id = 1),
    provider          TEXT NOT NULL CHECK (length(provider) BETWEEN 1 AND 40),
    endpoint_hint     TEXT NOT NULL DEFAULT '',
    secret_sealed     TEXT NOT NULL,
    cert_not_after    TEXT NOT NULL DEFAULT '' CHECK (cert_not_after = '' OR cert_not_after ~ '^[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]$'),
    cert_subject_hint TEXT NOT NULL DEFAULT '',
    active            BIGINT NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
    updated_by        BIGINT REFERENCES users(id),
    updated_at        TEXT NOT NULL
);

CREATE FUNCTION trg_tax_asp_settings_no_delete_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '대행사 설정은 지울 수 없습니다(사용 끄기만 됩니다)';
  RETURN OLD;
END
$fn$;
CREATE TRIGGER trg_tax_asp_settings_no_delete BEFORE DELETE ON tax_asp_settings FOR EACH ROW EXECUTE FUNCTION trg_tax_asp_settings_no_delete_fn();

CREATE TABLE tax_sync_state (
    id BIGINT PRIMARY KEY CHECK (id = 1),
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

ALTER TABLE privacy_access_subjects DROP CONSTRAINT privacy_access_subjects_subject_type_check;
ALTER TABLE privacy_access_subjects ADD CONSTRAINT privacy_access_subjects_subject_type_check CHECK (subject_type IN ('user','vendor','company','payment','counterparty'));

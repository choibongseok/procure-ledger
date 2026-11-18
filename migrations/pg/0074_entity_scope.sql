-- 0074 (B44 1단계 platform): SQLite 0074_entity_scope.sql 의 짝이다. PG 는 표를 다시 만들지 않고 제약만 바꾼다.
ALTER TABLE role_grants ADD COLUMN scope TEXT NOT NULL DEFAULT 'own' CHECK (scope IN ('own','entity','group'));
ALTER TABLE role_grants ADD COLUMN scope_entity_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE role_grants ADD CONSTRAINT role_grants_scope_entity_check CHECK ((scope = 'entity' AND scope_entity_id > 0) OR (scope <> 'entity' AND scope_entity_id = 0));
DO $do$
DECLARE c text;
BEGIN
  SELECT conname INTO c FROM pg_constraint WHERE conrelid = 'role_grants'::regclass AND contype = 'u';
  EXECUTE 'ALTER TABLE role_grants DROP CONSTRAINT ' || quote_ident(c);
END
$do$;
ALTER TABLE role_grants ADD CONSTRAINT role_grants_unique_scope UNIQUE (role_id, target_type, target_id, unit_kind, include_descendants, scope, scope_entity_id);

ALTER TABLE user_effective_permissions ADD COLUMN scope TEXT NOT NULL DEFAULT 'own';
ALTER TABLE user_effective_permissions ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION trg_role_grants_user_ai_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.target_type = 'user' THEN
    INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id)
    SELECT NEW.target_id, rp.permission, NEW.role_id, NEW.id, NEW.scope, NEW.scope_entity_id FROM role_permissions rp WHERE rp.role_id = NEW.role_id ON CONFLICT DO NOTHING;
  END IF;
  RETURN NULL;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_role_permissions_ai_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id)
  SELECT g.target_id, NEW.permission, NEW.role_id, g.id, g.scope, g.scope_entity_id FROM role_grants g WHERE g.role_id = NEW.role_id AND g.target_type = 'user' ON CONFLICT DO NOTHING;
  RETURN NULL;
END
$fn$;

ALTER TABLE audit_logs ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 0;
INSERT INTO app_settings (key, value, updated_at) SELECT 'audit.entity_epoch_seq', CAST(COALESCE(MAX(seq), 0) AS TEXT), to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM audit_logs;

-- 0079 (B44 2단계 platform): SQLite 0079_scim_group_scope.sql 의 짝이다.
ALTER TABLE scim_group_maps ADD COLUMN scope TEXT NOT NULL DEFAULT 'own' CHECK (scope IN ('own','entity'));
ALTER TABLE scim_group_maps ADD COLUMN scope_entity_id BIGINT NOT NULL DEFAULT 0;

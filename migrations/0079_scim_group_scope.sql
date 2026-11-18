-- 0079 (B44 2단계 platform, 명세 9.3): SCIM 그룹 매핑(역할 모드)의 부여 범위. own(소속 법인 전체, 기본) 또는 entity(법인 하나)만 준다.
-- group(모든 법인) 범위는 2인 확인이 필요해 SCIM 이 자동으로 줄 수 없다.
ALTER TABLE scim_group_maps ADD COLUMN scope TEXT NOT NULL DEFAULT 'own' CHECK (scope IN ('own','entity'));
ALTER TABLE scim_group_maps ADD COLUMN scope_entity_id INTEGER NOT NULL DEFAULT 0;

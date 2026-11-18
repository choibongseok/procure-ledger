-- 0059: SCIM Groups 매핑(B40 3단계, 4절·9절).
-- 매핑은 관리자 화면에서만 만든다(IdP 가 그룹을 보내도 매핑이 없으면 만들어지지 않고 동기화 기록에 거부로 남는다).
-- kind=unit 이면 멤버가 그 조직으로 전보되고, kind=role 이면 멤버에게 사용자 대상 직접 부여(role_grants, source 'manual')가 생긴다.
-- role_grants.source 의 CHECK 는 바꾸지 않고 만든 부여 번호를 grant_id 로 추적한다(사람이 따로 준 같은 부여는 건드리지 않는다: grant_id NULL).
CREATE TABLE scim_group_maps (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source            TEXT NOT NULL,
    group_external_id TEXT NOT NULL CHECK (length(group_external_id) BETWEEN 1 AND 255),
    display_name      TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL CHECK (kind IN ('unit','role')),
    unit_id           INTEGER REFERENCES org_units(id),
    role_id           INTEGER REFERENCES roles(id),
    created_by        INTEGER NOT NULL REFERENCES users(id),
    created_at        TEXT NOT NULL,
    UNIQUE (source, group_external_id),
    CHECK ((kind = 'unit' AND unit_id IS NOT NULL AND role_id IS NULL) OR (kind = 'role' AND role_id IS NOT NULL AND unit_id IS NULL))
);

CREATE TABLE scim_group_members (
    map_id   INTEGER NOT NULL REFERENCES scim_group_maps(id),
    user_id  INTEGER NOT NULL REFERENCES users(id),
    grant_id INTEGER,                                  -- role 모드: 이 SCIM 이 만든 role_grants 번호(없으면 NULL: 이미 있던 부여)
    added_at TEXT NOT NULL,                            -- 여러 조직 그룹에 속하면 가장 최근에 들어온 그룹이 주 소속이다
    PRIMARY KEY (map_id, user_id)
);
CREATE INDEX idx_scim_group_members_user ON scim_group_members(user_id);

-- 0074 (B44 1단계 platform, 명세 9·13절): 역할 부여의 범위, 실효 권한 캐시의 범위 열, 감사 로그의 법인 열과 이관 경계.
--  - role_grants.scope(own|entity|group)·scope_entity_id: 부여가 효력을 갖는 법인. 기존 부여는 모두 own(소속·겸직 법인 전체)이라 법인이 하나뿐이면 동작이 같다.
--    같은 역할을 같은 대상에게 법인별로 따로 줄 수 있도록 유일 제약에 범위를 넣는다(SQLite 는 제약을 바꿀 수 없어 표를 다시 만들고 캐시 유지 트리거를 다시 만든다, 0042 와 같은 모양).
--  - user_effective_permissions.scope·entity_id: 부여의 범위를 그대로 옮긴 캐시 열(계산은 access.Recompute, 사용자 직접 부여는 아래 트리거). own 은 읽을 때 사람의 법인 집합으로 푼다.
--  - audit_logs.entity_id: 사건이 속한 법인(0 = 법인과 무관). ADD COLUMN 이라 표를 다시 만들지 않고 행을 훑지 않는다. 0 이 아닐 때만 해시에 들어간다(contract/audit.HashRecord).
--    이관 시점의 머리 seq 를 app_settings 'audit.entity_epoch_seq' 에 남겨, 그 이하 행의 entity_id 가 0 이 아니면 검증기가 변조로 판정한다(명세 13.1.1).
--    색인 (entity_id, seq)는 여기서 만들지 않는다: 감사 표는 체인 잠금 아래 기록되므로 큰 표의 색인 생성은 별도 단계(erp-admin audit-entity-index)로 한다.
CREATE TABLE role_grants_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    role_id             INTEGER NOT NULL REFERENCES roles(id),
    target_type         TEXT NOT NULL CHECK (target_type IN ('user','unit','rank','unit_head')),
    target_id           INTEGER NOT NULL DEFAULT 0,
    unit_kind           TEXT NOT NULL DEFAULT '',
    include_descendants INTEGER NOT NULL DEFAULT 0 CHECK (include_descendants IN (0,1)),
    source              TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','compat')),
    reason              TEXT NOT NULL DEFAULT '',
    created_by          INTEGER REFERENCES users(id),
    created_at          TEXT NOT NULL,
    scope               TEXT NOT NULL DEFAULT 'own' CHECK (scope IN ('own','entity','group')),
    scope_entity_id     INTEGER NOT NULL DEFAULT 0 CHECK ((scope = 'entity' AND scope_entity_id > 0) OR (scope <> 'entity' AND scope_entity_id = 0)),
    UNIQUE (role_id, target_type, target_id, unit_kind, include_descendants, scope, scope_entity_id)
);
DROP TRIGGER trg_role_permissions_ai; -- role_grants 를 참조하는 트리거는 표를 바꾸는 동안 내려 둔다(아래에서 다시 만든다)
DROP TRIGGER trg_users_role_compat_ai;
DROP TRIGGER trg_users_role_compat_au;
INSERT INTO role_grants_new (id, role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_by, created_at)
    SELECT id, role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_by, created_at FROM role_grants;
DROP TABLE role_grants;
ALTER TABLE role_grants_new RENAME TO role_grants;
CREATE INDEX idx_role_grants_target ON role_grants(target_type, target_id);

ALTER TABLE user_effective_permissions ADD COLUMN scope TEXT NOT NULL DEFAULT 'own';
ALTER TABLE user_effective_permissions ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER trg_role_grants_user_ai AFTER INSERT ON role_grants WHEN NEW.target_type = 'user'
BEGIN
    INSERT OR IGNORE INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id)
    SELECT NEW.target_id, rp.permission, NEW.role_id, NEW.id, NEW.scope, NEW.scope_entity_id FROM role_permissions rp WHERE rp.role_id = NEW.role_id;
END;
CREATE TRIGGER trg_role_grants_ad AFTER DELETE ON role_grants
BEGIN
    DELETE FROM user_effective_permissions WHERE grant_id = OLD.id;
END;
CREATE TRIGGER trg_role_permissions_ai AFTER INSERT ON role_permissions
BEGIN
    INSERT OR IGNORE INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id)
    SELECT g.target_id, NEW.permission, NEW.role_id, g.id, g.scope, g.scope_entity_id FROM role_grants g WHERE g.role_id = NEW.role_id AND g.target_type = 'user';
END;

CREATE TRIGGER trg_users_role_compat_ai AFTER INSERT ON users
BEGIN
    INSERT OR IGNORE INTO role_grants (role_id, target_type, target_id, source, reason, created_at)
    SELECT r.id, 'user', NEW.id, 'compat', 'users.role 호환', strftime('%Y-%m-%dT%H:%M:%SZ','now')
    FROM roles r WHERE r.key = NEW.role AND r.kind = 'template';
END;
CREATE TRIGGER trg_users_role_compat_au AFTER UPDATE OF role ON users WHEN NEW.role IS NOT OLD.role
BEGIN
    DELETE FROM role_grants WHERE target_type = 'user' AND target_id = NEW.id AND source = 'compat';
    INSERT OR IGNORE INTO role_grants (role_id, target_type, target_id, source, reason, created_at)
    SELECT r.id, 'user', NEW.id, 'compat', 'users.role 호환', strftime('%Y-%m-%dT%H:%M:%SZ','now')
    FROM roles r WHERE r.key = NEW.role AND r.kind = 'template';
END;

ALTER TABLE audit_logs ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 0;
INSERT INTO app_settings (key, value, updated_at) SELECT 'audit.entity_epoch_seq', CAST(COALESCE(MAX(seq), 0) AS TEXT), strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM audit_logs;

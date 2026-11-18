-- B33 사용자 정의 역할: 역할 표·권한 묶음·부여·실효 권한 캐시·위험 권한 2인 확인 요청.
-- 기본 템플릿 5개의 권한은 코드(contract.RolePermissions)가 정본이고 서버 기동 때 role_permissions 에 동기화한다(access.SyncTemplates).
CREATE TABLE roles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL UNIQUE,                 -- 템플릿: admin|finance|manager|staff|auditor, 사용자 정의: r_<번호>
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('template','custom')),
    version     INTEGER NOT NULL DEFAULT 1,           -- 동시 편집 방지(낙관적 잠금)
    created_by  INTEGER REFERENCES users(id),
    created_at  TEXT NOT NULL,
    updated_by  INTEGER REFERENCES users(id),
    updated_at  TEXT NOT NULL,
    deleted_at  TEXT
);
INSERT INTO roles (key, name, description, kind, created_at, updated_at) VALUES
    ('admin',   '관리자', '기본 템플릿: 조직·사용자·결재 양식·로그인 설정 등 관리 권한', 'template', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('finance', '재무',   '기본 템플릿: 재무 결재·전표·지급·마감 권한',                 'template', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('manager', '결재자', '기본 템플릿: 조직장·결재 권한',                               'template', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('staff',   '직원',   '기본 템플릿: 일반 업무 권한',                                 'template', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    ('auditor', '감사',   '기본 템플릿: 조회 중심 권한과 감사 로그',                     'template', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));

CREATE TABLE role_permissions (
    role_id    INTEGER NOT NULL REFERENCES roles(id),
    permission TEXT NOT NULL,
    PRIMARY KEY (role_id, permission)
);

-- 부여: 역할을 사람·조직(하위 포함 선택)·직위·조직장(조직 종류)에게 준다. source=compat 는 users.role 호환(이관·단일 역할 PATCH)이 만든 직접 부여다.
CREATE TABLE role_grants (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    role_id             INTEGER NOT NULL REFERENCES roles(id),
    target_type         TEXT NOT NULL CHECK (target_type IN ('user','unit','rank','unit_head')),
    target_id           INTEGER NOT NULL DEFAULT 0,     -- user: 사용자, unit: 조직, rank: 직위. unit_head 는 0
    unit_kind           TEXT NOT NULL DEFAULT '',       -- unit_head: 조직 종류
    include_descendants INTEGER NOT NULL DEFAULT 0 CHECK (include_descendants IN (0,1)),
    source              TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','compat')),
    reason              TEXT NOT NULL DEFAULT '',
    created_by          INTEGER REFERENCES users(id),
    created_at          TEXT NOT NULL,
    UNIQUE (role_id, target_type, target_id, unit_kind, include_descendants)
);
CREATE INDEX idx_role_grants_target ON role_grants(target_type, target_id);

-- 실효 권한 캐시(계산 결과). 판정·출처 표시·SQL 조인에 쓰고, 변경 시점에 다시 계산한다(사용자 직접 부여는 아래 트리거가 같은 트랜잭션에서 맞춘다).
CREATE TABLE user_effective_permissions (
    user_id    INTEGER NOT NULL,
    permission TEXT NOT NULL,
    role_id    INTEGER NOT NULL,
    grant_id   INTEGER NOT NULL,
    PRIMARY KEY (user_id, permission, role_id, grant_id)
);
CREATE INDEX idx_uep_user ON user_effective_permissions(user_id);
CREATE INDEX idx_uep_grant ON user_effective_permissions(grant_id);
CREATE INDEX idx_uep_perm ON user_effective_permissions(permission, user_id);

-- 위험 권한 2인 확인 요청(단계 3에서 쓴다).
CREATE TABLE role_change_requests (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    requested_by INTEGER NOT NULL REFERENCES users(id),
    requested_at TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired')),
    decided_by   INTEGER REFERENCES users(id),
    decided_at   TEXT,
    expires_at   TEXT NOT NULL
);

-- 캐시 유지 트리거(사용자 직접 부여). 조직·직위·조직장 부여의 캐시는 access.Recompute 가 다시 계산한다.
CREATE TRIGGER trg_role_grants_user_ai AFTER INSERT ON role_grants WHEN NEW.target_type = 'user'
BEGIN
    INSERT OR IGNORE INTO user_effective_permissions (user_id, permission, role_id, grant_id)
    SELECT NEW.target_id, rp.permission, NEW.role_id, NEW.id FROM role_permissions rp WHERE rp.role_id = NEW.role_id;
END;
CREATE TRIGGER trg_role_grants_ad AFTER DELETE ON role_grants
BEGIN
    DELETE FROM user_effective_permissions WHERE grant_id = OLD.id;
END;
CREATE TRIGGER trg_role_permissions_ai AFTER INSERT ON role_permissions
BEGIN
    INSERT OR IGNORE INTO user_effective_permissions (user_id, permission, role_id, grant_id)
    SELECT g.target_id, NEW.permission, NEW.role_id, g.id FROM role_grants g WHERE g.role_id = NEW.role_id AND g.target_type = 'user';
END;
CREATE TRIGGER trg_role_permissions_ad AFTER DELETE ON role_permissions
BEGIN
    DELETE FROM user_effective_permissions WHERE role_id = OLD.role_id AND permission = OLD.permission;
END;

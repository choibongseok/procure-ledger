-- migrate:fk-off
-- B33: users.role 의 CHECK(역할 5개 고정)를 푼다. SQLite 는 CHECK 를 지울 수 없어 표를 다시 만든다(사용자 행·번호·다른 표의 외래키는 그대로).
-- role 은 이제 "기본 역할 키"(호환·표시용, 빈 문자열이면 없음)이고 권한 판정은 role_grants → 실효 권한 캐시가 한다.
-- 롤백 주의: 사용자 정의 역할만 가진 사용자(role 이 '' 이거나 r_ 로 시작)가 생긴 뒤에는 이전 코드로 돌아갈 수 없다. 적용 전에 DB 를 백업한다.
PRAGMA legacy_alter_table = ON;

CREATE TABLE users_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    oidc_sub      TEXT UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT '',
    position      TEXT CHECK (position IN ('team_lead','dept_head')),
    department_id INTEGER NOT NULL REFERENCES departments(id),
    active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
INSERT INTO users_new (id, oidc_sub, email, name, role, position, department_id, active, created_at)
SELECT id, oidc_sub, email, name, role, position, department_id, active, created_at FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE INDEX idx_users_department ON users(department_id);

-- 0010 의 조직 투영 보호 트리거(표를 다시 만들며 사라졌으므로 같은 정의로 다시 만든다).
CREATE TRIGGER trg_users_org_projection_guard BEFORE UPDATE OF department_id, position ON users
WHEN EXISTS (SELECT 1 FROM org_person_spans WHERE user_id = NEW.id)
 AND NOT EXISTS (SELECT 1 FROM org_write_guard)
 AND (NEW.department_id IS NOT OLD.department_id OR NEW.position IS NOT OLD.position)
BEGIN SELECT RAISE(ABORT, '소속·직책은 인사 발령으로만 바꿀 수 있습니다'); END;

-- users.role 호환: 템플릿 키로 만들어진·바뀐 사용자에게 같은 템플릿의 직접 부여(source=compat)를 맞춘다(단일 역할 PATCH·시드·시험이 users 를 직접 쓰는 경로).
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

PRAGMA legacy_alter_table = OFF;

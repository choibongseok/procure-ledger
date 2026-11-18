-- B33 이관: 기존 사용자마다 users.role 과 같은 기본 템플릿의 사용자 직접 부여를 만든다. 그러면 실효 권한이 이관 전 RolePermissions[role] 과 같다.
-- (권한 묶음은 서버 기동 때 코드에서 role_permissions 로 동기화되고, 캐시는 트리거가 맞춘다.)
INSERT OR IGNORE INTO role_grants (role_id, target_type, target_id, source, reason, created_at)
SELECT r.id, 'user', u.id, 'compat', 'B33 이관', strftime('%Y-%m-%dT%H:%M:%SZ','now')
FROM users u JOIN roles r ON r.key = u.role AND r.kind = 'template';

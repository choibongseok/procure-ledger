package migrations_test

import (
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B33(AC-1303): 0041 상태의 DB(사용자·세션·토큰·조직 행이 있음)에 0042~0044 를 적용해도 사용자 행·번호·외래키가 그대로이고,
// users.role CHECK 만 풀리며, 기존 사용자마다 같은 템플릿의 직접 부여(호환)가 생긴다.
func TestUsersRoleRelaxPreservesData(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0041_cards.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (1, '구매팀')`)
	mustExec(t, db, `INSERT INTO users (id, email, name, role, position, department_id, oidc_sub) VALUES
		(1,'a@x','가','staff',NULL,1,'s1'),(2,'b@x','나','manager','team_lead',1,'s2'),(3,'c@x','다','finance',NULL,1,NULL),(7,'d@x','라','admin',NULL,1,NULL),(9,'e@x','마','auditor',NULL,1,NULL)`)
	mustExec(t, db, `INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at) VALUES ('h1',2,'c','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO personal_tokens (user_id,name,token_prefix,token_hash,created_at,expires_at) VALUES (3,'t','plp_ab','hash1','a','b')`)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 사용자 행·번호 보존(AUTOINCREMENT 계속 이어짐).
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 5 {
		t.Fatalf("사용자 %d명", n)
	}
	var email, pos string
	_ = db.QueryRow(`SELECT email, COALESCE(position,'') FROM users WHERE id = 2 AND oidc_sub = 's2'`).Scan(&email, &pos)
	if email != "b@x" || pos != "team_lead" {
		t.Fatalf("행이 보존되지 않음: %q %q", email, pos)
	}
	mustExec(t, db, `INSERT INTO users (email, name, role, department_id) VALUES ('f@x','바','staff',1)`)
	var newID int64
	_ = db.QueryRow(`SELECT id FROM users WHERE email='f@x'`).Scan(&newID)
	if newID != 10 {
		t.Fatalf("다음 사용자 번호 %d(10 이어야 함)", newID)
	}
	// 외래키 위반 0건, 다른 표의 참조가 새 users 를 가리킨다.
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("외래키 위반이 있음")
	}
	rows.Close()
	var sess int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions s JOIN users u ON u.id = s.user_id`).Scan(&sess)
	if sess != 1 {
		t.Fatal("세션이 사용자와 이어지지 않음")
	}
	// 외래키 검사가 다시 켜져 있다.
	var fk int
	_ = db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Fatal("마이그레이션 뒤 외래키 검사가 꺼진 채로 남음")
	}
	wantErr(t, db, "FOREIGN KEY", `INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at) VALUES ('h2',999,'c','a','b','c')`)
	// CHECK 가 풀렸다: 사용자 정의 역할 키·빈 값 허용. position 의 CHECK 와 인덱스·보호 트리거는 남아 있다.
	mustExec(t, db, `INSERT INTO users (email, name, role, department_id) VALUES ('g@x','사','r_5',1)`)
	mustExec(t, db, `INSERT INTO users (email, name, department_id) VALUES ('h@x','아',1)`)
	wantErr(t, db, "CHECK", `INSERT INTO users (email,name,role,position,department_id) VALUES ('i@x','자','staff','boss',1)`)
	var idx, trg int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_users_department'`).Scan(&idx)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='trg_users_org_projection_guard'`).Scan(&trg)
	if idx != 1 || trg != 1 {
		t.Fatalf("인덱스 %d 트리거 %d", idx, trg)
	}
	// 기존 사용자 5명 + 새 사용자 f(staff) = 템플릿 직접 부여(호환) 6건. r_5·빈 역할은 부여가 없다.
	var g int
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants WHERE source='compat' AND target_type='user'`).Scan(&g)
	if g != 6 {
		t.Fatalf("호환 부여 %d건(6 이어야 함)", g)
	}
	var keyOf string
	_ = db.QueryRow(`SELECT r.key FROM role_grants g JOIN roles r ON r.id = g.role_id WHERE g.target_id = 7`).Scan(&keyOf)
	if keyOf != "admin" {
		t.Fatalf("7번 사용자의 부여 %q", keyOf)
	}
	// users.role 이 바뀌면 호환 부여가 교체된다(단일 역할 PATCH 호환).
	mustExec(t, db, `UPDATE users SET role = 'finance' WHERE id = 1`)
	_ = db.QueryRow(`SELECT r.key FROM role_grants g JOIN roles r ON r.id = g.role_id WHERE g.target_id = 1`).Scan(&keyOf)
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants WHERE target_id = 1`).Scan(&n)
	if keyOf != "finance" || n != 1 {
		t.Fatalf("역할 변경 뒤 부여 %q(%d건)", keyOf, n)
	}
	// 템플릿 권한이 들어오면 캐시가 트리거로 맞는다.
	mustExec(t, db, `INSERT INTO role_permissions (role_id, permission) SELECT id, 'purchase:read' FROM roles WHERE key='finance'`)
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id IN (1,3) AND permission='purchase:read'`).Scan(&n)
	if n != 2 {
		t.Fatalf("캐시 행 %d(2 이어야 함)", n)
	}
	mustExec(t, db, `DELETE FROM role_grants WHERE target_id = 3`)
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = 3`).Scan(&n)
	if n != 0 {
		t.Fatal("부여를 지웠는데 캐시가 남음")
	}
}

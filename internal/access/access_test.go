package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

const today = contract.Day("2026-11-12")

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (1, '구매팀')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func addUser(t *testing.T, db *sql.DB, email, role string) int64 {
	t.Helper()
	return dbtest.InsertID(t, db, `INSERT INTO users (email, name, role, department_id) VALUES (?,?,?,1)`, email, email, role)
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// AC-1300: DB 의 기본 템플릿 권한 = 골든 = 코드(RolePermissions). 권한을 바꾸면 UPDATE_GOLDEN=1 로 골든을 다시 만들고 명세 사유를 적는다(골든 diff 가 곧 변경 목록).
func TestTemplatesMatchGoldenAndCode(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	g := loadGolden(t)
	for role, perms := range g.Roles {
		want := append([]string(nil), perms...)
		sort.Strings(want)
		rows, err := db.Query(`SELECT rp.permission FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.key = ? ORDER BY rp.permission`, role)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var p string
			_ = rows.Scan(&p)
			got = append(got, p)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: DB 템플릿 권한이 골든과 다름\n got  %v\n want %v", role, got, want)
		}
		code := currentMatrix().Roles[role]
		if !reflect.DeepEqual(code, want) {
			t.Errorf("%s: 코드의 권한이 골든과 다름(권한을 바꿨다면 UPDATE_GOLDEN=1 로 골든 갱신)\n code %v\n want %v", role, code, want)
		}
	}
}

// AC-1301: 이관(호환 부여) 직후 모든 사용자의 실효 권한이 RolePermissions[users.role] 과 정확히 같다.
func TestEffectivePermsEqualTemplatePerms(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	ids := map[contract.Role]int64{}
	for i, r := range contract.Roles {
		ids[r] = addUser(t, db, string(r)+"@x", string(r))
		_ = i
	}
	if _, err := SyncTemplates(ctx, db); err != nil { // 사용자가 먼저 있고 템플릿 권한이 나중에 들어오는 순서(기존 DB 이관)
		t.Fatal(err)
	}
	for r, id := range ids {
		got, err := PermsOf(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		want := map[contract.Permission]bool{}
		for _, p := range contract.RolePermissions[r] {
			want[p] = true
		}
		if len(got) != len(want) {
			t.Errorf("%s: 권한 %d개(기대 %d)", r, len(got), len(want))
		}
		for p := range want {
			if !got.Has(p) {
				t.Errorf("%s: %s 가 빠짐", r, p)
			}
		}
		pr := contract.Principal{UserID: id, Role: r, Perms: got}
		for _, p := range contract.AllPermissions {
			if pr.Can(p) != contract.RoleHas(r, p) {
				t.Errorf("%s: Can(%s)=%v 이전 판정 %v", r, p, pr.Can(p), contract.RoleHas(r, p))
			}
		}
	}
	// 나중에 만든 사용자도 같다(호환 트리거).
	late := addUser(t, db, "late@x", "finance")
	if p, _ := PermsOf(ctx, db, late); !p.Has(contract.PermLedgerWrite) || p.Has(contract.PermAuditRead) {
		t.Fatalf("나중에 만든 재무 사용자의 권한이 이상함: %v", SortedStrings(p))
	}
	// 역할이 바뀌면 권한도 바뀐다.
	if _, err := db.Exec(`UPDATE users SET role = 'auditor' WHERE id = ?`, late); err != nil {
		t.Fatal(err)
	}
	if p, _ := PermsOf(ctx, db, late); p.Has(contract.PermLedgerWrite) || !p.Has(contract.PermAuditRead) {
		t.Fatalf("역할 변경 뒤 권한: %v", SortedStrings(p))
	}
}

// 권한이 없는 사용자는 빈(non-nil) 집합이고 아무것도 못 한다(fail-closed).
func TestNoGrantMeansNoPermissions(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	_, _ = SyncTemplates(ctx, db)
	id := addUser(t, db, "none@x", "")
	p, err := PermsOf(ctx, db, id)
	if err != nil || p == nil || len(p) != 0 {
		t.Fatalf("%v %v", p, err)
	}
	pr := contract.Principal{UserID: id, Role: contract.RoleAdmin, Perms: p} // Role 이 admin 이어도 집합이 비면 거부
	for _, perm := range contract.AllPermissions {
		if pr.Can(perm) {
			t.Fatalf("빈 권한 집합인데 %s 허용", perm)
		}
	}
	if !pr.Can(contract.PermAuthenticated) || !pr.Can(contract.PermSetup) || !pr.Can(contract.PermPublic) {
		t.Fatal("로그인 사용자는 authenticated·setup 경로가 열려야 함")
	}
}

// SyncTemplates 는 멱등이고 코드에 없는 권한은 제거한다.
func TestSyncTemplatesIdempotentAndRemovesUnknown(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	r1, err := SyncTemplates(ctx, db)
	if err != nil || r1.Added == 0 {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := SyncTemplates(ctx, db)
	if r2.Added+r2.Removed+len(r2.Unknown) != 0 {
		t.Fatalf("두 번째는 바뀌는 게 없어야 함: %+v", r2)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'ghost:perm' FROM roles WHERE key='staff'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'admin:everything' FROM roles WHERE key='auditor'`); err != nil {
		t.Fatal(err)
	}
	r3, _ := SyncTemplates(ctx, db)
	if r3.Removed != 2 {
		t.Fatalf("템플릿에서 코드에 없는 권한 제거: %+v", r3)
	}
	// 사용자 정의 역할에 든 코드에 없는 권한도 제거되고 보고된다(코드에서 권한이 삭제된 경우).
	if _, err := db.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_1','구매 전담','custom','a','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, p FROM roles, (SELECT 'purchase:read' AS p UNION SELECT 'removed:perm') WHERE key='r_1'`); err != nil {
		t.Fatal(err)
	}
	if r, _ := SyncTemplates(ctx, db); len(r.Unknown) != 1 || r.Unknown[0] != "r_1:removed:perm" {
		t.Fatalf("사용자 정의 역할의 알 수 없는 권한: %+v", r)
	}
	// 템플릿이 코드와 다르게 고쳐져 있으면 코드에 맞춘다.
	if _, err := db.Exec(`DELETE FROM role_permissions WHERE role_id = (SELECT id FROM roles WHERE key='staff') AND permission = 'purchase:create'`); err != nil {
		t.Fatal(err)
	}
	if r4, _ := SyncTemplates(ctx, db); r4.Added != 1 {
		t.Fatalf("템플릿 복구: %+v", r4)
	}
}

// AC-1311(기반): 캐시는 전체 재계산 결과와 같다. 어긋나면 감지하고 재계산이 바로잡는다.
func TestCacheMatchesRecompute(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, r := range contract.Roles {
		addUser(t, db, string(r)+"@x", string(r))
	}
	_, _ = SyncTemplates(ctx, db)
	if d, err := CacheDiffers(ctx, db, today); err != nil || d {
		t.Fatalf("트리거가 맞춘 캐시가 재계산과 다름: %v %v", d, err)
	}
	if _, err := db.Exec(`DELETE FROM user_effective_permissions WHERE (user_id, permission, role_id, grant_id) IN (SELECT user_id, permission, role_id, grant_id FROM user_effective_permissions ORDER BY user_id, permission, role_id, grant_id LIMIT 3)`); err != nil {
		t.Fatal(err)
	}
	if d, _ := CacheDiffers(ctx, db, today); !d {
		t.Fatal("어긋난 캐시를 감지하지 못함")
	}
	tx, _ := db.BeginTx(ctx, nil)
	n, err := RecomputeAll(ctx, tx, today)
	if err != nil || n != 3 {
		t.Fatalf("복구 %d행 %v", n, err)
	}
	_ = tx.Commit()
	if d, _ := CacheDiffers(ctx, db, today); d {
		t.Fatal("재계산 뒤에도 다름")
	}
}

func TestSourcesAndRoleNames(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	_, _ = SyncTemplates(ctx, db)
	id := addUser(t, db, "fin@x", "finance")
	src, err := Sources(ctx, db, id)
	if err != nil || len(src) == 0 {
		t.Fatalf("%v %v", src, err)
	}
	for _, s := range src {
		if s.RoleName != "재무" || s.TargetType != "user" || s.TargetID != id {
			t.Fatalf("출처: %+v", s)
		}
	}
	names, _ := RoleNamesOf(ctx, db, id)
	if !reflect.DeepEqual(names, []string{"재무"}) {
		t.Fatalf("%v", names)
	}
}

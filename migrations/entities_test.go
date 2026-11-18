package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B44 1단계: 0073(법인·사업장·기본 법인)·0074(부여 범위·감사 법인 열). 이관 전 데이터는 그대로이고 기본 법인(id 1)으로 옮겨진다(AC-4401·4402·4415).

func openEntityDB(t *testing.T, through string) (*sql.DB, *audit.Store, contract.TxManager) {
	t.Helper()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(context.Background(), db, through); err != nil {
		t.Fatal(err)
	}
	return db, audit.New(db), dbutil.NewTxManager(db, dbutil.DialectOf(db))
}

func recordN(t *testing.T, store *audit.Store, txs contract.TxManager, ctx context.Context, n int, entity int64) {
	t.Helper()
	actor := contract.Principal{UserID: 7, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	for i := 0; i < n; i++ {
		if err := txs.WithTx(ctx, func(tx contract.Tx) error {
			return store.Record(ctx, tx, caudit.Entry{Actor: actor, Action: "setting.x", TargetType: caudit.TargetSetting, TargetID: "x", Result: caudit.ResultSuccess, EntityID: entity})
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func verifyOK(t *testing.T, store *audit.Store) {
	t.Helper()
	res, err := store.Verify(context.Background())
	if err != nil || !res.ChainOK {
		t.Fatalf("체인 검증: %+v %v", res, err)
	}
}

func TestEntityMigrationMovesCompanyToDefaultEntity(t *testing.T) {
	ctx := context.Background()
	db, store, txs := openEntityDB(t, "0069_tax_invoices.sql")
	if _, err := db.Exec(`INSERT INTO company_settings (id, name, business_no, representative, address, phone, fiscal_start_month, version, updated_at) VALUES (1, '에이엑스 주식회사', '123-45-67890', '홍길동', '서울', '02-1', 4, 3, '2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"a@x.test", "b@x.test", "c@x.test"} {
		if _, err := db.Exec(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, 'staff', 1)`, e, e); err != nil {
			t.Fatal(err)
		}
	}
	// 템플릿 역할의 권한은 서버 기동 때 동기화되므로 시험에서는 직접 넣는다(캐시 유지 트리거가 기존 부여의 캐시 행을 만든다).
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'approval:read' FROM roles WHERE key IN ('staff','finance')`); err != nil {
		t.Fatal(err)
	}
	var grantsBefore, cacheBefore int
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants`).Scan(&grantsBefore)
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions`).Scan(&cacheBefore)
	if grantsBefore == 0 || cacheBefore == 0 {
		t.Fatalf("시험 전제: 부여 %d, 캐시 %d", grantsBefore, cacheBefore)
	}
	recordN(t, store, txs, ctx, 5, 0)
	var headBefore string
	var seqBefore int64
	_ = db.QueryRow(`SELECT seq, hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&seqBefore, &headBefore)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var code, name, bizNo string
	var fiscal int
	var def int
	if err := db.QueryRow(`SELECT code, name, business_no, fiscal_start_month, is_default FROM entities WHERE id = 1`).Scan(&code, &name, &bizNo, &fiscal, &def); err != nil {
		t.Fatal(err)
	}
	if code != "MAIN" || name != "에이엑스 주식회사" || bizNo != "123-45-67890" || fiscal != 4 || def != 1 {
		t.Fatalf("기본 법인: %s %s %s %d %d", code, name, bizNo, fiscal, def)
	}
	var sites int
	_ = db.QueryRow(`SELECT COUNT(*) FROM entity_sites WHERE entity_id = 1 AND site_no = '0000' AND kind = 'head'`).Scan(&sites)
	if sites != 1 {
		t.Fatal("주사업장이 만들어지지 않음")
	}
	var grantsAfter, cacheAfter, ownRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants`).Scan(&grantsAfter)
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions`).Scan(&cacheAfter)
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants WHERE scope = 'own' AND scope_entity_id = 0`).Scan(&ownRows)
	if grantsAfter != grantsBefore || cacheAfter != cacheBefore || ownRows != grantsAfter {
		t.Fatalf("부여·캐시 보존: 부여 %d→%d, 캐시 %d→%d, own %d", grantsBefore, grantsAfter, cacheBefore, cacheAfter, ownRows)
	}
	// 사용자 직접 부여 트리거가 범위 열을 채운다(새 사용자 → 호환 부여 → 캐시).
	if _, err := db.Exec(`INSERT INTO users (email, name, role, department_id) VALUES ('d@x.test', 'd', 'finance', 1)`); err != nil {
		t.Fatal(err)
	}
	var scoped int
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE u.email = 'd@x.test' AND e.scope = 'own' AND e.entity_id = 0`).Scan(&scoped)
	if scoped == 0 {
		t.Fatal("새 사용자의 캐시 행이 만들어지지 않음")
	}
	// 이관 전 기록은 그대로 검증되고 머리 해시가 같다. 이관 경계(epoch)가 머리 seq 로 남는다.
	verifyOK(t, store)
	var headAfter, epoch string
	_ = db.QueryRow(`SELECT hash FROM audit_logs WHERE seq = ?`, seqBefore).Scan(&headAfter)
	_ = db.QueryRow(`SELECT value FROM app_settings WHERE key = 'audit.entity_epoch_seq'`).Scan(&epoch)
	if headAfter != headBefore || epoch != "5" {
		t.Fatalf("머리 해시 %q→%q, epoch %q", headBefore, headAfter, epoch)
	}
	// 회사 설정을 저장하면 기본 법인이 따라온다.
	if _, err := db.Exec(`UPDATE company_settings SET name = '새 이름', business_no = '111-22-33333', version = version + 1, updated_at = '2026-11-02T00:00:00Z' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT name, business_no FROM entities WHERE id = 1`).Scan(&name, &bizNo)
	if name != "새 이름" || bizNo != "111-22-33333" {
		t.Fatalf("회사 설정 동기화: %s %s", name, bizNo)
	}
	// 기본 법인은 지우거나 끌 수 없다. 사업자번호는 법인마다 유일하다.
	if _, err := db.Exec(`DELETE FROM entities WHERE id = 1`); err == nil {
		t.Fatal("법인이 삭제됨")
	}
	if _, err := db.Exec(`UPDATE entities SET active = 0 WHERE id = 1`); err == nil {
		t.Fatal("기본 법인이 비활성화됨")
	}
	if _, err := db.Exec(`INSERT INTO entities (code, name, business_no, created_at, updated_at) VALUES ('ABC', '계열사', '111-22-33333', 't', 't')`); err == nil {
		t.Fatal("사업자번호 중복이 들어감")
	}
	if _, err := db.Exec(`INSERT INTO entities (code, name, created_at, updated_at) VALUES ('abc', '계열사', 't', 't')`); err == nil {
		t.Fatal("소문자 코드가 들어감")
	}
	if _, err := db.Exec(`INSERT INTO entities (code, name, created_at, updated_at) VALUES ('ABC', '계열사', 't', 't')`); err != nil {
		t.Fatal(err)
	}
}

func TestEntityMigrationWithoutCompanyUsesOrgRootName(t *testing.T) {
	ctx := context.Background()
	db, _, _ := openEntityDB(t, "0069_tax_invoices.sql")
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM entities WHERE id = 1`).Scan(&name); err != nil || name == "" {
		t.Fatalf("회사 설정이 없어도 기본 법인이 만들어져야 한다: %q %v", name, err)
	}
}

// 감사 체인(B44 13.1, AC-4415): 기존 기록은 그대로 검증되고, 법인이 있는 새 기록은 법인 값을 바꾸면 검증이 그 지점을 짚으며, 이관 전 행의 법인 값 변조도 짚는다.
func TestAuditEntityChainTamperDetection(t *testing.T) {
	ctx := context.Background()
	db, store, txs := openEntityDB(t, "0069_tax_invoices.sql")
	recordN(t, store, txs, ctx, 3, 0)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	verifyOK(t, store)
	recordN(t, store, txs, ctx, 1, 0) // seq 4: 법인과 무관
	recordN(t, store, txs, ctx, 1, 2) // seq 5: 법인 2
	recordN(t, store, txs, ctx, 1, 2) // seq 6
	recordN(t, store, txs, ctx, 1, 0) // seq 7
	var e5, e4 int64
	_ = db.QueryRow(`SELECT entity_id FROM audit_logs WHERE seq = 5`).Scan(&e5)
	_ = db.QueryRow(`SELECT entity_id FROM audit_logs WHERE seq = 4`).Scan(&e4)
	if e5 != 2 || e4 != 0 {
		t.Fatalf("법인 값 저장: seq4=%d seq5=%d", e4, e5)
	}
	verifyOK(t, store)

	// 사후 변조는 추가 전용 트리거를 풀어야 가능하다(DB 파일에 직접 접근하는 사람).
	if _, err := db.Exec(`DROP TRIGGER trg_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	broken := func(seq int64, wantWhy string) {
		t.Helper()
		res, err := store.Verify(ctx)
		if err != nil || res.ChainOK || res.BrokenSeq != seq || !strings.Contains(res.BrokenWhy, wantWhy) {
			t.Fatalf("seq %d 에서 %q 로 깨져야 한다: %+v %v", seq, wantWhy, res, err)
		}
	}
	for _, c := range []struct {
		name string
		from int64
	}{{"2→3", 3}, {"2→0", 0}} {
		if _, err := db.Exec(`UPDATE audit_logs SET entity_id = ? WHERE seq = 5`, c.from); err != nil {
			t.Fatal(err)
		}
		broken(5, "변조")
		_ = c.name
	}
	if _, err := db.Exec(`UPDATE audit_logs SET entity_id = 2 WHERE seq = 5`); err != nil {
		t.Fatal(err)
	}
	verifyOK(t, store)
	// 이관 전 행(seq <= epoch)의 법인 값은 해시에 없어도 경계 검사가 잡는다.
	if _, err := db.Exec(`UPDATE audit_logs SET entity_id = 1 WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	broken(2, "이관 전 기록의 법인 값")
	if _, err := db.Exec(`UPDATE audit_logs SET entity_id = 0 WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	verifyOK(t, store)
}

// 이관 전 마이그레이션 상태의 DB(옛 백업 세트)를 새 코드가 그대로 열어 체인을 검증하고 목록을 읽을 수 있다(명세 13.1.1 보완 1).
func TestAuditVerifyWorksOnPreEntitySchema(t *testing.T) {
	ctx := context.Background()
	_, store, txs := openEntityDB(t, "0069_tax_invoices.sql")
	recordN(t, store, txs, ctx, 4, 7) // 열이 없는 스키마에서는 법인 값을 남기지 않는다(0 으로 기록)
	verifyOK(t, store)
	recs, err := store.List(ctx, caudit.Filter{})
	if err != nil || len(recs) != 4 {
		t.Fatalf("목록: %d %v", len(recs), err)
	}
	for _, r := range recs {
		if r.EntityID != 0 {
			t.Fatalf("이관 전 스키마의 기록은 법인 0: %d", r.EntityID)
		}
	}
}

// 법인 범위 감사 담당은 범위 안의 법인 사건과 법인과 무관한 사건만 본다.
func TestAuditListFiltersByEntityScope(t *testing.T) {
	ctx := context.Background()
	db, store, txs := openEntityDB(t, "0069_tax_invoices.sql")
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	recordN(t, store, txs, ctx, 2, 0)
	recordN(t, store, txs, ctx, 3, 1)
	recordN(t, store, txs, ctx, 4, 2)
	count := func(sc *contract.EntityScope) int {
		recs, err := store.List(ctx, caudit.Filter{Entities: sc, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return len(recs)
	}
	if n := count(nil); n != 9 {
		t.Fatalf("필터 없음: %d", n)
	}
	if n := count(&contract.EntityScope{All: true}); n != 9 {
		t.Fatalf("그룹: %d", n)
	}
	if n := count(&contract.EntityScope{IDs: []int64{1}}); n != 5 {
		t.Fatalf("법인 1: %d(법인 3건 + 무관 2건)", n)
	}
	if n := count(&contract.EntityScope{IDs: []int64{2}}); n != 6 {
		t.Fatalf("법인 2: %d", n)
	}
	if n := count(&contract.EntityScope{}); n != 2 {
		t.Fatalf("범위 없음: %d(법인과 무관한 사건만)", n)
	}
}

// 감사 로그의 법인 색인은 마이그레이션이 아니라 별도 단계(erp-admin audit-entity-index)로 만든다: 이관 직후에는 없고, 만든 뒤에는 다시 불러도 같다(멱등).
func TestEnsureEntityIndexIsSeparateAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, _, _ := openEntityDB(t, "0069_tax_invoices.sql")
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	has := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_audit_entity'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if has() != 0 {
		t.Fatal("마이그레이션이 감사 색인을 만들었다: 큰 표의 색인 생성은 별도 단계여야 한다")
	}
	for i := 0; i < 2; i++ {
		if err := audit.EnsureEntityIndex(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if has() != 1 {
		t.Fatal("색인이 만들어지지 않았다")
	}
}

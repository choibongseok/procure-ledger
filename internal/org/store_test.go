package org

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/access"
	"math/rand"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

func testDB(t *testing.T) (*sql.DB, contract.SQLTxManager) {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(context.Background(), db); err != nil { // 기본 역할 템플릿 권한(B33)
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (1, '임시부서')`); err != nil {
		t.Fatal(err)
	}
	return db, contract.SQLTxManager{DB: db}
}

func addUser(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (?, ?, ?, 'staff', 1)`,
		id, fmt.Sprintf("u%d@pl.test", id), fmt.Sprintf("사용자%d", id)); err != nil {
		t.Fatal(err)
	}
}

// register 는 조직 서비스가 할 일의 뼈대다: 이벤트를 추가하고 같은 트랜잭션에서 구간을 다시 쓴다.
func register(ctx context.Context, txs contract.SQLTxManager, evs ...Event) error {
	return txs.WithTx(ctx, func(tx contract.Tx) error {
		for _, e := range evs {
			if _, err := Append(ctx, tx, e); err != nil {
				return err
			}
		}
		_, err := Rebuild(ctx, tx)
		return err
	})
}

func seedBase(t *testing.T, db *sql.DB, txs contract.SQLTxManager) {
	t.Helper()
	ctx := context.Background()
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		for _, u := range []struct {
			code, name string
			kind       contract.OrgKind
			parent     int64
		}{{"CO", "PL 주식회사", contract.OrgCompany, 0}, {"DEP", "구매팀", contract.OrgDepartment, 1}, {"T1", "구매1팀", contract.OrgTeam, 2}, {"T2", "구매2팀", contract.OrgTeam, 2}} {
			id, err := NewUnitID(ctx, tx, u.code, 0)
			if err != nil {
				return err
			}
			if _, err := Append(ctx, tx, Event{Subject: SubjectUnit, SubjectID: id, Kind: KindUnitCreate, Effective: "2020-01-01",
				P: Payload{Code: u.code, Name: u.name, UnitKind: u.kind, Parent: u.parent, Sort: int(id)}}); err != nil {
				return err
			}
		}
		_, err := Rebuild(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreRebuildMatchesFold(t *testing.T) {
	db, txs := testDB(t)
	ctx := context.Background()
	seedBase(t, db, txs)
	addUser(t, db, 10)
	if err := register(ctx, txs,
		Event{Subject: SubjectPerson, SubjectID: 10, Kind: KindHire, Effective: "2021-01-01", P: Payload{Rank: 1, Unit: 3}},
		Event{Subject: SubjectUnit, SubjectID: 3, Kind: KindHeadSet, Effective: "2021-02-01", P: Payload{User: 10}},
	); err != nil {
		t.Fatal(err)
	}
	if err := Consistent(ctx, db); err != nil {
		t.Fatalf("정합성: %v", err)
	}
	var head int64
	if err := db.QueryRow(`SELECT head_user_id FROM org_unit_spans WHERE unit_id = 3 AND valid_from <= '2021-03-01' AND (valid_to IS NULL OR '2021-03-01' < valid_to)`).Scan(&head); err != nil || head != 10 {
		t.Fatalf("저장된 조직장 %d %v", head, err)
	}
}

// 규칙을 어기는 이벤트는 등록 트랜잭션 전체가 롤백되어 로그에도 구간에도 남지 않는다.
func TestStoreInvalidEventRollsBack(t *testing.T) {
	db, txs := testDB(t)
	ctx := context.Background()
	seedBase(t, db, txs)
	addUser(t, db, 10)
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&before)
	err := register(ctx, txs, Event{Subject: SubjectPerson, SubjectID: 10, Kind: KindTransfer, Effective: "2021-01-01", P: Payload{Unit: 3}})
	if err == nil || !strings.Contains(err.Error(), "입사 전인 사람") {
		t.Fatalf("입사하지 않은 사람 전보: %v", err)
	}
	var after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&after)
	if after != before {
		t.Fatalf("롤백되지 않음: %d → %d", before, after)
	}
	if err := Consistent(ctx, db); err != nil {
		t.Fatal(err)
	}
}

// AC-35: 이벤트 로그와 조직 식별자는 수정·삭제할 수 없다.
func TestStoreAppendOnlyTriggers(t *testing.T) {
	db, txs := testDB(t)
	seedBase(t, db, txs)
	for _, q := range []string{
		`UPDATE org_events SET reason = 'x' WHERE id = 1`, `DELETE FROM org_events WHERE id = 1`,
		`UPDATE org_units SET code = 'X' WHERE id = 1`, `DELETE FROM org_units WHERE id = 1`,
	} {
		if _, err := db.Exec(q); err == nil || !strings.Contains(err.Error(), "추가 전용") {
			t.Errorf("%s: 추가 전용 오류를 기대했으나 %v", q, err)
		}
	}
}

// AC-63: 쓰기 보호 행 없이는 구간 테이블과 등록된 사용자의 부서·직책을 바꿀 수 없다. 미등록 사용자는 이전처럼 된다.
func TestStoreWriteGuard(t *testing.T) {
	db, txs := testDB(t)
	ctx := context.Background()
	seedBase(t, db, txs)
	addUser(t, db, 10)
	addUser(t, db, 11) // 조직에 등록하지 않는 사용자
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (2, '다른부서')`); err != nil {
		t.Fatal(err)
	}
	if err := register(ctx, txs, Event{Subject: SubjectPerson, SubjectID: 10, Kind: KindHire, Effective: "2021-01-01", P: Payload{Rank: 1, Unit: 3}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO org_unit_spans (unit_id, valid_from, code, name, kind) VALUES (9, '2020-01-01', 'Z', 'Z', 'team')`,
		`UPDATE org_unit_spans SET name = '바꿈'`, `DELETE FROM org_unit_spans`,
		`UPDATE org_person_spans SET primary_unit_id = 4`, `DELETE FROM org_person_spans`,
		`INSERT INTO org_concurrent_spans (user_id, unit_id, valid_from) VALUES (10, 4, '2021-01-01')`,
		`INSERT INTO org_acting_spans (unit_id, user_id, valid_from) VALUES (3, 10, '2021-01-01')`,
		`UPDATE users SET department_id = 2 WHERE id = 10`, `UPDATE users SET position = 'team_lead' WHERE id = 10`,
	} {
		if _, err := db.Exec(q); err == nil || !(strings.Contains(err.Error(), "조직 서비스만") || strings.Contains(err.Error(), "인사 발령으로만")) {
			t.Errorf("%s: 보호 오류를 기대했으나 %v", q, err)
		}
	}
	if _, err := db.Exec(`UPDATE users SET department_id = 1, position = 'team_lead' WHERE id = 11`); err != nil {
		t.Errorf("조직 미등록 사용자는 이전처럼 바꿀 수 있어야 함: %v", err)
	}
	// 이름 같은 값으로 바꾸는 UPDATE(변화 없음)나 다른 칸 수정은 막지 않는다.
	if _, err := db.Exec(`UPDATE users SET name = '새 이름', department_id = department_id WHERE id = 10`); err != nil {
		t.Errorf("부서·직책이 그대로인 수정은 허용: %v", err)
	}
	// 보호 행이 있는 트랜잭션 안에서는 바꿀 수 있고, 끝나면 보호 행이 남지 않는다.
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		return withGuard(ctx, tx, func() error {
			_, err := tx.ExecContext(ctx, `UPDATE users SET department_id = 2 WHERE id = 10`)
			return err
		})
	})
	if err != nil {
		t.Fatalf("보호 행 안에서는 허용: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_write_guard`).Scan(&n)
	if n != 0 {
		t.Fatalf("보호 행이 남음: %d", n)
	}
}

// AC-61: 저장된 구간을 몰래 바꾸면 정합성 검사가 잡는다.
func TestStoreConsistencyDetectsTampering(t *testing.T) {
	db, txs := testDB(t)
	ctx := context.Background()
	seedBase(t, db, txs)
	addUser(t, db, 10)
	if err := register(ctx, txs, Event{Subject: SubjectPerson, SubjectID: 10, Kind: KindHire, Effective: "2021-01-01", P: Payload{Rank: 1, Unit: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := Consistent(ctx, db); err != nil {
		t.Fatal(err)
	}
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		return withGuard(ctx, tx, func() error {
			_, err := tx.ExecContext(ctx, `UPDATE org_person_spans SET primary_unit_id = 4 WHERE user_id = 10`)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Consistent(ctx, db); err == nil || !strings.Contains(err.Error(), "사람 구간이 다릅니다") {
		t.Fatalf("변조를 못 잡음: %v", err)
	}
}

// AC-61: 무작위로 만든 유효한 이벤트 열 뒤에도 저장된 구간이 다시 계산한 결과와 같다.
func TestStoreRandomEventsStayConsistent(t *testing.T) {
	db, txs := testDB(t)
	ctx := context.Background()
	seedBase(t, db, txs)
	const people = 12
	for u := int64(10); u < 10+people; u++ {
		addUser(t, db, u)
	}
	rng := rand.New(rand.NewSource(7))
	days := []string{"2021-01-01", "2021-03-01", "2021-06-15", "2022-01-01", "2022-09-01", "2023-05-05", "2024-02-02"}
	units := []int64{1, 2, 3, 4}
	applied := 0
	for i := 0; i < 400; i++ {
		u := int64(10 + rng.Intn(people))
		day := contract.Day(days[rng.Intn(len(days))])
		var e Event
		switch rng.Intn(7) {
		case 0:
			e = Event{Subject: SubjectPerson, SubjectID: u, Kind: KindHire, Effective: day, P: Payload{Rank: 1, Unit: units[1+rng.Intn(3)]}}
		case 1:
			e = Event{Subject: SubjectPerson, SubjectID: u, Kind: KindTransfer, Effective: day, P: Payload{Unit: units[1+rng.Intn(3)]}}
		case 2:
			e = Event{Subject: SubjectPerson, SubjectID: u, Kind: KindConcurrentAdd, Effective: day, P: Payload{Unit: units[1+rng.Intn(3)]}}
		case 3:
			e = Event{Subject: SubjectPerson, SubjectID: u, Kind: KindConcurrentEnd, Effective: day, P: Payload{Unit: units[1+rng.Intn(3)]}}
		case 4:
			e = Event{Subject: SubjectUnit, SubjectID: units[1+rng.Intn(3)], Kind: KindHeadSet, Effective: day, P: Payload{User: u}}
		case 5:
			e = Event{Subject: SubjectPerson, SubjectID: u, Kind: KindLeave, Effective: AddDays(day, 1), P: Payload{LastDay: day}}
		default:
			e = Event{Subject: SubjectUnit, SubjectID: units[1+rng.Intn(3)], Kind: KindActingSet, Effective: day, P: Payload{User: u, End: AddDays(day, 10)}}
		}
		if err := register(ctx, txs, e); err == nil {
			applied++
		}
	}
	if applied < 40 {
		t.Fatalf("유효한 이벤트가 너무 적게 들어감: %d", applied)
	}
	if err := Consistent(ctx, db); err != nil {
		t.Fatalf("무작위 이벤트 %d 건 뒤 정합성: %v", applied, err)
	}
}

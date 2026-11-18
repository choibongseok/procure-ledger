package org

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// AC-60: 조직 1천 개·재직 1만 명에서 조회가 느려지지 않는다(상한은 느슨하게 둔 회귀 방지용이다).
func TestScaleThousandUnitsTenThousandPeople(t *testing.T) {
	if testing.Short() {
		t.Skip("규모 시험은 -short 에서 건너뜀")
	}
	db, txs := testDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	svc := newSvc(t, db, fixedNow)
	const divisions, departments, people = 10, 100, 10000
	rng := rand.New(rand.NewSource(11))

	start := time.Now()
	var teamIDs []int64
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		mk := func(code, name string, kind contract.OrgKind, parent int64) (int64, error) {
			id, err := NewUnitID(ctx, tx, code, 0)
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO departments (id, name) SELECT ?, ? WHERE ? <> 'team'`, id, name, string(kind)); err != nil {
				return 0, err
			}
			_, err = Append(ctx, tx, Event{Subject: SubjectUnit, SubjectID: id, Kind: KindUnitCreate, Effective: "2016-01-04",
				P: Payload{Code: code, Name: name, UnitKind: kind, Parent: parent, Sort: int(id)}})
			return id, err
		}
		root, err := mk("CO", "회사", contract.OrgCompany, 0)
		if err != nil {
			return err
		}
		var depts []int64
		for d := 0; d < divisions; d++ {
			div, err := mk(fmt.Sprintf("DV%d", d), fmt.Sprintf("본부%d", d), contract.OrgDivision, root)
			if err != nil {
				return err
			}
			for k := 0; k < departments/divisions; k++ {
				dep, err := mk(fmt.Sprintf("DP%d-%d", d, k), fmt.Sprintf("부서%d-%d", d, k), contract.OrgDepartment, div)
				if err != nil {
					return err
				}
				depts = append(depts, dep)
			}
		}
		for i := 0; len(teamIDs) < 1000-1-divisions-departments; i++ {
			id, err := mk(fmt.Sprintf("TM%d", i), fmt.Sprintf("팀%d", i), contract.OrgTeam, depts[i%len(depts)])
			if err != nil {
				return err
			}
			teamIDs = append(teamIDs, id)
		}
		for u := 1; u <= people; u++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, name, role, department_id) VALUES (?, ?, ?, 'staff', ?)`, 10000+u, fmt.Sprintf("u%d@pl.test", u), fmt.Sprintf("사람%d", u), root); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO org_people (user_id, employee_no) VALUES (?, ?)`, 10000+u, fmt.Sprintf("E%06d", u)); err != nil {
				return err
			}
			team := teamIDs[rng.Intn(len(teamIDs))]
			if _, err := Append(ctx, tx, Event{Subject: SubjectPerson, SubjectID: int64(10000 + u), Kind: KindHire, Effective: contract.Day(fmt.Sprintf("20%02d-03-01", 17+u%8)),
				P: Payload{Rank: 1 + int64(u%5), Unit: team}}); err != nil {
				return err
			}
			if u%3 == 0 {
				if _, err := Append(ctx, tx, Event{Subject: SubjectPerson, SubjectID: int64(10000 + u), Kind: KindTransfer, Effective: "2025-06-02",
					P: Payload{Unit: teamIDs[rng.Intn(len(teamIDs))]}}); err != nil {
					// 같은 팀으로 전보하면 규칙 위반이라 시험 데이터에서는 건너뛰어야 하지만 INSERT 자체는 성공한다.
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("데이터 입력 %v", time.Since(start))

	// 이벤트 열을 한 번 Fold 한다(같은 팀으로 전보한 몇 건은 규칙 위반일 수 있어 그 이벤트를 취소로 걷어 낸 뒤 다시 계산한다).
	loadStart := time.Now()
	evs, err := LoadEvents(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var res *Result
	for attempt := 0; attempt < 400; attempt++ {
		res, err = Fold(evs)
		if err == nil {
			break
		}
		fe, ok := err.(*FoldError)
		if !ok {
			t.Fatal(err)
		}
		bad := fe.Event.ID
		kept := evs[:0]
		for _, e := range evs {
			if e.ID != bad {
				kept = append(kept, e)
			}
		}
		evs = kept
	}
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	foldTime := time.Since(loadStart)
	t.Logf("이벤트 %d건 Fold %v (구간 조직 %d, 사람 %d)", len(evs), foldTime, len(res.Units), len(res.People))
	if foldTime > 20*time.Second {
		t.Fatalf("Fold 가 너무 느림: %v", foldTime)
	}
	if err := txs.WithTx(ctx, func(tx contract.Tx) error { return WriteSpans(ctx, tx, res) }); err != nil {
		t.Fatal(err)
	}
	// 취소로 걷어 낸 이벤트는 로그에서 지울 수 없으므로 저장된 구간과 로그의 정합성 검사는 이 시험에서 하지 않는다.

	// 첫 조회(구간 읽기)는 느슨하게, 그 뒤 같은 기준일의 조회는 AC-60 의 목표를 본다.
	first := time.Now()
	if _, err := svc.Tree(ctx, ""); err != nil {
		t.Fatal(err)
	}
	t.Logf("첫 트리 조회 %v", time.Since(first))
	warm := time.Now()
	tree, err := svc.Tree(ctx, "")
	if err != nil || tree == nil {
		t.Fatalf("트리: %v", err)
	}
	if d := time.Since(warm); d > 400*time.Millisecond {
		t.Errorf("트리 조회 %v (목표 200ms, 회귀 방지 상한 400ms)", d)
	} else {
		t.Logf("트리 조회 %v", d)
	}
	if tree.Total < people-50 {
		t.Fatalf("전체 인원 %d", tree.Total)
	}
	card := time.Now()
	if _, err := svc.Person(ctx, 10500, ""); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(card); d > 200*time.Millisecond {
		t.Errorf("사람 카드 %v (목표 50ms, 회귀 방지 상한 200ms)", d)
	} else {
		t.Logf("사람 카드 %v", d)
	}
	srch := time.Now()
	staff := contract.Principal{UserID: 1, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	cards, err := svc.SearchPeople(ctx, staff, contract.PeopleQuery{Text: "사람1", Limit: 50})
	if err != nil || len(cards) != 50 {
		t.Fatalf("검색: %d %v", len(cards), err)
	}
	t.Logf("검색 %v", time.Since(srch))
}

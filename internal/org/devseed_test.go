package org

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func seededEnv(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, _ := testDB(t)
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, db)
	// 시계: KST 2026-03-10 정오
	svc := newSvc(t, db, func() time.Time { return time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC) })
	did, err := svc.SeedDev(context.Background())
	if err != nil || !did {
		t.Fatalf("SeedDev: %v %v", did, err)
	}
	return svc, db
}

func countQ(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-80·AC-81: 규모와 사례.
func TestDevSeedScaleAndCases(t *testing.T) {
	svc, db := seededEnv(t)
	ctx := context.Background()
	today := svc.Today()
	if err := Consistent(ctx, db); err != nil {
		t.Fatalf("정합성: %v", err)
	}
	sn, _ := svc.snap(ctx, today)
	kinds := map[contract.OrgKind]int{}
	var teams, closedTeams int
	for _, u := range sn.Units {
		kinds[u.Kind]++
		if u.Kind == contract.OrgTeam {
			teams++
			if u.Closed {
				closedTeams++
			}
		}
	}
	if kinds[contract.OrgCompany] != 1 || kinds[contract.OrgDivision] != 4 || kinds[contract.OrgDepartment] != 5 || teams < 10 {
		t.Fatalf("조직 구성 %v 팀 %d", kinds, teams)
	}
	if closedTeams != 1 {
		t.Fatalf("폐지 조직 %d", closedTeams)
	}
	employed := 0
	for id := range sn.People {
		if sn.Employed(id) {
			employed++
		}
	}
	if employed < 55 || employed > 70 {
		t.Fatalf("재직자 %d명(약 60명)", employed)
	}

	// 기존 dev 계정 9개의 자리: 호환 투영 결과가 기존 부서·직책과 같다(구매팀 직원 요청 → 팀장 → 부서장 흐름 유지).
	type want struct {
		dept int64
		pos  string
		role string
	}
	deptID := map[string]int64{"구매팀": 1, "경영지원팀": 2, "영업팀": 3}
	for email, w := range map[string]want{
		"admin@pl.test": {deptID["경영지원팀"], "", "admin"}, "finance@pl.test": {deptID["경영지원팀"], "", "finance"}, "finance2@pl.test": {deptID["경영지원팀"], "", "finance"},
		"lead@pl.test": {deptID["구매팀"], "team_lead", "manager"}, "lead2@pl.test": {deptID["구매팀"], "team_lead", "manager"},
		"head@pl.test": {deptID["구매팀"], "dept_head", "manager"}, "staff@pl.test": {deptID["구매팀"], "", "staff"},
		"staff2@pl.test": {deptID["영업팀"], "", "staff"}, "auditor@pl.test": {deptID["경영지원팀"], "", "auditor"},
	} {
		var dept int64
		var pos sql.NullString
		var role string
		var active int
		if err := db.QueryRow(`SELECT department_id, position, role, active FROM users WHERE email = ?`, email).Scan(&dept, &pos, &role, &active); err != nil {
			t.Fatal(err)
		}
		if dept != w.dept || pos.String != w.pos || role != w.role || active != 1 {
			t.Errorf("%s: dept=%d pos=%q role=%s active=%d, want %+v", email, dept, pos.String, role, active, w)
		}
	}
	// 본부장·대표의 department_id 는 본부·회사 번호라서 구매팀 결재선 후보(position+department_id 일치)에 끼지 않는다(AC-73).
	var candidates int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE active = 1 AND role = 'manager' AND position = 'dept_head' AND department_id = ?`, deptID["구매팀"]).Scan(&candidates)
	if candidates != 1 {
		t.Fatalf("구매팀 부서장 후보 %d명(구매부서장 1명이어야 함)", candidates)
	}
	var tl int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE active = 1 AND role = 'manager' AND position = 'team_lead' AND department_id = ?`, deptID["구매팀"]).Scan(&tl)
	if tl != 2 {
		t.Fatalf("구매팀 팀장 후보 %d명(lead·lead2 두 명)", tl)
	}

	// 사례 ① 겸직 조직장: 한 사람이 두 조직의 조직장
	headCount := map[int64]int{}
	for _, u := range sn.Units {
		if u.HeadUser != 0 && !u.HeadActing {
			headCount[u.HeadUser]++
		}
	}
	multi := 0
	for _, n := range headCount {
		if n >= 2 {
			multi++
		}
	}
	if multi < 1 {
		t.Fatal("겸직 조직장 사례가 없음")
	}
	// ② 공석 + 직무대행: 영업2팀
	var sl2 UnitSpan
	for _, u := range sn.Units {
		if u.Name == "영업2팀" {
			sl2 = u
		}
	}
	if sl2.HeadUser == 0 || !sl2.HeadActing {
		t.Fatalf("영업2팀은 공석이고 직무대행자가 있어야 함: %+v", sl2)
	}
	// ③ 퇴사자 2명(하나는 재입사)
	left, rehired := 0, 0
	for _, p := range sn.People {
		if p.Left {
			left++
		}
	}
	res, _, _ := svc.current(ctx, db)
	for uid, n := range func() map[int64]int {
		m := map[int64]int{}
		for _, p := range res.People {
			if p.Left {
				m[p.UserID]++
			}
		}
		return m
	}() {
		if n > 0 && sn.Employed(uid) {
			rehired++
		}
	}
	if left != 1 || rehired != 1 {
		t.Fatalf("퇴사자 %d명(오늘 기준 1명) 재입사 %d명(1명)", left, rehired)
	}
	// ④ 예약 전보 1건 ⑤ 취소·정정 이력 ⑥ 폐지 조직 ⑦ 겸직 5건 이상 ⑧ 동명이인
	if countQ(t, db, `SELECT COUNT(*) FROM org_events WHERE kind = 'transfer' AND effective_date > ?`, string(today)) != 1 {
		t.Fatal("예약 전보 1건")
	}
	if countQ(t, db, `SELECT COUNT(*) FROM org_events WHERE kind = 'cancel'`) != 2 || countQ(t, db, `SELECT COUNT(*) FROM org_events WHERE corrects_id IS NOT NULL`) != 1 {
		t.Fatal("취소(정정의 취소 포함 2건)와 정정(1건) 이력")
	}
	if countQ(t, db, `SELECT COUNT(*) FROM org_events WHERE kind = 'concurrent_add'`) < 5 {
		t.Fatal("겸직 5건 이상")
	}
	if countQ(t, db, `SELECT COUNT(*) FROM (SELECT name FROM users GROUP BY name HAVING COUNT(*) = 2)`) < 1 {
		t.Fatal("동명이인 한 쌍")
	}
	if countQ(t, db, `SELECT COUNT(*) FROM users WHERE name LIKE '%구매%'`) < 3 {
		t.Fatal("이름에 구매가 들어간 사람(조직명과 겹침)")
	}
	// 과거 기준일: 2022-01-01 에는 구매3팀이 없고(2022-03 입사), 2023-01-01 에는 있다. 폐지 뒤에는 폐지 상태로 남는다.
	for day, want := range map[contract.Day]bool{"2023-01-01": false, "2025-01-01": true} {
		s2, _ := svc.snap(ctx, day)
		for _, u := range s2.Units {
			if u.Name == "구매3팀" && u.Closed != want {
				t.Errorf("%s 구매3팀 폐지=%v, want %v", day, u.Closed, want)
			}
		}
	}
	// 시드는 한 번만: 다시 불러도 아무것도 하지 않는다.
	if did, err := svc.SeedDev(ctx); err != nil || did {
		t.Fatalf("두 번째 시드: %v %v", did, err)
	}
}

// 시드 사람 카드: 직무대행·겸직이 보이고 사번·직위가 있다. 연락처는 직원에게 가려진다.
func TestDevSeedDirectory(t *testing.T) {
	svc, db := seededEnv(t)
	ctx := context.Background()
	staff := contract.Principal{UserID: 99999, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	cards, err := svc.SearchPeople(ctx, staff, contract.PeopleQuery{Text: "구매", Limit: 100})
	if err != nil || len(cards) < 5 {
		t.Fatalf("검색: %d %v", len(cards), err)
	}
	for _, c := range cards {
		if c.Phone != "" && !strings.Contains(c.Phone, "*") {
			t.Fatalf("직원에게 휴대전화 원문: %+v", c)
		}
	}
	// 같은 이름 검색은 둘을 돌려주고 소속 경로로 구분된다.
	var dup string
	_ = db.QueryRow(`SELECT name FROM users GROUP BY name HAVING COUNT(*) = 2 LIMIT 1`).Scan(&dup)
	pair, _ := svc.SearchPeople(ctx, staff, contract.PeopleQuery{Text: dup})
	if len(pair) != 2 || pair[0].Primary.Unit.ID == pair[1].Primary.Unit.ID && len(pair[0].Concurrent) == len(pair[1].Concurrent) {
		t.Fatalf("동명이인 %q: %d명", dup, len(pair))
	}
	tree, err := svc.Tree(ctx, "")
	if err != nil || tree == nil || tree.Total < 55 {
		t.Fatalf("트리: %v %+v", err, tree)
	}
}

// 개발용 시드: 영업2팀 직무대행자는 결재 권한이 있는 역할이고, 결재 권한 없는 조직장 사례는 구매 흐름과 무관한 플랫폼개발팀 하나뿐이다.
func TestDevSeedApprovalRoleCases(t *testing.T) {
	svc, _ := seededEnv(t)
	issues, err := svc.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range issues {
		if i.Kind == "head_no_approve" {
			names = append(names, i.Name)
		}
	}
	if len(names) != 1 || names[0] != "플랫폼개발팀" {
		t.Fatalf("결재 권한 없는 조직장·대행자 사례 %v(플랫폼개발팀 하나여야 함)", names)
	}
}

// B27: 월 마감 해제 요청서의 결재선(재무팀장)이 개발 환경에서 풀리도록 재무팀(T-FIN)에 결재 권한이 있는 조직장이 있고,
// 재무 담당 두 명(finance·finance2)은 조직장이 아니라 재무팀의 구성원이다(본인 올림 규칙을 타지 않는다).
func TestDevSeedFinanceTeamHasApprovingHead(t *testing.T) {
	svc, db := seededEnv(t)
	sn, err := svc.snap(context.Background(), svc.Today())
	if err != nil {
		t.Fatal(err)
	}
	var fin int64
	if err := db.QueryRow(`SELECT id FROM org_units WHERE code = 'T-FIN'`).Scan(&fin); err != nil {
		t.Fatal(err)
	}
	head := sn.Units[fin].HeadUser
	if head == 0 {
		t.Fatal("재무팀(T-FIN)에 조직장이 없음")
	}
	var role, email string
	if err := db.QueryRow(`SELECT role, email FROM users WHERE id = ?`, head).Scan(&role, &email); err != nil {
		t.Fatal(err)
	}
	if role != string(contract.RoleManager) {
		t.Errorf("재무팀장의 권한 역할 %q(결재 가능한 결재자여야 함)", role)
	}
	if email == "finance@pl.test" || email == "finance2@pl.test" {
		t.Errorf("재무 담당 계정이 재무팀장이면 마감 해제 요청이 본인 올림이 된다: %s", email)
	}
	for _, e := range []string{"finance@pl.test", "finance2@pl.test"} {
		var uid int64
		if err := db.QueryRow(`SELECT id FROM users WHERE email = ?`, e).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if p, ok := sn.People[uid]; !ok || p.Primary != fin {
			t.Errorf("%s 가 재무팀 소속이 아님", e)
		}
	}
}

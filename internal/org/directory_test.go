package org

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

// directoryScenario 는 실제 서비스와 가짜(fake.Org)에 같은 조직을 만들어 조회 결과가 같은지 비교한다.
func TestDirectoryMatchesFake(t *testing.T) {
	e := newEnv(t)
	co, div, dep, t1, t2 := e.baseOrg()
	// 사람들(번호는 입사 순서대로 새 사용자 번호가 붙는다: 관리자 900 은 조직 미등록)
	lead := e.reg(Command{Kind: KindHire, Name: "구매팀장", Email: "lead@pl.test", ToUnit: t1, Rank: 4, Role: contract.RoleManager, Phone: "010-1111-2222", Extension: "1001", Effective: "2021-01-04"}).UserID
	staff := e.reg(Command{Kind: KindHire, Name: "구매직원", Email: "staff@pl.test", ToUnit: t1, Rank: 2, Role: contract.RoleStaff, Phone: "010-3333-4444", Extension: "1002", Effective: "2021-01-04"}).UserID
	head := e.reg(Command{Kind: KindHire, Name: "구매부서장", Email: "head@pl.test", ToUnit: dep, Rank: 5, Role: contract.RoleManager, Phone: "010-5555-6666", Extension: "1003", Effective: "2021-01-04"}).UserID
	gone := e.reg(Command{Kind: KindHire, Name: "퇴사자", Email: "gone@pl.test", ToUnit: t2, Rank: 1, Role: contract.RoleStaff, Phone: "010-7777-8888", Effective: "2021-01-04"}).UserID
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: lead, Effective: "2021-02-01"})
	e.reg(Command{Kind: KindHeadSet, UnitID: dep, UserID: head, Effective: "2021-02-01"})
	e.reg(Command{Kind: KindConcurrentAdd, UserID: staff, ToUnit: t2, Effective: "2022-01-03"})
	e.reg(Command{Kind: KindActingSet, UnitID: t2, UserID: staff, Effective: "2026-03-01", EndDay: "2026-03-31"})
	e.reg(Command{Kind: KindLeave, UserID: gone, LastDay: "2026-03-09", LeaveKind: "자진", Confirm: true, Reason: "퇴사 처리"})
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (950, 'legacy@pl.test', '기존 사용자', 'staff', 900)`); err != nil {
		t.Fatal(err)
	}

	f := fake.NewOrg()
	f.AddUnit(co, "CO", "PL 주식회사", contract.OrgCompany, 0)
	f.AddUnit(div, "DIV", "구매·물류본부", contract.OrgDivision, co)
	f.AddUnit(dep, "DEP", "구매팀", contract.OrgDepartment, div)
	f.AddUnit(t1, "T1", "구매1팀", contract.OrgTeam, dep)
	f.AddUnit(t2, "T2", "구매2팀", contract.OrgTeam, dep)
	f.AddPerson(lead, "구매팀장", "차장", t1, "lead@pl.test", "010-1111-2222", "1001")
	f.AddPerson(staff, "구매직원", "대리", t1, "staff@pl.test", "010-3333-4444", "1002")
	f.AddPerson(head, "구매부서장", "부장", dep, "head@pl.test", "010-5555-6666", "1003")
	f.AddPerson(gone, "퇴사자", "사원", t2, "gone@pl.test", "010-7777-8888", "")
	f.SetHead(t1, lead).SetHead(dep, head).AddConcurrent(staff, t2).SetActing(t2, staff).Leave(gone)
	f.AddUnregistered(950)

	ctx := e.ctx
	today := e.svc.Today()
	// 조직 조회
	for _, id := range []int64{co, div, dep, t1, t2} {
		ru, err1 := e.svc.Unit(ctx, id, "")
		fu, err2 := f.Unit(ctx, id, "")
		if err1 != nil || err2 != nil || ru.ID != fu.ID || ru.Name != fu.Name || ru.Kind != fu.Kind || ru.ParentID != fu.ParentID {
			t.Errorf("Unit(%d): %+v %v / %+v %v", id, ru, err1, fu, err2)
		}
		rp, _ := e.svc.Path(ctx, id, today)
		fp, _ := f.Path(ctx, id, today)
		if contract.OrgPathText(rp) != contract.OrgPathText(fp) {
			t.Errorf("Path(%d): %q / %q", id, contract.OrgPathText(rp), contract.OrgPathText(fp))
		}
		ra, _ := e.svc.Ancestors(ctx, id, "")
		fa, _ := f.Ancestors(ctx, id, "")
		if len(ra) != len(fa) {
			t.Errorf("Ancestors(%d) %d / %d", id, len(ra), len(fa))
		}
		rd, _ := e.svc.Descendants(ctx, id, "", true)
		fd, _ := f.Descendants(ctx, id, "", true)
		if len(rd) != len(fd) {
			t.Errorf("Descendants(%d) %d / %d", id, len(rd), len(fd))
		}
		rh, _ := e.svc.Head(ctx, id, "")
		fh, _ := f.Head(ctx, id, "")
		if rh != fh {
			t.Errorf("Head(%d): %+v / %+v", id, rh, fh)
		}
		rm, _ := e.svc.MembersOf(ctx, id, "", true)
		fm, _ := f.MembersOf(ctx, id, "", true)
		if !reflect.DeepEqual(rm, fm) {
			t.Errorf("MembersOf(%d): %v / %v", id, rm, fm)
		}
		n, _ := e.svc.CountMembers(ctx, id, "", false)
		fn, _ := f.CountMembers(ctx, id, "", false)
		if n != fn {
			t.Errorf("CountMembers(%d): %d / %d", id, n, fn)
		}
	}
	for _, u := range []int64{lead, staff, head} {
		rs, _ := e.svc.HeadedScope(ctx, u, "")
		fs, _ := f.HeadedScope(ctx, u, "")
		if !reflect.DeepEqual(rs, fs) {
			t.Errorf("HeadedScope(%d): %v / %v", u, rs, fs)
		}
		rh, _ := e.svc.UnitsHeadedBy(ctx, u, "")
		fh, _ := f.UnitsHeadedBy(ctx, u, "")
		if len(rh) != len(fh) {
			t.Errorf("UnitsHeadedBy(%d): %d / %d", u, len(rh), len(fh))
		}
	}
	// 직무대행: 구매2팀은 조직장이 없고 직원(staff)이 대행한다.
	if h, _ := e.svc.Head(ctx, t2, ""); h.UserID != staff || !h.Acting {
		t.Fatalf("구매2팀 대행: %+v", h)
	}
	// 폐지되지 않은 하위 조직 번호가 열람 범위에 들어가고, 폐지된 하위 조직도 남는다.
	e.reg(Command{Kind: KindUnitCreate, Code: "T3", Name: "구매3팀", UnitKind: contract.OrgTeam, ParentID: dep})
	e.reg(Command{Kind: KindUnitClose, UnitID: dep + 3, Effective: today})
	if sc, _ := e.svc.HeadedScope(ctx, head, ""); len(sc) != 4 {
		t.Fatalf("폐지된 하위 조직 포함: %v", sc)
	}
	// FindUnits
	rf, _ := e.svc.FindUnits(ctx, "구매", "", 0)
	if len(rf) != 5 { // 구매·물류본부, 구매팀, 구매1팀, 구매2팀, 구매3팀
		t.Fatalf("FindUnits: %d", len(rf))
	}
	// 사람 카드와 마스킹: 직원 / 관리자 / MCP 채널(관리자·본인)
	principals := map[string]contract.Principal{
		"직원":       {UserID: staff, Role: contract.RoleStaff, Channel: contract.ChannelWeb},
		"본인(웹)":    {UserID: lead, Role: contract.RoleManager, Channel: contract.ChannelWeb},
		"관리자":      {UserID: 900, Role: contract.RoleAdmin, Channel: contract.ChannelWeb},
		"관리자(MCP)": {UserID: 900, Role: contract.RoleAdmin, Channel: contract.ChannelMCP},
		"본인(MCP)":  {UserID: lead, Role: contract.RoleManager, Channel: contract.ChannelMCP},
	}
	for name, p := range principals {
		rc, err1 := e.svc.Card(ctx, p, lead, "")
		fc, err2 := f.Card(ctx, p, lead, "")
		if err1 != nil || err2 != nil {
			t.Fatalf("%s Card: %v %v", name, err1, err2)
		}
		// 가린 값의 모양은 마스커 구현마다 다르므로 "원문인가"만 비교한다.
		if (rc.Email == "lead@pl.test") != (fc.Email == "lead@pl.test") || (rc.Phone == "010-1111-2222") != (fc.Phone == "010-1111-2222") ||
			rc.Extension != fc.Extension || rc.Name != fc.Name || rc.Rank != fc.Rank {
			t.Errorf("%s Card: %+v / %+v", name, rc, fc)
		}
	}
	// MCP 채널은 권한자·본인에게도 원문이 나가지 않는다(AC-67a).
	for _, name := range []string{"관리자(MCP)", "본인(MCP)"} {
		c, _ := e.svc.Card(ctx, principals[name], lead, "")
		if c.Email == "lead@pl.test" || c.Phone == "010-1111-2222" {
			t.Errorf("%s 에게 원문이 나감: %+v", name, c)
		}
	}
	if c, _ := e.svc.Card(ctx, principals["관리자"], lead, ""); c.Email != "lead@pl.test" {
		t.Errorf("관리자에게는 원문: %+v", c)
	}
	// 사람 카드: 겸직·대행
	sp, err := e.svc.Person(ctx, staff, "")
	if err != nil || len(sp.Concurrent) != 1 || sp.Concurrent[0].Unit.ID != t2 || len(sp.Acting) != 1 || !sp.Acting[0].Acting || sp.Primary.IsHead {
		t.Fatalf("구매직원: %+v %v", sp, err)
	}
	if lp, _ := e.svc.Person(ctx, lead, ""); !lp.Primary.IsHead || lp.Rank != "차장" || lp.EmployeeNo == "" {
		t.Fatalf("구매팀장: %+v", lp)
	}
	// 검색: 이름·조직명·직책, 퇴사자는 org:history 가 없으면 나오지 않는다(AC-57)
	for _, q := range []contract.PeopleQuery{{Text: "구매"}, {Text: "팀장"}, {Text: "구매2팀"}, {HeadOnly: true}, {UnitID: dep}} {
		rs, err1 := e.svc.SearchPeople(ctx, principals["직원"], q)
		fs, err2 := f.SearchPeople(ctx, principals["직원"], q)
		if err1 != nil || err2 != nil {
			t.Fatalf("검색 %+v: %v %v", q, err1, err2)
		}
		if len(rs) != len(fs) {
			t.Errorf("검색 %+v: %d / %d", q, len(rs), len(fs))
		}
	}
	left := contract.PeopleQuery{Text: "퇴사자", IncludeLeft: true}
	if rs, _ := e.svc.SearchPeople(ctx, principals["직원"], left); len(rs) != 0 {
		t.Fatalf("권한 없는 호출자에게 퇴사자: %+v", rs)
	}
	auditor := contract.Principal{UserID: 901, Role: contract.RoleAuditor, Channel: contract.ChannelWeb}
	if rs, _ := e.svc.SearchPeople(ctx, auditor, left); len(rs) != 1 || rs[0].Active {
		t.Fatalf("org:history 권한자는 퇴사자를 본다: %+v", rs)
	}
	// 오류 의미: 조직 미등록은 ErrNotInOrg, 없는 사용자는 ErrNotFound, 형식 오류는 ErrValidation
	if _, err := e.svc.Person(ctx, 950, ""); !errors.Is(err, contract.ErrNotInOrg) || errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("조직 미등록: %v", err)
	}
	if _, err := e.svc.Person(ctx, 12345, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사용자: %v", err)
	}
	if _, err := e.svc.Unit(ctx, t1, "2026-02-30"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("기준일 형식: %v", err)
	}
	if _, err := e.svc.Unit(ctx, 9999, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 조직: %v", err)
	}
	// 기준일: 입사 전 날짜는 그 사람이 없고, 조직이 없던 날짜는 ErrNotFound
	if _, err := e.svc.Person(ctx, lead, "2020-12-31"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("입사 전: %v", err)
	}
	if _, err := e.svc.Unit(ctx, t1, "2019-12-31"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("조직 설립 전: %v", err)
	}
	if h, _ := e.svc.Head(ctx, t1, "2021-01-31"); !h.Vacant {
		t.Fatalf("보임 전은 공석: %+v", h)
	}
	if h, _ := e.svc.Head(ctx, t1, "2021-02-01"); h.UserID != lead {
		t.Fatalf("보임 당일: %+v", h)
	}
}

// 캐시는 이벤트가 추가되면 다음 조회에 반영되고, 다른 서비스 인스턴스(다른 프로세스)가 쓴 변경도 본다.
func TestDirectoryCacheFollowsEvents(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	ctx := e.ctx
	if u, _ := e.svc.Unit(ctx, t1, ""); u.Name != "구매1팀" {
		t.Fatal(u)
	}
	other, err := New(Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.store, Masker: e.svc.masker, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Register(ctx, e.admin, Command{Kind: KindUnitRename, UnitID: t1, Name: "구매일팀"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.svc.Unit(ctx, t1, ""); u.Name != "구매일팀" {
		t.Fatalf("다른 인스턴스의 변경이 보이지 않음: %+v", u)
	}
}

// 퇴사 인증 판정은 투영(users.active)과 무관하게 오늘 날짜로 결정된다(AC-55, AC-56).
func TestEmploymentDeniedDoesNotNeedProjection(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	legacy := int64(950)
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (950, 'l@pl.test', '기존', 'staff', 900)`); err != nil {
		t.Fatal(err)
	}
	e.reg(Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-20", LeaveKind: "자진"})
	check := func(user int64, day contract.Day) bool {
		d, err := EmploymentDenied(e.ctx, e.db, user, day)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if check(u, "2026-03-20") || !check(u, "2026-03-21") {
		t.Fatal("마지막 근무일까지 허용, 다음 날부터 거부")
	}
	// KST 자정 경계(UTC 15:00)
	if check(u, contract.DayOf(time.Date(2026, 3, 20, 14, 59, 59, 0, time.UTC))) || !check(u, contract.DayOf(time.Date(2026, 3, 20, 15, 0, 0, 0, time.UTC))) {
		t.Fatal("KST 경계")
	}
	// 입사 전에도 거부, 조직 미등록 사용자는 거부하지 않는다.
	if !check(u, "2020-12-31") || check(legacy, "2026-03-21") || check(12345, "2026-03-21") {
		t.Fatal("입사 전 거부 / 미등록 사용자 허용")
	}
	// 투영을 돌리지 않았으므로 users.active 는 아직 1 이다(그래도 판정은 거부).
	if active, _, _ := userRow(e, u); active != 1 {
		t.Fatal("투영 전 active")
	}
}

// 배치 조회는 단건 조회와 같은 값을 돌려주고, 없는 대상은 결과에서 빠진다.
func TestDirectoryBatchMatchesSingle(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, t2 := e.baseOrg()
	a := e.hire("가", "a@pl.test", t1, contract.RoleManager, "2021-01-04")
	b := e.hire("나", "b@pl.test", dep, contract.RoleManager, "2021-01-04")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: a, Effective: "2021-02-01"})
	hs, err := e.svc.Heads(e.ctx, []int64{t1, t2, 9999}, "")
	if err != nil || len(hs) != 2 || hs[t1].UserID != a || !hs[t2].Vacant {
		t.Fatalf("Heads: %+v %v", hs, err)
	}
	ps, err := e.svc.Persons(e.ctx, []int64{a, b, 12345}, "")
	if err != nil || len(ps) != 2 {
		t.Fatalf("Persons: %+v %v", ps, err)
	}
	one, _ := e.svc.Person(e.ctx, a, "")
	if ps[a].Primary.Unit.ID != one.Primary.Unit.ID || ps[a].Rank != one.Rank || ps[a].EmployeeNo != one.EmployeeNo || !ps[a].Primary.IsHead {
		t.Fatalf("배치와 단건이 다름: %+v / %+v", ps[a], one)
	}
	// 폐지된 하위 조직도 조직장의 열람 범위에 남는다(과거 귀속 건 열람용).
	e.reg(Command{Kind: KindUnitCreate, Code: "T9", Name: "임시팀", UnitKind: contract.OrgTeam, ParentID: dep})
	e.reg(Command{Kind: KindHeadSet, UnitID: dep, UserID: b, Effective: "2021-02-01"})
	e.reg(Command{Kind: KindUnitClose, UnitID: dep + 3})
	sc, _ := e.svc.HeadedScope(e.ctx, b, "")
	found := false
	for _, id := range sc {
		found = found || id == dep+3
	}
	if !found {
		t.Fatalf("폐지된 하위 조직이 열람 범위에 없음: %v", sc)
	}
}

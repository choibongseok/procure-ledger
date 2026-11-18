package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

func newOrg() *fake.Org {
	o := fake.NewOrg()
	o.AddUnit(1, "CO", "PL 주식회사", contract.OrgCompany, 0)
	o.AddUnit(2, "DIV-PL", "구매·물류본부", contract.OrgDivision, 1)
	o.AddUnit(3, "DEP-PU", "구매팀", contract.OrgDepartment, 2)
	o.AddUnit(4, "TM-PU1", "구매1팀", contract.OrgTeam, 3)
	o.AddUnit(5, "TM-PU2", "구매2팀", contract.OrgTeam, 3)
	o.AddPerson(10, "구매팀장", "차장", 4, "lead@pl.test", "010-1111-2222", "1234")
	o.AddPerson(11, "구매직원", "대리", 4, "staff@pl.test", "010-3333-4444", "1235")
	o.AddPerson(12, "구매부서장", "부장", 3, "head@pl.test", "010-5555-6666", "1236")
	o.SetHead(4, 10).SetHead(3, 12)
	return o
}

func TestFakeOrgPathAndHeads(t *testing.T) {
	o, ctx := newOrg(), context.Background()
	path, err := o.Path(ctx, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := contract.OrgPathText(path); got != "구매·물류본부 > 구매팀 > 구매1팀" {
		t.Fatalf("경로 문자열 %q", got)
	}
	if got := contract.HeadTitle(contract.OrgTeam) + contract.HeadTitle(contract.OrgDepartment) + contract.HeadTitle(contract.OrgDivision) + contract.HeadTitle(contract.OrgCompany); got != "팀장부서장본부장대표" {
		t.Fatalf("직책 이름 %q", got)
	}
	// 조직장 → 직무대행 → 공석
	if h, _ := o.Head(ctx, 4, ""); h.UserID != 10 || h.Acting || h.Vacant {
		t.Fatalf("조직장 %+v", h)
	}
	if h, _ := o.Head(ctx, 5, ""); !h.Vacant {
		t.Fatalf("공석이어야 함 %+v", h)
	}
	o.SetActing(5, 11)
	if h, _ := o.Head(ctx, 5, ""); h.UserID != 11 || !h.Acting {
		t.Fatalf("직무대행 %+v", h)
	}
	// 조직장이 퇴사하면 자동 공석
	o.Leave(10)
	if h, _ := o.Head(ctx, 4, ""); !h.Vacant {
		t.Fatalf("퇴사한 조직장의 조직은 공석: %+v", h)
	}
	// 열람 범위: 부서장은 자기 부서와 하위 팀 전체
	scope, _ := o.HeadedScope(ctx, 12, "")
	if len(scope) != 3 || scope[0] != 3 || scope[1] != 4 || scope[2] != 5 {
		t.Fatalf("열람 범위 %v", scope)
	}
	anc, _ := o.Ancestors(ctx, 4, "")
	if len(anc) != 3 || anc[0].ID != 3 || anc[2].ID != 1 {
		t.Fatalf("상위 순회 %+v", anc)
	}
	if _, err := o.Unit(ctx, 99, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 조직: %v", err)
	}
}

func TestFakeOrgNotInOrgAndMembers(t *testing.T) {
	o, ctx := newOrg(), context.Background()
	o.AddUnregistered(77)
	if _, err := o.Person(ctx, 77, ""); !errors.Is(err, contract.ErrNotInOrg) || errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("조직 미등록: %v", err)
	}
	if _, err := o.Person(ctx, 78, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사용자: %v", err)
	}
	o.AddConcurrent(11, 5) // 구매직원이 구매2팀 겸직
	ids, _ := o.MembersOf(ctx, 3, "", true)
	if len(ids) != 3 {
		t.Fatalf("구매팀 전체 %v (겸직 중복 없이 3명)", ids)
	}
	n, _ := o.CountMembers(ctx, 5, "", false)
	if n != 1 {
		t.Fatalf("구매2팀 인원 %d", n)
	}
	units, _ := o.FindUnits(ctx, "구매", "", 0)
	if len(units) != 4 { // 구매·물류본부, 구매팀, 구매1팀, 구매2팀
		t.Fatalf("이름 찾기 %d건", len(units))
	}
}

func TestFakeOrgMasking(t *testing.T) {
	o, ctx := newOrg(), context.Background()
	staff := contract.Principal{UserID: 11, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	admin := contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	adminMCP := contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelMCP}
	selfMCP := contract.Principal{UserID: 10, Role: contract.RoleManager, Channel: contract.ChannelMCP}

	c, _ := o.Card(ctx, staff, 10, "")
	if c.Email == "lead@pl.test" || c.Phone == "010-1111-2222" || c.Extension != "1234" {
		t.Fatalf("직원에게 팀장의 연락처는 가려지고 내선은 보여야 함: %+v", c)
	}
	own, _ := o.Card(ctx, staff, 11, "")
	if own.Email != "staff@pl.test" || own.Phone != "010-3333-4444" {
		t.Fatalf("본인은 원문: %+v", own)
	}
	a, _ := o.Card(ctx, admin, 10, "")
	if a.Email != "lead@pl.test" {
		t.Fatalf("관리자(마스크 해제 권한)는 원문: %+v", a)
	}
	for name, pr := range map[string]contract.Principal{"관리자(MCP)": adminMCP, "본인(MCP)": selfMCP} {
		cs, err := o.SearchPeople(ctx, pr, contract.PeopleQuery{Text: "구매팀장"})
		if err != nil || len(cs) != 1 {
			t.Fatalf("%s 검색: %v %v", name, cs, err)
		}
		if cs[0].Email == "lead@pl.test" || cs[0].Phone == "010-1111-2222" {
			t.Fatalf("%s 에게 연락처 원문이 나감: %+v", name, cs[0])
		}
	}
}

func TestFakeOrgSearchExcludesLeftWithoutHistoryPermission(t *testing.T) {
	o, ctx := newOrg(), context.Background()
	o.Leave(11)
	staff := contract.Principal{UserID: 10, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	auditor := contract.Principal{UserID: 2, Role: contract.RoleAuditor, Channel: contract.ChannelWeb}
	got, _ := o.SearchPeople(ctx, staff, contract.PeopleQuery{Text: "구매직원", IncludeLeft: true})
	if len(got) != 0 {
		t.Fatalf("권한 없는 호출자에게 퇴사자가 나옴: %+v", got)
	}
	got, _ = o.SearchPeople(ctx, auditor, contract.PeopleQuery{Text: "구매직원", IncludeLeft: true})
	if len(got) != 1 || got[0].Active {
		t.Fatalf("org:history 권한자는 퇴사자를 본다: %+v", got)
	}
	heads, _ := o.SearchPeople(ctx, staff, contract.PeopleQuery{HeadOnly: true, Limit: 1000})
	if len(heads) != 2 {
		t.Fatalf("조직장만: %d", len(heads))
	}
}

// KST 날짜 경계: UTC 15:00 에 날짜가 바뀐다.
func TestDayOfKSTBoundary(t *testing.T) {
	before := time.Date(2026, 3, 1, 14, 59, 59, 0, time.UTC)
	after := time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC)
	if contract.DayOf(before) != "2026-03-01" || contract.DayOf(after) != "2026-03-02" {
		t.Fatalf("KST 날짜 경계: %s %s", contract.DayOf(before), contract.DayOf(after))
	}
}

// B21 요청: 단계 높이, 배치 조회, 열람 범위에 폐지된 하위 조직 포함.
func TestFakeOrgBatchAndLevel(t *testing.T) {
	if contract.OrgTeam.Level() != 1 || contract.OrgDepartment.Level() != 2 || contract.OrgDivision.Level() != 3 || contract.OrgCompany.Level() != 4 || contract.OrgKind("x").Level() != 0 {
		t.Fatal("OrgKind.Level")
	}
	o, ctx := newOrg(), context.Background()
	hs, _ := o.Heads(ctx, []int64{3, 4, 5, 99}, "")
	if len(hs) != 3 || hs[3].UserID != 12 || hs[4].UserID != 10 || !hs[5].Vacant {
		t.Fatalf("Heads: %+v", hs)
	}
	ps, _ := o.Persons(ctx, []int64{10, 11, 99}, "")
	if len(ps) != 2 || ps[10].Primary.Unit.ID != 4 {
		t.Fatalf("Persons: %+v", ps)
	}
	o.CloseUnit(5)
	scope, _ := o.HeadedScope(ctx, 12, "")
	if len(scope) != 3 {
		t.Fatalf("폐지된 하위 조직도 열람 범위에 포함: %v", scope)
	}
}

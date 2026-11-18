package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

// 결재선 만들기(buildLine) 표 테스트: 명세 3.1 의 예시 1~14 와 불가 사유(AC-01~11). 가짜 조직과 메모리 사용자·규칙을 쓴다.

type memLineDeps struct {
	users map[int64]lineUser
	tiers func(amount int64) []domain.StepKind
}

func (m memLineDeps) LineKinds(_ context.Context, _ contract.Tx, amount int64) ([]domain.StepKind, error) {
	return m.tiers(amount), nil
}

func (m memLineDeps) LineUser(_ context.Context, _ contract.Tx, id int64) (lineUser, error) {
	u, ok := m.users[id]
	if !ok {
		return lineUser{}, contract.ErrNotFound
	}
	return u, nil
}

func (m memLineDeps) FinancePool(_ context.Context, _ contract.Tx) ([]lineUser, error) {
	var out []lineUser
	for id := int64(1); id < 100; id++ {
		if u, ok := m.users[id]; ok && u.Role == contract.RoleFinance && u.Active {
			out = append(out, u)
		}
	}
	return out, nil
}

func defaultTiers(amount int64) []domain.StepKind {
	switch {
	case amount < 1_000_000:
		return []domain.StepKind{domain.StepTeamLead}
	case amount < 10_000_000:
		return []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead}
	case amount < 100_000_000:
		return []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead, domain.StepFinance}
	}
	return []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead, domain.StepDivisionHead, domain.StepFinance}
}

// 사용자 번호.
const (
	lStaff    = 1  // 구매1팀 직원
	lLead     = 2  // 구매1팀 팀장
	lLead2    = 3  // 구매2팀 팀장
	lHead     = 4  // 구매팀 부서장
	lDivBuy   = 5  // 구매·물류본부장
	lCEO      = 6  // 대표
	lFinance  = 7  // 재무
	lSales2   = 8  // 영업2팀 직원(팀장 공석)
	lActing   = 9  // 영업2팀 직무대행자(영업팀 소속)
	lY        = 10 // 출하팀 팀장 겸 물류팀 부서장(겸임)
	lShip     = 11 // 출하팀 직원
	lSalesDpt = 12 // 영업팀 부서장
	lDivSales = 13 // 영업본부장
	lPlan     = 14 // 영업기획팀(본부 직속 팀) 직원
	lPlanLead = 15 // 영업기획팀 팀장
	lDirect   = 16 // 구매팀(부서) 직속 직원
	lNoAuth   = 17 // 구매3팀 팀장(권한 역할 staff)
	lTeam3    = 18 // 구매3팀 직원
	lFinance2 = 19 // 재무 2
)

type lineEnv struct {
	org   *fake.Org
	deps  memLineDeps
	asOf  contract.Day
	ctx   context.Context
	units map[int64]contract.OrgUnit
}

func newLineEnv() *lineEnv {
	o := fake.NewOrg()
	// 1 회사 / 2 경영지원본부 / 3 구매·물류본부 / 4 영업본부 / 5 구매팀(부서) / 6 구매1팀 / 7 구매2팀 / 8 물류팀 / 9 입고팀 / 10 출하팀
	// 11 영업팀 / 12 영업2팀 / 13 영업기획팀(본부 직속 팀) / 14 구매3팀
	o.AddUnit(1, "PL", "PL 주식회사", contract.OrgCompany, 0)
	o.AddUnit(2, "MGT", "경영지원본부", contract.OrgDivision, 1)
	o.AddUnit(3, "BUY", "구매·물류본부", contract.OrgDivision, 1)
	o.AddUnit(4, "SAL", "영업본부", contract.OrgDivision, 1)
	o.AddUnit(5, "BUYD", "구매팀", contract.OrgDepartment, 3)
	o.AddUnit(6, "BUY1", "구매1팀", contract.OrgTeam, 5)
	o.AddUnit(7, "BUY2", "구매2팀", contract.OrgTeam, 5)
	o.AddUnit(8, "LOG", "물류팀", contract.OrgDepartment, 3)
	o.AddUnit(9, "IN", "입고팀", contract.OrgTeam, 8)
	o.AddUnit(10, "OUT", "출하팀", contract.OrgTeam, 8)
	o.AddUnit(11, "SALD", "영업팀", contract.OrgDepartment, 4)
	o.AddUnit(12, "SAL2", "영업2팀", contract.OrgTeam, 11)
	o.AddUnit(13, "PLAN", "영업기획팀", contract.OrgTeam, 4)
	o.AddUnit(14, "BUY3", "구매3팀", contract.OrgTeam, 5)

	users := map[int64]lineUser{}
	add := func(id int64, name string, role contract.Role, unit int64) {
		o.AddPerson(id, name, "과장", unit, "", "", "")
		users[id] = lineUser{ID: id, Name: name, Role: role, Active: true, Perms: templatePerms(role)}
	}
	add(lStaff, "구매직원", contract.RoleStaff, 6)
	add(lLead, "구매팀장", contract.RoleManager, 6)
	add(lLead2, "구매부팀장", contract.RoleManager, 7)
	add(lHead, "구매부서장", contract.RoleManager, 5)
	add(lDivBuy, "최본부", contract.RoleManager, 3)
	add(lCEO, "신대표", contract.RoleManager, 1)
	add(lFinance, "재무담당", contract.RoleFinance, 2)
	add(lSales2, "영업직원", contract.RoleStaff, 12)
	add(lActing, "대행자", contract.RoleManager, 11)
	add(lY, "정출하", contract.RoleManager, 10)
	add(lShip, "출하직원", contract.RoleStaff, 10)
	add(lSalesDpt, "윤영업", contract.RoleManager, 11)
	add(lDivSales, "강본부", contract.RoleManager, 4)
	add(lPlan, "기획직원", contract.RoleStaff, 13)
	add(lPlanLead, "기획팀장", contract.RoleManager, 13)
	add(lDirect, "부서직속", contract.RoleStaff, 5)
	add(lNoAuth, "권한없는팀장", contract.RoleStaff, 14)
	add(lTeam3, "구매3팀직원", contract.RoleStaff, 14)
	add(lFinance2, "재무2", contract.RoleFinance, 2)

	o.SetHead(6, lLead).SetHead(7, lLead2).SetHead(5, lHead).SetHead(3, lDivBuy).SetHead(1, lCEO)
	o.AddConcurrent(lY, 8)           // 조직장은 그 조직의 구성원(주소속 또는 겸직)이어야 한다
	o.SetHead(10, lY).SetHead(8, lY) // 같은 사람이 출하팀 팀장과 물류팀 부서장을 겸임한다
	o.SetHead(11, lSalesDpt).SetHead(4, lDivSales).SetHead(13, lPlanLead).SetHead(14, lNoAuth)
	o.SetActing(12, lActing) // 영업2팀 팀장 공석, 직무대행

	return &lineEnv{org: o, deps: memLineDeps{users: users, tiers: defaultTiers}, asOf: "2026-11-11", ctx: context.Background()}
}

func (e *lineEnv) build(t *testing.T, requester, amount int64) domain.LinePreview {
	t.Helper()
	got, _, err := buildLine(e.ctx, nil, e.org, e.deps, requester, amount, e.asOf)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// who 는 결재선을 "단계:이름" 목록으로 줄여 비교하기 쉽게 한다. 재무 단계는 "재무", 생략은 "생략", 비어 있으면 "없음".
func who(p domain.LinePreview) string {
	var parts []string
	for _, s := range p.Steps {
		switch {
		case s.Omitted:
			parts = append(parts, "생략")
		case s.Missing:
			parts = append(parts, "없음")
		case s.Kind == domain.StepFinance:
			parts = append(parts, fmt.Sprintf("재무(%d)", s.PoolSize))
		default:
			parts = append(parts, s.ApproverName)
		}
	}
	return strings.Join(parts, " > ")
}

func TestBuildLineExamples(t *testing.T) {
	type tc struct {
		name      string
		requester int64
		amount    int64
		want      string
		complete  bool
		check     func(t *testing.T, p domain.LinePreview)
	}
	cases := []tc{
		{name: "예시1 직원 50만: 팀장", requester: lStaff, amount: 500_000, want: "구매팀장", complete: true},
		{name: "예시2 직원 500만: 팀장 부서장", requester: lStaff, amount: 5_000_000, want: "구매팀장 > 구매부서장", complete: true},
		{name: "예시3 직원 2천만: 팀장 부서장 재무풀", requester: lStaff, amount: 20_000_000, want: "구매팀장 > 구매부서장 > 재무(2)", complete: true},
		{name: "예시4 팀장 50만: 부서장으로 올림", requester: lLead, amount: 500_000, want: "구매부서장", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			s := p.Steps[0]
			if s.RaisedFrom != "구매1팀" || s.RaiseReason != raiseSelf || !strings.Contains(s.RaisedNote, "요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다.") {
				t.Fatalf("올림 정보 %+v", s)
			}
			for _, s := range p.Steps {
				if s.ApproverID == lLead2 {
					t.Fatal("같은 부서의 다른 팀장은 후보가 아니다")
				}
			}
		}},
		{name: "예시5 팀장 500만: 부서장 본부장", requester: lLead, amount: 5_000_000, want: "구매부서장 > 최본부", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			if p.Steps[1].RaiseReason != "" {
				t.Fatalf("2단계는 올림이 아니다: %+v", p.Steps[1])
			}
			if p.Steps[1].LayerNote == "" {
				t.Fatal("2단계(부서장 단계)는 위 층이 본부라 층 없음 비고가 있어야 한다")
			}
		}},
		{name: "예시6 부서장 2천만: 본부장 대표 재무", requester: lHead, amount: 20_000_000, want: "최본부 > 신대표 > 재무(2)", complete: true},
		{name: "예시7 영업2팀 직원: 직무대행자", requester: lSales2, amount: 500_000, want: "대행자", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			if !p.Steps[0].Acting {
				t.Fatal("직무대행 표시가 있어야 한다")
			}
		}},
		{name: "예시9 본부 직속 팀 직원 500만: 팀장 본부장", requester: lPlan, amount: 5_000_000, want: "기획팀장 > 강본부", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			if !strings.Contains(p.Steps[1].LayerNote, "부서장 층이 없어 본부장이 맡습니다") {
				t.Fatalf("층 없음 비고 %q", p.Steps[1].LayerNote)
			}
		}},
		{name: "예시10 부서 직속 직원 500만: 부서장 본부장(단계 수 유지)", requester: lDirect, amount: 5_000_000, want: "구매부서장 > 최본부", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			if len(p.Steps) != 2 {
				t.Fatalf("단계 수가 줄면 안 된다: %d", len(p.Steps))
			}
		}},
		{name: "예시11 겸임 조직장은 같은 사람이면 위로 올림", requester: lShip, amount: 5_000_000, want: "정출하 > 최본부", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			s := p.Steps[1]
			if s.RaiseReason != raiseSame || s.RaisedFrom != "물류팀" {
				t.Fatalf("같은 사람 올림 %+v", s)
			}
		}},
		{name: "예시12 대표 요청은 불가", requester: lCEO, amount: 500_000, want: "없음", complete: false, check: func(t *testing.T, p domain.LinePreview) {
			if len(p.Problems) != 1 || p.Problems[0] != problemCEO {
				t.Fatalf("문구 %v", p.Problems)
			}
		}},
		{name: "예시13 본부장 요청 500만: 대표만, 부서장 단계 생략", requester: lDivBuy, amount: 5_000_000, want: "신대표 > 생략", complete: true, check: func(t *testing.T, p domain.LinePreview) {
			if !p.Steps[1].Omitted || p.Steps[1].Order != 0 {
				t.Fatalf("생략 단계 %+v", p.Steps[1])
			}
		}},
		{name: "예시14 조직장에게 결재 권한 없음", requester: lTeam3, amount: 500_000, want: "없음", complete: false, check: func(t *testing.T, p domain.LinePreview) {
			want := "1단계(팀장): 구매·물류본부 > 구매팀 > 구매3팀 팀장 권한없는팀장님에게 결재 권한이 없어 상신할 수 없습니다. 관리자에게 권한 역할 부여를 요청하세요."
			if len(p.Problems) != 1 || p.Problems[0] != want {
				t.Fatalf("문구 %v", p.Problems)
			}
		}},
		{name: "1억 이상: 본부장 단계 포함", requester: lStaff, amount: 150_000_000, want: "구매팀장 > 구매부서장 > 최본부 > 재무(2)", complete: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newLineEnv()
			p := e.build(t, c.requester, c.amount)
			if got := who(p); got != c.want {
				t.Fatalf("결재선 %q, 기대 %q (문제 %v)", got, c.want, p.Problems)
			}
			if p.Complete != c.complete {
				t.Fatalf("Complete %v, 기대 %v (문제 %v)", p.Complete, c.complete, p.Problems)
			}
			if c.check != nil {
				c.check(t, p)
			}
		})
	}
}

func TestBuildLineVacantWithoutActingRaisesWithReason(t *testing.T) {
	e := newLineEnv()
	e.org.SetActing(12, 0) // 대행 해제: 영업2팀은 공석
	p := e.build(t, lSales2, 5_000_000)
	if got := who(p); got != "윤영업 > 강본부" {
		t.Fatalf("결재선 %q", got)
	}
	if s := p.Steps[0]; s.RaisedFrom != "영업2팀" || s.RaiseReason != raiseVacant || !strings.Contains(s.RaisedNote, "영업2팀 팀장이 공석이라 한 단계 위로 올렸습니다.") {
		t.Fatalf("공석 올림 %+v", s)
	}
}

func TestBuildLineInactiveHeadIsRaised(t *testing.T) {
	e := newLineEnv()
	u := e.deps.users[lLead]
	u.Active = false
	e.deps.users[lLead] = u
	p := e.build(t, lStaff, 500_000)
	if got := who(p); got != "구매부서장" || p.Steps[0].RaiseReason != raiseInactive {
		t.Fatalf("재직 중이 아님 올림 %q %+v", got, p.Steps[0])
	}
}

func TestBuildLineProblemsNotErrors(t *testing.T) {
	e := newLineEnv()
	e.org.AddUnregistered(30)
	e.deps.users[30] = lineUser{ID: 30, Name: "미등록", Role: contract.RoleStaff, Active: true, Perms: templatePerms(contract.RoleStaff)}
	if p := e.build(t, 30, 500_000); p.Complete || len(p.Problems) != 1 || p.Problems[0] != problemNotInOrg {
		t.Fatalf("조직 미등록 %+v", p)
	}
	e.org.Leave(lStaff)
	if p := e.build(t, lStaff, 500_000); p.Complete || len(p.Problems) != 1 || p.Problems[0] != problemLeft {
		t.Fatalf("퇴사 %+v", p)
	}
	e.deps.tiers = func(int64) []domain.StepKind { return nil }
	if p := e.build(t, lLead, 500_000); p.Complete || len(p.Problems) != 1 || p.Problems[0] != problemNoRules(500_000) {
		t.Fatalf("규칙 없음 %+v", p)
	}
}

func TestBuildLineErrorsForBadInput(t *testing.T) {
	e := newLineEnv()
	if _, _, err := buildLine(e.ctx, nil, e.org, e.deps, lStaff, -5, e.asOf); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("금액 음수: %v", err)
	}
	if _, _, err := buildLine(e.ctx, nil, e.org, e.deps, 999, 500_000, e.asOf); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사용자: %v", err)
	}
}

func TestBuildLineFinancePoolNeedsSomeoneOtherThanRequester(t *testing.T) {
	e := newLineEnv()
	delete(e.deps.users, lFinance2) // 재무가 한 명뿐
	// 재무 담당이 직접 올리는 요청: 풀에서 요청자를 빼면 비어 상신 불가
	e.org.AddUnit(20, "FIN", "재무팀", contract.OrgDepartment, 2)
	e.org.AddPerson(lFinance, "재무담당", "과장", 20, "", "", "")
	e.org.SetHead(20, lDivBuy) // 재무팀 부서장은 다른 사람. 요청자 재무가 2천만 원을 올린다.
	p := e.build(t, lFinance, 20_000_000)
	if p.Complete {
		t.Fatalf("재무 풀이 요청자뿐이면 불가여야 한다: %+v", p)
	}
	found := false
	for _, pr := range p.Problems {
		if strings.Contains(pr, "재무 담당자가 요청자 외에 없습니다") {
			found = true
		}
	}
	if !found {
		t.Fatalf("재무 풀 문구 없음: %v", p.Problems)
	}
}

// 한 사람이 같은 결재선의 두 단계에 배정되지 않는다(AC-12). 조직 구성을 바꿔 가며 검사한다.
func TestBuildLineNeverAssignsOnePersonTwice(t *testing.T) {
	for head := int64(1); head <= 19; head++ {
		e := newLineEnv()
		// 구매1팀·구매팀·구매·물류본부의 조직장을 모두 같은 사람으로 만든다.
		e.org.SetHead(6, head).SetHead(5, head).SetHead(3, head)
		for _, req := range []int64{lStaff, lLead, lHead} {
			if req == head {
				continue
			}
			p := e.build(t, req, 20_000_000)
			seen := map[int64]bool{}
			for _, s := range p.Steps {
				if s.ApproverID == 0 || s.Omitted || s.Missing {
					continue
				}
				if seen[s.ApproverID] {
					t.Fatalf("조직장 %d, 요청자 %d: 같은 사람이 두 단계에 배정됨 %v", head, req, who(p))
				}
				seen[s.ApproverID] = true
				if s.ApproverID == req {
					t.Fatalf("요청자 본인이 결재자: %v", who(p))
				}
			}
		}
	}
}

// AC-61: 결재선 불가·올림·층 없음·생략 문구가 명세 7.1 의 문구 목록과 글자 그대로 같다(화면·상신 400·who_approves 가 같은 문장을 쓴다).
func TestLineSentencesMatchSpec(t *testing.T) {
	cases := map[string]string{
		problemNotInOrg:         "조직에 등록되지 않아 결재선을 만들 수 없습니다. 관리자에게 인사 발령(입사) 등록을 요청하세요.",
		problemLeft:             "퇴사 처리된 사용자는 상신할 수 없습니다.",
		problemCEO:              "대표의 구매요청은 결재선을 만들 수 없습니다. 관리자에게 문의하세요.",
		problemNoRules(500_000): "결재선 규칙이 없습니다(금액 500000원). 관리자에게 문의하세요.",
		problemNoAuthority(1, "팀장", "구매팀 > 구매1팀", "홍길동"): "1단계(팀장): 구매팀 > 구매1팀 팀장 홍길동님에게 결재 권한이 없어 상신할 수 없습니다. 관리자에게 권한 역할 부여를 요청하세요.",
		problemExhausted(2, "부서장", "구매팀"):                "2단계(부서장): 위로 올라가도 맡을 결재자를 찾지 못했습니다(구매팀까지 확인). 관리자에게 조직장 보임을 요청하세요.",
		problemNoFinance(3): "재무 단계(3단계)를 맡을 재무 담당자가 요청자 외에 없습니다. 관리자에게 결재자 지정을 요청하세요.",
		raiseSentence("구매1팀", "팀장", raiseSelf):      "요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다.",
		raiseSentence("구매1팀", "팀장", raiseVacant):    "구매1팀 팀장이 공석이라 한 단계 위로 올렸습니다.",
		raiseSentence("물류팀", "부서장", raiseSame):      "물류팀 부서장이 앞 단계 결재자와 같은 분이라 한 단계 위로 올렸습니다.",
		raiseSentence("영업팀", "부서장", raiseInactive):  "영업팀 부서장이 재직 중이 아니어서 한 단계 위로 올렸습니다.",
		raiseSentence("PL 주식회사", "대표", raiseVacant): "PL 주식회사 대표가 공석이라 한 단계 위로 올렸습니다.",
		raiseSentence("PL 주식회사", "대표", raiseSelf):   "요청자 본인이 PL 주식회사 대표라 한 단계 위로 올렸습니다.",
		raiseSentence("재무팀", "재무", raiseVacant):     "재무팀 재무가 공석이라 한 단계 위로 올렸습니다.",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("문구가 명세와 다름:\n got  %q\n want %q", got, want)
		}
	}
	e := newLineEnv()
	p := e.build(t, lDivBuy, 5_000_000)
	if got := p.Steps[1].Reason; got != "더 올라갈 곳이 없어 부서장 단계는 생략합니다(대표가 이미 결재선에 있습니다)." {
		t.Errorf("생략 문구: %q", got)
	}
	p = e.build(t, lPlan, 5_000_000)
	if got := p.Steps[1].LayerNote; got != "이 조직에는 부서장 층이 없어 본부장이 맡습니다." {
		t.Errorf("층 없음 문구: %q", got)
	}
}

// templatePerms 는 기본 템플릿 역할의 권한 집합이다(메모리 결재선 시험용. DB 시험은 실효 권한 캐시를 쓴다).
func templatePerms(r contract.Role) contract.PermSet {
	s := contract.NewPermSet()
	for _, p := range contract.RolePermissions[r] {
		s[p] = struct{}{}
	}
	return s
}

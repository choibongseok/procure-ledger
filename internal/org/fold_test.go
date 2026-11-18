package org

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 이벤트 열 만들기 도우미: 등록 번호는 추가한 순서다.
type evlog struct{ evs []Event }

func (l *evlog) add(subject string, id int64, kind string, day string, p Payload) int64 {
	n := int64(len(l.evs) + 1)
	l.evs = append(l.evs, Event{ID: n, Subject: subject, SubjectID: id, Kind: kind, Effective: contract.Day(day), P: p})
	return n
}

func (l *evlog) unit(id int64, code, name string, kind contract.OrgKind, parent int64, day string) {
	l.add(SubjectUnit, id, KindUnitCreate, day, Payload{Code: code, Name: name, UnitKind: kind, Parent: parent, Sort: int(id)})
}

func (l *evlog) hire(user int64, unit int64, day string) {
	l.add(SubjectPerson, user, KindHire, day, Payload{Rank: 1, Unit: unit})
}

func (l *evlog) cancel(target int64) {
	t := l.evs[target-1]
	n := int64(len(l.evs) + 1)
	l.evs = append(l.evs, Event{ID: n, Subject: t.Subject, SubjectID: t.SubjectID, Kind: KindCancel, Effective: t.Effective, Cancels: target})
}

func (l *evlog) fold(t *testing.T) *Result {
	t.Helper()
	r, err := Fold(l.evs)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return r
}

func (l *evlog) foldErr(t *testing.T) *FoldError {
	t.Helper()
	_, err := Fold(l.evs)
	var fe *FoldError
	if !errors.As(err, &fe) {
		t.Fatalf("규칙 위반이 감지되어야 함: %v", err)
	}
	return fe
}

// 기본 조직: 회사(1) > 본부(2) > 부서(3) > 팀(4, 5).
func baseOrg() *evlog {
	l := &evlog{}
	l.unit(1, "CO", "PL 주식회사", contract.OrgCompany, 0, "2020-01-01")
	l.unit(2, "DIV", "구매·물류본부", contract.OrgDivision, 1, "2020-01-01")
	l.unit(3, "DEP", "구매팀", contract.OrgDepartment, 2, "2020-01-01")
	l.unit(4, "T1", "구매1팀", contract.OrgTeam, 3, "2020-01-01")
	l.unit(5, "T2", "구매2팀", contract.OrgTeam, 3, "2020-01-01")
	return l
}

func TestFoldTransferHistoryAsOf(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-03-01")
	l.add(SubjectPerson, 10, KindTransfer, "2023-04-01", Payload{Unit: 5})
	l.add(SubjectPerson, 10, KindRankChange, "2024-01-01", Payload{Rank: 2})
	r := l.fold(t)
	for day, want := range map[string]int64{"2021-03-01": 4, "2023-03-31": 4, "2023-04-01": 5, "2026-01-01": 5} {
		p, ok := r.PersonAt(10, contract.Day(day))
		if !ok || p.Primary != want {
			t.Errorf("%s 주소속 %d(%v), want %d", day, p.Primary, ok, want)
		}
	}
	if _, ok := r.PersonAt(10, "2021-02-28"); ok {
		t.Error("입사 전에는 사람이 없어야 함")
	}
	if p, _ := r.PersonAt(10, "2023-12-31"); p.Rank != 1 {
		t.Errorf("승진 전 직위 %d", p.Rank)
	}
	if p, _ := r.PersonAt(10, "2024-01-01"); p.Rank != 2 {
		t.Errorf("승진 뒤 직위 %d", p.Rank)
	}
}

func TestFoldHeadRules(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.hire(11, 5, "2021-01-01")
	// 구성원이 아닌 사람은 조직장이 될 수 없다.
	bad := baseOrg()
	bad.hire(11, 5, "2021-01-01")
	bad.add(SubjectUnit, 4, KindHeadSet, "2021-06-01", Payload{User: 11})
	if fe := bad.foldErr(t); !strings.Contains(fe.Msg, "구성원만") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	l.add(SubjectUnit, 4, KindHeadSet, "2021-06-01", Payload{User: 10})
	// 겸직 조직장: 구매2팀 구성원이 아닌 10 을 겸직으로 넣고 조직장으로 보임
	l.add(SubjectPerson, 10, KindConcurrentAdd, "2021-07-01", Payload{Unit: 5})
	l.add(SubjectUnit, 5, KindHeadSet, "2021-07-01", Payload{User: 10})
	r := l.fold(t)
	if u, _ := r.UnitAt(4, "2021-06-01"); u.HeadUser != 10 || u.HeadActing {
		t.Fatalf("조직장 %+v", u)
	}
	if u, _ := r.UnitAt(5, "2021-07-01"); u.HeadUser != 10 {
		t.Fatalf("겸직 조직장 %+v", u)
	}
	// 전보하면 옛 조직의 조직장 자리는 자동 공석이 되고 돌아와도 되살아나지 않는다.
	l.add(SubjectPerson, 10, KindTransfer, "2022-01-01", Payload{Unit: 3})
	l.add(SubjectPerson, 10, KindTransfer, "2022-06-01", Payload{Unit: 4})
	r = l.fold(t)
	if u, _ := r.UnitAt(4, "2022-01-01"); u.HeadUser != 0 {
		t.Fatalf("전보한 날부터 공석이어야 함: %+v", u)
	}
	if u, _ := r.UnitAt(4, "2022-06-01"); u.HeadUser != 0 {
		t.Fatalf("돌아왔다고 조직장이 되살아나면 안 됨: %+v", u)
	}
	if u, _ := r.UnitAt(5, "2022-01-01"); u.HeadUser != 10 {
		// 구매2팀은 겸직이라 전보 시 겸직이 유지되면 조직장도 유지된다.
		t.Fatalf("겸직 조직은 유지: %+v", u)
	}
}

func TestFoldActingAndHeadPriority(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.hire(11, 5, "2021-01-01")
	l.add(SubjectUnit, 5, KindActingSet, "2022-03-01", Payload{User: 10, End: "2022-03-15"})
	r := l.fold(t)
	if u, _ := r.UnitAt(5, "2022-03-01"); u.HeadUser != 10 || !u.HeadActing {
		t.Fatalf("대행 시작일: %+v", u)
	}
	if u, _ := r.UnitAt(5, "2022-03-15"); u.HeadUser != 10 || !u.HeadActing {
		t.Fatalf("종료일(포함): %+v", u)
	}
	if u, _ := r.UnitAt(5, "2022-03-16"); u.HeadUser != 0 {
		t.Fatalf("종료 다음 날은 공석: %+v", u)
	}
	// 겹치는 대행은 거부
	l.add(SubjectUnit, 5, KindActingSet, "2022-03-10", Payload{User: 11, End: "2022-03-20"})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "이미 직무대행이 있습니다") {
		t.Fatalf("겹침 문구: %s", fe.Msg)
	}
	l.evs = l.evs[:len(l.evs)-1]
	// 대행 기간 중 조직장이 보임되면 그날부터 조직장이 우선이다.
	l.add(SubjectUnit, 5, KindHeadSet, "2022-03-08", Payload{User: 11})
	r = l.fold(t)
	if u, _ := r.UnitAt(5, "2022-03-08"); u.HeadUser != 11 || u.HeadActing {
		t.Fatalf("조직장 보임 우선: %+v", u)
	}
	// 조직장이 있는 날에 시작하는 대행은 등록할 수 없다.
	l.add(SubjectUnit, 5, KindActingSet, "2022-04-01", Payload{User: 10, End: "2022-04-05"})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "조직장이 있는 기간") {
		t.Fatalf("문구: %s", fe.Msg)
	}
}

func TestFoldCloseAndMoveRules(t *testing.T) {
	// 하위 조직·인원이 남은 조직의 폐지는 거부
	l := baseOrg()
	l.add(SubjectUnit, 3, KindUnitClose, "2021-01-01", Payload{})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "하위 조직이 남아") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	l = baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.add(SubjectUnit, 4, KindUnitClose, "2022-01-01", Payload{})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "소속 인원이 남아") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	l.evs = l.evs[:len(l.evs)-1]
	// 인원을 옮긴 뒤 폐지하면 되고, 폐지된 조직으로의 전보는 거부, 재개하면 다시 가능
	l.add(SubjectPerson, 10, KindTransfer, "2021-12-01", Payload{Unit: 5})
	l.add(SubjectUnit, 4, KindUnitClose, "2022-01-01", Payload{})
	r := l.fold(t)
	if u, _ := r.UnitAt(4, "2021-12-31"); u.Closed {
		t.Fatal("폐지 전")
	}
	if u, _ := r.UnitAt(4, "2022-01-01"); !u.Closed {
		t.Fatal("폐지 뒤")
	}
	n := l.add(SubjectPerson, 10, KindTransfer, "2022-02-01", Payload{Unit: 4})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "폐지된 조직") || fe.Event.ID != n {
		t.Fatalf("문구/이벤트: %s %d", fe.Msg, fe.Event.ID)
	}
	l.add(SubjectUnit, 4, KindUnitReopen, "2022-01-15", Payload{})
	l.fold(t)
	// 순환 이동 거부
	m := baseOrg()
	m.add(SubjectUnit, 2, KindUnitMove, "2021-01-01", Payload{Parent: 4})
	if fe := m.foldErr(t); !strings.Contains(fe.Msg, "자기 하위 조직") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	// 회사 이동·폐지 거부
	m = baseOrg()
	m.add(SubjectUnit, 1, KindUnitClose, "2021-01-01", Payload{})
	if fe := m.foldErr(t); !strings.Contains(fe.Msg, "회사") {
		t.Fatalf("문구: %s", fe.Msg)
	}
}

func TestFoldNameAndCompanyRules(t *testing.T) {
	l := baseOrg()
	l.unit(6, "DEP2", "구매팀", contract.OrgDepartment, 2, "2020-02-01")
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "회사 안에서 하나뿐") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	l = baseOrg()
	l.unit(6, "T1B", "구매1팀", contract.OrgTeam, 3, "2020-02-01")
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "같은 이름") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	// 다른 부서 밑이면 같은 팀 이름 허용
	l = baseOrg()
	l.unit(6, "DEP2", "물류팀", contract.OrgDepartment, 2, "2020-02-01")
	l.unit(7, "T1B", "구매1팀", contract.OrgTeam, 6, "2020-02-01")
	l.fold(t)
	// 회사(법인의 루트)는 여러 개 둘 수 있다(B44). 회사 이름은 전체에서 유일하고, 본부·부서 이름은 같은 회사 안에서만 유일하다.
	l = baseOrg()
	l.unit(6, "CO2", "다른 회사", contract.OrgCompany, 0, "2020-02-01")
	l.unit(7, "DEP9", "구매팀", contract.OrgDepartment, 6, "2020-03-01") // 다른 회사의 같은 이름 부서
	l.fold(t)
	l = baseOrg()
	l.unit(6, "CO2", "PL 주식회사", contract.OrgCompany, 0, "2020-02-01")
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "회사 안에서 하나뿐") {
		t.Fatalf("같은 이름의 회사: %s", fe.Msg)
	}
	l = baseOrg()
	l.unit(6, "T1", "새 팀", contract.OrgTeam, 3, "2020-02-01") // 코드 중복
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "조직 코드") {
		t.Fatalf("문구: %s", fe.Msg)
	}
}

func TestFoldConcurrentRules(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.add(SubjectPerson, 10, KindConcurrentAdd, "2021-02-01", Payload{Unit: 4})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "이미 그 조직") {
		t.Fatalf("주소속 겸직: %s", fe.Msg)
	}
	l = baseOrg()
	l.unit(6, "U6", "개발팀", contract.OrgDepartment, 2, "2020-01-01")
	l.unit(7, "U7", "인사팀", contract.OrgDepartment, 2, "2020-01-01")
	l.unit(8, "U8", "재무팀", contract.OrgDepartment, 2, "2020-01-01")
	l.unit(9, "U9", "총무팀", contract.OrgDepartment, 2, "2020-01-01")
	l.hire(10, 4, "2021-01-01")
	for i, u := range []int64{5, 6, 7, 8} {
		l.add(SubjectPerson, 10, KindConcurrentAdd, "2021-02-0"+string(rune('1'+i)), Payload{Unit: u})
	}
	l.fold(t)
	l.add(SubjectPerson, 10, KindConcurrentAdd, "2021-03-01", Payload{Unit: 9})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "겸직은 최대 4개") {
		t.Fatalf("한도 문구: %s", fe.Msg)
	}
	// 겸직하던 조직으로 전보하면 겸직이 자동 해제된다.
	l.evs = l.evs[:len(l.evs)-1]
	l.add(SubjectPerson, 10, KindTransfer, "2021-04-01", Payload{Unit: 5})
	r := l.fold(t)
	if c := r.ConcurrentAt(10, "2021-04-01"); len(c) != 3 {
		t.Fatalf("겸직 %v", c)
	}
	if c := r.ConcurrentAt(10, "2021-03-31"); len(c) != 4 {
		t.Fatalf("전보 전 겸직 %v", c)
	}
}

func TestFoldLeaveRehireAndOrdering(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.add(SubjectUnit, 4, KindHeadSet, "2021-02-01", Payload{User: 10})
	l.add(SubjectPerson, 10, KindLeave, "2022-01-01", Payload{LastDay: "2021-12-31"})
	r := l.fold(t)
	if p, _ := r.PersonAt(10, "2021-12-31"); p.Left {
		t.Fatal("마지막 근무일에는 재직")
	}
	if p, _ := r.PersonAt(10, "2022-01-01"); !p.Left {
		t.Fatal("다음 날부터 퇴사")
	}
	if u, _ := r.UnitAt(4, "2022-01-01"); u.HeadUser != 0 {
		t.Fatalf("퇴사한 조직장의 조직은 공석: %+v", u)
	}
	// 퇴사자에게 발령하면 거부
	l.add(SubjectPerson, 10, KindTransfer, "2022-02-01", Payload{Unit: 5})
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "퇴사한 사람") {
		t.Fatalf("문구: %s", fe.Msg)
	}
	// 재입사는 허용, 같은 날 전보 후 퇴사는 되지만 퇴사 후 전보는 거부(등록 순서)
	l.evs = l.evs[:len(l.evs)-1]
	l.hire(10, 5, "2023-01-01")
	l.add(SubjectPerson, 10, KindTransfer, "2023-06-01", Payload{Unit: 4})
	l.add(SubjectPerson, 10, KindLeave, "2023-06-01", Payload{LastDay: "2023-05-31"})
	l.fold(t)
	l.evs[len(l.evs)-1], l.evs[len(l.evs)-2] = l.evs[len(l.evs)-2], l.evs[len(l.evs)-1]
	l.evs[len(l.evs)-1].ID, l.evs[len(l.evs)-2].ID = l.evs[len(l.evs)-2].ID, l.evs[len(l.evs)-1].ID
	if fe := l.foldErr(t); !strings.Contains(fe.Msg, "퇴사한 사람") {
		t.Fatalf("퇴사 뒤 전보: %s", fe.Msg)
	}
	// 입사일보다 이른 퇴사일
	m := baseOrg()
	m.hire(10, 4, "2021-05-01")
	m.add(SubjectPerson, 10, KindLeave, "2021-05-01", Payload{LastDay: "2021-04-30"})
	if fe := m.foldErr(t); !strings.Contains(fe.Msg, "입사일보다 이른") {
		t.Fatalf("문구: %s", fe.Msg)
	}
}

// 소급으로 끼워 넣은 이벤트가 뒤쪽 이벤트와 충돌하면 어긋나는 이벤트가 지목된다.
func TestFoldRetroactiveConflictPointsAtLaterEvent(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	later := l.add(SubjectPerson, 10, KindTransfer, "2024-01-01", Payload{Unit: 5})
	l.fold(t)
	// 2023 년에 5팀을 폐지하는 소급 이벤트: 2024 전보가 폐지된 조직으로 가게 된다.
	l.add(SubjectUnit, 5, KindUnitClose, "2023-01-01", Payload{})
	fe := l.foldErr(t)
	if fe.Event.ID != later || !strings.Contains(fe.Msg, "폐지된 조직") {
		t.Fatalf("지목된 이벤트 %d(%s), want %d", fe.Event.ID, fe.Msg, later)
	}
	if !strings.Contains(fe.Error(), "2024. 1. 1. 전보") {
		t.Fatalf("오류 문구에 날짜와 종류가 있어야 함: %s", fe.Error())
	}
}

func TestFoldCancelRemovesEventFromComputation(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	tr := l.add(SubjectPerson, 10, KindTransfer, "2022-01-01", Payload{Unit: 5})
	if r := l.fold(t); func() int64 { p, _ := r.PersonAt(10, "2022-06-01"); return p.Primary }() != 5 {
		t.Fatal("전보 반영")
	}
	l.cancel(tr)
	r := l.fold(t)
	if p, _ := r.PersonAt(10, "2022-06-01"); p.Primary != 4 {
		t.Fatalf("취소된 전보는 계산에서 빠져야 함: %+v", p)
	}
	// 취소가 뒤 이벤트와 맞지 않으면(입사를 취소) 거부된다.
	m := baseOrg()
	h := int64(len(m.evs) + 1)
	m.hire(10, 4, "2021-01-01")
	m.add(SubjectPerson, 10, KindTransfer, "2022-01-01", Payload{Unit: 5})
	m.cancel(h)
	if fe := m.foldErr(t); !strings.Contains(fe.Msg, "입사 전인 사람") {
		t.Fatalf("입사 취소 뒤 전보: %s", fe.Msg)
	}
}

// 같은 날 만들었다 바꾼 값은 길이 0 구간을 남기지 않는다.
func TestFoldNoZeroLengthSpans(t *testing.T) {
	l := baseOrg()
	l.hire(10, 4, "2021-01-01")
	l.add(SubjectPerson, 10, KindTransfer, "2021-01-01", Payload{Unit: 5})
	l.add(SubjectPerson, 10, KindConcurrentAdd, "2021-02-01", Payload{Unit: 4})
	l.add(SubjectPerson, 10, KindConcurrentEnd, "2021-02-01", Payload{Unit: 4})
	r := l.fold(t)
	for _, s := range r.People {
		if s.To != "" && s.To <= s.From {
			t.Fatalf("길이 0 구간: %+v", s)
		}
	}
	if len(r.Concurrent) != 0 {
		t.Fatalf("같은 날 추가·해제한 겸직은 남지 않아야 함: %+v", r.Concurrent)
	}
	if p, _ := r.PersonAt(10, "2021-01-01"); p.Primary != 5 {
		t.Fatalf("같은 날 두 번 전보하면 마지막이 주소속: %+v", p)
	}
}

func TestDayHelpers(t *testing.T) {
	if AddDays("2021-12-31", 1) != "2022-01-01" || AddDays("2020-03-01", -1) != "2020-02-29" {
		t.Fatal("AddDays")
	}
	if DaysBetween("2021-01-01", "2021-02-01") != 31 {
		t.Fatal("DaysBetween")
	}
	if DayText("2026-03-01") != "2026. 3. 1." {
		t.Fatal("DayText")
	}
	if _, err := ParseDay("2026-02-30"); err == nil {
		t.Fatal("없는 날짜는 거부")
	}
	if d, err := ParseDay(" 2026-02-28 "); err != nil || d != "2026-02-28" {
		t.Fatalf("ParseDay %v %v", d, err)
	}
}

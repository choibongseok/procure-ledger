package org

import (
	"fmt"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 구간 계산 결과. 구간은 [From, To) 이고 To 가 비어 있으면 열려 있다.

// UnitSpan 은 조직 단위의 구간별 값이다. HeadUser 는 그 기간의 실효 조직장(조직장 또는 직무대행자, 없으면 0)이다.
type UnitSpan struct {
	UnitID     int64
	From, To   contract.Day
	Code, Name string
	Kind       contract.OrgKind
	ParentID   int64
	Sort       int
	Closed     bool
	HeadUser   int64
	HeadActing bool
	dead       bool
}

// PersonSpan 은 사람의 구간별 값이다.
type PersonSpan struct {
	UserID   int64
	From, To contract.Day
	Left     bool // 퇴사 상태
	Rank     int64
	Primary  int64
	Absence  string // 휴직 구분(빈 값 = 휴직 아님, B50). 재직 상태는 그대로다
	dead     bool
}

// ConcurrentSpan 은 겸직 구간이다.
type ConcurrentSpan struct {
	UserID, UnitID int64
	From, To       contract.Day
	dead           bool
}

// ActingSpan 은 직무대행 구간이다.
type ActingSpan struct {
	UnitID, UserID int64
	From, To       contract.Day
}

// Result 는 Fold 의 결과다.
type Result struct {
	Units      []UnitSpan
	People     []PersonSpan
	Concurrent []ConcurrentSpan
	Acting     []ActingSpan
}

// FoldError 는 이벤트 열이 규칙을 어길 때의 오류다. Event 가 어긋나는 이벤트다.
type FoldError struct {
	Event Event
	Msg   string
}

func (e *FoldError) Error() string { return e.Event.Describe() + ": " + e.Msg }

type unitState struct {
	code, name string
	kind       contract.OrgKind
	parent     int64
	sort       int
	closed     bool
	head       int64
}

type personState struct {
	left    bool
	absence string // 휴직 구분, 빈 값 = 휴직 아님
	rank    int64
	primary int64
	conc    []int64
	hired   contract.Day
}

type actingIv struct {
	unit, user int64
	start, end contract.Day
}

type folder struct {
	units  map[int64]*unitState
	people map[int64]*personState
	acting []*actingIv
	// 퇴사 이벤트의 효력일(사람별, 취소된 것 제외). 재직 중인 사람의 입사 거부 문구를 가르는 데 쓴다.
	leaveDays map[int64][]contract.Day

	dirtyUnits  map[int64]bool
	dirtyPeople map[int64]bool

	out        Result
	unitOpen   map[int64]int
	personOpen map[int64]int
	concOpen   map[[2]int64]int
}

// Fold 는 이벤트 전체를 (효력일, 등록 번호) 순서로 처음부터 적용해 구간을 계산한다.
// 취소된 이벤트와 취소 이벤트는 계산에서 빠진다. 규칙을 어기는 이벤트가 있으면 *FoldError 를 돌려준다.
func Fold(all []Event) (*Result, error) {
	cancelled := map[int64]bool{}
	for _, e := range all {
		if e.Kind == KindCancel && e.Cancels != 0 {
			cancelled[e.Cancels] = true
		}
	}
	var evs []Event
	for _, e := range all {
		if e.Kind == KindCancel || cancelled[e.ID] {
			continue
		}
		evs = append(evs, e)
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Effective != evs[j].Effective {
			return evs[i].Effective < evs[j].Effective
		}
		return evs[i].ID < evs[j].ID
	})

	f := &folder{
		units: map[int64]*unitState{}, people: map[int64]*personState{},
		dirtyUnits: map[int64]bool{}, dirtyPeople: map[int64]bool{},
		unitOpen: map[int64]int{}, personOpen: map[int64]int{}, concOpen: map[[2]int64]int{},
		leaveDays: map[int64][]contract.Day{},
	}
	for _, e := range evs {
		if e.Kind == KindLeave {
			f.leaveDays[e.SubjectID] = append(f.leaveDays[e.SubjectID], e.Effective)
		}
	}

	// 날짜 경계: 이벤트 효력일과 직무대행 종료 다음 날.
	dateSet := map[contract.Day]bool{}
	for _, e := range evs {
		dateSet[e.Effective] = true
		if e.Kind == KindActingSet && e.P.End != "" {
			dateSet[AddDays(e.P.End, 1)] = true
		}
	}
	dates := make([]contract.Day, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })

	idx := 0
	for _, d := range dates {
		for idx < len(evs) && evs[idx].Effective == d {
			if err := f.apply(evs[idx]); err != nil {
				return nil, err
			}
			idx++
		}
		f.snapshot(d)
	}
	f.finish()
	return &f.out, nil
}

func fail(e Event, format string, a ...any) error {
	return &FoldError{Event: e, Msg: fmt.Sprintf(format, a...)}
}

func (f *folder) isMember(p *personState, unit int64) bool {
	if p.primary == unit {
		return true
	}
	for _, c := range p.conc {
		if c == unit {
			return true
		}
	}
	return false
}

func (f *folder) isDescendant(candidate, ancestor int64) bool {
	for cur := candidate; cur != 0; {
		if cur == ancestor {
			return true
		}
		u := f.units[cur]
		if u == nil {
			return false
		}
		cur = u.parent
	}
	return false
}

func uniqueGlobally(k contract.OrgKind) bool {
	return k == contract.OrgCompany || k == contract.OrgDivision || k == contract.OrgDepartment
}

// nameTaken 은 이름 규칙(본부·부서·회사는 전체에서 유일, 팀은 같은 부모 아래에서 유일)을 어기는지 본다.
func (f *folder) nameTaken(self int64, kind contract.OrgKind, parent int64, name string) bool {
	myRoot := self
	if kind != contract.OrgCompany {
		myRoot = f.rootOf(parent)
	}
	for id, u := range f.units {
		if id == self || u.name != name {
			continue
		}
		if uniqueGlobally(kind) && uniqueGlobally(u.kind) {
			// 회사(법인 루트)끼리는 이름이 전체에서 유일하고, 본부·부서는 같은 회사(루트) 안에서 유일하다(법인이 다르면 같은 이름을 쓸 수 있다, B44).
			if kind == contract.OrgCompany && u.kind == contract.OrgCompany {
				return true
			}
			if f.rootOf(id) == myRoot {
				return true
			}
			continue
		}
		if !uniqueGlobally(kind) && !uniqueGlobally(u.kind) && u.parent == parent {
			return true
		}
		// 팀과 본부·부서가 같은 부모 아래 같은 이름이면 혼동되므로 막는다.
		if u.parent == parent && parent != 0 {
			return true
		}
	}
	return false
}

// rootOf 는 조직이 속한 트리의 루트(회사) 번호다. 부모가 없으면 자기 자신이다. 순환을 만나면 거기서 멈춘다.
func (f *folder) rootOf(unit int64) int64 {
	cur := unit
	for i := 0; i < 64; i++ {
		u := f.units[cur]
		if u == nil || u.parent == 0 {
			return cur
		}
		cur = u.parent
	}
	return cur
}

func (f *folder) openUnitCount(parent int64) int {
	n := 0
	for _, u := range f.units {
		if u.parent == parent && !u.closed {
			n++
		}
	}
	return n
}

func (f *folder) membersOf(unit int64) int {
	n := 0
	for _, p := range f.people {
		if !p.left && f.isMember(p, unit) {
			n++
		}
	}
	return n
}

// vacate 는 사람이 더는 구성원이 아닌 조직의 조직장 자리를 비운다(3.1 의 7).
func (f *folder) vacate(user int64, p *personState) {
	for id, u := range f.units {
		if u.head == user && (p.left || !f.isMember(p, id)) {
			u.head = 0
			f.dirtyUnits[id] = true
		}
	}
}

func (f *folder) headSeats(user int64) int {
	n := 0
	for _, u := range f.units {
		if u.head == user {
			n++
		}
	}
	return n
}

func (f *folder) apply(e Event) error {
	switch e.Kind {
	case KindUnitCreate:
		return f.unitCreate(e)
	case KindUnitRename, KindUnitMove, KindUnitSort, KindUnitClose, KindUnitReopen, KindHeadSet, KindHeadClear, KindActingSet:
		return f.unitChange(e)
	case KindHire, KindTransfer, KindRankChange, KindConcurrentAdd, KindConcurrentEnd, KindLeave, KindAbsenceStart, KindAbsenceEnd:
		return f.personChange(e)
	}
	return fail(e, "알 수 없는 발령 종류입니다.")
}

func (f *folder) unitCreate(e Event) error {
	p := e.P
	if _, ok := f.units[e.SubjectID]; ok {
		return fail(e, "이미 만들어진 조직입니다.")
	}
	if !p.UnitKind.IsKnown() {
		return fail(e, "조직 종류가 올바르지 않습니다.")
	}
	if p.Name == "" || p.Code == "" {
		return fail(e, "조직 이름과 코드를 입력하세요.")
	}
	if p.UnitKind == contract.OrgCompany {
		if p.Parent != 0 {
			return fail(e, "회사는 상위 조직을 가질 수 없습니다.")
		}
		// 회사(법인의 루트 조직)는 법인마다 하나씩 여러 개 둘 수 있다(B44). 이름은 회사끼리 유일하다(nameTaken).
	} else {
		par := f.units[p.Parent]
		if par == nil {
			return fail(e, "상위 조직이 없습니다.")
		}
		if par.closed {
			return fail(e, "폐지된 조직 밑에는 조직을 만들 수 없습니다.")
		}
	}
	for _, u := range f.units {
		if u.code == p.Code {
			return fail(e, "이미 사용 중인 조직 코드입니다.")
		}
	}
	if f.nameTaken(e.SubjectID, p.UnitKind, p.Parent, p.Name) {
		return fail(e, "%s", nameMsg(p.UnitKind))
	}
	f.units[e.SubjectID] = &unitState{code: p.Code, name: p.Name, kind: p.UnitKind, parent: p.Parent, sort: p.Sort}
	f.dirtyUnits[e.SubjectID] = true
	return nil
}

func nameMsg(k contract.OrgKind) string {
	if uniqueGlobally(k) {
		return "본부·부서 이름은 회사 안에서 하나뿐이어야 합니다."
	}
	return "같은 상위 조직 아래에 같은 이름이 있습니다."
}

func (f *folder) unitChange(e Event) error {
	u := f.units[e.SubjectID]
	if u == nil {
		return fail(e, "만들어지지 않은 조직입니다.")
	}
	if u.closed && e.Kind != KindUnitReopen {
		return fail(e, "폐지된 조직은 바꿀 수 없습니다(재개하려면 조직 재개를 등록하세요).")
	}
	switch e.Kind {
	case KindUnitRename:
		if e.P.Name == "" {
			return fail(e, "조직 이름을 입력하세요.")
		}
		if f.nameTaken(e.SubjectID, u.kind, u.parent, e.P.Name) {
			return fail(e, "%s", nameMsg(u.kind))
		}
		u.name = e.P.Name
	case KindUnitMove:
		if u.kind == contract.OrgCompany {
			return fail(e, "회사는 옮길 수 없습니다.")
		}
		np := f.units[e.P.Parent]
		if np == nil {
			return fail(e, "새 상위 조직이 없습니다.")
		}
		if f.isDescendant(e.P.Parent, e.SubjectID) {
			return fail(e, "자기 하위 조직 밑으로는 옮길 수 없습니다.")
		}
		if np.closed {
			return fail(e, "폐지된 조직 밑으로는 옮길 수 없습니다.")
		}
		if f.nameTaken(e.SubjectID, u.kind, e.P.Parent, u.name) {
			return fail(e, "%s", nameMsg(u.kind))
		}
		u.parent = e.P.Parent
	case KindUnitSort:
		u.sort = e.P.Sort
	case KindUnitClose:
		if u.kind == contract.OrgCompany {
			return fail(e, "회사는 폐지할 수 없습니다.")
		}
		if n := f.openUnitCount(e.SubjectID); n > 0 {
			return fail(e, "하위 조직이 남아 있어 폐지할 수 없습니다(%d곳).", n)
		}
		if n := f.membersOf(e.SubjectID); n > 0 {
			return fail(e, "소속 인원이 남아 있어 폐지할 수 없습니다(%d명).", n)
		}
		u.closed = true
		u.head = 0
	case KindUnitReopen:
		if !u.closed {
			return fail(e, "폐지되지 않은 조직입니다.")
		}
		if par := f.units[u.parent]; par != nil && par.closed {
			return fail(e, "상위 조직이 폐지되어 있어 재개할 수 없습니다.")
		}
		u.closed = false
	case KindHeadSet:
		p := f.people[e.P.User]
		if p == nil || p.left {
			return fail(e, "퇴사했거나 효력일에 재직 중이 아닌 사람입니다.")
		}
		if !f.isMember(p, e.SubjectID) {
			return fail(e, "이 조직의 구성원만 조직장이 될 수 있습니다.")
		}
		prev := u.head
		u.head = e.P.User
		if f.headSeats(e.P.User) > MaxHeadSeats {
			u.head = prev
			return fail(e, "한 사람이 조직장을 맡을 수 있는 조직은 최대 %d곳입니다.", MaxHeadSeats)
		}
	case KindHeadClear:
		if u.head == 0 {
			return fail(e, "조직장이 없는 조직입니다.")
		}
		u.head = 0
	case KindActingSet:
		return f.actingSet(e, u)
	}
	f.dirtyUnits[e.SubjectID] = true
	return nil
}

func (f *folder) actingSet(e Event, u *unitState) error {
	p := f.people[e.P.User]
	if p == nil || p.left {
		return fail(e, "퇴사했거나 효력일에 재직 중이 아닌 사람입니다.")
	}
	if e.P.End == "" || e.P.End < e.Effective {
		return fail(e, "직무대행 종료일은 시작일 이후여야 합니다.")
	}
	if u.head != 0 {
		// 조직장이 휴직 중이면 그 기간의 결재를 맡길 직무대행을 둘 수 있다(B50 8.3).
		if hp := f.people[u.head]; hp == nil || hp.absence == "" {
			return fail(e, "조직장이 있는 기간에는 직무대행을 둘 수 없습니다.")
		}
	}
	for _, iv := range f.acting {
		if iv.unit == e.SubjectID && !(e.P.End < iv.start || iv.end < e.Effective) {
			return fail(e, "이 조직의 같은 기간에 이미 직무대행이 있습니다({u:%d}, %s~%s).", iv.user, DayText(iv.start), DayText(iv.end))
		}
	}
	f.acting = append(f.acting, &actingIv{unit: e.SubjectID, user: e.P.User, start: e.Effective, end: e.P.End})
	f.out.Acting = append(f.out.Acting, ActingSpan{UnitID: e.SubjectID, UserID: e.P.User, From: e.Effective, To: AddDays(e.P.End, 1)})
	f.dirtyUnits[e.SubjectID] = true
	return nil
}

func (f *folder) personChange(e Event) error {
	id := e.SubjectID
	p := f.people[id]
	if e.Kind == KindHire {
		if p != nil && !p.left {
			// 이 입사일 뒤에 퇴사가 등록되어 있으면, 퇴사 기간 안의 날짜로 재입사하려는 것이다.
			for _, ld := range f.leaveDays[id] {
				if ld >= e.Effective {
					return fail(e, "퇴사일(%s) 이전 날짜로는 재입사할 수 없습니다. 퇴사일 이후 날짜로 등록하세요.", DayText(ld))
				}
			}
			return fail(e, "이미 재직 중인 사람입니다.")
		}
		if e.P.Rank == 0 {
			return fail(e, "직위를 고르세요.")
		}
		un := f.units[e.P.Unit]
		if un == nil {
			return fail(e, "소속 조직이 없습니다.")
		}
		if un.closed {
			return fail(e, "폐지된 조직으로는 발령할 수 없습니다.")
		}
		if p == nil {
			p = &personState{}
			f.people[id] = p
		}
		*p = personState{rank: e.P.Rank, primary: e.P.Unit, hired: e.Effective}
		f.dirtyPeople[id] = true
		return nil
	}
	if p == nil {
		return fail(e, "입사 전인 사람에게는 발령할 수 없습니다. 입사일 이후 날짜로 등록하세요.")
	}
	if p.left {
		return fail(e, "퇴사한 사람에게는 발령할 수 없습니다.")
	}
	switch e.Kind {
	case KindTransfer:
		un := f.units[e.P.Unit]
		if un == nil {
			return fail(e, "소속 조직이 없습니다.")
		}
		if un.closed {
			return fail(e, "폐지된 조직으로는 발령할 수 없습니다.")
		}
		if p.primary == e.P.Unit {
			return fail(e, "이미 그 조직에 소속되어 있습니다.")
		}
		p.primary = e.P.Unit
		p.conc = removeID(p.conc, e.P.Unit) // 겸직하던 조직으로 전보하면 겸직은 자동 해제
	case KindRankChange:
		if e.P.Rank == 0 || e.P.Rank == p.rank {
			return fail(e, "바뀌는 직위가 없습니다.")
		}
		p.rank = e.P.Rank
	case KindConcurrentAdd:
		un := f.units[e.P.Unit]
		if un == nil {
			return fail(e, "겸직 조직이 없습니다.")
		}
		if un.closed {
			return fail(e, "폐지된 조직으로는 발령할 수 없습니다.")
		}
		if p.primary == e.P.Unit || containsID(p.conc, e.P.Unit) {
			return fail(e, "이미 그 조직에 소속되어 있습니다.")
		}
		if len(p.conc) >= MaxConcurrent {
			return fail(e, "겸직은 최대 %d개까지 둘 수 있습니다.", MaxConcurrent)
		}
		p.conc = append(p.conc, e.P.Unit)
	case KindConcurrentEnd:
		if !containsID(p.conc, e.P.Unit) {
			return fail(e, "겸직 중이 아닌 조직입니다.")
		}
		p.conc = removeID(p.conc, e.P.Unit)
	case KindLeave:
		if e.P.LastDay == "" || e.Effective != AddDays(e.P.LastDay, 1) {
			return fail(e, "퇴사 효력일은 마지막 근무일 다음 날이어야 합니다.")
		}
		if e.P.LastDay < p.hired {
			return fail(e, "입사일보다 이른 퇴사일입니다.")
		}
		p.left = true
		p.absence = "" // 휴직 중 퇴사는 퇴사 효력일에 휴직이 끝난 것으로 계산한다
		p.conc = nil
	case KindAbsenceStart:
		if p.absence != "" {
			return fail(e, "이미 휴직 중입니다.")
		}
		if !KnownAbsenceKind(e.P.Absence) {
			return fail(e, "휴직 구분이 올바르지 않습니다.")
		}
		if e.P.ReturnDay != "" && e.P.ReturnDay <= e.Effective {
			return fail(e, "예정 복직일은 휴직 시작일 이후여야 합니다.")
		}
		p.absence = e.P.Absence
	case KindAbsenceEnd:
		if p.absence == "" {
			return fail(e, "휴직 중이 아닙니다.")
		}
		p.absence = ""
	}
	f.vacate(id, p)
	f.dirtyPeople[id] = true
	return nil
}

func containsID(a []int64, x int64) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

func removeID(a []int64, x int64) []int64 {
	out := a[:0:0]
	for _, v := range a {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// effHead 는 날짜 d 의 실효 조직장이다(조직장 → 직무대행 → 없음).
func (f *folder) effHead(unit int64, d contract.Day) (int64, bool) {
	u := f.units[unit]
	if u == nil {
		return 0, false
	}
	onLeave := false
	if u.head != 0 {
		if hp := f.people[u.head]; hp == nil || hp.absence == "" {
			return u.head, false
		}
		onLeave = true // 조직장이 휴직 중이면 직무대행이 있을 때만 대행자가 이어받는다
	}
	for _, iv := range f.acting {
		if iv.unit == unit && iv.start <= d && d <= iv.end {
			if p := f.people[iv.user]; p != nil && !p.left {
				return iv.user, true
			}
		}
	}
	if onLeave {
		return u.head, false
	}
	return 0, false
}

// snapshot 은 날짜 d 의 이벤트를 다 적용한 뒤, 바뀐 대상의 구간을 닫고 새로 연다.
func (f *folder) snapshot(d contract.Day) {
	for _, iv := range f.acting {
		if iv.start == d || AddDays(iv.end, 1) == d || f.dirtyPeople[iv.user] {
			f.dirtyUnits[iv.unit] = true
		}
	}
	// 조직장의 재직·휴직 상태가 바뀌면 그 조직의 실효 조직장도 다시 계산한다(휴직 중 조직장의 직무대행).
	for id, u := range f.units {
		if u.head != 0 && f.dirtyPeople[u.head] {
			f.dirtyUnits[id] = true
		}
	}
	for id := range f.dirtyUnits {
		u := f.units[id]
		hu, ha := f.effHead(id, d)
		f.setUnit(UnitSpan{UnitID: id, From: d, Code: u.code, Name: u.name, Kind: u.kind, ParentID: u.parent, Sort: u.sort,
			Closed: u.closed, HeadUser: hu, HeadActing: ha})
	}
	for id := range f.dirtyPeople {
		p := f.people[id]
		f.setPerson(PersonSpan{UserID: id, From: d, Left: p.left, Rank: p.rank, Primary: p.primary, Absence: p.absence})
		f.setConcurrent(id, p.conc, d)
	}
	f.dirtyUnits = map[int64]bool{}
	f.dirtyPeople = map[int64]bool{}
}

func (f *folder) setUnit(n UnitSpan) {
	if i, ok := f.unitOpen[n.UnitID]; ok {
		cur := &f.out.Units[i]
		same := cur.Code == n.Code && cur.Name == n.Name && cur.Kind == n.Kind && cur.ParentID == n.ParentID && cur.Sort == n.Sort &&
			cur.Closed == n.Closed && cur.HeadUser == n.HeadUser && cur.HeadActing == n.HeadActing
		if same {
			return
		}
		if cur.From == n.From { // 같은 날 다시 바뀜: 길이 0 구간을 만들지 않고 덮어쓴다
			*cur = n
			return
		}
		cur.To = n.From
	}
	f.out.Units = append(f.out.Units, n)
	f.unitOpen[n.UnitID] = len(f.out.Units) - 1
}

func (f *folder) setPerson(n PersonSpan) {
	if i, ok := f.personOpen[n.UserID]; ok {
		cur := &f.out.People[i]
		if cur.Left == n.Left && cur.Rank == n.Rank && cur.Primary == n.Primary && cur.Absence == n.Absence {
			return
		}
		if cur.From == n.From {
			*cur = n
			return
		}
		cur.To = n.From
	}
	f.out.People = append(f.out.People, n)
	f.personOpen[n.UserID] = len(f.out.People) - 1
}

func (f *folder) setConcurrent(user int64, now []int64, d contract.Day) {
	want := map[int64]bool{}
	for _, u := range now {
		want[u] = true
	}
	for key, i := range f.concOpen {
		if key[0] != user || want[key[1]] {
			continue
		}
		cur := &f.out.Concurrent[i]
		if cur.From == d {
			cur.dead = true
		} else {
			cur.To = d
		}
		delete(f.concOpen, key)
	}
	for _, u := range now {
		key := [2]int64{user, u}
		if _, ok := f.concOpen[key]; ok {
			continue
		}
		f.out.Concurrent = append(f.out.Concurrent, ConcurrentSpan{UserID: user, UnitID: u, From: d})
		f.concOpen[key] = len(f.out.Concurrent) - 1
	}
}

// finish 는 길이 0 으로 지워진 구간을 빼고 결과를 정해진 순서로 정렬한다.
func (f *folder) finish() {
	units := f.out.Units[:0]
	for _, s := range f.out.Units {
		if !s.dead {
			units = append(units, s)
		}
	}
	f.out.Units = units
	conc := f.out.Concurrent[:0]
	for _, s := range f.out.Concurrent {
		if !s.dead {
			conc = append(conc, s)
		}
	}
	f.out.Concurrent = conc
	sort.Slice(f.out.Units, func(i, j int) bool {
		a, b := f.out.Units[i], f.out.Units[j]
		return a.UnitID < b.UnitID || (a.UnitID == b.UnitID && a.From < b.From)
	})
	sort.Slice(f.out.People, func(i, j int) bool {
		a, b := f.out.People[i], f.out.People[j]
		return a.UserID < b.UserID || (a.UserID == b.UserID && a.From < b.From)
	})
	sort.Slice(f.out.Concurrent, func(i, j int) bool {
		a, b := f.out.Concurrent[i], f.out.Concurrent[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.UnitID != b.UnitID {
			return a.UnitID < b.UnitID
		}
		return a.From < b.From
	})
	sort.Slice(f.out.Acting, func(i, j int) bool {
		a, b := f.out.Acting[i], f.out.Acting[j]
		if a.UnitID != b.UnitID {
			return a.UnitID < b.UnitID
		}
		return a.From < b.From
	})
}

// UnitAt 은 기준일의 조직 단위 값이다.
func (r *Result) UnitAt(id int64, d contract.Day) (UnitSpan, bool) {
	for _, s := range r.Units {
		if s.UnitID == id && s.From <= d && (s.To == "" || d < s.To) {
			return s, true
		}
	}
	return UnitSpan{}, false
}

// PersonAt 은 기준일의 사람 값이다.
func (r *Result) PersonAt(id int64, d contract.Day) (PersonSpan, bool) {
	for _, s := range r.People {
		if s.UserID == id && s.From <= d && (s.To == "" || d < s.To) {
			return s, true
		}
	}
	return PersonSpan{}, false
}

// ConcurrentAt 은 기준일의 겸직 조직 번호들이다.
func (r *Result) ConcurrentAt(id int64, d contract.Day) []int64 {
	var out []int64
	for _, s := range r.Concurrent {
		if s.UserID == id && s.From <= d && (s.To == "" || d < s.To) {
			out = append(out, s.UnitID)
		}
	}
	return out
}

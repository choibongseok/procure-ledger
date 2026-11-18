package fake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Org 는 contract.OrgDirectory 의 메모리 구현이다(B20). erp·ai 의 테스트가 조직 서비스(SQLite) 없이 쓴다.
//
// 기준일(asOf)은 무시하고 지금 담아 둔 상태를 그대로 돌려준다. 날짜별로 다른 모습이 필요한 테스트는
// 날짜마다 Org 를 따로 만들거나 값을 바꿔 가며 쓴다. 규칙은 실제 구현과 같다.
//   - 조직장 → 직무대행 → 공석 순으로 Head 를 돌려준다.
//   - 연락처는 contract.MaskContact 로 가린다(MCP 채널은 권한자·본인에게도 가린다).
//   - 조직에 등록되지 않은 사용자는 ErrNotInOrg, 없는 사용자·조직은 ErrNotFound.
//   - 퇴사자는 org:history 권한이 있을 때만 검색에 포함된다.
type Org struct {
	mu     sync.Mutex
	Masker contract.Masker

	units   map[int64]contract.OrgUnit
	heads   map[int64]int64 // 조직 → 조직장 사용자
	acting  map[int64]int64 // 조직 → 직무대행자
	people  map[int64]*orgPerson
	unknown map[int64]bool // 조직에 등록되지 않은 사용자(계정만 있음)
}

type orgPerson struct {
	userID     int64
	name       string
	email      string
	phone      string
	extension  string
	employeeNo string
	rank       string
	active     bool
	onLeave    bool // 휴직 중(B50): 재직(active)은 그대로다
	primary    int64
	concurrent []int64
}

// NewOrg 는 빈 조직을 만든다. Masker 는 fake.Masker 가 기본이다.
func NewOrg() *Org {
	return &Org{
		Masker: Masker{},
		units:  map[int64]contract.OrgUnit{}, heads: map[int64]int64{}, acting: map[int64]int64{},
		people: map[int64]*orgPerson{}, unknown: map[int64]bool{},
	}
}

var _ contract.OrgDirectory = (*Org)(nil)

// AddUnit 은 조직을 넣는다(parent 0 이면 루트).
func (o *Org) AddUnit(id int64, code, name string, kind contract.OrgKind, parent int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.units[id] = contract.OrgUnit{ID: id, Code: code, Name: name, Kind: kind, ParentID: parent, SortOrder: int(id)}
	return o
}

// CloseUnit 은 조직을 폐지 상태로 바꾼다.
func (o *Org) CloseUnit(id int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	u := o.units[id]
	u.Closed = true
	o.units[id] = u
	return o
}

// AddPerson 은 재직자를 넣는다. 연락처 값은 원문이다.
func (o *Org) AddPerson(userID int64, name, rank string, primaryUnit int64, email, phone, ext string) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.people[userID] = &orgPerson{userID: userID, name: name, rank: rank, primary: primaryUnit, email: email, phone: phone,
		extension: ext, active: true, employeeNo: fmt.Sprintf("E%06d", userID)}
	return o
}

// AddConcurrent 는 겸직을 더한다.
func (o *Org) AddConcurrent(userID, unitID int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p := o.people[userID]; p != nil {
		p.concurrent = append(p.concurrent, unitID)
	}
	return o
}

// Leave 는 사람을 퇴사 상태로 만든다. 그 사람이 조직장이던 조직은 공석이 된다.
func (o *Org) Leave(userID int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p := o.people[userID]; p != nil {
		p.active = false
	}
	return o
}

// SetOnLeave 는 재직자를 휴직 상태로 바꾸거나(true) 복직시킨다(false). 재직·소속은 그대로다(B50).
func (o *Org) SetOnLeave(userID int64, on bool) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p := o.people[userID]; p != nil {
		p.onLeave = on
	}
	return o
}

// SetHead 는 조직장을, SetActing 은 직무대행자를 정한다(0 이면 해제).
func (o *Org) SetHead(unitID, userID int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	if userID == 0 {
		delete(o.heads, unitID)
	} else {
		o.heads[unitID] = userID
	}
	return o
}

func (o *Org) SetActing(unitID, userID int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	if userID == 0 {
		delete(o.acting, unitID)
	} else {
		o.acting[unitID] = userID
	}
	return o
}

// AddUnregistered 는 조직에 등록되지 않은 사용자(계정만 있음)를 표시한다.
func (o *Org) AddUnregistered(userID int64) *Org {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.unknown[userID] = true
	return o
}

func (o *Org) unit(id int64) (contract.OrgUnit, error) {
	u, ok := o.units[id]
	if !ok {
		return contract.OrgUnit{}, contract.ErrNotFound
	}
	return u, nil
}

func (o *Org) sortedUnits(ids []int64) []contract.OrgUnit {
	out := make([]contract.OrgUnit, 0, len(ids))
	for _, id := range ids {
		out = append(out, o.units[id])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (o *Org) Unit(_ context.Context, id int64, _ contract.Day) (contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.unit(id)
}

func (o *Org) Children(_ context.Context, id int64, _ contract.Day) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.unit(id); err != nil {
		return nil, err
	}
	var ids []int64
	for _, u := range o.units {
		if u.ParentID == id {
			ids = append(ids, u.ID)
		}
	}
	return o.sortedUnits(ids), nil
}

func (o *Org) ancestors(id int64) ([]contract.OrgUnit, error) {
	var out []contract.OrgUnit
	u, err := o.unit(id)
	if err != nil {
		return nil, err
	}
	for u.ParentID != 0 {
		p, err := o.unit(u.ParentID)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
		u = p
	}
	return out, nil
}

func (o *Org) Path(_ context.Context, id int64, _ contract.Day) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.path(id)
}

func (o *Org) path(id int64) ([]contract.OrgUnit, error) {
	u, err := o.unit(id)
	if err != nil {
		return nil, err
	}
	anc, err := o.ancestors(id)
	if err != nil {
		return nil, err
	}
	out := make([]contract.OrgUnit, 0, len(anc)+1)
	for i := len(anc) - 1; i >= 0; i-- {
		out = append(out, anc[i])
	}
	return append(out, u), nil
}

func (o *Org) Ancestors(_ context.Context, id int64, _ contract.Day) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ancestors(id)
}

func (o *Org) descendantIDs(id int64) []int64 {
	var out []int64
	for _, u := range o.units {
		if u.ParentID == id {
			out = append(out, u.ID)
			out = append(out, o.descendantIDs(u.ID)...)
		}
	}
	return out
}

func (o *Org) Descendants(_ context.Context, id int64, _ contract.Day, includeSelf bool) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.unit(id); err != nil {
		return nil, err
	}
	ids := o.descendantIDs(id)
	if includeSelf {
		ids = append(ids, id)
	}
	return o.sortedUnits(ids), nil
}

func (o *Org) FindUnits(_ context.Context, text string, _ contract.Day, limit int) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	if limit > contract.OrgFindMax {
		limit = contract.OrgFindMax
	}
	text = strings.ToLower(strings.TrimSpace(text))
	var ids []int64
	for _, u := range o.units {
		if text == "" || strings.Contains(strings.ToLower(u.Name), text) || strings.Contains(strings.ToLower(u.Code), text) {
			ids = append(ids, u.ID)
		}
	}
	out := o.sortedUnits(ids)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// headUser 는 재직 중이고 그 조직의 구성원인 조직장만 인정한다.
func (o *Org) head(unitID int64) contract.OrgHead {
	if uid, ok := o.heads[unitID]; ok {
		if p := o.people[uid]; p != nil && p.active && o.isMember(p, unitID) {
			return contract.OrgHead{UserID: uid}
		}
	}
	if uid, ok := o.acting[unitID]; ok {
		if p := o.people[uid]; p != nil && p.active {
			return contract.OrgHead{UserID: uid, Acting: true}
		}
	}
	return contract.OrgHead{Vacant: true}
}

func (o *Org) isMember(p *orgPerson, unitID int64) bool {
	if p.primary == unitID {
		return true
	}
	for _, c := range p.concurrent {
		if c == unitID {
			return true
		}
	}
	return false
}

func (o *Org) Head(_ context.Context, unitID int64, _ contract.Day) (contract.OrgHead, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.unit(unitID); err != nil {
		return contract.OrgHead{}, err
	}
	return o.head(unitID), nil
}

func (o *Org) Heads(_ context.Context, unitIDs []int64, _ contract.Day) (map[int64]contract.OrgHead, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[int64]contract.OrgHead{}
	for _, id := range unitIDs {
		if _, ok := o.units[id]; ok {
			out[id] = o.head(id)
		}
	}
	return out, nil
}

func (o *Org) Persons(_ context.Context, userIDs []int64, _ contract.Day) (map[int64]contract.OrgPerson, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[int64]contract.OrgPerson{}
	for _, id := range userIDs {
		if p, err := o.person(id); err == nil {
			out[id] = p
		}
	}
	return out, nil
}

func (o *Org) headedBy(userID int64) []int64 {
	var ids []int64
	for unitID := range o.units {
		if h := o.head(unitID); !h.Vacant && !h.Acting && h.UserID == userID {
			ids = append(ids, unitID)
		}
	}
	return ids
}

func (o *Org) UnitsHeadedBy(_ context.Context, userID int64, _ contract.Day) ([]contract.OrgUnit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sortedUnits(o.headedBy(userID)), nil
}

func (o *Org) HeadedScope(_ context.Context, userID int64, _ contract.Day) ([]int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	set := map[int64]bool{}
	for unitID := range o.units {
		if h := o.head(unitID); !h.Vacant && h.UserID == userID {
			set[unitID] = true
			for _, d := range o.descendantIDs(unitID) {
				set[d] = true
			}
		}
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (o *Org) place(unitID int64, userID int64) (contract.OrgPlace, error) {
	u, err := o.unit(unitID)
	if err != nil {
		return contract.OrgPlace{}, err
	}
	path, err := o.path(unitID)
	if err != nil {
		return contract.OrgPlace{}, err
	}
	h := o.head(unitID)
	return contract.OrgPlace{Unit: u, Path: path, IsHead: !h.Vacant && !h.Acting && h.UserID == userID,
		Acting: h.Acting && h.UserID == userID}, nil
}

func (o *Org) person(userID int64) (contract.OrgPerson, error) {
	p := o.people[userID]
	if p == nil {
		if o.unknown[userID] {
			return contract.OrgPerson{}, contract.ErrNotInOrg
		}
		return contract.OrgPerson{}, contract.ErrNotFound
	}
	prim, err := o.place(p.primary, userID)
	if err != nil {
		return contract.OrgPerson{}, err
	}
	out := contract.OrgPerson{UserID: userID, Active: p.active, OnLeave: p.active && p.onLeave, EmployeeNo: p.employeeNo, Rank: p.rank, Primary: prim}
	for _, c := range p.concurrent {
		pl, err := o.place(c, userID)
		if err != nil {
			return contract.OrgPerson{}, err
		}
		out.Concurrent = append(out.Concurrent, pl)
	}
	for unitID, uid := range o.acting {
		if uid == userID {
			pl, err := o.place(unitID, userID)
			if err != nil {
				return contract.OrgPerson{}, err
			}
			out.Acting = append(out.Acting, pl)
		}
	}
	sort.Slice(out.Acting, func(i, j int) bool { return out.Acting[i].Unit.ID < out.Acting[j].Unit.ID })
	return out, nil
}

func (o *Org) Person(_ context.Context, userID int64, _ contract.Day) (contract.OrgPerson, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.person(userID)
}

func (o *Org) memberIDs(unitID int64, withDesc bool) map[int64]bool {
	units := map[int64]bool{unitID: true}
	if withDesc {
		for _, d := range o.descendantIDs(unitID) {
			units[d] = true
		}
	}
	out := map[int64]bool{}
	for _, p := range o.people {
		if !p.active {
			continue
		}
		if units[p.primary] {
			out[p.userID] = true
		}
		for _, c := range p.concurrent {
			if units[c] {
				out[p.userID] = true
			}
		}
	}
	return out
}

func (o *Org) MembersOf(_ context.Context, unitID int64, _ contract.Day, withDescendants bool) ([]int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.unit(unitID); err != nil {
		return nil, err
	}
	set := o.memberIDs(unitID, withDescendants)
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (o *Org) CountMembers(ctx context.Context, unitID int64, asOf contract.Day, withDescendants bool) (int, error) {
	ids, err := o.MembersOf(ctx, unitID, asOf, withDescendants)
	return len(ids), err
}

func (o *Org) card(p contract.Principal, userID int64) (contract.PersonCard, error) {
	op, err := o.person(userID)
	if err != nil {
		return contract.PersonCard{}, err
	}
	if !op.Active && !p.Can(contract.PermOrgHistory) {
		return contract.PersonCard{}, contract.ErrNotFound // 퇴사자 카드는 org:history 권한자만(존재 숨김)
	}
	pp := o.people[userID]
	return contract.PersonCard{
		OrgPerson: op, Name: pp.name, Extension: pp.extension,
		Email: contract.MaskContact(o.Masker, p, userID, contract.FieldEmail, pp.email),
		Phone: contract.MaskContact(o.Masker, p, userID, contract.FieldPhone, pp.phone),
	}, nil
}

func (o *Org) Card(_ context.Context, p contract.Principal, userID int64, _ contract.Day) (contract.PersonCard, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.card(p, userID)
}

func (o *Org) SearchPeople(_ context.Context, p contract.Principal, q contract.PeopleQuery) ([]contract.PersonCard, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = contract.OrgSearchDefault
	}
	if limit > contract.OrgSearchMax {
		limit = contract.OrgSearchMax
	}
	includeLeft := q.IncludeLeft && p.Can(contract.PermOrgHistory)
	var scope map[int64]bool
	if q.UnitID != 0 {
		if _, err := o.unit(q.UnitID); err != nil {
			return nil, err
		}
		scope = map[int64]bool{q.UnitID: true}
		for _, d := range o.descendantIDs(q.UnitID) {
			scope[d] = true
		}
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	var ids []int64
	for id, pp := range o.people {
		if !pp.active && !includeLeft {
			continue
		}
		op, err := o.person(id)
		if err != nil {
			continue
		}
		if scope != nil {
			in := scope[pp.primary]
			for _, c := range pp.concurrent {
				in = in || scope[c]
			}
			if !in {
				continue
			}
		}
		if q.HeadOnly && !isHeadAnywhere(op) {
			continue
		}
		if text != "" && !o.textMatch(pp, op, text) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]contract.PersonCard, 0, len(ids))
	for _, id := range ids {
		c, err := o.card(p, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func isHeadAnywhere(op contract.OrgPerson) bool {
	if op.Primary.IsHead {
		return true
	}
	for _, c := range op.Concurrent {
		if c.IsHead {
			return true
		}
	}
	return false
}

func (o *Org) textMatch(pp *orgPerson, op contract.OrgPerson, text string) bool {
	hay := []string{pp.name, op.Primary.Unit.Name}
	for _, c := range op.Concurrent {
		hay = append(hay, c.Unit.Name)
	}
	for _, pl := range append([]contract.OrgPlace{op.Primary}, op.Concurrent...) {
		if pl.IsHead {
			hay = append(hay, contract.HeadTitle(pl.Unit.Kind))
		}
	}
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), text) {
			return true
		}
	}
	return false
}

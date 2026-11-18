package org

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// EmploymentDenied 는 사용자가 "오늘 기준 조직 상태상 재직이 아니어서" 인증을 거부해야 하는지다(명세 3.7).
// 조직에 등록되지 않은 사용자(구간 행이 없음)는 거부하지 않는다. 등록된 사용자는 오늘을 덮는 재직 구간이 없으면
// (퇴사했거나 아직 입사 전이면) 거부한다. 투영 갱신(users.active)과 무관하게 인증 경로가 직접 부른다.
func EmploymentDenied(ctx context.Context, q Querier, userID int64, today contract.Day) (bool, error) {
	var denied bool // PG 는 불리언, SQLite 는 0/1 이라 bool 로 읽는다
	err := q.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM org_person_spans WHERE user_id = ?1)
		AND NOT EXISTS (SELECT 1 FROM org_person_spans WHERE user_id = ?1 AND left_flag = 0 AND valid_from <= ?2 AND (valid_to IS NULL OR ?2 < valid_to))`,
		userID, string(today)).Scan(&denied)
	return denied, err
}

// LeftUsers 는 주어진 사용자 중 오늘 기준 퇴사 상태인 사람의 집합이다(과거 기록의 이름 뒤에 "(퇴사)" 를 붙이는 데 쓴다, 명세 3.7).
// 조직에 등록되지 않은 사용자와 입사 전인 사용자는 포함하지 않는다.
func (s *Service) LeftUsers(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	today := string(s.Today())
	for _, id := range ids {
		var left bool // EXISTS 는 PG 에서 불리언이라 bool 로 읽는다(SQLite 의 0/1 도 bool 로 읽힌다)
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM org_person_spans WHERE user_id = ?1 AND left_flag = 1 AND valid_from <= ?2 AND (valid_to IS NULL OR ?2 < valid_to))`,
			id, today).Scan(&left); err != nil {
			return nil, err
		}
		if left {
			out[id] = true
		}
	}
	return out, nil
}

var _ contract.OrgDirectory = (*Service)(nil)

func (s *Service) dayOrToday(d contract.Day) (contract.Day, error) {
	if d == "" {
		return s.Today(), nil
	}
	if _, err := ParseDay(string(d)); err != nil {
		return "", verr(err.Error())
	}
	return d, nil
}

func toUnit(u UnitSpan) contract.OrgUnit {
	return contract.OrgUnit{ID: u.UnitID, Code: u.Code, Name: u.Name, Kind: u.Kind, ParentID: u.ParentID, SortOrder: u.Sort, Closed: u.Closed}
}

func toUnits(us []UnitSpan) []contract.OrgUnit {
	out := make([]contract.OrgUnit, len(us))
	for i, u := range us {
		out[i] = toUnit(u)
	}
	return out
}

func (s *Service) snapAt(ctx context.Context, asOf contract.Day) (*Snap, error) {
	d, err := s.dayOrToday(asOf)
	if err != nil {
		return nil, err
	}
	return s.snap(ctx, d)
}

func (s *Service) Unit(ctx context.Context, id int64, asOf contract.Day) (contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return contract.OrgUnit{}, err
	}
	u, ok := sn.Units[id]
	if !ok {
		return contract.OrgUnit{}, contract.ErrNotFound
	}
	return toUnit(u), nil
}

func (s *Service) Children(ctx context.Context, id int64, asOf contract.Day) ([]contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	if _, ok := sn.Units[id]; !ok {
		return nil, contract.ErrNotFound
	}
	out := make([]contract.OrgUnit, 0, len(sn.Children[id]))
	for _, c := range sn.Children[id] {
		out = append(out, toUnit(sn.Units[c]))
	}
	return out, nil
}

func (s *Service) Path(ctx context.Context, id int64, asOf contract.Day) ([]contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	p := sn.Path(id)
	if p == nil {
		return nil, contract.ErrNotFound
	}
	return toUnits(p), nil
}

func (s *Service) Ancestors(ctx context.Context, id int64, asOf contract.Day) ([]contract.OrgUnit, error) {
	path, err := s.Path(ctx, id, asOf)
	if err != nil {
		return nil, err
	}
	out := make([]contract.OrgUnit, 0, len(path)-1)
	for i := len(path) - 2; i >= 0; i-- {
		out = append(out, path[i])
	}
	return out, nil
}

func (sn *Snap) subtree(id int64, includeSelf bool) []int64 {
	var out []int64
	if includeSelf {
		out = append(out, id)
	}
	for _, c := range sn.Children[id] {
		out = append(out, sn.subtree(c, true)...)
	}
	return out
}

func (s *Service) Descendants(ctx context.Context, id int64, asOf contract.Day, includeSelf bool) ([]contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	if _, ok := sn.Units[id]; !ok {
		return nil, contract.ErrNotFound
	}
	ids := sn.subtree(id, includeSelf)
	out := make([]contract.OrgUnit, 0, len(ids))
	for _, x := range ids {
		out = append(out, toUnit(sn.Units[x]))
	}
	return out, nil
}

func (s *Service) FindUnits(ctx context.Context, text string, asOf contract.Day, limit int) ([]contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > contract.OrgFindMax {
		limit = contract.OrgFindMax
	}
	text = strings.ToLower(strings.TrimSpace(text))
	var ids []int64
	for id, u := range sn.Units {
		if text == "" || strings.Contains(strings.ToLower(u.Name), text) || strings.Contains(strings.ToLower(u.Code), text) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := sn.Units[ids[i]], sn.Units[ids[j]]
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		return a.UnitID < b.UnitID
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]contract.OrgUnit, 0, len(ids))
	for _, id := range ids {
		out = append(out, toUnit(sn.Units[id]))
	}
	return out, nil
}

func (s *Service) Head(ctx context.Context, unitID int64, asOf contract.Day) (contract.OrgHead, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return contract.OrgHead{}, err
	}
	u, ok := sn.Units[unitID]
	if !ok {
		return contract.OrgHead{}, contract.ErrNotFound
	}
	if u.HeadUser == 0 {
		return contract.OrgHead{Vacant: true}, nil
	}
	return contract.OrgHead{UserID: u.HeadUser, Acting: u.HeadActing}, nil
}

func (s *Service) Heads(ctx context.Context, unitIDs []int64, asOf contract.Day) (map[int64]contract.OrgHead, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]contract.OrgHead, len(unitIDs))
	for _, id := range unitIDs {
		u, ok := sn.Units[id]
		if !ok {
			continue
		}
		if u.HeadUser == 0 {
			out[id] = contract.OrgHead{Vacant: true}
		} else {
			out[id] = contract.OrgHead{UserID: u.HeadUser, Acting: u.HeadActing}
		}
	}
	return out, nil
}

func (s *Service) UnitsHeadedBy(ctx context.Context, userID int64, asOf contract.Day) ([]contract.OrgUnit, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	var out []contract.OrgUnit
	for _, id := range sn.HeadedUnits(userID) {
		out = append(out, toUnit(sn.Units[id]))
	}
	return out, nil
}

// HeadedScope 는 사용자가 조직장(직무대행 포함)인 조직과 그 하위 조직 번호다. 폐지된 하위 조직도 포함한다.
func (s *Service) HeadedScope(ctx context.Context, userID int64, asOf contract.Day) ([]int64, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	set := map[int64]bool{}
	for id, u := range sn.Units {
		if u.HeadUser == userID {
			for _, x := range sn.subtree(id, true) {
				set[x] = true
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

func (sn *Snap) place(unit, user int64, acting bool) contract.OrgPlace {
	u := sn.Units[unit]
	return contract.OrgPlace{Unit: toUnit(u), Path: toUnits(sn.Path(unit)), IsHead: !acting && u.HeadUser == user && !u.HeadActing, Acting: acting}
}

func (s *Service) ranks(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM org_ranks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		m[id] = n
	}
	return m, rows.Err()
}

type contactRow struct {
	name, email, phone, ext, empNo string
}

func (s *Service) contacts(ctx context.Context, ids []int64) (map[int64]contactRow, error) {
	m := map[int64]contactRow{}
	var rows *sql.Rows
	var err error
	if ids == nil {
		rows, err = s.db.QueryContext(ctx, `SELECT u.id, u.name, u.email, o.phone, o.extension, o.employee_no FROM users u JOIN org_people o ON o.user_id = u.id`)
	} else {
		if len(ids) == 0 {
			return m, nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err = s.db.QueryContext(ctx, `SELECT u.id, u.name, u.email, o.phone, o.extension, o.employee_no FROM users u JOIN org_people o ON o.user_id = u.id WHERE u.id IN (`+marks+`)`, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c contactRow
		if err := rows.Scan(&id, &c.name, &c.email, &c.phone, &c.ext, &c.empNo); err != nil {
			return nil, err
		}
		m[id] = c
	}
	return m, rows.Err()
}

func (s *Service) person(ctx context.Context, sn *Snap, userID int64, ranks map[int64]string, empNo string) (contract.OrgPerson, error) {
	p, ok := sn.People[userID]
	if !ok {
		res, _, err := s.current(ctx, s.db)
		if err != nil {
			return contract.OrgPerson{}, err
		}
		if hasSpans(res)[userID] {
			return contract.OrgPerson{}, contract.ErrNotFound // 등록은 됐지만 그 날짜에는 소속이 없다(입사 전)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&n); err != nil {
			return contract.OrgPerson{}, err
		}
		if n == 0 {
			return contract.OrgPerson{}, contract.ErrNotFound
		}
		return contract.OrgPerson{}, contract.ErrNotInOrg
	}
	out := contract.OrgPerson{UserID: userID, Active: !p.Left, OnLeave: !p.Left && p.Absence != "", AbsenceKind: p.Absence, EmployeeNo: empNo, Rank: ranks[p.Rank], Primary: sn.place(p.Primary, userID, false)}
	for _, c := range sn.Conc[userID] {
		out.Concurrent = append(out.Concurrent, sn.place(c, userID, false))
	}
	for _, a := range sn.Acting[userID] {
		out.Acting = append(out.Acting, sn.place(a, userID, true))
	}
	return out, nil
}

func (s *Service) Person(ctx context.Context, userID int64, asOf contract.Day) (contract.OrgPerson, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return contract.OrgPerson{}, err
	}
	ranks, err := s.ranks(ctx)
	if err != nil {
		return contract.OrgPerson{}, err
	}
	cs, err := s.contacts(ctx, []int64{userID})
	if err != nil {
		return contract.OrgPerson{}, err
	}
	return s.person(ctx, sn, userID, ranks, cs[userID].empNo)
}

func (s *Service) Persons(ctx context.Context, userIDs []int64, asOf contract.Day) (map[int64]contract.OrgPerson, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	ranks, err := s.ranks(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.contacts(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]contract.OrgPerson, len(userIDs))
	for _, id := range userIDs {
		if _, ok := sn.People[id]; !ok {
			continue
		}
		p, err := s.person(ctx, sn, id, ranks, cs[id].empNo)
		if err != nil {
			continue
		}
		out[id] = p
	}
	return out, nil
}

func (sn *Snap) members(unit int64, withDesc bool) []int64 {
	units := map[int64]bool{unit: true}
	if withDesc {
		for _, x := range sn.subtree(unit, true) {
			units[x] = true
		}
	}
	set := map[int64]bool{}
	for id, p := range sn.People {
		if p.Left {
			continue
		}
		if units[p.Primary] {
			set[id] = true
			continue
		}
		for _, c := range sn.Conc[id] {
			if units[c] {
				set[id] = true
				break
			}
		}
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Service) MembersOf(ctx context.Context, unitID int64, asOf contract.Day, withDescendants bool) ([]int64, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	if _, ok := sn.Units[unitID]; !ok {
		return nil, contract.ErrNotFound
	}
	return sn.members(unitID, withDescendants), nil
}

func (s *Service) CountMembers(ctx context.Context, unitID int64, asOf contract.Day, withDescendants bool) (int, error) {
	ids, err := s.MembersOf(ctx, unitID, asOf, withDescendants)
	return len(ids), err
}

func (s *Service) cardFor(p contract.Principal, userID int64, op contract.OrgPerson, c contactRow) contract.PersonCard {
	return contract.PersonCard{
		OrgPerson: op, Name: c.name, Extension: c.ext,
		Email: contract.MaskContact(s.masker, p, userID, contract.FieldEmail, c.email),
		Phone: contract.MaskContact(s.masker, p, userID, contract.FieldPhone, c.phone),
	}
}

func (s *Service) Card(ctx context.Context, p contract.Principal, userID int64, asOf contract.Day) (contract.PersonCard, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return contract.PersonCard{}, err
	}
	ranks, err := s.ranks(ctx)
	if err != nil {
		return contract.PersonCard{}, err
	}
	cs, err := s.contacts(ctx, []int64{userID})
	if err != nil {
		return contract.PersonCard{}, err
	}
	c, found := cs[userID]
	op, err := s.person(ctx, sn, userID, ranks, c.empNo)
	if err != nil {
		return contract.PersonCard{}, err
	}
	if !found {
		return contract.PersonCard{}, contract.ErrNotFound
	}
	// 기준일에 퇴사 상태인 사람의 카드는 org:history 권한자(admin·auditor)만 본다. 그 밖에는 존재를 숨긴다(404).
	// 기준일이 과거이고 그날은 재직이었던 사람은 퇴사자가 아니므로 누구나 본다.
	if !op.Active && !p.Can(contract.PermOrgHistory) {
		return contract.PersonCard{}, contract.ErrNotFound
	}
	card := s.cardFor(p, userID, op, c)
	// 다른 사람의 연락처 원문이 실리면 응답 전에 기록한다(본인·가린 응답은 제외). 기록하지 못하면 카드를 내주지 않는다.
	if err := s.noteCards(ctx, p, privacy.ScreenPersonCard, false, "", []contract.PersonCard{card}, cs); err != nil {
		return contract.PersonCard{}, err
	}
	return card, nil
}

func (s *Service) SearchPeople(ctx context.Context, p contract.Principal, q contract.PeopleQuery) ([]contract.PersonCard, error) {
	cards, _, err := s.searchPeople(ctx, p, q, contract.OrgSearchMax, p, privacy.ScreenPeopleList, false)
	return cards, err
}

// ListPeople 는 내보내기(B28)용 구성원 목록이다. 화면 검색(SearchPeople)과 같은 조건·열람 규칙을 쓰되 개수 상한을 max 로 올리고,
// 조건에 맞는 전체 건수(상한으로 자르기 전)를 함께 돌려준다. maskContacts 가 참이면 mask:bypass 권한자·본인에게도 이메일·휴대전화를 가린다
// (파일은 화면보다 오래 남으므로 원문은 명시 선택일 때만 나간다).
func (s *Service) ListPeople(ctx context.Context, p contract.Principal, q contract.PeopleQuery, max int, maskContacts bool) ([]contract.PersonCard, int, error) {
	viewer := p
	if maskContacts {
		viewer = contract.Principal{Role: contract.RoleStaff, Channel: p.Channel} // 번호 0·권한 없음: 누구의 연락처도 원문으로 보지 못한다
	}
	if max <= 0 {
		max = contract.OrgSearchMax
	}
	// 원문 포함 내보내기(maskContacts 가 거짓)는 파일 내보내기로 기록한다. 가린 내보내기는 원문이 없어 기록이 없다.
	return s.searchPeople(ctx, p, q, max, viewer, privacy.ScreenPeopleExport, true)
}

// searchPeople 은 조건에 맞는 사람을 이름순으로 limit 개(상한 cap)까지 카드로 만든다. 두 번째 값은 상한으로 자르기 전의 전체 건수다.
// p 는 퇴사자 포함 같은 열람 권한 판정에, viewer 는 연락처 마스킹 판정에 쓴다.
func (s *Service) searchPeople(ctx context.Context, p contract.Principal, q contract.PeopleQuery, cap int, viewer contract.Principal, screen string, export bool) ([]contract.PersonCard, int, error) {
	asOf := q.AsOf
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = contract.OrgSearchDefault
	}
	if limit > cap {
		limit = cap
	}
	includeLeft := q.IncludeLeft && p.Can(contract.PermOrgHistory)
	var scope map[int64]bool
	if q.UnitID != 0 {
		if _, ok := sn.Units[q.UnitID]; !ok {
			return nil, 0, contract.ErrNotFound
		}
		scope = map[int64]bool{}
		for _, x := range sn.subtree(q.UnitID, true) {
			scope[x] = true
		}
	}
	ranks, err := s.ranks(ctx)
	if err != nil {
		return nil, 0, err
	}
	cs, err := s.contacts(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	type cand struct {
		id   int64
		name string
	}
	var cands []cand
	for id, ps := range sn.People {
		if ps.Left && !includeLeft {
			continue
		}
		c, ok := cs[id]
		if !ok {
			continue
		}
		if scope != nil {
			in := scope[ps.Primary]
			for _, cu := range sn.Conc[id] {
				in = in || scope[cu]
			}
			if !in {
				continue
			}
		}
		if q.HeadOnly {
			isHead := false
			for _, hu := range sn.HeadedUnits(id) {
				isHead = isHead || hu != 0
			}
			if !isHead {
				continue
			}
		}
		if text != "" && !matchPerson(sn, id, ps, c.name, text) {
			continue
		}
		cands = append(cands, cand{id, c.name})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].name != cands[j].name {
			return cands[i].name < cands[j].name
		}
		return cands[i].id < cands[j].id
	})
	total := len(cands)
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]contract.PersonCard, 0, len(cands))
	for _, c := range cands {
		op, err := s.person(ctx, sn, c.id, ranks, cs[c.id].empNo)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s.cardFor(viewer, c.id, op, cs[c.id]))
	}
	// 연락처 원문이 실린 사람들을 응답 한 번에 한 번 기록한다(열람자는 p). 기록하지 못하면 목록을 내주지 않는다.
	dataset := ""
	if export {
		dataset = "org_people"
	}
	if err := s.noteCards(ctx, p, screen, export, dataset, out, cs); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// matchPerson 은 이름·소속 조직명·직책(조직장일 때 팀장 등)에 부분 일치하는지 본다.
func matchPerson(sn *Snap, id int64, ps PersonSpan, name, text string) bool {
	hay := []string{name, sn.Units[ps.Primary].Name}
	for _, c := range sn.Conc[id] {
		hay = append(hay, sn.Units[c].Name)
	}
	for _, hu := range sn.HeadedUnits(id) {
		hay = append(hay, contract.HeadTitle(sn.Units[hu].Kind))
	}
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), text) {
			return true
		}
	}
	return false
}

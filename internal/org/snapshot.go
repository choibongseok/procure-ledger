package org

import (
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Snap 은 구간 계산 결과(Result)에서 한 기준일의 모습만 뽑은 색인이다. 읽기 전용이다.
type Snap struct {
	Day      contract.Day
	Units    map[int64]UnitSpan
	Children map[int64][]int64 // 정렬 순서(정렬값, 번호)
	People   map[int64]PersonSpan
	Conc     map[int64][]int64 // 사람 → 겸직 조직(번호 순)
	Acting   map[int64][]int64 // 사람 → 직무대행 중인 조직(번호 순)
	headOf   map[int64][]int64 // 사용자 → 조직장인 조직(직무대행 제외)
}

func covers(from, to, d contract.Day) bool { return from <= d && (to == "" || d < to) }

// NewSnap 은 기준일 d 의 색인을 만든다.
func NewSnap(r *Result, d contract.Day) *Snap {
	s := &Snap{Day: d, Units: map[int64]UnitSpan{}, Children: map[int64][]int64{}, People: map[int64]PersonSpan{},
		Conc: map[int64][]int64{}, Acting: map[int64][]int64{}, headOf: map[int64][]int64{}}
	for _, u := range r.Units {
		if covers(u.From, u.To, d) {
			s.Units[u.UnitID] = u
		}
	}
	for id, u := range s.Units {
		if u.ParentID != 0 {
			s.Children[u.ParentID] = append(s.Children[u.ParentID], id)
		}
		if u.HeadUser != 0 && !u.HeadActing {
			s.headOf[u.HeadUser] = append(s.headOf[u.HeadUser], id)
		}
	}
	for p, kids := range s.Children {
		sort.Slice(kids, func(i, j int) bool {
			a, b := s.Units[kids[i]], s.Units[kids[j]]
			if a.Sort != b.Sort {
				return a.Sort < b.Sort
			}
			return a.UnitID < b.UnitID
		})
		s.Children[p] = kids
	}
	for _, p := range r.People {
		if covers(p.From, p.To, d) {
			s.People[p.UserID] = p
		}
	}
	for _, c := range r.Concurrent {
		if covers(c.From, c.To, d) {
			s.Conc[c.UserID] = append(s.Conc[c.UserID], c.UnitID)
		}
	}
	for _, a := range r.Acting {
		if covers(a.From, a.To, d) {
			s.Acting[a.UserID] = append(s.Acting[a.UserID], a.UnitID)
		}
	}
	for _, m := range []map[int64][]int64{s.Conc, s.Acting, s.headOf} {
		for k, v := range m {
			sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
			m[k] = v
		}
	}
	return s
}

// Employed 는 기준일에 재직 중인지다(조직에 등록됐고 퇴사 상태가 아님).
func (s *Snap) Employed(user int64) bool {
	p, ok := s.People[user]
	return ok && !p.Left
}

// Path 는 회사부터 해당 조직까지의 경로다. 없는 조직이면 nil.
func (s *Snap) Path(id int64) []UnitSpan {
	var rev []UnitSpan
	for cur := id; cur != 0; {
		u, ok := s.Units[cur]
		if !ok {
			return nil
		}
		rev = append(rev, u)
		cur = u.ParentID
		if len(rev) > 1000 { // 순환 방지(Fold 가 막지만 방어)
			return nil
		}
	}
	out := make([]UnitSpan, len(rev))
	for i := range rev {
		out[len(rev)-1-i] = rev[i]
	}
	return out
}

// DeptOf 는 호환 투영의 부서 번호다: 조직에서 위로 올라가며 처음 만나는 부서(자기 포함), 없으면 본부, 없으면 회사.
func (s *Snap) DeptOf(unit int64) int64 {
	path := s.Path(unit)
	for _, want := range []contract.OrgKind{contract.OrgDepartment, contract.OrgDivision, contract.OrgCompany} {
		for i := len(path) - 1; i >= 0; i-- {
			if path[i].Kind == want {
				return path[i].UnitID
			}
		}
	}
	return unit
}

// PositionOf 는 호환 투영의 직책이다: 부서·본부·회사 조직장이면 dept_head, 팀 조직장이면 team_lead, 아니면 "".
func (s *Snap) PositionOf(user int64) string {
	pos := ""
	for _, id := range s.headOf[user] {
		switch s.Units[id].Kind {
		case contract.OrgDepartment, contract.OrgDivision, contract.OrgCompany:
			return "dept_head"
		case contract.OrgTeam:
			pos = "team_lead"
		}
	}
	return pos
}

// HeadedUnits 는 사용자가 조직장인 조직(직무대행 제외)이다.
func (s *Snap) HeadedUnits(user int64) []int64 { return s.headOf[user] }

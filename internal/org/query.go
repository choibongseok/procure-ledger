package org

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// TreeNode 는 조직 트리의 한 노드다(조직도 화면용). Direct 는 그 조직 구성원(주소속·겸직) 수, Total 은 하위 조직까지 합친 수다.
type TreeNode struct {
	ID         int64
	Code       string
	Name       string
	Kind       contract.OrgKind
	Closed     bool
	HeadUserID int64
	HeadName   string
	HeadActing bool
	Direct     int
	Total      int
	Children   []*TreeNode
}

// Tree 는 기준일의 조직 트리(회사가 루트)다. 조직이 하나도 없으면 nil 이다.
func (s *Service) Tree(ctx context.Context, asOf contract.Day) (*TreeNode, error) {
	sn, err := s.snapAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	// 회사(법인의 루트 조직)가 여럿이면 기본은 번호가 가장 작은 회사의 트리다(기본 법인의 루트). 현재 법인이 정해져 있으면 그 법인의 루트다(아래).
	var rootID int64
	for id, u := range sn.Units {
		if u.Kind == contract.OrgCompany && (rootID == 0 || id < rootID) {
			rootID = id
		}
	}
	// 현재 법인이 하나로 정해져 있으면(B44 법인 선택기) 그 법인에 연결된 루트 조직의 트리다. 연결이 없거나 "전체" 모드면 위의 기본 규칙을 그대로 쓴다(법인이 하나뿐이면 동작이 같다).
	if a, ok := contract.ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		var root sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT root_unit_id FROM entities WHERE id = ? AND active = 1`, a.ID).Scan(&root); err == nil && root.Valid {
			if u, found := sn.Units[root.Int64]; found && u.Kind == contract.OrgCompany {
				rootID = root.Int64
			}
		}
	}
	if rootID == 0 {
		return nil, nil
	}
	direct, total := map[int64]int{}, map[int64]int{}
	for uid, p := range sn.People {
		if p.Left {
			continue
		}
		units := append([]int64{p.Primary}, sn.Conc[uid]...)
		visited := map[int64]bool{}
		for _, u := range units {
			direct[u]++
			for cur := u; cur != 0 && !visited[cur]; {
				visited[cur] = true
				total[cur]++
				cur = sn.Units[cur].ParentID
			}
		}
	}
	names, err := s.namesOf(ctx, sn)
	if err != nil {
		return nil, err
	}
	var build func(id int64) *TreeNode
	build = func(id int64) *TreeNode {
		u := sn.Units[id]
		n := &TreeNode{ID: id, Code: u.Code, Name: u.Name, Kind: u.Kind, Closed: u.Closed, HeadUserID: u.HeadUser, HeadName: names[u.HeadUser],
			HeadActing: u.HeadActing, Direct: direct[id], Total: total[id]}
		for _, c := range sn.Children[id] {
			n.Children = append(n.Children, build(c))
		}
		return n
	}
	return build(rootID), nil
}

// namesOf 는 기준일 조직장 이름을 한 번에 읽는다.
func (s *Service) namesOf(ctx context.Context, sn *Snap) (map[int64]string, error) {
	want := map[int64]bool{}
	for _, u := range sn.Units {
		if u.HeadUser != 0 {
			want[u.HeadUser] = true
		}
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	cs, err := s.contacts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for id, c := range cs {
		out[id] = c.name
	}
	return out, nil
}

// EventView 는 발령·조직 변경 목록의 한 줄이다.
type EventView struct {
	ID          int64
	Kind        string
	KindLabel   string
	Subject     string
	SubjectID   int64
	SubjectName string
	Effective   contract.Day
	Summary     string
	Reason      string
	CreatedBy   string
	CreatedAt   string
	Status      string // scheduled(예약) | applied(적용됨) | cancelled(취소됨)
	Corrects    int64
	// HasAbsenceReason 은 휴직 발령에 사유가 적혀 있다는 표시다(원문은 싣지 않는다, AbsenceReason 조회로만).
	HasAbsenceReason bool
}

// EventFilter 는 발령 목록 조건이다. Status 가 비면 전체, Q 는 대상 이름·요약에 부분 일치한다.
type EventFilter struct {
	Status string
	Q      string
	UserID int64        // 이 사람과 관련된 이벤트만(발령 이력)
	UnitID int64        // 이 조직을 대상으로 한 이벤트(조직 변경 이력)만
	Kind   string       // 비면 전체. 발령 종류(transfer 등)
	From   contract.Day // 효력일 범위(양끝 포함, 비면 제한 없음)
	To     contract.Day
	Offset int // 앞에서 건너뛸 건수(페이지 나누기)
	Limit  int // 한 번에 돌려줄 최대 건수(기본·상한 500)
}

// EventPage 는 한 쪽의 발령 목록과 조건에 맞는 전체 건수다.
type EventPage struct {
	Items []EventView
	Total int
}

func (s *Service) unitNameAt(res *Result, id int64) string {
	for i := len(res.Units) - 1; i >= 0; i-- {
		if res.Units[i].UnitID == id {
			return res.Units[i].Name
		}
	}
	return fmt.Sprintf("조직 #%d", id)
}

func (s *Service) rankNames(ctx context.Context) map[int64]string {
	m, err := s.ranks(ctx)
	if err != nil {
		return map[int64]string{}
	}
	return m
}

// ListEvents 는 이벤트 로그를 최신순으로 사람이 읽는 형태로 돌려준다(취소 이벤트는 대상 이벤트의 상태로 합쳐 보인다).
func (s *Service) ListEvents(ctx context.Context, f EventFilter) ([]EventView, error) {
	pg, err := s.ListEventsPage(ctx, f)
	return pg.Items, err
}

// ListEventsPage 는 ListEvents 와 같되 조건에 맞는 전체 건수도 함께 돌려준다(Offset·Limit 으로 쪽을 나눈다).
func (s *Service) ListEventsPage(ctx context.Context, f EventFilter) (EventPage, error) {
	evs, err := LoadEvents(ctx, s.db)
	if err != nil {
		return EventPage{}, err
	}
	res, _, err := s.current(ctx, s.db)
	if err != nil {
		return EventPage{}, err
	}
	today := s.Today()
	cancelled := map[int64]bool{}
	for _, e := range evs {
		if e.Kind == KindCancel {
			cancelled[e.Cancels] = true
		}
	}
	ranks := s.rankNames(ctx)
	ids := map[int64]bool{}
	for _, e := range evs {
		if e.Subject == SubjectPerson {
			ids[e.SubjectID] = true
		}
		ids[e.P.User] = true
		ids[e.CreatedBy] = true
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		if id != 0 {
			list = append(list, id)
		}
	}
	names := map[int64]string{}
	if len(list) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
		args := make([]any, len(list))
		for i, id := range list {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM users WHERE id IN (`+marks+`)`, args...)
		if err != nil {
			return EventPage{}, err
		}
		for rows.Next() {
			var id int64
			var n string
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return EventPage{}, err
			}
			names[id] = n
		}
		rows.Close()
	}
	nameOf := func(id int64) string {
		if id == 0 {
			return ""
		}
		if n, ok := names[id]; ok {
			return n
		}
		return fmt.Sprintf("사용자 #%d", id)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := strings.ToLower(strings.TrimSpace(f.Q))
	var out []EventView
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Kind == KindCancel {
			continue
		}
		if f.UnitID != 0 && !(e.Subject == SubjectUnit && e.SubjectID == f.UnitID) {
			continue
		}
		if f.Kind != "" && f.Kind != e.Kind {
			continue
		}
		if (f.From != "" && e.Effective < f.From) || (f.To != "" && e.Effective > f.To) {
			continue
		}
		if f.UserID != 0 && !(e.Subject == SubjectPerson && e.SubjectID == f.UserID) && e.P.User != f.UserID {
			continue
		}
		status := "applied"
		switch {
		case cancelled[e.ID]:
			status = "cancelled"
		case e.Effective > today:
			status = "scheduled"
		}
		if f.Status != "" && f.Status != status {
			continue
		}
		v := EventView{ID: e.ID, Kind: e.Kind, KindLabel: KindLabel(e.Kind), Subject: e.Subject, SubjectID: e.SubjectID, Effective: e.Effective,
			Reason: e.Reason, Status: status, Corrects: e.Corrects, CreatedBy: nameOf(e.CreatedBy)}
		if e.CreatedBy == 0 {
			v.CreatedBy = "시스템"
		}
		if e.Subject == SubjectPerson {
			v.SubjectName = nameOf(e.SubjectID)
		} else {
			v.SubjectName = s.unitNameAt(res, e.SubjectID)
		}
		v.HasAbsenceReason = e.Kind == KindAbsenceStart && e.P.AbsenceReason != ""
		v.Summary = s.summarize(e, res, ranks, nameOf)
		if q != "" && !strings.Contains(strings.ToLower(v.SubjectName+" "+v.Summary+" "+v.KindLabel), q) {
			continue
		}
		out = append(out, v)
	}
	total := len(out)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			out = nil
		} else {
			out = out[f.Offset:]
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return EventPage{Items: out, Total: total}, nil
}

func (s *Service) summarize(e Event, res *Result, ranks map[int64]string, nameOf func(int64) string) string {
	switch e.Kind {
	case KindUnitCreate:
		if e.P.Parent == 0 {
			return fmt.Sprintf("%s(%s) 신설", e.P.Name, kindNames[e.P.UnitKind])
		}
		return fmt.Sprintf("%s(%s) 신설, 상위 %s", e.P.Name, kindNames[e.P.UnitKind], s.unitNameAt(res, e.P.Parent))
	case KindUnitRename:
		return "이름을 " + e.P.Name + "(으)로 변경"
	case KindUnitMove:
		return "상위 조직을 " + s.unitNameAt(res, e.P.Parent) + "(으)로 이동"
	case KindUnitSort:
		return fmt.Sprintf("정렬 순서 %d", e.P.Sort)
	case KindHeadSet:
		return fmt.Sprintf("%s 조직장에 %s 보임", s.unitNameAt(res, e.SubjectID), nameOf(e.P.User))
	case KindHeadClear:
		return s.unitNameAt(res, e.SubjectID) + " 조직장 해임"
	case KindActingSet:
		return fmt.Sprintf("%s 직무대행 %s(%s~%s)", s.unitNameAt(res, e.SubjectID), nameOf(e.P.User), DayText(e.Effective), DayText(e.P.End))
	case KindHire:
		return fmt.Sprintf("%s 소속, 직위 %s", s.unitNameAt(res, e.P.Unit), ranks[e.P.Rank])
	case KindTransfer:
		return s.unitNameAt(res, e.P.Unit) + "(으)로 전보"
	case KindRankChange:
		return "직위 " + ranks[e.P.Rank] + "(으)로 변경"
	case KindConcurrentAdd:
		return s.unitNameAt(res, e.P.Unit) + " 겸직"
	case KindConcurrentEnd:
		return s.unitNameAt(res, e.P.Unit) + " 겸직 해제"
	case KindLeave:
		return fmt.Sprintf("마지막 근무일 %s(%s)", DayText(e.P.LastDay), e.P.Leave)
	case KindAbsenceStart:
		// 사유 원문은 목록에 싣지 않는다(민감 정보). 있다는 사실만 보인다.
		out := "휴직(" + AbsenceLabel(e.P.Absence) + ")" + returnText(e.P.ReturnDay)
		if e.P.AbsenceReason != "" {
			out += ", 사유 있음"
		}
		return out
	case KindAbsenceEnd:
		return "복직"
	}
	return ""
}

// Rank 는 직위 표의 한 줄이다.
type Rank struct {
	ID     int64
	Name   string
	Sort   int
	Active bool
	InUse  int // 오늘 이 직위로 재직 중인 사람 수
}

// ListRanks 는 직위 표를 순서대로 돌려준다.
func (s *Service) ListRanks(ctx context.Context) ([]Rank, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, sort_order, active FROM org_ranks ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	var out []Rank
	for rows.Next() {
		var r Rank
		var a int
		if err := rows.Scan(&r.ID, &r.Name, &r.Sort, &a); err != nil {
			rows.Close()
			return nil, err
		}
		r.Active = a == 1
		out = append(out, r)
	}
	rows.Close()
	sn, err := s.snapAt(ctx, "")
	if err != nil {
		return nil, err
	}
	use := map[int64]int{}
	for _, p := range sn.People {
		if !p.Left {
			use[p.Rank]++
		}
	}
	for i := range out {
		out[i].InUse = use[out[i].ID]
	}
	return out, nil
}

// RankInput 은 직위 표 편집 입력이다. ID 가 0 이면 새 직위다.
type RankInput struct {
	ID     int64
	Name   string
	Sort   int
	Active bool
}

// SaveRanks 는 직위 표를 저장한다(추가·이름·순서·사용 여부, 삭제 없음). 바뀐 직위마다 감사 기록 한 건이다.
func (s *Service) SaveRanks(ctx context.Context, p contract.Principal, in []RankInput) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return err
	}
	cur, err := s.ListRanks(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]Rank{}
	for _, r := range cur {
		byID[r.ID] = r
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		seen := map[string]bool{}
		for _, r := range in {
			name := strings.TrimSpace(r.Name)
			if name == "" {
				return verr("직위 이름을 입력하세요.")
			}
			if seen[name] {
				return verr("같은 이름의 직위가 있습니다.")
			}
			seen[name] = true
			old, exists := byID[r.ID]
			if r.ID != 0 && !exists {
				return contract.ErrNotFound
			}
			if exists && old.Active && !r.Active && old.InUse > 0 {
				return verr(fmt.Sprintf("사용 중인 직위는 중지할 수 없습니다(%d명).", old.InUse))
			}
			if exists && old.Name == name && old.Sort == r.Sort && old.Active == r.Active {
				continue
			}
			var id int64 = r.ID
			if !exists {
				newID, err := dbutil.InsertID(ctx, tx, `INSERT INTO org_ranks (name, sort_order, active) VALUES (?,?,?)`, name, r.Sort, boolInt(r.Active))
				if err != nil {
					if dbutil.IsUnique(err) {
						return verr("같은 이름의 직위가 있습니다.")
					}
					return err
				}
				id = newID
			} else if _, err := tx.ExecContext(ctx, `UPDATE org_ranks SET name = ?, sort_order = ?, active = ? WHERE id = ?`, name, r.Sort, boolInt(r.Active), id); err != nil {
				if dbutil.IsUnique(err) {
					return verr("같은 이름의 직위가 있습니다.")
				}
				return err
			}
			var diff []audit.DiffItem // 바뀐 값만 남긴다(새 직위는 이름·순서를 모두)
			if !exists || old.Name != name {
				diff = append(diff, audit.DiffItem{Key: "rank_name", Old: old.Name, New: name})
			}
			if !exists || old.Sort != r.Sort {
				diff = append(diff, audit.DiffItem{Key: "sort_order", Old: fmt.Sprint(old.Sort), New: fmt.Sprint(r.Sort)})
			}
			if !exists || old.Active != r.Active {
				diff = append(diff, audit.DiffItem{Key: "active", Old: yesNoActive(exists && old.Active), New: yesNoActive(r.Active)})
			}
			if err := s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "org.rank_set", TargetType: audit.TargetOrgRank, TargetID: fmt.Sprint(id),
				Result: audit.ResultSuccess, Diff: diff}); err != nil {
				return err
			}
		}
		return nil
	})
}

func yesNoActive(b bool) string {
	if b {
		return "사용"
	}
	return "중지"
}

// Issue 는 조직 관리 화면의 정합성 목록 한 줄이다.
type Issue struct {
	Kind   string // vacant_auto | vacant_open | head_no_approve | unregistered
	UnitID int64
	UserID int64
	Name   string
	Detail string
}

// Issues 는 조직장이 자동으로 공석이 된 조직, 공석인데 직무대행도 없는 조직, 조직에 등록되지 않은 사용자를 모은다.
func (s *Service) Issues(ctx context.Context) ([]Issue, error) {
	sn, err := s.snapAt(ctx, "")
	if err != nil {
		return nil, err
	}
	evs, err := LoadEvents(ctx, s.db)
	if err != nil {
		return nil, err
	}
	cancelled := map[int64]bool{}
	for _, e := range evs {
		if e.Kind == KindCancel {
			cancelled[e.Cancels] = true
		}
	}
	lastHead := map[int64]string{}
	for _, e := range evs {
		if e.Subject == SubjectUnit && !cancelled[e.ID] && e.Effective <= sn.Day {
			if e.Kind == KindHeadSet {
				lastHead[e.SubjectID] = KindHeadSet
			} else if e.Kind == KindHeadClear {
				lastHead[e.SubjectID] = KindHeadClear
			}
		}
	}
	ids := make([]int64, 0, len(sn.Units))
	for id := range sn.Units {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []Issue
	for _, id := range ids {
		u := sn.Units[id]
		if u.Closed || u.HeadUser != 0 || u.HeadActing {
			continue
		}
		if lastHead[id] == KindHeadSet {
			out = append(out, Issue{Kind: "vacant_auto", UnitID: id, Name: u.Name, Detail: "조직장이 전보·퇴사 등으로 자동 공석이 되었습니다"})
		} else {
			out = append(out, Issue{Kind: "vacant_open", UnitID: id, Name: u.Name, Detail: "공석이고 직무대행도 없습니다"})
		}
	}
	// 조직장(직무대행 포함)인데 결재 권한이 없는 역할이면, 이 사람이 결재선에 걸린 조직 직원의 상신이 막힌다(B21).
	decide := map[int64]bool{}
	roleNames := map[int64]string{}
	rr, err := s.db.QueryContext(ctx, `SELECT u.id, CASE WHEN EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = 'approval:decide') THEN 1 ELSE 0 END,
		COALESCE((SELECT string_agg(n, ',') FROM (SELECT DISTINCT r.name AS n FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id WHERE e.user_id = u.id) d), '') FROM users u`)
	if err != nil {
		return nil, err
	}
	for rr.Next() {
		var id int64
		var ok int
		var names string
		if err := rr.Scan(&id, &ok, &names); err != nil {
			rr.Close()
			return nil, err
		}
		decide[id], roleNames[id] = ok == 1, strings.ReplaceAll(names, ",", "·")
	}
	rr.Close()
	for _, id := range ids {
		u := sn.Units[id]
		if u.Closed || u.HeadUser == 0 || decide[u.HeadUser] {
			continue
		}
		who := "조직장"
		if u.HeadActing {
			who = "직무대행자"
		}
		out = append(out, Issue{Kind: "head_no_approve", UnitID: id, UserID: u.HeadUser, Name: u.Name,
			Detail: fmt.Sprintf("%s의 권한 역할은 '%s'입니다. 결재 권한이 없어 이 조직 직원의 상신이 막힙니다", who, roleNames[u.HeadUser])})
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.name FROM users u WHERE u.active = 1 AND NOT EXISTS (SELECT 1 FROM org_people o WHERE o.user_id = u.id) ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.UserID, &i.Name); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		i.Kind, i.Detail = "unregistered", "조직에 등록되지 않은 사용자입니다"
		out = append(out, i)
	}
	return out, rows.Err()
}

// ActingView 는 조직의 직무대행 한 건이다(종료일은 포함).
type ActingView struct {
	UserID int64
	Name   string
	From   contract.Day
	To     contract.Day
}

// ActingList 는 조직의 지금 진행 중이거나 앞으로 예정된 직무대행을 시작일 순으로 돌려준다.
func (s *Service) ActingList(ctx context.Context, unitID int64) ([]ActingView, error) {
	res, _, err := s.current(ctx, s.db)
	if err != nil {
		return nil, err
	}
	today := s.Today()
	var out []ActingView
	for _, a := range res.Acting {
		if a.UnitID == unitID && a.To > today {
			out = append(out, ActingView{UserID: a.UserID, Name: s.userName(ctx, s.db, a.UserID), From: a.From, To: AddDays(a.To, -1)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out, nil
}

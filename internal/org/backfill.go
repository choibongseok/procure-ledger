package org

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const settingBackfill = "org_backfill"

// Backfill 은 조직 이벤트가 하나도 없는 DB 에서 기존 부서·사용자를 조직 모델로 옮긴다(명세 11.4). 서버가 기동할 때 부른다.
//   - 멱등이고 한 번만 실행된다: app_settings 에 표지를 먼저 쓰고(쓰기 잠금) 그 안에서 이벤트가 없는지 다시 확인한다.
//     두 프로세스가 동시에 불러도 한쪽만 옮기고 다른 쪽은 아무것도 하지 않는다.
//   - 옮길 것이 없으면(부서도 사용자도 없는 새 DB) 표지만 남기지 않고 끝난다(dev 시드나 관리자가 조직을 만든다).
//   - 부서 번호를 그대로 쓰므로 기존 구매요청 등의 department_id 가 계속 유효하다. dept_head 직책은 부서 조직장,
//     team_lead 직책은 "{부서명} {n}팀"을 만들어 그 팀의 조직장으로 옮겨 호환 투영 결과가 기존 값과 같아진다.
//     한 부서에 dept_head 가 여럿이면 번호가 작은 사람만 조직장이 되고 나머지는 직책이 투영에서 사라진다(경고 로그).
//
// 옮겼으면 true 를 돌려준다.
func (s *Service) Backfill(ctx context.Context) (bool, error) {
	var did bool
	for attempt := 0; attempt < 3; attempt++ {
		err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
			// 쓰기 잠금을 먼저 잡는다(동시에 실행되는 다른 프로세스를 기다리게 한다).
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, 'running') ON CONFLICT DO NOTHING`, settingBackfill); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_events`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				did = false
				return nil
			}
			ok, err := s.backfillTx(ctx, tx)
			did = ok
			return err
		})
		if err == nil {
			s.invalidate()
			return did, nil
		}
		if !strings.Contains(err.Error(), "BUSY") && !strings.Contains(err.Error(), "locked") {
			return false, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, fmt.Errorf("org: 백필을 마치지 못했습니다(DB 가 계속 사용 중입니다)")
}

// createdDay 는 계정 생성 시각(UTC, RFC3339)을 KST 날짜로 바꾼다. 읽을 수 없으면 fallback 이다.
// UTC 날짜를 그대로 쓰면 KST 00:00~09:00 에 만든 계정이 전날로 보인다.
func createdDay(created string, fallback contract.Day) contract.Day {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, created); err == nil {
			return contract.DayOf(t)
		}
	}
	return fallback
}

type legacyUser struct {
	id       int64
	name     string
	dept     int64
	position string
	created  contract.Day
}

func (s *Service) backfillTx(ctx context.Context, tx contract.Tx) (bool, error) {
	type dept struct {
		id   int64
		name string
	}
	var depts []dept
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM departments ORDER BY id`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var d dept
		if err := rows.Scan(&d.id, &d.name); err != nil {
			rows.Close()
			return false, err
		}
		depts = append(depts, d)
	}
	rows.Close()
	var users []legacyUser
	rows, err = tx.QueryContext(ctx, `SELECT id, name, department_id, COALESCE(position,''), created_at FROM users ORDER BY id`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var u legacyUser
		var created string
		if err := rows.Scan(&u.id, &u.name, &u.dept, &u.position, &created); err != nil {
			rows.Close()
			return false, err
		}
		u.created = createdDay(created, s.Today())
		users = append(users, u)
	}
	rows.Close()
	if len(depts) == 0 && len(users) == 0 {
		return false, nil
	}

	base := s.Today()
	for _, u := range users {
		if u.created < base {
			base = u.created
		}
	}
	var rankID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM org_ranks ORDER BY sort_order LIMIT 1`).Scan(&rankID); err != nil {
		return false, err
	}

	var evs []Event
	add := func(e Event) { evs = append(evs, e) }
	unit := func(id int64, code, name string, kind contract.OrgKind, parent int64, sort int) {
		add(Event{Subject: SubjectUnit, SubjectID: id, Kind: KindUnitCreate, Effective: base,
			P: Payload{Code: code, Name: name, UnitKind: kind, Parent: parent, Sort: sort}})
	}

	// 기존 부서 번호를 그대로 쓰는 조직 단위부터 만든다.
	for _, d := range depts {
		if _, err := NewUnitID(ctx, tx, fmt.Sprintf("D%d", d.id), d.id); err != nil {
			return false, err
		}
	}
	rootName := "회사"
	for _, d := range depts {
		if d.name == rootName {
			rootName = "회사(전체)"
		}
	}
	rootID, err := NewUnitID(ctx, tx, "ROOT", 0)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO departments (id, name) VALUES (?, ?)`, rootID, rootName); err != nil {
		return false, err
	}
	if err := dbutil.SyncSequence(ctx, tx, "departments"); err != nil {
		return false, err
	}
	unit(rootID, "ROOT", rootName, contract.OrgCompany, 0, 0)
	for _, d := range depts {
		unit(d.id, fmt.Sprintf("D%d", d.id), d.name, contract.OrgDepartment, rootID, int(d.id))
	}

	// team_lead 마다 팀을 만든다.
	teamOf := map[int64]int64{}
	perDept := map[int64]int{}
	for _, u := range users {
		if u.position != "team_lead" {
			continue
		}
		perDept[u.dept]++
		code := fmt.Sprintf("D%d-T%d", u.dept, perDept[u.dept])
		id, err := NewUnitID(ctx, tx, code, 0)
		if err != nil {
			return false, err
		}
		name := ""
		for _, d := range depts {
			if d.id == u.dept {
				name = fmt.Sprintf("%s %d팀", d.name, perDept[u.dept])
			}
		}
		unit(id, code, name, contract.OrgTeam, u.dept, perDept[u.dept])
		teamOf[u.id] = id
	}

	// 사람: 입사(주소속 = 기존 부서 또는 자기 팀).
	for _, u := range users {
		primary := u.dept
		if t, ok := teamOf[u.id]; ok {
			primary = t
		}
		hire := u.created
		if hire < base {
			hire = base
		}
		no := fmt.Sprintf("E%06d", u.id)
		if _, err := tx.ExecContext(ctx, `INSERT INTO org_people (user_id, employee_no) VALUES (?, ?)`, u.id, no); err != nil {
			return false, err
		}
		add(Event{Subject: SubjectPerson, SubjectID: u.id, Kind: KindHire, Effective: hire, P: Payload{Rank: rankID, Unit: primary}})
	}
	// 조직장: dept_head 는 부서마다 번호가 작은 한 명, team_lead 는 자기 팀.
	headed := map[int64]bool{}
	sort.Slice(users, func(i, j int) bool { return users[i].id < users[j].id })
	for _, u := range users {
		hire := u.created
		if hire < base {
			hire = base
		}
		switch u.position {
		case "dept_head":
			if headed[u.dept] {
				log.Printf("org: 부서 %d 에 dept_head 가 여럿입니다. 사용자 %d 의 직책은 조직도에 옮기지 못했습니다", u.dept, u.id)
				continue
			}
			headed[u.dept] = true
			add(Event{Subject: SubjectUnit, SubjectID: u.dept, Kind: KindHeadSet, Effective: hire, P: Payload{User: u.id}})
		case "team_lead":
			add(Event{Subject: SubjectUnit, SubjectID: teamOf[u.id], Kind: KindHeadSet, Effective: hire, P: Payload{User: u.id}})
		}
	}
	for i := range evs {
		evs[i].CreatedBy = 0
		if _, err := Append(ctx, tx, evs[i]); err != nil {
			return false, err
		}
	}
	res, err := Rebuild(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("org: 기존 데이터를 조직도로 옮기지 못했습니다: %w", foldToUserError(err))
	}
	// 투영 결과는 기존 값과 같아야 한다. 같으므로 바뀐 사용자 수는 보통 0 이다.
	today := s.Today()
	n, err := s.project(ctx, tx, res, NewSnap(res, today), func(int64) bool { return true })
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_settings SET value = 'done', updated_at = ? WHERE key = ?`,
		s.now().UTC().Format(time.RFC3339), settingBackfill); err != nil {
		return false, err
	}
	if err := s.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "org.projection_apply", TargetType: audit.TargetOrgUnit, TargetID: "all",
		Result: audit.ResultSuccess,
		Diff:   []audit.DiffItem{{Key: "changed_users", New: fmt.Sprint(n)}, {Key: "date", New: string(today)}}, Detail: fmt.Sprintf("기존 부서·사용자를 조직도로 옮김(발령 이벤트 %d건을 한 번에 만듦)", len(evs))}); err != nil {
		return false, err
	}
	log.Printf("org: 기존 부서 %d개, 사용자 %d명을 조직도로 옮겼습니다", len(depts), len(users))
	return true, nil
}

package org

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 휴직·복직 알림(B50 8.3 의 5). 직속 조직장과 인사 담당(attendance:hr)에게 앱 안 알림을 만든다. 사유·구분은 싣지 않는다.
// 발령을 등록하는 트랜잭션에서 만든다(효력일이 미래이면 문구에 날짜가 있다).

const (
	notifyAbsenceStart = "hr_absence_start"
	notifyAbsenceEnd   = "hr_absence_end"
)

// directHeads 는 효력일 기준 사람의 직속 조직장이다: 주소속 조직의 실효 조직장, 본인이 조직장이면 위로 올라가며 처음 만나는 다른 사람.
func directHeads(sn *Snap, user int64) []int64 {
	p, ok := sn.People[user]
	if !ok {
		return nil
	}
	path := sn.Path(p.Primary)
	for i := len(path) - 1; i >= 0; i-- {
		if h := path[i].HeadUser; h != 0 && h != user {
			return []int64{h}
		}
	}
	return nil
}

// absenceWarnings 는 휴직 발령 뒤에도 조직장이 그대로 휴직 중인 조직(직무대행이 없는 조직)을 알리는 문구다.
func (s *Service) absenceWarnings(ctx context.Context, q Querier, res *Result, c Command) []string {
	if c.Kind != KindAbsenceStart {
		return nil
	}
	sn := NewSnap(res, c.Effective)
	var out []string
	for _, id := range sn.HeadedUnits(c.UserID) {
		out = append(out, fmt.Sprintf("이 사람은 %s의 조직장입니다. 직무대행을 지정하세요(지정하지 않으면 이 조직의 결재선이 한 단계 위로 올라갑니다).", unitLabel(sn, id)))
	}
	return out
}

// absenceRecipients 는 휴직 알림을 받는 사람이다: 직속 조직장과 인사 담당(attendance:hr), 대상자 본인은 뺀다.
func (s *Service) absenceRecipients(ctx context.Context, tx contract.Tx, sn *Snap, subject int64) ([]int64, error) {
	to := map[int64]bool{}
	var order []int64
	add := func(u int64) {
		if u != 0 && u != subject && !to[u] {
			to[u] = true
			order = append(order, u)
		}
	}
	for _, h := range directHeads(sn, subject) {
		add(h)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1
		WHERE e.permission = ? ORDER BY e.user_id`, string(contract.PermAttendanceHR))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		add(id)
	}
	return order, rows.Err()
}

// notifyAbsence 는 휴직·복직 알림을 만든다. 같은 트랜잭션에서 부른다.
func (s *Service) notifyAbsence(ctx context.Context, tx contract.Tx, c Command, res *Result, subject int64) error {
	if c.Kind != KindAbsenceStart && c.Kind != KindAbsenceEnd {
		return nil
	}
	sn := NewSnap(res, c.Effective)
	order, err := s.absenceRecipients(ctx, tx, sn, subject)
	if err != nil {
		return err
	}
	name := s.userName(ctx, tx, subject)
	kind, title, body := notifyAbsenceStart, name+"님이 휴직합니다", fmt.Sprintf("%s부터 휴직합니다%s.", DayText(c.Effective), returnText(c.ReturnDay))
	if c.Kind == KindAbsenceEnd {
		kind, title, body = notifyAbsenceEnd, name+"님이 복직합니다", fmt.Sprintf("%s부터 복직합니다.", DayText(c.Effective))
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05Z")
	for _, u := range order {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			u, kind, title, body, "/org", "person", subject, now); err != nil {
			return err
		}
	}
	return nil
}

// notifyOverdueKind 는 예정 복직일 경과 알림의 종류다. 같은 사람·같은 예정일에 받는 사람마다 한 번만 만든다(ref_type 에 예정일을 담아 거른다).
const notifyOverdueKind = "hr_absence_return_overdue"

// overdueReturns 는 오늘 휴직 중이면서 예정 복직일이 지난 사람과 그 예정일이다. 같은 사람의 가장 나중 휴직 발령(취소되지 않은, 효력일이 오늘 이전)의 예정일을 본다.
func (s *Service) overdueReturns(ctx context.Context, today contract.Day) (map[int64]contract.Day, *Snap, error) {
	sn, err := s.snap(ctx, today)
	if err != nil {
		return nil, nil, err
	}
	evs, err := LoadEvents(ctx, s.db)
	if err != nil {
		return nil, nil, err
	}
	cancelled := map[int64]bool{}
	for _, e := range evs {
		if e.Kind == KindCancel {
			cancelled[e.Cancels] = true
		}
	}
	last := map[int64]Event{}
	for _, e := range evs {
		if e.Kind != KindAbsenceStart || cancelled[e.ID] || e.Effective > today {
			continue
		}
		if cur, ok := last[e.SubjectID]; !ok || e.Effective >= cur.Effective {
			last[e.SubjectID] = e
		}
	}
	out := map[int64]contract.Day{}
	for user, e := range last {
		p, ok := sn.People[user]
		if !ok || p.Left || p.Absence == "" || e.P.ReturnDay == "" || e.P.ReturnDay >= today {
			continue
		}
		out[user] = e.P.ReturnDay
	}
	return out, sn, nil
}

// NotifyOverdueReturns 는 예정 복직일이 지났는데 복직 발령이 없는 사람을 인사 담당과 직속 조직장에게 앱 안 알림으로 알린다(B50 8.1). 자동 복직하지 않는다.
// 사유·구분은 싣지 않는다. 일일 작업(근태 모듈의 RunOnce)이 부르며 몇 번 돌아도 같은 사람·같은 예정일은 받는 사람마다 한 번이다. 만든 알림 수를 돌려준다.
func (s *Service) NotifyOverdueReturns(ctx context.Context, asOf time.Time) (int, error) {
	today := contract.DayOf(asOf)
	over, sn, err := s.overdueReturns(ctx, today)
	if err != nil || len(over) == 0 {
		return 0, err
	}
	n := 0
	users := make([]int64, 0, len(over))
	for u := range over {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		now := s.now().UTC().Format("2006-01-02T15:04:05Z")
		for _, subject := range users {
			day := over[subject]
			to, err := s.absenceRecipients(ctx, tx, sn, subject)
			if err != nil {
				return err
			}
			name := s.userName(ctx, tx, subject)
			ref := "return_overdue:" + string(day)
			for _, u := range to {
				var have int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND ref_type = ? AND ref_id = ?`, u, ref, subject).Scan(&have); err != nil {
					return err
				}
				if have > 0 {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
					u, notifyOverdueKind, name+"님의 예정 복직일이 지났습니다", fmt.Sprintf("예정 복직일(%s)이 지났지만 복직 발령이 없습니다. 복직 발령을 등록하거나 휴직 기간을 확인하세요.", DayText(day)), "/org/appointments", ref, subject, now); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}

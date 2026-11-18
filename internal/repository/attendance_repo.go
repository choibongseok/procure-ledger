package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/holders"
)

// SQLAttendance 는 출퇴근·근무 유형·정정·근태 마감(B50 5단계)의 저장소다. 모든 표가 법인 소유라 읽기·쓰기에 현재 법인 조건(contract.SQLEntity)이 붙는다.
// 기록 행은 지울 수 없고 정정은 추가 전용 줄과 함께만 바뀐다(서비스가 지킨다. DB 트리거가 한 번 더 막는다).
type SQLAttendance struct{}

// AttRecord 는 하루 출퇴근 행이다. 시각은 RFC3339 UTC 문자열(없으면 "")이다.
type AttRecord struct {
	ID       int64
	EntityID int64
	UserID   int64
	Date     string
	In       string
	Out      string
	Source   string
}

const attRecCols = `id, entity_id, user_id, work_date, check_in_at, check_out_at, source`

func scanAttRecord(s interface{ Scan(...any) error }) (AttRecord, error) {
	var r AttRecord
	err := s.Scan(&r.ID, &r.EntityID, &r.UserID, &r.Date, &r.In, &r.Out, &r.Source)
	return r, err
}

func (SQLAttendance) Record(ctx context.Context, q contract.Tx, userID int64, date string) (AttRecord, bool, error) {
	r, err := scanAttRecord(q.QueryRowContext(ctx, `SELECT `+attRecCols+` FROM attendance_records WHERE `+contract.SQLEntity(ctx, "")+` AND user_id = ? AND work_date = ?`, userID, date))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// RecordsBetween 은 사람의 기간(양 끝 포함) 기록을 날짜 순으로 돌려준다.
func (SQLAttendance) RecordsBetween(ctx context.Context, q contract.Tx, userID int64, from, to string) ([]AttRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+attRecCols+` FROM attendance_records WHERE `+contract.SQLEntity(ctx, "")+` AND user_id = ? AND work_date >= ? AND work_date <= ? ORDER BY work_date`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttRecord
	for rows.Next() {
		r, err := scanAttRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRecord 는 그날 행이 없을 때 새로 만든다. 같은 (사용자, 근무일)이 이미 있으면 ok=false 다(연속 클릭·두 탭, 유일 키가 막는다).
func (SQLAttendance) InsertRecord(ctx context.Context, q contract.Tx, entityID, userID int64, date, in, out, source string, now time.Time) (int64, bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO attendance_records (entity_id, user_id, work_date, check_in_at, check_out_at, source, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		entityID, userID, date, in, out, source, ts(now), ts(now))
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return 0, false, err
	}
	r, _, err := SQLAttendance{}.Record(contractEntityAny(ctx), q, userID, date)
	return r.ID, true, err
}

// contractEntityAny 는 방금 쓴 행을 법인 조건 없이 다시 읽기 위한 전체 모드 context 다(번호만 필요하다).
func contractEntityAny(ctx context.Context) context.Context {
	return contract.WithActiveEntity(ctx, contract.ActiveEntity{All: true})
}

// SetCheckOut 은 퇴근 시각이 아직 없을 때만 채운다. 채웠으면 true.
func (SQLAttendance) SetCheckOut(ctx context.Context, q contract.Tx, id int64, at string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE attendance_records SET check_out_at = ?, updated_at = ? WHERE id = ? AND check_out_at = ''`, at, ts(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ApplyTimes 는 정정 결과(출근·퇴근 시각)를 기록에 반영한다. 정정 줄이 같은 트랜잭션에서 함께 쓰인다.
func (SQLAttendance) ApplyTimes(ctx context.Context, q contract.Tx, id int64, in, out, source string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE attendance_records SET check_in_at = ?, check_out_at = ?, source = ?, updated_at = ? WHERE id = ?`, in, out, source, ts(now), id)
	return err
}

// RecordDates 는 출근 기록이 있는(체크인이 있는) 근무일 집합이다(출근율 계산).
func (SQLAttendance) RecordDates(ctx context.Context, q contract.Tx, userID int64, from, to string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT work_date FROM attendance_records WHERE `+contract.SQLEntity(ctx, "")+` AND user_id = ? AND work_date >= ? AND work_date <= ? AND check_in_at <> ''`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// FirstRecordDate 는 현재 법인에서 출퇴근 기록이 시작된 날이다(가장 이른 근무일). 기록이 없으면 "".
func (SQLAttendance) FirstRecordDate(ctx context.Context, q contract.Tx) (string, error) {
	var d sql.NullString
	err := q.QueryRowContext(ctx, `SELECT MIN(work_date) FROM attendance_records WHERE `+contract.SQLEntity(ctx, "")).Scan(&d)
	return d.String, err
}

// PaidLeaveDays 는 승인된 유급 휴가(출근한 것으로 보는 날)의 신청 기간들이다. 날짜 단위 전개는 서비스가 한다.
func (SQLAttendance) PaidLeavePeriods(ctx context.Context, q contract.Tx, userID int64, from, to string) ([][2]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.start_date, r.end_date FROM leave_requests r JOIN leave_types t ON t.entity_id = r.entity_id AND t.code = r.leave_code
		WHERE `+contract.SQLEntity(ctx, "r")+` AND r.user_id = ? AND r.status = 'approved' AND t.paid = 1 AND r.start_date <= ? AND r.end_date >= ?`, userID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllLeavePeriods 는 날짜에 걸친 승인·대기 휴가(종류 상관없이) 안내용이다.
func (SQLAttendance) ApprovedLeaveOn(ctx context.Context, q contract.Tx, userID int64, date string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests WHERE `+contract.SQLEntity(ctx, "")+` AND user_id = ? AND status = 'approved' AND start_date <= ? AND end_date >= ?`, userID, date, date).Scan(&n)
	return n > 0, err
}

// ---- 정정 ----

const corrCols = `c.id, c.user_id, COALESCE(u.name,''), c.work_date, c.old_in, c.old_out, c.new_in, c.new_out, c.reason, c.requested_by, COALESCE(rb.name,''), c.status, c.approved_by, COALESCE(ab.name,''), c.decide_note, c.created_at, c.decided_at`
const corrFrom = ` FROM attendance_corrections c LEFT JOIN users u ON u.id = c.user_id LEFT JOIN users rb ON rb.id = c.requested_by LEFT JOIN users ab ON ab.id = c.approved_by `

func scanCorr(s interface{ Scan(...any) error }) (domain.Correction, error) {
	var c domain.Correction
	err := s.Scan(&c.ID, &c.UserID, &c.UserName, &c.WorkDate, &c.OldIn, &c.OldOut, &c.NewIn, &c.NewOut, &c.Reason, &c.RequestedBy, &c.RequestedName, &c.Status, &c.ApprovedBy, &c.ApprovedName, &c.DecideNote, &c.CreatedAt, &c.DecidedAt)
	return c, err
}

// InsertCorrection 은 정정 줄을 더한다(요청 또는 직접 정정으로 이미 반영된 줄). 시각 값은 RFC3339 UTC 이다.
func (SQLAttendance) InsertCorrection(ctx context.Context, q contract.Tx, entityID int64, c domain.Correction, now time.Time) (int64, error) {
	decided := ""
	if c.Status != domain.CorrectionRequested {
		decided = ts(now)
	}
	return dbutil.InsertID(ctx, q, `INSERT INTO attendance_corrections (entity_id, user_id, work_date, old_in, old_out, new_in, new_out, reason, requested_by, status, approved_by, created_at, decided_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entityID, c.UserID, c.WorkDate, c.OldIn, c.OldOut, c.NewIn, c.NewOut, c.Reason, c.RequestedBy, c.Status, c.ApprovedBy, ts(now), decided)
}

func (SQLAttendance) CorrectionByID(ctx context.Context, q contract.Tx, id int64) (domain.Correction, error) {
	c, err := scanCorr(q.QueryRowContext(ctx, `SELECT `+corrCols+corrFrom+` WHERE `+contract.SQLEntity(ctx, "c")+` AND c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, contract.ErrNotFound
	}
	return c, err
}

// DecideCorrection 은 요청 중인 줄의 결정을 기록한다. 이미 결정됐으면 false(경합에서 한 번만 적용된다).
func (SQLAttendance) DecideCorrection(ctx context.Context, q contract.Tx, id int64, status string, by int64, note string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE attendance_corrections SET status = ?, approved_by = ?, decide_note = ?, decided_at = ? WHERE id = ? AND status = 'requested'`, status, by, note, ts(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CorrectionsOfUser 는 한 사람의 정정 줄이다(최근 순).
func (SQLAttendance) CorrectionsOfUser(ctx context.Context, q contract.Tx, userID int64, limit int) ([]domain.Correction, error) {
	return queryCorr(ctx, q, `SELECT `+corrCols+corrFrom+` WHERE `+contract.SQLEntity(ctx, "c")+` AND c.user_id = ? ORDER BY c.id DESC LIMIT ?`, userID, limit)
}

// PendingCorrections 는 현재 법인의 승인 대기 정정 요청이다(범위 거르기는 서비스가 한다).
func (SQLAttendance) PendingCorrections(ctx context.Context, q contract.Tx) ([]domain.Correction, error) {
	return queryCorr(ctx, q, `SELECT `+corrCols+corrFrom+` WHERE `+contract.SQLEntity(ctx, "c")+` AND c.status = 'requested' ORDER BY c.id`)
}

// PendingOn 은 그날 승인 대기 정정 요청의 번호다(없으면 0).
func (SQLAttendance) PendingOn(ctx context.Context, q contract.Tx, userID int64, date string) (int64, error) {
	var id sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT MIN(id) FROM attendance_corrections WHERE `+contract.SQLEntity(ctx, "")+` AND user_id = ? AND work_date = ? AND status = 'requested'`, userID, date).Scan(&id)
	return id.Int64, err
}

func queryCorr(ctx context.Context, q contract.Tx, query string, args ...any) ([]domain.Correction, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Correction
	for rows.Next() {
		c, err := scanCorr(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- 근태 마감 ----

// MonthClosed 는 법인의 그 달(YYYY-MM)이 근태 마감된 상태인지다(해제되지 않았다).
func (SQLAttendance) MonthClosed(ctx context.Context, q contract.Tx, entityID int64, month string) (bool, error) {
	var reopened string
	err := q.QueryRowContext(ctx, `SELECT reopened_at FROM attendance_closes WHERE entity_id = ? AND month = ?`, entityID, month).Scan(&reopened)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && reopened == "", err
}

// Closes 는 현재 법인의 마감 줄(최근 달 먼저)이다.
func (SQLAttendance) Closes(ctx context.Context, q contract.Tx) ([]domain.AttendanceClose, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.month, c.closed_at, COALESCE(cu.name,''), c.reopened_at, COALESCE(ru.name,''), c.reopen_reason
		FROM attendance_closes c LEFT JOIN users cu ON cu.id = c.closed_by LEFT JOIN users ru ON ru.id = c.reopened_by WHERE `+contract.SQLEntity(ctx, "c")+` ORDER BY c.month DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AttendanceClose
	for rows.Next() {
		var c domain.AttendanceClose
		if err := rows.Scan(&c.Month, &c.ClosedAt, &c.ClosedBy, &c.ReopenedAt, &c.ReopenedBy, &c.ReopenReason); err != nil {
			return nil, err
		}
		c.Closed = c.ReopenedAt == ""
		out = append(out, c)
	}
	return out, rows.Err()
}

// Close 는 그 달을 마감한다(해제된 달을 다시 마감하면 해제 정보를 지운다). 이미 마감된 달이면 false.
func (r SQLAttendance) Close(ctx context.Context, q contract.Tx, entityID int64, month string, by int64, now time.Time) (bool, error) {
	if closed, err := r.MonthClosed(ctx, q, entityID, month); err != nil || closed {
		return false, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO attendance_closes (entity_id, month, closed_by, closed_at) VALUES (?,?,?,?)
		ON CONFLICT (entity_id, month) DO UPDATE SET closed_by = excluded.closed_by, closed_at = excluded.closed_at, reopened_by = 0, reopened_at = '', reopen_reason = ''`, entityID, month, by, ts(now)); err != nil {
		return false, err
	}
	return true, nil
}

// Reopen 은 마감된 달을 해제한다. 마감 상태가 아니면 false.
func (SQLAttendance) Reopen(ctx context.Context, q contract.Tx, entityID int64, month string, by int64, reason string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE attendance_closes SET reopened_by = ?, reopened_at = ?, reopen_reason = ? WHERE entity_id = ? AND month = ? AND reopened_at = ''`, by, ts(now), reason, entityID, month)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ---- 근무 유형 ----

const workTypeCols = `id, code, name, kind, start_time, end_time, break_start, break_end, core_start, core_end, weekly_minutes, active`

func scanWorkType(s interface{ Scan(...any) error }) (domain.WorkType, error) {
	var w domain.WorkType
	var active int
	err := s.Scan(&w.ID, &w.Code, &w.Name, &w.Kind, &w.StartTime, &w.EndTime, &w.BreakStart, &w.BreakEnd, &w.CoreStart, &w.CoreEnd, &w.WeeklyMinutes, &active)
	w.Active = active == 1
	return w, err
}

func (SQLAttendance) WorkTypes(ctx context.Context, q contract.Tx) ([]domain.WorkType, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+workTypeCols+` FROM work_types WHERE `+contract.SQLEntity(ctx, "")+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WorkType
	for rows.Next() {
		w, err := scanWorkType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (SQLAttendance) WorkTypeByID(ctx context.Context, q contract.Tx, id int64) (domain.WorkType, error) {
	w, err := scanWorkType(q.QueryRowContext(ctx, `SELECT `+workTypeCols+` FROM work_types WHERE `+contract.SQLEntity(ctx, "")+` AND id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, contract.ErrNotFound
	}
	return w, err
}

func (SQLAttendance) InsertWorkType(ctx context.Context, q contract.Tx, in domain.WorkTypeInput, now time.Time) (int64, error) {
	return dbutil.InsertID(ctx, q, `INSERT INTO work_types (entity_id, code, name, kind, start_time, end_time, break_start, break_end, core_start, core_end, weekly_minutes, active, created_at) VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.Code, in.Name, in.Kind, in.StartTime, in.EndTime, in.BreakStart, in.BreakEnd, in.CoreStart, in.CoreEnd, in.WeeklyMinutes, boolInt(in.Active), ts(now))
}

func (SQLAttendance) UpdateWorkType(ctx context.Context, q contract.Tx, id int64, in domain.WorkTypeInput) error {
	_, err := q.ExecContext(ctx, `UPDATE work_types SET name = ?, kind = ?, start_time = ?, end_time = ?, break_start = ?, break_end = ?, core_start = ?, core_end = ?, weekly_minutes = ?, active = ? WHERE `+contract.SQLEntity(ctx, "")+` AND id = ?`,
		in.Name, in.Kind, in.StartTime, in.EndTime, in.BreakStart, in.BreakEnd, in.CoreStart, in.CoreEnd, in.WeeklyMinutes, boolInt(in.Active), id)
	return err
}

// InsertAssignment 는 배정 이력 한 줄을 더한다. 같은 (사용자, 효력일)이 이미 있으면 false.
func (SQLAttendance) InsertAssignment(ctx context.Context, q contract.Tx, userID, workTypeID int64, validFrom string, by int64, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO work_assignments (entity_id, user_id, work_type_id, valid_from, created_by, created_at) VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?) ON CONFLICT DO NOTHING`, userID, workTypeID, validFrom, by, ts(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// WorkTypeOn 은 그날 사람에게 배정된 근무 유형이다(효력일이 그날 이하인 가장 늦은 배정). 없으면 ok=false.
func (SQLAttendance) WorkTypeOn(ctx context.Context, q contract.Tx, userID int64, date string) (domain.WorkType, bool, error) {
	w, err := scanWorkType(q.QueryRowContext(ctx, `SELECT t.id, t.code, t.name, t.kind, t.start_time, t.end_time, t.break_start, t.break_end, t.core_start, t.core_end, t.weekly_minutes, t.active
		FROM work_assignments a JOIN work_types t ON t.id = a.work_type_id WHERE `+contract.SQLEntity(ctx, "a")+` AND a.user_id = ? AND a.valid_from <= ? ORDER BY a.valid_from DESC LIMIT 1`, userID, date))
	if errors.Is(err, sql.ErrNoRows) {
		return w, false, nil
	}
	return w, err == nil, err
}

func (SQLAttendance) Assignments(ctx context.Context, q contract.Tx, userID int64) ([]domain.WorkAssignment, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.id, a.user_id, a.work_type_id, t.name, a.valid_from, COALESCE(u.name,'') FROM work_assignments a JOIN work_types t ON t.id = a.work_type_id LEFT JOIN users u ON u.id = a.created_by
		WHERE `+contract.SQLEntity(ctx, "a")+` AND a.user_id = ? ORDER BY a.valid_from DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WorkAssignment
	for rows.Next() {
		var a domain.WorkAssignment
		if err := rows.Scan(&a.ID, &a.UserID, &a.WorkTypeID, &a.TypeName, &a.ValidFrom, &a.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- 주 경고 ----

// InsertWarning 은 같은 주·같은 단계의 경고를 한 번만 남긴다. 새로 남겼으면 true(그때만 알린다).
func (SQLAttendance) InsertWarning(ctx context.Context, q contract.Tx, userID int64, weekStart, level string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO attendance_warnings (user_id, week_start, level, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, userID, weekStart, level, ts(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HRUsers 는 현재 법인에서 인사 담당 권한(attendance:hr)을 가진 활성 사용자다(알림 대상).
func (SQLAttendance) HRUsers(ctx context.Context, q contract.Tx) ([]int64, error) {
	if a, ok := contract.ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return holders.Of(ctx, q, contract.PermAttendanceHR, a.ID, contract.DayOf(time.Now()))
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT u.id FROM users u JOIN user_effective_permissions ep ON ep.user_id = u.id AND ep.permission = ? WHERE u.active = 1 ORDER BY u.id`, string(contract.PermAttendanceHR))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- 달력·현황·집계(6단계) ----

// LeaveEntry 는 승인된 휴가 한 건(달력·집계용)이다. 사유는 읽지 않는다.
type LeaveEntry struct {
	UserID   int64
	Start    string
	End      string
	Part     string
	Code     string
	TypeName string
	Balance  string
	Minutes  int64
}

// ApprovedLeaves 는 [from, to] 와 겹치는 현재 법인의 승인된 휴가다(범위 거르기는 서비스가 한다).
func (SQLAttendance) ApprovedLeaves(ctx context.Context, q contract.Tx, from, to string) ([]LeaveEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.user_id, r.start_date, r.end_date, r.part, r.leave_code, COALESCE(t.name, r.leave_code), r.balance, r.minutes
		FROM leave_requests r LEFT JOIN leave_types t ON t.entity_id = r.entity_id AND t.code = r.leave_code
		WHERE `+contract.SQLEntity(ctx, "r")+` AND r.status = 'approved' AND r.start_date <= ? AND r.end_date >= ? ORDER BY r.user_id, r.start_date`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaveEntry
	for rows.Next() {
		var l LeaveEntry
		if err := rows.Scan(&l.UserID, &l.Start, &l.End, &l.Part, &l.Code, &l.TypeName, &l.Balance, &l.Minutes); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RecordsInRange 는 현재 법인의 모든 사람의 기록(기간 양 끝 포함)이다.
func (SQLAttendance) RecordsInRange(ctx context.Context, q contract.Tx, from, to string) ([]AttRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+attRecCols+` FROM attendance_records WHERE `+contract.SQLEntity(ctx, "")+` AND work_date >= ? AND work_date <= ? ORDER BY user_id, work_date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttRecord
	for rows.Next() {
		r, err := scanAttRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TypeFrom 은 근무 유형 배정의 한 구간(효력일부터)이다.
type TypeFrom struct {
	From string
	WT   domain.WorkType
}

// Timeline 은 사람의 배정 이력(효력일 순)이다. to 이후에 효력이 시작되는 배정은 뺀다.
func (SQLAttendance) Timeline(ctx context.Context, q contract.Tx, userID int64, to string) ([]TypeFrom, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.valid_from, t.id, t.code, t.name, t.kind, t.start_time, t.end_time, t.break_start, t.break_end, t.core_start, t.core_end, t.weekly_minutes, t.active
		FROM work_assignments a JOIN work_types t ON t.id = a.work_type_id WHERE `+contract.SQLEntity(ctx, "a")+` AND a.user_id = ? AND a.valid_from <= ? ORDER BY a.valid_from`, userID, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TypeFrom
	for rows.Next() {
		var f TypeFrom
		var active int
		w := &f.WT
		if err := rows.Scan(&f.From, &w.ID, &w.Code, &w.Name, &w.Kind, &w.StartTime, &w.EndTime, &w.BreakStart, &w.BreakEnd, &w.CoreStart, &w.CoreEnd, &w.WeeklyMinutes, &active); err != nil {
			return nil, err
		}
		w.Active = active == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

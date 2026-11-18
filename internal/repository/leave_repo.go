package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLLeave 는 근태·휴가(B50)의 저장소다. 모든 표가 법인 소유라 읽기·쓰기에 현재 법인 조건(contract.SQLEntity)이 붙는다.
// 잔여는 leave_ledger 의 합이고 이 저장소에는 잔여를 직접 고치는 함수가 없다(Append 만 있다).
type SQLLeave struct{}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---- 휴가 종류 ----

// systemLeaveTypes 는 법인마다 처음 읽을 때 만드는 시스템 종류다(명세 5.3).
var systemLeaveTypes = []domain.LeaveType{
	{Code: "annual", Name: "연차", Unit: "day", Balance: domain.LeaveBalanceAnnual, Paid: true, System: true, Active: true},
	{Code: "hourly", Name: "시간 단위 연차", Unit: "hour", Balance: domain.LeaveBalanceAnnual, Paid: true, System: true, Active: true},
	{Code: "sick", Name: "병가", Unit: "day", Paid: true, ProofRequired: true, System: true, Active: true},
	{Code: "family_event", Name: "경조휴가", Unit: "day", Paid: true, Active: true},
	{Code: "official", Name: "공가", Unit: "day", Paid: true, System: true, Active: true},
	{Code: "compensatory", Name: "대체휴가", Unit: "day", Balance: domain.LeaveBalanceCompensatory, Paid: true, Active: true},
}

func (SQLLeave) EnsureTypes(ctx context.Context, q contract.Tx, entityID int64, now time.Time) error {
	for _, t := range systemLeaveTypes {
		if _, err := q.ExecContext(ctx, `INSERT INTO leave_types (entity_id, code, name, unit, balance, paid, proof_required, max_days, system, active, created_at)
			VALUES (?,?,?,?,?,?,?,0,?,1,?) ON CONFLICT DO NOTHING`, entityID, t.Code, t.Name, t.Unit, t.Balance, boolInt(t.Paid), boolInt(t.ProofRequired), boolInt(t.System), ts(now)); err != nil {
			return err
		}
	}
	return nil
}

func scanLeaveType(s interface{ Scan(...any) error }) (domain.LeaveType, error) {
	var t domain.LeaveType
	var paid, proof, system, active int
	err := s.Scan(&t.ID, &t.Code, &t.Name, &t.Unit, &t.Balance, &paid, &proof, &t.MaxDays, &system, &active)
	t.Paid, t.ProofRequired, t.System, t.Active = paid == 1, proof == 1, system == 1, active == 1
	return t, err
}

const leaveTypeCols = `id, code, name, unit, balance, paid, proof_required, max_days, system, active`

func (SQLLeave) Types(ctx context.Context, q contract.Tx) ([]domain.LeaveType, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+leaveTypeCols+` FROM leave_types WHERE `+contract.SQLEntity(ctx, "leave_types")+` ORDER BY system DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LeaveType
	for rows.Next() {
		t, err := scanLeaveType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (SQLLeave) TypeByCode(ctx context.Context, q contract.Tx, code string) (domain.LeaveType, error) {
	t, err := scanLeaveType(q.QueryRowContext(ctx, `SELECT `+leaveTypeCols+` FROM leave_types WHERE `+contract.SQLEntity(ctx, "leave_types")+` AND code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return t, contract.ErrNotFound
	}
	return t, err
}

func (SQLLeave) TypeByID(ctx context.Context, q contract.Tx, id int64) (domain.LeaveType, error) {
	t, err := scanLeaveType(q.QueryRowContext(ctx, `SELECT `+leaveTypeCols+` FROM leave_types WHERE `+contract.SQLEntity(ctx, "leave_types")+` AND id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, contract.ErrNotFound
	}
	return t, err
}

func (SQLLeave) InsertType(ctx context.Context, q contract.Tx, in domain.LeaveTypeInput, now time.Time) (int64, error) {
	return dbutil.InsertID(ctx, q, `INSERT INTO leave_types (entity_id, code, name, unit, balance, paid, proof_required, max_days, system, active, created_at) VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?,?,?,0,?,?)`,
		in.Code, in.Name, in.Unit, in.Balance, boolInt(in.Paid), boolInt(in.ProofRequired), in.MaxDays, boolInt(in.Active), ts(now))
}

func (SQLLeave) UpdateType(ctx context.Context, q contract.Tx, id int64, in domain.LeaveTypeInput, system bool) error {
	if system { // 시스템 종류는 코드·단위·잔여 묶음·일수 상한을 바꾸지 않는다
		_, err := q.ExecContext(ctx, `UPDATE leave_types SET name = ?, paid = ?, proof_required = ?, active = ? WHERE `+contract.SQLEntity(ctx, "leave_types")+` AND id = ?`, in.Name, boolInt(in.Paid), boolInt(in.ProofRequired), boolInt(in.Active), id)
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE leave_types SET name = ?, unit = ?, balance = ?, paid = ?, proof_required = ?, max_days = ?, active = ? WHERE `+contract.SQLEntity(ctx, "leave_types")+` AND id = ?`,
		in.Name, in.Unit, in.Balance, boolInt(in.Paid), boolInt(in.ProofRequired), in.MaxDays, boolInt(in.Active), id)
	return err
}

// ---- 규칙(효력일 이력) ----

func scanPolicy(s interface{ Scan(...any) error }) (domain.LeavePolicy, error) {
	var p domain.LeavePolicy
	var att, canceled int
	err := s.Scan(&p.ID, &p.EffectiveFrom, &p.Basis, &p.WorkdayMinutes, &p.HourUnitMinutes, &att, &p.CarryMaxMinutes, &p.CarryValidMonths, &p.ExpireMonths, &p.FirstYearMode, &p.CreatedByName, &p.CreatedAt, &canceled)
	p.AttendanceRule, p.Canceled = att == 1, canceled == 1
	return p, err
}

const policyCols = `p.id, p.effective_from, p.basis, p.workday_minutes, p.hour_unit_minutes, p.attendance_rule, p.carry_max_minutes, p.carry_valid_months, p.expire_months, p.first_year_mode, COALESCE(u.name,''), p.created_at, p.canceled`

func (SQLLeave) Policies(ctx context.Context, q contract.Tx) ([]domain.LeavePolicy, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+policyCols+` FROM leave_policies p LEFT JOIN users u ON u.id = p.created_by WHERE `+contract.SQLEntity(ctx, "p")+` ORDER BY p.effective_from DESC, p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LeavePolicy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PolicyOn 은 그날 유효한 규칙(효력일이 그날 이전·같은 취소되지 않은 가장 최근 행)이다. 없으면 ok=false.
func (SQLLeave) PolicyOn(ctx context.Context, q contract.Tx, day string) (domain.LeavePolicy, bool, error) {
	p, err := scanPolicy(q.QueryRowContext(ctx, `SELECT `+policyCols+` FROM leave_policies p LEFT JOIN users u ON u.id = p.created_by WHERE `+contract.SQLEntity(ctx, "p")+` AND p.canceled = 0 AND p.effective_from <= ? ORDER BY p.effective_from DESC LIMIT 1`, day))
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// FirstPolicyDate 는 취소되지 않은 가장 이른 규칙의 효력일이다(그 전의 발생은 만들지 않는다: 이전 잔여는 초기 이관으로 넣는다). 없으면 "".
func (SQLLeave) FirstPolicyDate(ctx context.Context, q contract.Tx) (string, error) {
	var d sql.NullString
	err := q.QueryRowContext(ctx, `SELECT MIN(effective_from) FROM leave_policies WHERE `+contract.SQLEntity(ctx, "leave_policies")+` AND canceled = 0`).Scan(&d)
	return d.String, err
}

func (SQLLeave) InsertPolicy(ctx context.Context, q contract.Tx, in domain.LeavePolicyInput, by int64, now time.Time) (int64, error) {
	return dbutil.InsertID(ctx, q, `INSERT INTO leave_policies (entity_id, effective_from, basis, workday_minutes, hour_unit_minutes, attendance_rule, carry_max_minutes, carry_valid_months, expire_months, first_year_mode, created_by, created_at)
		VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?,?,?,?,?,?,?)`, in.EffectiveFrom, in.Basis, in.WorkdayMinutes, in.HourUnitMinutes, boolInt(in.AttendanceRule), in.CarryMaxMinutes, in.CarryValidMonths, in.ExpireMonths, in.FirstYearMode, by, ts(now))
}

func (SQLLeave) PolicyByID(ctx context.Context, q contract.Tx, id int64) (domain.LeavePolicy, error) {
	p, err := scanPolicy(q.QueryRowContext(ctx, `SELECT `+policyCols+` FROM leave_policies p LEFT JOIN users u ON u.id = p.created_by WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, contract.ErrNotFound
	}
	return p, err
}

func (SQLLeave) CancelPolicy(ctx context.Context, q contract.Tx, id, by int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE leave_policies SET canceled = 1, canceled_by = ?, canceled_at = ? WHERE `+contract.SQLEntity(ctx, "leave_policies")+` AND id = ? AND canceled = 0`, by, ts(now), id)
	return err
}

// ---- 원장 ----

// LeaveLedgerEntry 는 원장에 더할 한 줄이다. PeriodKey 가 있는 grant·carry·expire 는 (사용자, 묶음, 종류, 키)가 유일해 두 번 쓰이지 않는다.
type LeaveLedgerEntry struct {
	EntityID  int64 // 0 이면 현재 법인
	UserID    int64
	Balance   string
	Kind      string
	Minutes   int64
	PeriodKey string
	ExpiresOn string
	RefType   string
	RefID     int64
	Reason    string
	By        int64
	At        time.Time
}

// Append 는 원장에 한 줄을 더한다. 같은 (사용자, 묶음, 종류, PeriodKey)가 이미 있으면 아무것도 쓰지 않고 false 다(멱등).
func (SQLLeave) Append(ctx context.Context, q contract.Tx, e LeaveLedgerEntry) (bool, error) {
	ent := e.EntityID
	if ent == 0 {
		ent = contract.WriteEntityID(ctx)
	}
	res, err := q.ExecContext(ctx, `INSERT INTO leave_ledger (entity_id, user_id, balance, kind, minutes, period_key, expires_on, ref_type, ref_id, reason, created_by, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, ent, e.UserID, e.Balance, e.Kind, e.Minutes, e.PeriodKey, e.ExpiresOn, e.RefType, e.RefID, e.Reason, e.By, ts(e.At))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LockUser 는 한 사람의 휴가 쓰기를 직렬화한다(PG 자문 잠금. SQLite 는 연결 하나라 이미 직렬이다). 잔여를 읽고 예약하는 트랜잭션이 부른다.
func (SQLLeave) LockUser(ctx context.Context, q contract.Tx, userID int64) error {
	return dbutil.AcquireKeyLock(ctx, q, fmt.Sprintf("leave-user-%d", userID))
}

// BalanceMinutes 는 한 묶음의 잔여(원장 합)와 예약된 분량이다. 사용 가능 = 합이다(예약 줄이 이미 빠져 있다).
func (SQLLeave) BalanceMinutes(ctx context.Context, q contract.Tx, userID int64, balance string) (total, reserved int64, err error) {
	var t sql.NullInt64
	if err = q.QueryRowContext(ctx, `SELECT SUM(minutes) FROM leave_ledger WHERE `+contract.SQLEntity(ctx, "leave_ledger")+` AND user_id = ? AND balance = ?`, userID, balance).Scan(&t); err != nil {
		return 0, 0, err
	}
	// 예약 중인 분량은 신청 대기(pending) 건의 분량이다(원장의 복원 줄은 취소에도 쓰여 줄 종류만으로는 셀 수 없다).
	var r sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT SUM(minutes) FROM leave_requests WHERE `+contract.SQLEntity(ctx, "leave_requests")+` AND user_id = ? AND balance = ? AND status = 'pending'`, userID, balance).Scan(&r)
	return t.Int64, r.Int64, err
}

// LedgerLines 는 원장 줄을 최근 순으로 돌려준다.
func (SQLLeave) LedgerLines(ctx context.Context, q contract.Tx, userID int64, limit int) ([]domain.LeaveLedgerLine, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, `SELECT l.id, l.balance, l.kind, l.minutes, l.reason, l.ref_type, l.ref_id, l.expires_on, l.created_at, l.created_by, COALESCE(u.name,'')
		FROM leave_ledger l LEFT JOIN users u ON u.id = l.created_by WHERE `+contract.SQLEntity(ctx, "l")+` AND l.user_id = ? ORDER BY l.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LeaveLedgerLine
	for rows.Next() {
		var l domain.LeaveLedgerLine
		if err := rows.Scan(&l.ID, &l.Balance, &l.Kind, &l.Minutes, &l.Reason, &l.RefType, &l.RefID, &l.ExpiresOn, &l.CreatedAt, &l.CreatedBy, &l.CreatedByName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ExpiringGrant 는 소멸 대상 줄(grant·carry·adjust·opening 중 expires_on 이 있는 것)이다.
type ExpiringGrant struct {
	ID        int64
	Kind      string
	Minutes   int64
	ExpiresOn string
	Expired   int64 // 이미 소멸 줄로 빠진 분량(양수)
}

// ExpiringGrants 는 한 묶음의 소멸 대상 줄을 소멸일 순으로 돌려준다(이미 소멸한 분량 포함).
func (SQLLeave) ExpiringGrants(ctx context.Context, q contract.Tx, userID int64, balance string) ([]ExpiringGrant, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.kind, g.minutes, g.expires_on,
			COALESCE((SELECT -SUM(e.minutes) FROM leave_ledger e WHERE e.kind = 'expire' AND e.ref_type = 'leave_ledger' AND e.ref_id = g.id), 0)
		FROM leave_ledger g WHERE `+contract.SQLEntity(ctx, "g")+` AND g.user_id = ? AND g.balance = ? AND g.expires_on <> '' AND g.minutes > 0 AND g.kind IN ('grant','carry','adjust','opening')
		ORDER BY g.expires_on, g.id`, userID, balance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpiringGrant
	for rows.Next() {
		var g ExpiringGrant
		if err := rows.Scan(&g.ID, &g.Kind, &g.Minutes, &g.ExpiresOn, &g.Expired); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ConsumedMinutes 는 한 묶음에서 소멸 대상 줄을 먼저 쓴 순서(FIFO)로 깎아 낼 사용량이다: 예약·복원·사용의 합과 음수 조정의 합을 양수로 돌려준다.
func (SQLLeave) ConsumedMinutes(ctx context.Context, q contract.Tx, userID int64, balance string) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT -SUM(minutes) FROM leave_ledger WHERE `+contract.SQLEntity(ctx, "leave_ledger")+` AND user_id = ? AND balance = ?
		AND (kind IN ('reserve','release','use') OR (kind IN ('adjust','opening') AND minutes < 0))`, userID, balance).Scan(&n)
	return n.Int64, err
}

// ---- 사람·조직 읽기(입사일·법인·재직자) ----

// HireDate 는 asOf 에 유효한 입사일이다: 취소되지 않은 입사 발령 중 효력일이 asOf 이하인 가장 늦은 것(재입사는 새 입사일, 명세 D18). 없으면 "".
func (SQLLeave) HireDate(ctx context.Context, q contract.Tx, userID int64, asOf string) (string, error) {
	var d sql.NullString
	err := q.QueryRowContext(ctx, `SELECT MAX(e.effective_date) FROM org_events e WHERE e.subject_type = 'person' AND e.subject_id = ? AND e.kind = 'hire' AND e.effective_date <= ?
		AND NOT EXISTS (SELECT 1 FROM org_events c WHERE c.cancels_id = e.id)`, userID, asOf).Scan(&d)
	return d.String, err
}

// UserEntity 는 asOf 에 사람이 소속된 법인(주소속 조직의 법인)이다. 조직에 없으면 ok=false.
func (SQLLeave) UserEntity(ctx context.Context, q contract.Tx, userID int64, asOf string) (int64, bool, error) {
	var id sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT s.entity_id FROM org_person_spans p JOIN org_unit_spans s ON s.unit_id = p.primary_unit_id AND s.valid_from <= ? AND (s.valid_to IS NULL OR s.valid_to > ?)
		WHERE p.user_id = ? AND p.left_flag = 0 AND p.valid_from <= ? AND (p.valid_to IS NULL OR p.valid_to > ?) LIMIT 1`, asOf, asOf, userID, asOf, asOf).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id.Int64, err == nil && id.Valid, err
}

// ActiveEmployees 는 asOf 에 재직 중인 조직 등록 사용자의 번호와 법인이다(일일 발생 작업의 대상).
func (SQLLeave) ActiveEmployees(ctx context.Context, q contract.Tx, asOf string) (map[int64]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.user_id, s.entity_id FROM org_person_spans p JOIN org_unit_spans s ON s.unit_id = p.primary_unit_id AND s.valid_from <= ? AND (s.valid_to IS NULL OR s.valid_to > ?)
		JOIN users u ON u.id = p.user_id AND u.active = 1
		WHERE p.left_flag = 0 AND p.valid_from <= ? AND (p.valid_to IS NULL OR p.valid_to > ?)`, asOf, asOf, asOf, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var u, e int64
		if err := rows.Scan(&u, &e); err != nil {
			return nil, err
		}
		out[u] = e
	}
	return out, rows.Err()
}

// FiscalStartMonth 는 회사 설정(B34)의 회계연도 시작월이다. 저장된 적이 없으면 1.
func (SQLLeave) FiscalStartMonth(ctx context.Context, q contract.Tx) (int, error) {
	m, _, _, err := SQLCompany{}.FiscalStart(ctx, q) // 회사 설정 저장소 한 곳(현재 법인의 회계연도, B34·B44)
	return m, err
}

// ---- 신청 ----

const leaveReqCols = `r.id, r.doc_id, COALESCE(p.no,''), r.user_id, COALESCE(u.name,''), r.leave_code, COALESCE(t.name, r.leave_code), r.start_date, r.end_date, r.part, r.start_time, r.end_time, r.minutes, r.status, r.reason, r.delegate_user_id, r.created_at, r.balance, r.entity_id, r.note, r.absence_delegation_id, r.proof_attachment_id`

// LeaveReqRow 는 신청 행과 부가 정보(결재 문서 번호·신청자·종류 이름)다.
type LeaveReqRow struct {
	domain.LeaveRequest
	Balance  string
	EntityID int64
}

func scanLeaveReq(s interface{ Scan(...any) error }) (LeaveReqRow, error) {
	var r LeaveReqRow
	err := s.Scan(&r.ID, &r.DocID, &r.DocNo, &r.UserID, &r.UserName, &r.LeaveCode, &r.LeaveName, &r.StartDate, &r.EndDate, &r.Part, &r.StartTime, &r.EndTime, &r.Minutes, &r.Status, &r.Reason, &r.DelegateUserID, &r.CreatedAt, &r.Balance, &r.EntityID, &r.Note, &r.AbsenceDelegationID, &r.ProofAttachmentID)
	r.HasReason = r.Reason != ""
	return r, err
}

const leaveReqFrom = ` FROM leave_requests r LEFT JOIN purchase_requests p ON p.id = r.doc_id LEFT JOIN users u ON u.id = r.user_id LEFT JOIN leave_types t ON t.entity_id = r.entity_id AND t.code = r.leave_code `

func (SQLLeave) InsertRequest(ctx context.Context, q contract.Tx, r LeaveReqRow, entityID int64, now time.Time) (int64, error) {
	return dbutil.InsertID(ctx, q, `INSERT INTO leave_requests (entity_id, user_id, doc_id, leave_code, start_date, end_date, part, start_time, end_time, minutes, balance, status, reason, delegate_user_id, proof_attachment_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entityID, r.UserID, r.DocID, r.LeaveCode, r.StartDate, r.EndDate, r.Part, r.StartTime, r.EndTime, r.Minutes, r.Balance, r.Status, r.Reason, r.DelegateUserID, r.ProofAttachmentID, ts(now), ts(now))
}

func (SQLLeave) RequestByID(ctx context.Context, q contract.Tx, id int64) (LeaveReqRow, error) {
	r, err := scanLeaveReq(q.QueryRowContext(ctx, `SELECT `+leaveReqCols+leaveReqFrom+` WHERE `+contract.SQLEntity(ctx, "r")+` AND r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// RequestByDocID 는 결재 문서에 묶인 신청이다. 훅이 부르며 현재 법인 context 가 없을 수 있어 법인 조건을 걸지 않는다(문서 번호가 유일하다).
func (SQLLeave) RequestByDocID(ctx context.Context, q contract.Tx, docID int64) (LeaveReqRow, bool, error) {
	r, err := scanLeaveReq(q.QueryRowContext(ctx, `SELECT `+leaveReqCols+leaveReqFrom+` WHERE r.doc_id = ?`, docID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (SQLLeave) UserRequests(ctx context.Context, q contract.Tx, userID int64, limit int) ([]LeaveReqRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, `SELECT `+leaveReqCols+leaveReqFrom+` WHERE `+contract.SQLEntity(ctx, "r")+` AND r.user_id = ? ORDER BY r.start_date DESC, r.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaveReqRow
	for rows.Next() {
		r, err := scanLeaveReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRequestsBetween 은 기간과 겹치는 신청 대기·승인 건이다(충돌 검사).
func (SQLLeave) ActiveRequestsBetween(ctx context.Context, q contract.Tx, userID int64, from, to string) ([]LeaveReqRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+leaveReqCols+leaveReqFrom+` WHERE `+contract.SQLEntity(ctx, "r")+` AND r.user_id = ? AND r.status IN ('pending','approved') AND r.start_date <= ? AND r.end_date >= ? ORDER BY r.start_date`, userID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaveReqRow
	for rows.Next() {
		r, err := scanLeaveReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRequestStatus 는 신청 상태를 바꾼다. from 이 현재 상태와 같을 때만 바꾸고 바뀐 행이 있는지 돌려준다(경합에서 한 번만 적용된다).
func (SQLLeave) SetRequestStatus(ctx context.Context, q contract.Tx, id int64, from, to, note string, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE leave_requests SET status = ?, note = ?, updated_at = ? WHERE id = ? AND status = ?`, to, note, ts(now), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetAbsence 는 승인 훅이 만든 부재 행 번호와 안내 문구(부재 행을 만들지 못했을 때의 이유)를 신청에 남긴다(명세 6.7).
func (SQLLeave) SetAbsence(ctx context.Context, q contract.Tx, id, delegationID int64, note string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE leave_requests SET absence_delegation_id = ?, note = ?, updated_at = ? WHERE id = ?`, delegationID, note, ts(now), id)
	return err
}

// LinkProof 는 올려 둔 증빙 첨부를 결재 문서에 붙인다(문서가 임시저장일 때만 연결이 바뀐다). 본인이 올린 아직 어디에도 붙지 않은 첨부만 된다. 붙었으면 true.
func (SQLLeave) LinkProof(ctx context.Context, q contract.Tx, attachmentID, userID, docID int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE document_attachments SET doc_id = ? WHERE id = ? AND uploaded_by = ? AND doc_id IS NULL AND card_txn_id IS NULL AND filename <> ''`, docID, attachmentID, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ProofOwnedFree 는 증빙 첨부가 본인이 올린 아직 안 붙은 파일인지다(신청 전 검사).
func (SQLLeave) ProofOwnedFree(ctx context.Context, q contract.Tx, attachmentID, userID int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_attachments WHERE id = ? AND uploaded_by = ? AND doc_id IS NULL AND card_txn_id IS NULL AND filename <> ''`, attachmentID, userID).Scan(&n)
	return n > 0, err
}

// LeaveDocUser 는 doc_id 가 휴가 신청서(LEAVE)면 신청자다. 휴가 문서의 첨부는 신청자·직속 조직장·인사 담당·감사자만 받는다(명세 11.4).
func (SQLLeave) LeaveDocUser(ctx context.Context, q contract.Tx, docID int64) (userID int64, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT user_id FROM leave_requests WHERE doc_id = ?`, docID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return userID, err == nil, err
}

package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// DelegationRec 는 결재 위임 한 건이다(이름 포함).
type DelegationRec struct {
	ID                 int64
	DelegatorID        int64
	DelegatorName      string
	DelegateID         int64
	DelegateName       string
	From, To           time.Time
	CreatedAt          time.Time
	RevokedAt          *time.Time
	Kind, Reason, Note string
}

// DelegationInsert 는 위임 지정 입력이다.
type DelegationInsert struct {
	DelegatorID, DelegateID int64
	From, To, Now           time.Time
	Kind, Reason, Note      string
}

// UserRec 는 위임 규칙 판정에 쓰는 사용자 요약이다. 직책은 users.position 이 아니라 조직 조회에서 얻는다(B21).
type UserRec struct {
	ID           int64
	Name         string
	Role         contract.Role
	DepartmentID int64
	Active       bool
	Perms        contract.PermSet // 실효 권한(B33). 결재 자격·재무 단계 판정에 쓴다
}

// Can 은 사용자가 실효 권한을 가졌는지다.
func (u UserRec) Can(p contract.Permission) bool { return u.Perms.Has(p) }

// DelegationRepository 는 결재 위임 저장소다. 해제는 revoked_at 표시이며 삭제 메서드는 없다.
// 어느 단계를 누가 대신할 수 있는지(직책·서열·배정자)는 서비스가 조직 조회와 결재선 스냅샷으로 판정하고, 이 저장소는 위임 행만 돌려준다.
type DelegationRepository interface {
	Insert(ctx context.Context, q contract.Tx, in DelegationInsert) (int64, error)
	Get(ctx context.Context, q contract.Tx, id int64) (DelegationRec, error)
	Revoke(ctx context.Context, q contract.Tx, id int64, at time.Time) error
	ListGiven(ctx context.Context, q contract.Tx, delegatorID int64) ([]DelegationRec, error)
	ListReceived(ctx context.Context, q contract.Tx, delegateID int64) ([]DelegationRec, error)
	User(ctx context.Context, q contract.Tx, id int64) (UserRec, error)
	Candidates(ctx context.Context, q contract.Tx, excludeUser int64) ([]UserRec, error)
	// ActiveFrom 은 위임자 delegatorID 가 맡긴 유효한 위임(기간 안, 해제 전, 수임자 재직·결재 권한 역할)이다. excludeDelegation 위임은 없는 것으로 본다(id 순).
	ActiveFrom(ctx context.Context, q contract.Tx, delegatorID int64, now time.Time, excludeDelegation int64) ([]DelegationRec, error)
	// ActiveTo 는 수임자 delegateID 가 받은 유효한 위임(위임자 재직)이다(id 순).
	ActiveTo(ctx context.Context, q contract.Tx, delegateID int64, now time.Time) ([]DelegationRec, error)
	// B29 부재: 기간이 겹치는 부재, 알림 대상, 알림 표시.
	OverlappingAbsence(ctx context.Context, q contract.Tx, delegatorID int64, from, to time.Time) (DelegationRec, bool, error)
	NoticeStarted(ctx context.Context, q contract.Tx, now time.Time) ([]DelegationRec, error)
	NoticeEnded(ctx context.Context, q contract.Tx, now time.Time) ([]DelegationRec, error)
	MarkNotified(ctx context.Context, q contract.Tx, id int64, which string, at time.Time) (bool, error)
	// DecidedIDs 는 prID 의 현재 상신 차수에서 이미 결재(승인·반려)한 사용자 ID 다.
	DecidedIDs(ctx context.Context, q contract.Tx, prID int64) ([]int64, error)
}

// SQLDelegations 는 DelegationRepository 의 SQLite 구현이다.
type SQLDelegations struct{}

const delegationSelect = `SELECT d.id, d.delegator_id, du.name, d.delegate_id, eu.name, d.start_at, d.end_at, d.created_at, d.revoked_at, d.kind, d.reason, d.note
 FROM approval_delegations d JOIN users du ON du.id = d.delegator_id JOIN users eu ON eu.id = d.delegate_id`

func scanDelegation(s rowScanner) (DelegationRec, error) {
	var r DelegationRec
	var from, to, created string
	var revoked sql.NullString
	if err := s.Scan(&r.ID, &r.DelegatorID, &r.DelegatorName, &r.DelegateID, &r.DelegateName, &from, &to, &created, &revoked, &r.Kind, &r.Reason, &r.Note); err != nil {
		return r, err
	}
	r.From, r.To, r.CreatedAt = parseTime(from), parseTime(to), parseTime(created)
	if revoked.Valid {
		t := parseTime(revoked.String)
		r.RevokedAt = &t
	}
	return r, nil
}

func (SQLDelegations) Insert(ctx context.Context, q contract.Tx, in DelegationInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO approval_delegations (delegator_id, delegate_id, start_at, end_at, created_at, kind, reason, note) VALUES (?,?,?,?,?,?,?,?)`,
		in.DelegatorID, in.DelegateID, fmtTime(in.From), fmtTime(in.To), fmtTime(in.Now), kindOrDefault(in.Kind), in.Reason, in.Note)
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLDelegations) Get(ctx context.Context, q contract.Tx, id int64) (DelegationRec, error) {
	r, err := scanDelegation(q.QueryRowContext(ctx, delegationSelect+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// Revoke 는 아직 해제되지 않은 위임에 해제 시각을 찍는다. 이미 해제됐으면 ErrInvalidState.
func (SQLDelegations) Revoke(ctx context.Context, q contract.Tx, id int64, at time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE approval_delegations SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, fmtTime(at), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

func (SQLDelegations) list(ctx context.Context, q contract.Tx, col string, id int64) ([]DelegationRec, error) {
	rows, err := q.QueryContext(ctx, delegationSelect+` WHERE d.`+col+` = ? ORDER BY d.id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DelegationRec{}
	for rows.Next() {
		r, err := scanDelegation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r SQLDelegations) ListGiven(ctx context.Context, q contract.Tx, id int64) ([]DelegationRec, error) {
	return r.list(ctx, q, "delegator_id", id)
}

func (r SQLDelegations) ListReceived(ctx context.Context, q contract.Tx, id int64) ([]DelegationRec, error) {
	return r.list(ctx, q, "delegate_id", id)
}

func scanUser(s rowScanner) (UserRec, error) {
	var u UserRec
	var role string
	var active int
	if err := s.Scan(&u.ID, &u.Name, &role, &u.DepartmentID, &active); err != nil {
		return u, err
	}
	u.Role, u.Active = contract.Role(role), active == 1
	return u, nil
}

func (SQLDelegations) User(ctx context.Context, q contract.Tx, id int64) (UserRec, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT id, name, role, department_id, active FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, contract.ErrNotFound
	}
	if err != nil {
		return u, err
	}
	perms, err := effectivePerms(ctx, q, []int64{id})
	u.Perms = perms[id]
	return u, err
}

// Candidates 는 위임을 받을 수 있는 사용자 후보(결재 권한 approval:decide 를 가진 재직자)다. 직책 서열은 서비스가 따진다.
func (SQLDelegations) Candidates(ctx context.Context, q contract.Tx, excludeUser int64) ([]UserRec, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, role, department_id, active FROM users u
		WHERE active = 1 AND id <> ? AND EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = 'approval:decide')
		ORDER BY department_id, id`, excludeUser)
	if err != nil {
		return nil, err
	}
	out := []UserRec{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if out, err = filterByEntityHolders(ctx, q, contract.PermApprovalDecide, out, func(u UserRec) int64 { return u.ID }); err != nil {
		return nil, err
	}
	ids := make([]int64, len(out))
	for i, u := range out {
		ids[i] = u.ID
	}
	perms, err := effectivePerms(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Perms = perms[out[i].ID]
	}
	return out, nil
}

// activeWindow 는 해제되지 않았고 now 가 기간 안인 위임 조건이다. 인자: now, now.
const activeWindow = `d.revoked_at IS NULL AND d.start_at <= ? AND d.end_at > ?`

func (SQLDelegations) activeList(ctx context.Context, q contract.Tx, where string, args ...any) ([]DelegationRec, error) {
	rows, err := q.QueryContext(ctx, delegationSelect+` WHERE `+activeWindow+` AND du.active = 1 AND eu.active = 1 AND EXISTS (SELECT 1 FROM user_effective_permissions ep WHERE ep.user_id = eu.id AND ep.permission = 'approval:decide') AND `+where+` ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DelegationRec
	for rows.Next() {
		r, err := scanDelegation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r SQLDelegations) ActiveFrom(ctx context.Context, q contract.Tx, delegatorID int64, now time.Time, excludeDelegation int64) ([]DelegationRec, error) {
	n := fmtTime(now)
	return r.activeList(ctx, q, `d.delegator_id = ? AND d.id <> ?`, n, n, delegatorID, excludeDelegation)
}

func (r SQLDelegations) ActiveTo(ctx context.Context, q contract.Tx, delegateID int64, now time.Time) ([]DelegationRec, error) {
	n := fmtTime(now)
	return r.activeList(ctx, q, `d.delegate_id = ?`, n, n, delegateID)
}

func scanIDs(rows *sql.Rows, err error) ([]int64, error) {
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

func (SQLDelegations) DecidedIDs(ctx context.Context, q contract.Tx, prID int64) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT DISTINCT s.approver_id FROM approval_steps s JOIN purchase_requests p ON p.id = s.pr_id AND s.round = p.round
		WHERE `+contract.SQLEntity(ctx, "p")+` AND s.pr_id = ? AND s.approver_id IS NOT NULL`, prID))
}

// ActingCandidates 는 위임(대결)으로 열람할 수 있을 법한 진행 중 문서 번호다: 위임자가 지금 결재할 차례이거나(재무 풀 단계 포함) 내가 대결로 처리한 건.
// 최종 판정은 서비스의 canView 가 한다(이 조회는 후보를 좁힐 뿐이다).
func (SQLDelegations) ActingCandidates(ctx context.Context, q contract.Tx, userID int64, delegatorIDs []int64) ([]int64, error) {
	ph, args := idArgs(delegatorIDs)
	args = append([]any{userID, userID}, args...)
	return scanIDs(q.QueryContext(ctx, `SELECT p.id FROM purchase_requests p WHERE `+contract.SQLEntity(ctx, "p")+` AND p.status IN ('submitted','in_review') AND p.requester_id <> ? AND (
		EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.round = p.round AND s.approver_id = ? AND s.on_behalf_of IS NOT NULL)
		OR EXISTS (SELECT 1 FROM approval_steps s JOIN approval_step_current c ON c.pr_id = s.pr_id AND c.round = s.round AND c.seq = s.seq
			WHERE s.pr_id = p.id AND s.round = p.round AND s.status = 'pending' AND c.user_id IN (`+ph+`))
		OR EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.round = p.round AND s.status = 'pending' AND s.step_kind = 'finance'))
		ORDER BY p.id DESC LIMIT 500`, args...))
}

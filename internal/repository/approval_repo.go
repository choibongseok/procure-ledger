package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// HistoryInsert 는 결재 이력 한 줄 입력이다. 이력은 추가만 가능하다(수정·삭제 메서드는 없다).
type HistoryInsert struct {
	PRID    int64
	Round   int
	Seq     int
	ActorID int64
	Action  domain.ApprovalAction
	Comment string
	At      time.Time
	// OnBehalfOf 는 위임 결재일 때 위임자 ID(아니면 0).
	OnBehalfOf int64
}

// StepSnapshot 은 상신 때 저장하는 결재 단계 한 개다(B21 3.3). Assignee 가 0 이면 사람이 아니라 역할 풀(재무)이 맡는다.
type StepSnapshot struct {
	Seq          int
	Kind         domain.StepKind
	Role         string
	UnitID       int64
	UnitPath     string
	Title        string
	Acting       bool
	RaisedNote   string
	LayerNote    string
	Assignee     int64
	AssigneeName string
	Stage        int    // 0 이면 Seq 와 같다(순차). 같은 번호의 합의는 병렬이다
	Required     bool   // 기안자가 뺄 수 없는 단계
	Source       string // 결재자 지정 방식(org_level·unit_head·field_unit·fixed_user·drafter_choice)
	// B23 단계 3: 전결로 건너뛴 단계(상태 skipped 로 저장)와 전결 최종 결재 단계 표시.
	Skipped        bool
	FinalAuthority bool
}

// LineSnapshot 은 상신 한 번(차수)의 요청자 귀속 조직 스냅샷이다.
type LineSnapshot struct {
	AsOfDay           string
	RequesterUnitID   int64
	RequesterUnitPath string
	RequesterRank     string
	NotesJSON         string
	Now               time.Time
	FormVersionID     int64  // 양식 문서의 양식 버전(0 이면 없음)
	EditsJSON         string // 기안자가 편집한 결재선(JSON)
	AuthorityJSON     string // B23 단계 3: 상신 때 적용한 전결 판정(JSON, 없으면 "")
}

// StepRef 는 진행 중 결재 건의 단계 하나를 가리킨다.
type StepRef struct {
	PRID          int64
	Round         int
	Seq           int
	Kind          domain.StepKind
	Status        domain.StepStatus
	Role          string // approve | agree
	Stage         int
	RequesterID   int64
	PRTitle       string
	FormKind      string // purchase | general (소멸 목록의 문서 종류별 링크용)
	FormCode      string // 양식 코드(소멸 목록의 양식 열)
	RequesterName string // 요청자(기안자) 이름
	FormName      string
	DecidedAt     *time.Time
}

// ReassignInsert 는 관리자 결재자 재지정 기록 한 줄이다(추가 전용).
type ReassignInsert struct {
	PRID, ActorID    int64
	Round, Seq       int
	FromID, ToID     int64
	FromName, ToName string
	Cause, Reason    string
	Now              time.Time
}

// ApprovalRepository 는 결재 단계·이력 저장소다.
type ApprovalRepository interface {
	// InsertLine 은 한 차수의 결재선 스냅샷(요청자 귀속 조직, 단계, 직책 단계 배정자)을 저장한다. 첫 단계만 pending, 나머지는 waiting.
	InsertLine(ctx context.Context, q contract.Tx, prID int64, round int, snap LineSnapshot, steps []StepSnapshot) error
	PendingStep(ctx context.Context, q contract.Tx, prID int64, round int) (domain.ApprovalStep, error)
	DecideStep(ctx context.Context, q contract.Tx, prID int64, round, seq int, status domain.StepStatus, approverID, onBehalfOf int64, comment string, at time.Time) error
	ActivateStep(ctx context.Context, q contract.Tx, prID int64, round, seq int) error
	// PendingSteps 는 지금 처리할 차례인 단계 전부다(병렬 합의면 여럿).
	PendingSteps(ctx context.Context, q contract.Tx, prID int64, round int) ([]domain.ApprovalStep, error)
	// ActivateNextStage 는 대기 중인 가장 앞 단계 묶음을 pending 으로 올리고 그 묶음 번호를 돌려준다. 없으면 0.
	ActivateNextStage(ctx context.Context, q contract.Tx, prID int64, round int) (int, error)
	// SkipOpen 은 아직 처리하지 않은(pending·waiting) 단계를 건너뜀으로 바꾼다.
	SkipOpen(ctx context.Context, q contract.Tx, prID int64, round int) error
	InsertRecipients(ctx context.Context, q contract.Tx, docID int64, round int, rs []domain.DocRecipient) error
	ListRecipients(ctx context.Context, q contract.Tx, docID int64, round int) ([]domain.DocRecipient, error)
	InsertHistory(ctx context.Context, q contract.Tx, h HistoryInsert) error
	ListHistory(ctx context.Context, q contract.Tx, prID int64) ([]domain.ApprovalRecord, error)
	// StepAssignees 는 직책 단계의 현재 결재자(재지정 반영, 뷰 approval_step_current)다. 재무 단계는 비어 있다.
	StepAssignees(ctx context.Context, q contract.Tx, prID int64, round, seq int) ([]int64, error)
	// OpenSteps 는 한 건의 현재 차수에서 아직 결재가 끝나지 않은(지금 차례 + 대기) 단계를 순서대로 돌려준다.
	OpenSteps(ctx context.Context, q contract.Tx, prID int64, round int) ([]StepRef, error)
	// AssignedOpenSteps 는 userID 가 현재 결재자인, 진행 중 건의 결재 전 단계다.
	AssignedOpenSteps(ctx context.Context, q contract.Tx, userID int64) ([]StepRef, error)
	// FinanceOpenSteps 는 진행 중 건의 결재 전 재무 단계다(재무 역할 풀이 맡는다).
	FinanceOpenSteps(ctx context.Context, q contract.Tx) ([]StepRef, error)
	// AllOpenSteps 는 진행 중 건의 결재 전 단계 전부다(결재자 소멸 목록용, PR 제목 포함).
	AllOpenSteps(ctx context.Context, q contract.Tx) ([]StepRef, error)
	// DecidedOnBehalf 는 userID 가 현재 차수에서 위임으로 대신 결재한 위임자들이다.
	DecidedOnBehalf(ctx context.Context, q contract.Tx, prID int64, round int, userID int64) ([]int64, error)
	// DecidedPRs 는 userID 가 결재·합의·반려한 단계가 있는 구매요청 번호다(모든 차수, 번호 오름차순).
	DecidedPRs(ctx context.Context, q contract.Tx, userID int64) ([]int64, error)
	// InsertReassignment 는 재지정 기록을 추가한다.
	InsertReassignment(ctx context.Context, q contract.Tx, in ReassignInsert) error
}

// SQLApprovals 는 ApprovalRepository 의 SQLite 구현이다.
type SQLApprovals struct{ purchases SQLPurchases }

// InsertLine 은 스냅샷과 단계를 저장한다. 직책 단계는 배정자 행도 넣는다.
func (SQLApprovals) InsertLine(ctx context.Context, q contract.Tx, prID int64, round int, snap LineSnapshot, steps []StepSnapshot) error {
	if _, err := q.ExecContext(ctx, `INSERT INTO approval_lines (pr_id, round, as_of_day, requester_unit_id, requester_unit_path, requester_rank, notes, created_at, form_version_id, edits_json, authority_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, prID, round, snap.AsOfDay, snap.RequesterUnitID, snap.RequesterUnitPath, snap.RequesterRank, snap.NotesJSON, fmtTime(snap.Now),
		nullID(snap.FormVersionID), snap.EditsJSON, snap.AuthorityJSON); err != nil {
		return mapErr(err)
	}
	firstStage := 0
	for _, st := range steps {
		stg := st.Stage
		if stg == 0 {
			stg = st.Seq
		}
		if firstStage == 0 || stg < firstStage {
			firstStage = stg
		}
	}
	for _, st := range steps {
		stage := st.Stage
		if stage == 0 {
			stage = st.Seq
		}
		status := domain.StepWaiting
		if stage == firstStage {
			status = domain.StepPending
		}
		skipReason := ""
		if st.Skipped {
			status, skipReason = domain.StepSkipped, "전결" // 전결 규정으로 건너뛴 단계는 처음부터 건너뜀으로 저장한다
		}
		acting := 0
		if st.Acting {
			acting = 1
		}
		role := st.Role
		if role == "" {
			role = "approve"
		}
		source := st.Source
		if source == "" {
			source = "org_level"
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO approval_steps (pr_id, round, seq, step_kind, status, role, unit_id, unit_path, title, acting, raised_note, layer_note, stage, required, source, final_authority, skip_reason)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, prID, round, st.Seq, string(st.Kind), string(status), role, nullID(st.UnitID), st.UnitPath, st.Title, acting, st.RaisedNote, st.LayerNote,
			stage, boolInt(st.Required || st.Source == ""), source, boolInt(st.FinalAuthority), skipReason); err != nil {
			return mapErr(err)
		}
		if st.Assignee != 0 {
			if _, err := q.ExecContext(ctx, `INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (?,?,?,?,?)`,
				prID, round, st.Seq, st.Assignee, st.AssigneeName); err != nil {
				return mapErr(err)
			}
		}
	}
	return nil
}

// PendingStep 은 지금 결재할 차례의 단계다. 없으면 ErrInvalidState.
func (SQLApprovals) PendingStep(ctx context.Context, q contract.Tx, prID int64, round int) (domain.ApprovalStep, error) {
	steps, err := loadSteps(ctx, q, prID, round)
	if err != nil {
		return domain.ApprovalStep{}, err
	}
	for _, s := range steps {
		if s.Status == domain.StepPending {
			return s, nil
		}
	}
	return domain.ApprovalStep{}, contract.ErrInvalidState
}

// DecideStep 은 pending 단계를 결정한다. 이미 결정된 단계면 ErrInvalidState(이중 결재 방지).
func (SQLApprovals) DecideStep(ctx context.Context, q contract.Tx, prID int64, round, seq int, status domain.StepStatus, approverID, onBehalfOf int64, comment string, at time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE approval_steps SET status=?, approver_id=?, on_behalf_of=?, comment=?, decided_at=?
		WHERE pr_id=? AND round=? AND seq=? AND status='pending'`,
		string(status), approverID, nullID(onBehalfOf), comment, fmtTime(at), prID, round, seq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

// ActivateStep 은 waiting 단계를 pending 으로 올린다.
func (SQLApprovals) ActivateStep(ctx context.Context, q contract.Tx, prID int64, round, seq int) error {
	res, err := q.ExecContext(ctx, `UPDATE approval_steps SET status='pending' WHERE pr_id=? AND round=? AND seq=? AND status='waiting'`,
		prID, round, seq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

// InsertHistory 는 불변 이력을 추가한다. 자기 결재는 DB 트리거가 한 번 더 막는다.
func (SQLApprovals) InsertHistory(ctx context.Context, q contract.Tx, h HistoryInsert) error {
	_, err := q.ExecContext(ctx, `INSERT INTO approval_history (pr_id, round, seq, actor_id, on_behalf_of, action, comment, at) VALUES (?,?,?,?,?,?,?,?)`,
		h.PRID, h.Round, h.Seq, h.ActorID, nullID(h.OnBehalfOf), string(h.Action), h.Comment, fmtTime(h.At))
	return mapErr(err)
}

// ListHistory 는 PR 의 결재 이력(시간순)이다.
func (SQLApprovals) ListHistory(ctx context.Context, q contract.Tx, prID int64) ([]domain.ApprovalRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT h.id, h.pr_id, h.seq, h.actor_id, COALESCE(h.on_behalf_of,0), h.action, h.comment, h.at, COALESCE(a.name,''), COALESCE(o.name,'')
		FROM approval_history h LEFT JOIN users a ON a.id = h.actor_id LEFT JOIN users o ON o.id = h.on_behalf_of WHERE h.pr_id = ? ORDER BY h.id`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ApprovalRecord
	for rows.Next() {
		var r domain.ApprovalRecord
		var action, at string
		if err := rows.Scan(&r.ID, &r.PRID, &r.Seq, &r.ActorID, &r.OnBehalfOfID, &action, &r.Comment, &at, &r.ActorName, &r.OnBehalfOfName); err != nil {
			return nil, err
		}
		r.Action, r.At = domain.ApprovalAction(action), parseTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanStepRefs(rows *sql.Rows, err error) ([]StepRef, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepRef
	for rows.Next() {
		var r StepRef
		var kind, status string
		if err := rows.Scan(&r.PRID, &r.Round, &r.Seq, &kind, &status, &r.Role, &r.Stage, &r.RequesterID, &r.PRTitle, &r.FormKind, &r.FormCode, &r.FormName, &r.RequesterName); err != nil {
			return nil, err
		}
		r.Kind, r.Status = domain.StepKind(kind), domain.StepStatus(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

const openStepSelect = `SELECT s.pr_id, s.round, s.seq, s.step_kind, s.status, s.role, s.stage, p.requester_id, p.title,
		COALESCE((SELECT f.kind FROM approval_forms f WHERE f.id = p.form_id), 'purchase'),
		COALESCE((SELECT f.code FROM approval_forms f WHERE f.id = p.form_id), 'PUR'),
		COALESCE((SELECT f.name FROM approval_forms f WHERE f.id = p.form_id), '구매 품의서'),
		COALESCE((SELECT u.name FROM users u WHERE u.id = p.requester_id), '')
	FROM approval_steps s JOIN purchase_requests p ON p.id = s.pr_id AND p.round = s.round AND {entity_id:p}
	WHERE p.status IN ('submitted','in_review') AND s.status IN ('pending','waiting')`

// StepAssignees 는 직책 단계의 현재 결재자다.
func (SQLApprovals) StepAssignees(ctx context.Context, q contract.Tx, prID int64, round, seq int) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT user_id FROM approval_step_current WHERE pr_id = ? AND round = ? AND seq = ? ORDER BY user_id`, prID, round, seq))
}

func (SQLApprovals) OpenSteps(ctx context.Context, q contract.Tx, prID int64, round int) ([]StepRef, error) {
	return scanStepRefs(q.QueryContext(ctx, contract.EntitySQL(ctx, openStepSelect)+` AND s.pr_id = ? AND s.round = ? ORDER BY s.seq`, prID, round))
}

func (SQLApprovals) AssignedOpenSteps(ctx context.Context, q contract.Tx, userID int64) ([]StepRef, error) {
	return scanStepRefs(q.QueryContext(ctx, contract.EntitySQL(ctx, openStepSelect)+` AND EXISTS (SELECT 1 FROM approval_step_current c WHERE c.pr_id = s.pr_id AND c.round = s.round AND c.seq = s.seq AND c.user_id = ?)
		ORDER BY s.pr_id, s.seq`, userID))
}

func (SQLApprovals) FinanceOpenSteps(ctx context.Context, q contract.Tx) ([]StepRef, error) {
	return scanStepRefs(q.QueryContext(ctx, contract.EntitySQL(ctx, openStepSelect)+` AND s.step_kind = 'finance' ORDER BY s.pr_id, s.seq`))
}

func (SQLApprovals) AllOpenSteps(ctx context.Context, q contract.Tx) ([]StepRef, error) {
	return scanStepRefs(q.QueryContext(ctx, contract.EntitySQL(ctx, openStepSelect)+` ORDER BY s.pr_id, s.seq`))
}

func (SQLApprovals) DecidedOnBehalf(ctx context.Context, q contract.Tx, prID int64, round int, userID int64) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT DISTINCT on_behalf_of FROM approval_steps WHERE pr_id = ? AND round = ? AND approver_id = ? AND on_behalf_of IS NOT NULL`, prID, round, userID))
}

func (SQLApprovals) DecidedPRs(ctx context.Context, q contract.Tx, userID int64) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT DISTINCT pr_id FROM approval_steps WHERE approver_id = ? AND status IN ('approved','rejected','agreed') ORDER BY pr_id`, userID))
}

func (SQLApprovals) InsertReassignment(ctx context.Context, q contract.Tx, in ReassignInsert) error {
	_, err := q.ExecContext(ctx, `INSERT INTO approval_step_reassignments (pr_id, round, seq, from_user_id, from_name_at, to_user_id, to_name_at, cause, reason, actor_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, in.PRID, in.Round, in.Seq, in.FromID, in.FromName, in.ToID, in.ToName, in.Cause, in.Reason, in.ActorID, fmtTime(in.Now))
	return mapErr(err)
}

func (SQLApprovals) PendingSteps(ctx context.Context, q contract.Tx, prID int64, round int) ([]domain.ApprovalStep, error) {
	steps, err := loadSteps(ctx, q, prID, round)
	if err != nil {
		return nil, err
	}
	var out []domain.ApprovalStep
	for _, s := range steps {
		if s.Status == domain.StepPending {
			out = append(out, s)
		}
	}
	return out, nil
}

func (SQLApprovals) ActivateNextStage(ctx context.Context, q contract.Tx, prID int64, round int) (int, error) {
	var stage sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MIN(stage) FROM approval_steps WHERE pr_id = ? AND round = ? AND status = 'waiting'`, prID, round).Scan(&stage); err != nil {
		return 0, err
	}
	if !stage.Valid {
		return 0, nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE approval_steps SET status = 'pending' WHERE pr_id = ? AND round = ? AND stage = ? AND status = 'waiting'`, prID, round, stage.Int64); err != nil {
		return 0, err
	}
	return int(stage.Int64), nil
}

func (SQLApprovals) SkipOpen(ctx context.Context, q contract.Tx, prID int64, round int) error {
	_, err := q.ExecContext(ctx, `UPDATE approval_steps SET status = 'skipped' WHERE pr_id = ? AND round = ? AND status IN ('pending','waiting')`, prID, round)
	return err
}

func (SQLApprovals) InsertRecipients(ctx context.Context, q contract.Tx, docID int64, round int, rs []domain.DocRecipient) error {
	for _, r := range rs {
		if _, err := q.ExecContext(ctx, `INSERT INTO document_recipients (doc_id, round, kind, target_type, target_id, name_at, path_at, note) VALUES (?,?,?,?,?,?,?,?)`,
			docID, round, r.Kind, r.TargetType, r.TargetID, r.Name, r.Path, r.Note); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (SQLApprovals) ListRecipients(ctx context.Context, q contract.Tx, docID int64, round int) ([]domain.DocRecipient, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, target_type, target_id, name_at, path_at, note FROM document_recipients WHERE doc_id = ? AND round = ? ORDER BY id`, docID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DocRecipient
	for rows.Next() {
		var r domain.DocRecipient
		if err := rows.Scan(&r.Kind, &r.TargetType, &r.TargetID, &r.Name, &r.Path, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

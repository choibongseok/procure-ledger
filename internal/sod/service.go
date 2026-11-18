package sod

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 예외 유효 기간과 요청 대기 한도.
const (
	ExceptionDays  = 7
	requestKeepDay = 14 // 승인 없이 이 기간이 지난 요청은 만료로 닫는다
)

var exceptionSubjects = map[string]bool{"payment": true, "journal": true, "purchase_request": true, "card_txn": true, "tax_invoice": true}

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}
func serr(msg string) error { return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg) }

// RuleView 는 규칙 표 한 줄이다.
type RuleView struct {
	Code        string `json:"code"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	StepA       string `json:"stepA"`
	StepB       string `json:"stepB"`
	Mode        string `json:"mode"`
	Default     string `json:"default"`
	Locked      bool   `json:"locked"`
	Violations  int    `json:"violations30d"`
	Blocked     int    `json:"blocked30d"`
	HolderCount int    `json:"holders"` // hold 규칙: 두 권한을 함께 가진 사용자 수
}

// Rules 는 규칙 표(현재 모드·최근 30일 위반 건수)다(sod:read).
func (e *Engine) Rules(ctx context.Context) ([]RuleView, error) {
	if _, err := contract.Authorize(ctx, contract.PermSoDRead); err != nil {
		return nil, err
	}
	since := e.now().UTC().AddDate(0, 0, -30).Format(atLayout)
	counts := map[string][2]int{}
	rows, err := e.db.QueryContext(ctx, `SELECT rule_code, result, COUNT(*) FROM sod_violations WHERE at >= ? GROUP BY rule_code, result`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code, result string
		var n int
		if err := rows.Scan(&code, &result, &n); err != nil {
			rows.Close()
			return nil, err
		}
		c := counts[code]
		c[0] += n
		if result == "blocked" {
			c[1] += n
		}
		counts[code] = c
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	holds, err := Holds(ctx, e.db)
	if err != nil {
		return nil, err
	}
	out := make([]RuleView, 0, len(Rules))
	for _, r := range Rules {
		out = append(out, RuleView{Code: r.Code, Kind: r.Kind, Title: r.Title, StepA: r.StepA, StepB: r.StepB, Mode: ModeOf(ctx, e.db, r), Default: r.Default, Locked: r.Locked,
			Violations: counts[r.Code][0], Blocked: counts[r.Code][1], HolderCount: len(holds[r.Code])})
	}
	return out, nil
}

// SetMode 는 규칙 모드를 바꾼다(sod:manage, 로그인 세션만). 잠긴 규칙은 바꿀 수 없다. 감사 sod.rule_mode.
func (e *Engine) SetMode(ctx context.Context, rule, mode string) error {
	p, err := contract.Authorize(ctx, contract.PermSoDManage)
	if err != nil {
		return err
	}
	r, ok := RuleByCode(rule)
	if !ok {
		return contract.ErrNotFound
	}
	if mode != ModeOff && mode != ModeWarn && mode != ModeBlock {
		return verr("모드는 끔·경고·차단 중 하나여야 합니다")
	}
	if r.Locked {
		return serr("이 규칙은 코드가 이미 막고 있어 모드를 바꿀 수 없습니다")
	}
	return e.txs.WithTx(ctx, func(tx contract.Tx) error {
		old := ModeOf(ctx, tx, r)
		if old == mode {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sod_rule_settings (rule_code, mode, updated_by, updated_at) VALUES (?,?,?,?) ON CONFLICT(rule_code) DO UPDATE SET mode = excluded.mode, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			rule, mode, p.UserID, e.stamp()); err != nil {
			return err
		}
		return e.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "sod.rule_mode", TargetType: audit.TargetSoDRule, TargetID: rule, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "sod_mode", Old: old, New: mode}}, Detail: r.Title})
	})
}

// ViolationFilter 는 위반 목록 조건이다.
type ViolationFilter struct {
	Rule, Result string
	From, To     time.Time // 영 시각이면 제한 없음
	BeforeID     int64
	Limit        int
}

// ViolationView 는 위반 목록 한 줄이다.
type ViolationView struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Rule        string    `json:"rule"`
	RuleTitle   string    `json:"ruleTitle"`
	Result      string    `json:"result"`
	Mode        string    `json:"mode"`
	Kind        string    `json:"kind"`
	SubjectType string    `json:"subjectType"`
	SubjectID   int64     `json:"subjectId"`
	SubjectNo   string    `json:"subjectNo"`
	ActorID     int64     `json:"actorId"`
	ActorName   string    `json:"actorName"`
	ExceptionID int64     `json:"exceptionId,omitempty"`
	Note        string    `json:"note"`
}

// Violations 는 위반·차단·예외 통과 목록이다(sod:read, 최신순).
func (e *Engine) Violations(ctx context.Context, f ViolationFilter) ([]ViolationView, error) {
	if _, err := contract.Authorize(ctx, contract.PermSoDRead); err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	q := `SELECT v.id, v.at, v.rule_code, v.result, v.mode, v.kind, v.subject_type, v.subject_id, v.subject_no, v.actor_id, COALESCE(u.name,''), COALESCE(v.exception_id,0), v.note
		FROM sod_violations v LEFT JOIN users u ON u.id = v.actor_id WHERE 1=1`
	var args []any
	if f.Rule != "" {
		q += ` AND v.rule_code = ?`
		args = append(args, f.Rule)
	}
	if f.Result != "" {
		q += ` AND v.result = ?`
		args = append(args, f.Result)
	}
	if !f.From.IsZero() {
		q += ` AND v.at >= ?`
		args = append(args, f.From.UTC().Format(atLayout))
	}
	if !f.To.IsZero() {
		q += ` AND v.at < ?`
		args = append(args, f.To.UTC().Format(atLayout))
	}
	if f.BeforeID > 0 {
		q += ` AND v.id < ?`
		args = append(args, f.BeforeID)
	}
	q += fmt.Sprintf(` ORDER BY v.id DESC LIMIT %d`, f.Limit)
	rows, err := e.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ViolationView{}
	for rows.Next() {
		var v ViolationView
		var at string
		if err := rows.Scan(&v.ID, &at, &v.Rule, &v.Result, &v.Mode, &v.Kind, &v.SubjectType, &v.SubjectID, &v.SubjectNo, &v.ActorID, &v.ActorName, &v.ExceptionID, &v.Note); err != nil {
			return nil, err
		}
		v.At, _ = time.Parse(atLayout, at)
		if r, ok := RuleByCode(v.Rule); ok {
			v.RuleTitle = r.Title
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ExceptionView 는 예외 한 줄이다.
type ExceptionView struct {
	ID            int64      `json:"id"`
	Rule          string     `json:"rule"`
	RuleTitle     string     `json:"ruleTitle"`
	SubjectType   string     `json:"subjectType"`
	SubjectID     int64      `json:"subjectId"`
	SubjectNo     string     `json:"subjectNo"`
	UserID        int64      `json:"userId"`
	UserName      string     `json:"userName"`
	Reason        string     `json:"reason"`
	RequestedBy   int64      `json:"requestedBy"`
	RequestedName string     `json:"requestedName"`
	RequestedAt   time.Time  `json:"requestedAt"`
	ApprovedBy    int64      `json:"approvedBy,omitempty"`
	ApprovedName  string     `json:"approvedName,omitempty"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	UsedAt        *time.Time `json:"usedAt,omitempty"`
}

// Exceptions 는 예외 목록이다(sod:read, 최신순, 200건).
func (e *Engine) Exceptions(ctx context.Context) ([]ExceptionView, error) {
	if _, err := contract.Authorize(ctx, contract.PermSoDRead); err != nil {
		return nil, err
	}
	rows, err := e.db.QueryContext(ctx, `SELECT x.id, x.rule_code, x.subject_type, x.subject_id, x.subject_no, x.user_id, COALESCE(tu.name,''), x.reason, x.requested_by, COALESCE(ru.name,''), x.requested_at,
		COALESCE(x.approved_by,0), COALESCE(au.name,''), x.status, x.expires_at, x.used_at
		FROM sod_exceptions x LEFT JOIN users tu ON tu.id = x.user_id LEFT JOIN users ru ON ru.id = x.requested_by LEFT JOIN users au ON au.id = x.approved_by ORDER BY x.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExceptionView{}
	for rows.Next() {
		var v ExceptionView
		var req string
		var exp, used sql.NullString
		if err := rows.Scan(&v.ID, &v.Rule, &v.SubjectType, &v.SubjectID, &v.SubjectNo, &v.UserID, &v.UserName, &v.Reason, &v.RequestedBy, &v.RequestedName, &req, &v.ApprovedBy, &v.ApprovedName, &v.Status, &exp, &used); err != nil {
			return nil, err
		}
		v.RequestedAt, _ = time.Parse(atLayout, req)
		if exp.Valid {
			t, _ := time.Parse(atLayout, exp.String)
			v.ExpiresAt = &t
		}
		if used.Valid {
			t, _ := time.Parse(atLayout, used.String)
			v.UsedAt = &t
		}
		if r, ok := RuleByCode(v.Rule); ok {
			v.RuleTitle = r.Title
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ExceptionInput 은 예외 요청 입력이다.
type ExceptionInput struct {
	Rule        string `json:"rule"`
	SubjectType string `json:"subjectType"`
	SubjectID   int64  `json:"subjectId"`
	SubjectNo   string `json:"subjectNo"`
	UserID      int64  `json:"userId"` // 예외로 그 일을 하게 될 사람(앞 단계를 한 사람)
	Reason      string `json:"reason"`
}

func (e *Engine) audited(ctx context.Context, tx contract.Tx, p contract.Principal, action string, id int64, diff []audit.DiffItem) error {
	return e.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetSoDException, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess, Diff: diff})
}

// RequestException 은 예외를 요청한다(sod:manage). 잠긴 규칙·hold 규칙·꺼진 규칙에는 예외를 만들 수 없고 대상자 본인은 요청할 수 없다(D11). 감사 sod.exception_request.
func (e *Engine) RequestException(ctx context.Context, in ExceptionInput) (int64, error) {
	p, err := contract.Authorize(ctx, contract.PermSoDManage)
	if err != nil {
		return 0, err
	}
	r, ok := RuleByCode(in.Rule)
	if !ok || r.Kind != KindTxn {
		return 0, verr("예외를 만들 수 있는 규칙이 아닙니다")
	}
	if r.Locked {
		return 0, serr("이 규칙은 코드가 이미 막고 있어 예외를 만들 수 없습니다")
	}
	in.Reason, in.SubjectNo = strings.TrimSpace(in.Reason), strings.TrimSpace(in.SubjectNo)
	if n := len([]rune(in.Reason)); n < 10 || n > 300 {
		return 0, verr("사유는 10자 이상 300자 이하로 입력하세요")
	}
	if !exceptionSubjects[in.SubjectType] || in.SubjectID <= 0 {
		return 0, verr("예외를 적용할 거래를 지정하세요")
	}
	if in.UserID <= 0 {
		return 0, verr("예외로 처리할 사람을 지정하세요")
	}
	if in.UserID == p.UserID {
		return 0, serr("본인이 대상인 예외는 요청할 수 없습니다")
	}
	var id int64
	err = e.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND active = 1`, in.UserID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return verr("예외로 처리할 사람을 찾을 수 없습니다")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sod_exceptions WHERE rule_code = ? AND subject_type = ? AND subject_id = ? AND user_id = ? AND status IN ('requested','active')`,
			in.Rule, in.SubjectType, in.SubjectID, in.UserID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return serr("같은 거래에 대한 예외가 이미 요청되었거나 유효합니다")
		}
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO sod_exceptions (rule_code, subject_type, subject_id, subject_no, user_id, reason, requested_by, requested_at, status) VALUES (?,?,?,?,?,?,?,?,'requested')`,
			in.Rule, in.SubjectType, in.SubjectID, in.SubjectNo, in.UserID, in.Reason, p.UserID, e.stamp())
		if err != nil {
			return err
		}
		return e.audited(ctx, tx, p, "sod.exception_request", id, []audit.DiffItem{{Key: "sod_rule", New: in.Rule}, {Key: "sod_subject_type", New: in.SubjectType}, {Key: "sod_subject_id", New: fmt.Sprint(in.SubjectID)},
			{Key: "exception_user_id", New: fmt.Sprint(in.UserID)}, {Key: "exception_reason", New: in.Reason}})
	})
	return id, err
}

// DecideException 은 예외를 승인·반려한다(sod:exception_approve, 로그인 세션만). 요청자와 대상자는 승인할 수 없다(D11).
// 승인하면 7일 동안 한 번만 쓸 수 있다. 감사 sod.exception_approve·sod.exception_reject.
func (e *Engine) DecideException(ctx context.Context, id int64, approve bool, note string) error {
	p, err := contract.Authorize(ctx, contract.PermSoDExceptionApprove)
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	return e.txs.WithTx(ctx, func(tx contract.Tx) error {
		var status string
		var reqBy, target int64
		err := tx.QueryRowContext(ctx, `SELECT status, requested_by, user_id FROM sod_exceptions WHERE id = ?`, id).Scan(&status, &reqBy, &target)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "requested" {
			return serr("이미 처리된 예외입니다")
		}
		if p.UserID == reqBy || p.UserID == target {
			return serr("요청자와 예외 대상자는 승인할 수 없습니다. 다른 담당자가 처리해야 합니다")
		}
		if approve {
			exp := e.now().UTC().AddDate(0, 0, ExceptionDays).Format(atLayout)
			if _, err := tx.ExecContext(ctx, `UPDATE sod_exceptions SET status = 'active', approved_by = ?, approved_at = ?, expires_at = ? WHERE id = ?`, p.UserID, e.stamp(), exp, id); err != nil {
				return err
			}
			return e.audited(ctx, tx, p, "sod.exception_approve", id, []audit.DiffItem{{Key: "exception_expires_at", New: exp}})
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sod_exceptions SET status = 'rejected', approved_by = ?, approved_at = ? WHERE id = ?`, p.UserID, e.stamp(), id); err != nil {
			return err
		}
		diff := []audit.DiffItem{}
		if note != "" {
			diff = append(diff, audit.DiffItem{Key: "decision_note", New: note})
		}
		return e.audited(ctx, tx, p, "sod.exception_reject", id, diff)
	})
}

// RevokeException 은 아직 쓰지 않은 유효 예외를 거둔다(sod:exception_approve). 감사 sod.exception_revoke.
func (e *Engine) RevokeException(ctx context.Context, id int64) error {
	p, err := contract.Authorize(ctx, contract.PermSoDExceptionApprove)
	if err != nil {
		return err
	}
	return e.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sod_exceptions SET status = 'revoked' WHERE id = ? AND status IN ('requested','active')`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return serr("거둘 수 있는 예외가 아닙니다")
		}
		return e.audited(ctx, tx, p, "sod.exception_revoke", id, nil)
	})
}

var systemActor = contract.Principal{Role: "system", Channel: contract.ChannelWeb}

// Expire 는 기한이 지난 예외·오래 방치된 요청을 닫는다. 건수를 돌려준다.
func (e *Engine) Expire(ctx context.Context) (int, error) {
	now := e.stamp()
	cutoff := e.now().UTC().AddDate(0, 0, -requestKeepDay).Format(atLayout)
	total := 0
	err := e.txs.WithTx(ctx, func(tx contract.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM sod_exceptions WHERE (status = 'active' AND expires_at <= ?) OR (status = 'requested' AND requested_at <= ?) ORDER BY id`, now, cutoff)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE sod_exceptions SET status = 'expired' WHERE id = ?`, id); err != nil {
				return err
			}
			if err := e.audited(ctx, tx, systemActor, "sod.exception_expire", id, nil); err != nil {
				return err
			}
		}
		total = len(ids)
		return nil
	})
	return total, err
}

// Run 은 서버의 주기 작업이다(한 시간마다 만료 처리).
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := e.Expire(ctx); err != nil {
			log.Printf("sod: 예외 만료 처리 실패: %v", err)
		} else if n > 0 {
			log.Printf("sod: 예외 %d건 만료", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

package privacy

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
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 정보주체 열람 요청(B41 2단계, 명세 7절): 직원이 "내 정보를 누가 봤는지"를 요청하고, 다른 관리자가 승인하면 그 시점의 결과를 스냅샷으로 만든다.
// 결과에는 열람자의 이름이 없고 부서·역할만 담긴다(D7). 본인 요청은 본인이 처리할 수 없다(D9).
const (
	resultKeepDays = 30  // 승인한 뒤 결과를 볼 수 있는 기간
	resultMaxRows  = 500 // 스냅샷 행 상한. 넘으면 요약 한 줄
	maxPeriodDays  = 366
)

// RequestView 는 요청 한 건이다.
type RequestView struct {
	ID           int64      `json:"id"`
	UserID       int64      `json:"userId"`
	UserName     string     `json:"userName,omitempty"` // 관리자 목록에서만 채운다
	Status       string     `json:"status"`
	PeriodFrom   string     `json:"periodFrom"`
	PeriodTo     string     `json:"periodTo"`
	Reason       string     `json:"reason"`
	RequestedAt  time.Time  `json:"requestedAt"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`
	DecisionNote string     `json:"decisionNote,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

// ResultRow 는 스냅샷 한 줄이다. 열람자는 부서와 역할뿐이다.
type ResultRow struct {
	At         time.Time `json:"at"`
	ViewerUnit string    `json:"viewerUnit"`
	ViewerRole string    `json:"viewerRole"`
	Category   string    `json:"category"`
	Screen     string    `json:"screen"`
	Fields     string    `json:"fields"`
	IsExport   bool      `json:"isExport"`
	Summary    bool      `json:"summary"`
}

// SetDeliver 는 메일·웹훅 발송 건 생성기를 꽂는다(알림 서비스가 기록기보다 늦게 만들어져 조립 때 나중에 붙인다).
func (r *Recorder) SetDeliver(f func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error) {
	r.deliver = f
}

// authorizeDecider 는 열람 요청을 처리할 수 있는 사람인지 본다: privacy:request_manage, 또는 감사 대행 후보(privacy:log_read, 실제 대행 자격은 요청마다 확인).
func (r *Recorder) authorizeDecider(p contract.Principal) error {
	switch {
	case p.UserID == 0:
		return contract.ErrUnauthenticated
	case p.Can(contract.PermPrivacyRequestManage), p.Can(contract.PermPrivacyLogRead):
		return nil
	}
	return contract.ErrForbidden
}

// soleManager 는 활성 privacy:request_manage 보유자가 정확히 한 명일 때 그 사용자 번호다(아니면 0).
func (r *Recorder) soleManager(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE e.permission = ? AND u.active = 1 ORDER BY e.user_id LIMIT 2`, string(contract.PermPrivacyRequestManage))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil || len(ids) != 1 {
		return 0, err
	}
	return ids[0], nil
}

func (r *Recorder) notifyOne(ctx context.Context, tx contract.Tx, userID int64, kind, title, body, link string, refID int64) error {
	at := r.now()
	id, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: userID, Kind: kind, Title: title, Body: body, Link: link, RefType: "privacy_request", RefID: refID, Now: at})
	if err != nil {
		return err
	}
	if r.deliver != nil {
		if derr := r.deliver(ctx, tx, id, userID, kind, at); derr != nil {
			log.Printf("privacy: 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", id, derr)
		}
	}
	return nil
}

func parseDay(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", strings.TrimSpace(s), kst)
}

// CreateRequest 는 본인 정보에 대한 열람 요청을 올린다(로그인한 누구나). 열린 요청은 한 사람에 한 건이다. 관리자(privacy:request_manage)에게 알린다. 감사 privacy.request_create.
func (r *Recorder) CreateRequest(ctx context.Context, p contract.Principal, from, to, reason string) (int64, error) {
	if p.UserID == 0 {
		return 0, contract.ErrUnauthenticated
	}
	f, err := parseDay(from)
	if err != nil {
		return 0, verr("시작 날짜 형식이 올바르지 않습니다")
	}
	t, err := parseDay(to)
	if err != nil {
		return 0, verr("종료 날짜 형식이 올바르지 않습니다")
	}
	if t.Before(f) {
		return 0, verr("종료 날짜가 시작 날짜보다 앞설 수 없습니다")
	}
	if int(t.Sub(f).Hours()/24) >= maxPeriodDays {
		return 0, verr("기간은 최대 1년까지 요청할 수 있습니다")
	}
	today, _ := parseDay(r.today())
	if t.After(today) {
		return 0, verr("종료 날짜가 오늘보다 뒤일 수 없습니다")
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 200 {
		return 0, verr("사유는 200자 이하로 입력하세요")
	}
	var id int64
	err = r.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_subject_requests WHERE user_id = ? AND status = 'requested'`, p.UserID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return stateError("처리 중인 열람 요청이 있습니다. 결과가 나온 뒤 다시 요청하세요")
		}
		ms, _ := time.ParseInLocation("2006-01", r.now().In(kst).Format("2006-01"), kst) // 월 1회(명세 7.1.1): 이번 달(KST)에 반려되지 않은 요청이 이미 있으면 거부
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_subject_requests WHERE user_id = ? AND status <> 'rejected' AND requested_at >= ? AND requested_at < ?`,
			p.UserID, ms.UTC().Format(atLayout), ms.AddDate(0, 1, 0).UTC().Format(atLayout)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return stateError("열람 요청은 한 달에 한 번만 할 수 있습니다(반려된 요청은 세지 않습니다). 다음 달에 다시 요청하세요")
		}
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO privacy_subject_requests (user_id, status, period_from, period_to, reason, requested_at) VALUES (?,'requested',?,?,?,?)`,
			p.UserID, f.Format("2006-01-02"), t.Format("2006-01-02"), reason, r.now().UTC().Format(atLayout))
		if err != nil {
			return err
		}
		if err := r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.request_create", TargetType: audit.TargetPrivacyRequest, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "period_from", New: f.Format("2006-01-02")}, {Key: "period_to", New: t.Format("2006-01-02")}}}); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE e.permission = ? AND u.active = 1 AND e.user_id <> ? ORDER BY e.user_id`,
			string(contract.PermPrivacyRequestManage), p.UserID)
		if err != nil {
			return err
		}
		var admins []int64
		for rows.Next() {
			var a int64
			if err := rows.Scan(&a); err != nil {
				rows.Close()
				return err
			}
			admins = append(admins, a)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(admins) == 0 { // 처리할 관리자가 없으면(요청자가 유일한 관리자) 감사가 대신 처리한다(D9)
			arows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE e.permission = ? AND u.active = 1 AND e.user_id <> ? ORDER BY e.user_id`,
				string(contract.PermPrivacyLogRead), p.UserID)
			if err != nil {
				return err
			}
			for arows.Next() {
				var a int64
				if err := arows.Scan(&a); err != nil {
					arows.Close()
					return err
				}
				admins = append(admins, a)
			}
			if err := arows.Close(); err != nil {
				return err
			}
		}
		for _, a := range admins {
			if err := r.notifyOne(ctx, tx, a, "privacy_request_pending", "정보주체 열람 요청이 있습니다", "직원이 본인 정보의 열람 기록을 요청했습니다. 요청 화면에서 처리하세요.", "/admin/privacy/requests", id); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

const requestCols = `r.id, r.user_id, COALESCE(u.name,''), r.status, r.period_from, r.period_to, r.reason, r.requested_at, r.decided_at, r.decision_note, r.expires_at`

func (r *Recorder) scanRequests(rows *sql.Rows, withName bool) ([]RequestView, error) {
	defer rows.Close()
	now := r.now().UTC()
	out := []RequestView{}
	for rows.Next() {
		var v RequestView
		var req string
		var dec, exp sql.NullString
		var name string
		if err := rows.Scan(&v.ID, &v.UserID, &name, &v.Status, &v.PeriodFrom, &v.PeriodTo, &v.Reason, &req, &dec, &v.DecisionNote, &exp); err != nil {
			return nil, err
		}
		if withName {
			v.UserName = name
		}
		v.RequestedAt = parseAt(req)
		if dec.Valid {
			t := parseAt(dec.String)
			v.DecidedAt = &t
		}
		if exp.Valid {
			t := parseAt(exp.String)
			v.ExpiresAt = &t
			if v.Status == "approved" && !t.After(now) {
				v.Status = "expired"
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MyRequests 는 내 요청 목록이다(로그인한 누구나, 최신순).
func (r *Recorder) MyRequests(ctx context.Context, p contract.Principal) ([]RequestView, error) {
	if p.UserID == 0 {
		return nil, contract.ErrUnauthenticated
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+requestCols+` FROM privacy_subject_requests r JOIN users u ON u.id = r.user_id WHERE r.user_id = ? ORDER BY r.id DESC LIMIT 50`, p.UserID)
	if err != nil {
		return nil, err
	}
	return r.scanRequests(rows, false)
}

// ListRequests 는 처리 대기·처리 결과 목록이다(privacy:request_manage). 열람자 정보는 없다.
func (r *Recorder) ListRequests(ctx context.Context, p contract.Principal) ([]RequestView, error) {
	if err := r.authorizeDecider(p); err != nil {
		return nil, err
	}
	where, args := "", []any{}
	if !p.Can(contract.PermPrivacyRequestManage) { // 감사 대행(D9): 처리할 관리자가 없는 요청(요청자가 유일한 관리자)만 보인다
		sole, err := r.soleManager(ctx, r.db)
		if err != nil {
			return nil, err
		}
		where, args = " WHERE r.user_id = ?", []any{sole}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+requestCols+` FROM privacy_subject_requests r JOIN users u ON u.id = r.user_id`+where+` ORDER BY CASE r.status WHEN 'requested' THEN 0 ELSE 1 END, r.id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	return r.scanRequests(rows, true)
}

// DecideRequest 는 요청을 승인·반려한다(privacy:request_manage, 로그인 세션만). 본인 요청은 처리할 수 없다(D9). 반려는 사유 필수. 요청자가 유일한 관리자이면 감사(privacy:log_read)가 대신 처리한다(D9).
// 승인하면 요청 기간의 열람 기록을 스냅샷으로 만들고(열람자는 부서·역할만, 본인이 본 것은 제외) 요청자에게 알린다. 감사 privacy.request_approve·privacy.request_reject.
func (r *Recorder) DecideRequest(ctx context.Context, p contract.Principal, id int64, approve bool, note string) error {
	if err := r.authorizeDecider(p); err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return verr("처리 메모는 200자 이하로 입력하세요")
	}
	if !approve && note == "" { // 반려는 사유 필수(명세 7.1.2, AC-4111): 요청자가 결과 화면에서 사유를 본다
		return verr("반려 사유를 입력하세요")
	}
	return r.txs.WithTx(ctx, func(tx contract.Tx) error {
		var uid int64
		var status, from, to string
		err := tx.QueryRowContext(ctx, `SELECT user_id, status, period_from, period_to FROM privacy_subject_requests WHERE id = ?`, id).Scan(&uid, &status, &from, &to)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "requested" {
			return stateError("이미 처리된 요청입니다")
		}
		if uid == p.UserID {
			return stateError("본인이 올린 요청은 본인이 처리할 수 없습니다. 다른 관리자가 처리해야 합니다")
		}
		if !p.Can(contract.PermPrivacyRequestManage) { // 감사 대행(D9): 요청자가 유일한 관리자라 처리할 관리자가 없을 때만
			sole, err := r.soleManager(ctx, tx)
			if err != nil {
				return err
			}
			if sole != uid {
				return contract.ErrForbidden
			}
		}
		now := r.now().UTC()
		if !approve {
			if _, err := tx.ExecContext(ctx, `UPDATE privacy_subject_requests SET status = 'rejected', decided_by = ?, decided_at = ?, decision_note = ? WHERE id = ?`, p.UserID, now.Format(atLayout), note, id); err != nil {
				return err
			}
			if err := r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.request_reject", TargetType: audit.TargetPrivacyRequest, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess}); err != nil {
				return err
			}
			return r.notifyOne(ctx, tx, uid, "privacy_request_done", "열람 요청이 반려되었습니다", "요청이 반려되었습니다. 내 열람 요청 화면에서 사유를 확인하세요.", "/privacy/my-requests", id)
		}
		n, err := r.snapshot(ctx, tx, id, uid, from, to)
		if err != nil {
			return err
		}
		exp := now.AddDate(0, 0, resultKeepDays).Format(atLayout)
		if _, err := tx.ExecContext(ctx, `UPDATE privacy_subject_requests SET status = 'approved', decided_by = ?, decided_at = ?, decision_note = ?, expires_at = ? WHERE id = ?`, p.UserID, now.Format(atLayout), note, exp, id); err != nil {
			return err
		}
		if err := r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.request_approve", TargetType: audit.TargetPrivacyRequest, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "result_rows", New: fmt.Sprint(n)}}}); err != nil {
			return err
		}
		return r.notifyOne(ctx, tx, uid, "privacy_request_done", "열람 요청 결과가 나왔습니다", fmt.Sprintf("내 열람 요청 화면에서 결과를 확인하세요. %d일 동안 볼 수 있습니다.", resultKeepDays), "/privacy/my-requests", id)
	})
}

// snapshot 은 요청 기간(한국 시간 날짜, 양끝 포함)의 열람 기록을 결과 표로 옮긴다. 열람자는 그 시점의 소속·역할 이름만 담는다.
func (r *Recorder) snapshot(ctx context.Context, tx contract.Tx, reqID, userID int64, from, to string) (int, error) {
	f, _ := parseDay(from)
	t, _ := parseDay(to)
	lo, hi := f.UTC().Format(atLayout), t.AddDate(0, 0, 1).UTC().Format(atLayout)
	day := r.today()
	unit := strings.Replace(unitSQL, "%s", "l.actor_id", 1)
	rows, err := tx.QueryContext(ctx, `SELECT l.at, COALESCE(`+unit+`, ''), COALESCE((SELECT name FROM roles WHERE key = l.actor_role), l.actor_role), l.category, l.screen, l.fields, l.is_export
		FROM privacy_access_log l JOIN privacy_access_subjects s ON s.log_id = l.id
		WHERE s.subject_type = 'user' AND s.subject_id = ? AND l.at >= ? AND l.at < ? AND l.actor_id <> ? ORDER BY l.id LIMIT ?`,
		day, day, day, day, userID, lo, hi, userID, resultMaxRows+1)
	if err != nil {
		return 0, err
	}
	type row struct {
		at, unit, role, cat, screen, fields string
		exp                                 int
	}
	var got []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.at, &x.unit, &x.role, &x.cat, &x.screen, &x.fields, &x.exp); err != nil {
			rows.Close()
			return 0, err
		}
		got = append(got, x)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	extra := 0
	if len(got) > resultMaxRows {
		got = got[:resultMaxRows]
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_access_log l JOIN privacy_access_subjects s ON s.log_id = l.id WHERE s.subject_type = 'user' AND s.subject_id = ? AND l.at >= ? AND l.at < ? AND l.actor_id <> ?`,
			userID, lo, hi, userID).Scan(&extra); err != nil {
			return 0, err
		}
		extra -= resultMaxRows
	}
	for i, x := range got {
		if _, err := tx.ExecContext(ctx, `INSERT INTO privacy_request_results (request_id, seq, at, viewer_unit, viewer_role, category, screen, fields, is_export, summary) VALUES (?,?,?,?,?,?,?,?,?,0)`,
			reqID, i+1, x.at, x.unit, x.role, x.cat, x.screen, x.fields, x.exp); err != nil {
			return 0, err
		}
	}
	if extra > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO privacy_request_results (request_id, seq, at, viewer_unit, viewer_role, category, screen, fields, is_export, summary) VALUES (?,?,?,'','','','','[]',0,1)`,
			reqID, len(got)+1, r.now().UTC().Format(atLayout)); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE privacy_request_results SET screen = ? WHERE request_id = ? AND summary = 1`, fmt.Sprintf("이후 %d건은 생략되었습니다", extra), reqID); err != nil {
			return 0, err
		}
	}
	return len(got) + extra, nil
}

// MyResult 는 승인된 내 요청의 결과다(요청자 본인만, 만료 전). 다른 사람의 요청은 찾을 수 없음으로 응답한다.
func (r *Recorder) MyResult(ctx context.Context, p contract.Principal, id int64) ([]ResultRow, error) {
	if p.UserID == 0 {
		return nil, contract.ErrUnauthenticated
	}
	var status string
	var exp sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT status, expires_at FROM privacy_subject_requests WHERE id = ? AND user_id = ?`, id, p.UserID).Scan(&status, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, contract.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "approved" || !exp.Valid || !parseAt(exp.String).After(r.now().UTC()) {
		return nil, stateError("결과를 볼 수 있는 요청이 아닙니다(승인 전이거나 보관 기간이 지났습니다)")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT at, viewer_unit, viewer_role, category, screen, fields, is_export, summary FROM privacy_request_results WHERE request_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResultRow{}
	for rows.Next() {
		var x ResultRow
		var at string
		var exp, sum int
		if err := rows.Scan(&at, &x.ViewerUnit, &x.ViewerRole, &x.Category, &x.Screen, &x.Fields, &exp, &sum); err != nil {
			return nil, err
		}
		x.At, x.IsExport, x.Summary = parseAt(at), exp == 1, sum == 1
		out = append(out, x)
	}
	return out, rows.Err()
}

// ExpireRequests 는 보관 기간이 지난 요청의 결과 행을 지우고 상태를 만료로 바꾼다. 건수를 돌려준다.
func (r *Recorder) ExpireRequests(ctx context.Context) (int, error) {
	now := r.now().UTC().Format(atLayout)
	n := 0
	err := r.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM privacy_request_results WHERE request_id IN (SELECT id FROM privacy_subject_requests WHERE status = 'approved' AND expires_at <= ?)`, now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE privacy_subject_requests SET status = 'expired' WHERE status = 'approved' AND expires_at <= ?`, now)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

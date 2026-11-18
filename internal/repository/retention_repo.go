package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// SQLRetention 은 보존 연한 만료 문서의 목록과 폐기 저장소다(B29 ③).
// 폐기 후보 = 결재 완료(approved)·보존 기한 경과·법정 보존 아님·일반 양식·수신 처리 끝·보류 아님·미폐기·발주 연결 없음.
type SQLRetention struct{}

const (
	// 후보 조건(폐기 보류 여부만 호출하는 쪽이 붙인다). 보존 기한은 그 날까지 보존이므로 오늘보다 이전이면 후보다.
	retentionBase = ` FROM purchase_requests p
		JOIN approval_forms f ON f.id = p.form_id
		JOIN users u ON u.id = p.requester_id
		WHERE {entity_id:p} AND p.status = 'approved' AND p.disposed_at IS NULL AND p.legal_hold = 0 AND f.kind = 'general'
		  AND p.retention_until IS NOT NULL AND p.retention_until < ?
		  AND p.purchase_order_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM document_receipts r WHERE r.doc_id = p.id AND r.status IN ('pending','received','assigned'))`
	retentionCols = `SELECT p.id, p.no, p.title, f.code, f.name, u.name, COALESCE(p.completed_at,''), p.retention_until, p.disposal_hold, p.hold_reason`
)

func scanCandidate(sc interface{ Scan(...any) error }, today string) (domain.RetentionCandidate, error) {
	var c domain.RetentionCandidate
	var held int
	if err := sc.Scan(&c.ID, &c.No, &c.Title, &c.FormCode, &c.FormName, &c.RequesterName, &c.CompletedAt, &c.RetentionUntil, &held, &c.HoldReason); err != nil {
		return c, err
	}
	c.Held = held == 1
	if t, err := time.Parse(timeLayout, c.CompletedAt); err == nil {
		c.CompletedAt = t.In(kstLoc).Format("2006-01-02")
	}
	c.DaysOver = daysAfter(c.RetentionUntil, today)
	return c, nil
}

var kstLoc = time.FixedZone("KST", 9*3600)

// daysAfter 는 until(YYYY-MM-DD) 다음 날을 1 로 센 today 까지의 일수다. 아직 지나지 않았으면 0 이다.
func daysAfter(until, today string) int {
	u, err1 := time.Parse("2006-01-02", until)
	t, err2 := time.Parse("2006-01-02", today)
	if err1 != nil || err2 != nil || !t.After(u) {
		return 0
	}
	return int(t.Sub(u).Hours() / 24)
}

// Candidates 는 폐기 후보(보류 아님)다. 보존 기한이 오래 지난 순이고 limit·offset 으로 쪽을 나눈다.
func (SQLRetention) Candidates(ctx context.Context, q contract.Tx, today string, limit, offset int) ([]domain.RetentionCandidate, int, error) {
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*)`+contract.EntitySQL(ctx, retentionBase)+` AND p.disposal_hold = 0`, today).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, retentionCols+contract.EntitySQL(ctx, retentionBase)+` AND p.disposal_hold = 0 ORDER BY p.retention_until, p.id LIMIT ? OFFSET ?`, today, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.RetentionCandidate
	for rows.Next() {
		c, err := scanCandidate(rows, today)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// Holds 는 폐기 보류 중이고 아직 폐기되지 않은 문서다(기한이 아직 안 지났어도 보류는 보인다).
func (SQLRetention) Holds(ctx context.Context, q contract.Tx, today string) ([]domain.RetentionCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.no, p.title, f.code, f.name, u.name, COALESCE(p.completed_at,''), COALESCE(p.retention_until,''), p.disposal_hold, p.hold_reason
		FROM purchase_requests p JOIN approval_forms f ON f.id = p.form_id JOIN users u ON u.id = p.requester_id
		WHERE `+contract.SQLEntity(ctx, "p")+` AND p.disposal_hold = 1 AND p.disposed_at IS NULL ORDER BY p.retention_until, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RetentionCandidate
	for rows.Next() {
		c, err := scanCandidate(rows, today)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Count 는 폐기 후보 건수다.
func (SQLRetention) Count(ctx context.Context, q contract.Tx, today string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*)`+contract.EntitySQL(ctx, retentionBase)+` AND p.disposal_hold = 0`, today).Scan(&n)
	return n, err
}

// CandidateInfo 는 ids 중 지금 폐기 후보인 문서의 번호·양식·보존 기한이다(같은 트랜잭션에서 다시 판정한다). 후보가 아닌 번호는 결과에 없다.
func (SQLRetention) CandidateInfo(ctx context.Context, q contract.Tx, today string, ids []int64) (map[int64]domain.RetentionCandidate, error) {
	out := map[int64]domain.RetentionCandidate{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{today}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, retentionCols+contract.EntitySQL(ctx, retentionBase)+` AND p.disposal_hold = 0 AND p.id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCandidate(rows, today)
		if err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

// Dispose 는 문서 한 건을 폐기한다: 제목·본문·필드·의견·첨부·읽음·알림 내용을 지우고 폐기 기록을 채운다.
// 결재 이력·알림·수신 건의 의견을 비우는 갱신은 0029 의 좁은 트리거 예외로만 가능하다(disposed_at 을 먼저 채운다).
func (SQLRetention) Dispose(ctx context.Context, q contract.Tx, id, byUser int64, reason string, now time.Time) error {
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE purchase_requests SET disposed_at = ?, disposed_by = ?, disposal_reason = ?, title = '폐기된 문서', fields = '{}', search_text = '', reason = '', reject_reason = '', draft_edits = '', updated_at = ? WHERE ` + contract.SQLEntity(ctx, "purchase_requests") + ` AND id = ? AND disposed_at IS NULL`,
			[]any{fmtTime(now), byUser, reason, fmtTime(now), id}},
		// 첨부 내용·이름은 지우되 행은 남겨 "폐기된 문서의 첨부"임을 안내할 수 있게 한다(filename 이 빈 행이 폐기된 첨부다).
		{`UPDATE document_attachments SET filename = '', content_type = '', content = ?, size = 0, sha256 = '' WHERE doc_id = ? AND filename <> ''`, []any{[]byte{}, id}},
		{`DELETE FROM document_reads WHERE doc_id = ?`, []any{id}},
		{`DELETE FROM approval_nudges WHERE doc_id = ?`, []any{id}}, // 독촉 기록은 폐기 때 지운다(B29 6.3, 0032 트리거는 폐기된 문서에서만 삭제를 허용한다)
		{`UPDATE approval_history SET comment = '' WHERE pr_id = ? AND comment <> ''`, []any{id}},
		{`UPDATE approval_steps SET comment = '' WHERE pr_id = ? AND comment <> ''`, []any{id}},
		{`UPDATE document_receipts SET comment = '', reason = '' WHERE doc_id = ? AND (comment <> '' OR reason <> '')`, []any{id}},
		{`UPDATE notifications SET title = '폐기된 문서 관련 알림', body = '보존 기한이 지나 폐기된 문서에 대한 알림입니다.' WHERE ref_type = 'purchase_request' AND ref_id = ? AND (title <> '폐기된 문서 관련 알림' OR body <> '보존 기한이 지나 폐기된 문서에 대한 알림입니다.')`, []any{id}},
	}
	for i, s := range steps {
		res, err := q.ExecContext(ctx, s.sql, s.args...)
		if err != nil {
			return err
		}
		if i == 0 {
			if n, _ := res.RowsAffected(); n != 1 {
				return contract.ErrNotFound
			}
		}
	}
	// 폐기 문서는 번호·양식·기안자로만 찾아진다(명세 4.4). 검색 텍스트는 문서 저장 경로와 같은 함수 한 곳에서 만든다.
	// 사내 지식 색인(B47 D11): 폐기된 문서와 첨부의 조각은 같은 트랜잭션에서 바로 지운다(대기열에 맡기지 않는다).
	if err := PurgeApprovalDoc(ctx, q, id, now); err != nil {
		return err
	}
	return SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// SetHold 는 폐기 보류를 걸거나 푼다. 이미 폐기됐거나 그 상태이면 ErrInvalidState 다.
func (SQLRetention) SetHold(ctx context.Context, q contract.Tx, id int64, hold bool, reason string) error {
	h, want := 0, 1 // 풀기: 0 으로 바꾼다(지금 1 이어야 함)
	if hold {
		h, want = 1, 0
	}
	res, err := q.ExecContext(ctx, `UPDATE purchase_requests SET disposal_hold = ?, hold_reason = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ? AND disposed_at IS NULL AND status = 'approved' AND disposal_hold = ?`,
		h, reason, id, want)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

// DocNo 는 문서 번호와 양식 코드·보존 기한이다(감사 기록용). 없으면 ErrNotFound.
func (SQLRetention) DocNo(ctx context.Context, q contract.Tx, id int64) (domain.RetentionCandidate, error) {
	var c domain.RetentionCandidate
	err := q.QueryRowContext(ctx, `SELECT p.id, p.no, f.code, f.name, COALESCE(p.retention_until,'') FROM purchase_requests p JOIN approval_forms f ON f.id = p.form_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id).
		Scan(&c.ID, &c.No, &c.FormCode, &c.FormName, &c.RetentionUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return c, contract.ErrNotFound
	}
	return c, err
}

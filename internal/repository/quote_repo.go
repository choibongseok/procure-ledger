package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLQuotes 는 견적서 저장소다(B25 ②). 열람 범위는 구매요청(GetPR + ViewScope)이 정하고, 이 저장소는 요청 번호로만 거른다.
type SQLQuotes struct{}

// QuoteRec 은 저장된 견적서 한 줄이다(원문 텍스트는 읽을 때 따로 가져온다).
type QuoteRec struct {
	domain.Quote
	SHA256         string
	ExtractionJSON string
}

// QuoteWrite 는 견적서 신규·교체 입력이다.
type QuoteWrite struct {
	PRID           int64
	VendorID       int64
	Supplier       string
	Filename       string
	ContentType    string
	SHA256         string
	Text           string
	Status         string
	Edited         bool
	Lines          []domain.QuoteLine
	Total          int64
	DocTotal       *int64
	LeadDays       *int
	LeadText       string
	Warnings       []string
	ExtractionJSON string
	UploadedBy     int64
	Now            time.Time
}

const quoteSelect = `SELECT q.id, q.pr_id, COALESCE(q.vendor_id,0), COALESCE(v.name,''), q.supplier, q.filename, q.status, q.edited,
	q.lines_json, q.total_amount, q.doc_total_amount, q.lead_days, q.lead_text, q.warnings_json, q.sha256, q.extraction_json, q.created_at, q.updated_at
	FROM purchase_quotes q LEFT JOIN vendors v ON v.id = q.vendor_id`

func scanQuote(s rowScanner) (QuoteRec, error) {
	var r QuoteRec
	var edited int
	var linesJSON, warnJSON, created, updated string
	var docTotal, leadDays sql.NullInt64
	if err := s.Scan(&r.ID, &r.PRID, &r.VendorID, &r.VendorName, &r.Supplier, &r.Filename, &r.Status, &edited,
		&linesJSON, &r.Total, &docTotal, &leadDays, &r.LeadText, &warnJSON, &r.SHA256, &r.ExtractionJSON, &created, &updated); err != nil {
		return r, err
	}
	r.Edited = edited == 1
	_ = json.Unmarshal([]byte(linesJSON), &r.Lines)
	_ = json.Unmarshal([]byte(warnJSON), &r.Warnings)
	if r.Lines == nil {
		r.Lines = []domain.QuoteLine{}
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	if docTotal.Valid {
		r.HasDocTotal, r.DocTotal = true, docTotal.Int64
	}
	if leadDays.Valid {
		r.HasLead, r.LeadDays = true, int(leadDays.Int64)
	}
	r.Filled = r.Total > 0 && len(r.Lines) > 0
	r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
	return r, nil
}

// List 는 한 구매요청의 견적서를 올린 순서로 읽는다.
func (SQLQuotes) List(ctx context.Context, q contract.Tx, prID int64) ([]QuoteRec, error) {
	rows, err := q.QueryContext(ctx, quoteSelect+` WHERE q.pr_id = ? ORDER BY q.id`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuoteRec
	for rows.Next() {
		r, err := scanQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get 은 한 건이다. 없으면 ErrNotFound.
func (SQLQuotes) Get(ctx context.Context, q contract.Tx, id int64) (QuoteRec, error) {
	r, err := scanQuote(q.QueryRowContext(ctx, quoteSelect+` WHERE q.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

func quoteArgs(w QuoteWrite) []any {
	lines, _ := json.Marshal(w.Lines)
	warns, _ := json.Marshal(w.Warnings)
	var doc, lead any
	if w.DocTotal != nil {
		doc = *w.DocTotal
	}
	if w.LeadDays != nil {
		lead = *w.LeadDays
	}
	edited := 0
	if w.Edited {
		edited = 1
	}
	return []any{nullID(w.VendorID), w.Supplier, w.Filename, w.ContentType, w.SHA256, w.Text, w.Status, edited,
		string(lines), w.Total, doc, lead, w.LeadText, string(warns), w.ExtractionJSON}
}

// Insert 는 견적서를 더한다.
func (SQLQuotes) Insert(ctx context.Context, q contract.Tx, w QuoteWrite) (int64, error) {
	args := append([]any{w.PRID}, quoteArgs(w)...)
	args = append(args, w.UploadedBy, fmtTime(w.Now), fmtTime(w.Now))
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO purchase_quotes
		(pr_id, vendor_id, supplier, filename, content_type, sha256, text_content, status, edited, lines_json, total_amount, doc_total_amount, lead_days, lead_text, warnings_json, extraction_json, uploaded_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// Replace 는 같은 거래처의 견적서를 새 파일로 교체한다(번호와 선택은 그대로).
func (SQLQuotes) Replace(ctx context.Context, q contract.Tx, id int64, w QuoteWrite) error {
	args := append(quoteArgs(w), fmtTime(w.Now), id)
	_, err := q.ExecContext(ctx, `UPDATE purchase_quotes SET vendor_id=?, supplier=?, filename=?, content_type=?, sha256=?, text_content=?, status=?, edited=?,
		lines_json=?, total_amount=?, doc_total_amount=?, lead_days=?, lead_text=?, warnings_json=?, extraction_json=?, updated_at=? WHERE id=?`, args...)
	return mapErr(err)
}

// UpdateValues 는 사람이 고친 값(거래처·줄·합계·납기)을 저장한다. 원문·추출 결과는 그대로 둔다.
func (SQLQuotes) UpdateValues(ctx context.Context, q contract.Tx, id int64, w QuoteWrite) error {
	lines, _ := json.Marshal(w.Lines)
	warns, _ := json.Marshal(w.Warnings)
	var doc, lead any
	if w.DocTotal != nil {
		doc = *w.DocTotal
	}
	if w.LeadDays != nil {
		lead = *w.LeadDays
	}
	_, err := q.ExecContext(ctx, `UPDATE purchase_quotes SET vendor_id=?, lines_json=?, total_amount=?, doc_total_amount=?, lead_days=?, lead_text=?, warnings_json=?, edited=1, updated_at=? WHERE id=?`,
		nullID(w.VendorID), string(lines), w.Total, doc, lead, w.LeadText, string(warns), fmtTime(w.Now), id)
	return mapErr(err)
}

// Delete 는 견적서를 지운다.
func (SQLQuotes) Delete(ctx context.Context, q contract.Tx, id int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM purchase_quotes WHERE id = ?`, id)
	return mapErr(err)
}

// SetSelection 은 선택한 견적과 사유를 요청에 저장한다(quoteID 0 이면 해제하고 사유도 지운다).
func (SQLQuotes) SetSelection(ctx context.Context, q contract.Tx, prID, quoteID int64, reason string) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET quote_id = ?, quote_reason = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, nullID(quoteID), reason, prID)
	return mapErr(err)
}

// SetVendor 는 구매요청의 거래처를 바꾼다(견적 선택이 한다).
func (SQLQuotes) SetVendor(ctx context.Context, q contract.Tx, prID, vendorID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET vendor_id = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, nullID(vendorID), prID)
	return mapErr(err)
}

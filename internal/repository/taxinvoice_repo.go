package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 전자세금계산서 저장소(B43 1단계, 명세 4절). 상태 전이·접수 뒤 고정 칸·삭제 금지는 DB 트리거가 서비스 전이 표와 같은 모양으로 한 번 더 막는다(마이그레이션 0069).
// 주소·키·응답 본문은 어디에도 저장하지 않는다(전송 기록은 결과 코드만).

// SQLTaxInvoice 는 세금계산서 저장소다.
type SQLTaxInvoice struct{}

const taxCols = `t.id, t.no, t.direction, t.tax_type, t.status, t.approval_no, t.issue_date,
	t.supplier_biz_no, t.supplier_branch_no, t.supplier_name, t.supplier_rep, t.supplier_addr, t.supplier_email,
	t.buyer_biz_no, t.buyer_branch_no, t.buyer_name, t.buyer_rep, t.buyer_addr, t.buyer_email,
	t.supply_total, t.tax_total, t.total, COALESCE(t.amend_of_id,0), COALESCE((SELECT o.no FROM tax_invoices o WHERE o.entity_id = t.entity_id AND o.id = t.amend_of_id),''), COALESCE((SELECT o.approval_no FROM tax_invoices o WHERE o.entity_id = t.entity_id AND o.id = t.amend_of_id),''),
	COALESCE((SELECT a.no FROM tax_invoices a WHERE a.entity_id = t.entity_id AND a.amend_of_id = t.id AND a.status <> 'canceled' ORDER BY a.id LIMIT 1),''),
	t.amend_reason, t.charge_type, t.remark, t.source, t.provider_ref, t.attempts, t.resends, t.next_try_at, t.last_code,
	t.created_by, t.created_at, COALESCE(t.issued_by,0), t.issued_at, t.sent_at, t.accepted_at, t.canceled_at, t.cancel_reason, t.entity_id,
	COALESCE((SELECT en.name FROM entities en WHERE en.id <> t.entity_id AND en.business_no <> '' AND replace(en.business_no, '-', '') = CASE WHEN t.direction = 'sale' THEN t.buyer_biz_no ELSE t.supplier_biz_no END ORDER BY en.id LIMIT 1),'')`

func scanTax(s rowScanner) (domain.TaxInvoice, error) {
	var v domain.TaxInvoice
	var created string
	err := s.Scan(&v.ID, &v.No, &v.Direction, &v.TaxType, &v.Status, &v.ApprovalNo, &v.IssueDate,
		&v.Supplier.BizNo, &v.Supplier.BranchNo, &v.Supplier.Name, &v.Supplier.Rep, &v.Supplier.Addr, &v.Supplier.Email,
		&v.Buyer.BizNo, &v.Buyer.BranchNo, &v.Buyer.Name, &v.Buyer.Rep, &v.Buyer.Addr, &v.Buyer.Email,
		&v.SupplyTotal, &v.TaxTotal, &v.Total, &v.AmendOfID, &v.AmendOfNo, &v.AmendOfApprovalNo, &v.AmendedByNo,
		&v.AmendReason, &v.ChargeType, &v.Remark, &v.Source, &v.ProviderRef, &v.Attempts, &v.Resends, &v.NextTryAt, &v.LastCode,
		&v.CreatedBy, &created, &v.IssuedBy, &v.IssuedAt, &v.SentAt, &v.AcceptedAt, &v.CanceledAt, &v.CancelReason, &v.EntityID, &v.Intercompany)
	if err != nil {
		return v, err
	}
	v.CreatedAt = parseTime(created)
	if v.LastCode != "" {
		v.LastCodeText = domain.TaxCodeLabels[v.LastCode]
	}
	return v, nil
}

// NextSeq 는 다음 연속 번호다. 같은 번호를 두 트랜잭션이 받지 않게 PG 에서는 키 잠금을 잡는다(SQLite 는 쓰기가 직렬이다).
func (SQLTaxInvoice) NextSeq(ctx context.Context, tx contract.Tx) (int64, error) {
	if err := dbutil.AcquireKeyLock(ctx, tx, "tax-invoice-no"); err != nil {
		return 0, err
	}
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq_no),0)+1 FROM tax_invoices WHERE entity_id > 0`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Insert 는 헤더 한 줄을 쓴다(줄은 ReplaceLines). 승인번호 유일 색인 위반은 contract.ErrValidation 으로 돌아온다.
func (SQLTaxInvoice) Insert(ctx context.Context, tx contract.Tx, seq int64, v domain.TaxInvoice, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO tax_invoices (entity_id, seq_no, no, direction, tax_type, status, approval_no, issue_date,
		supplier_biz_no, supplier_branch_no, supplier_name, supplier_rep, supplier_addr, supplier_email,
		buyer_biz_no, buyer_branch_no, buyer_name, buyer_rep, buyer_addr, buyer_email,
		supply_total, tax_total, total, amend_of_id, amend_reason, charge_type, remark, source, provider_ref,
		created_by, created_at, updated_at, accepted_at)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?,?)`,
		seq, v.No, v.Direction, v.TaxType, v.Status, v.ApprovalNo, v.IssueDate,
		v.Supplier.BizNo, v.Supplier.BranchNo, v.Supplier.Name, v.Supplier.Rep, v.Supplier.Addr, v.Supplier.Email,
		v.Buyer.BizNo, v.Buyer.BranchNo, v.Buyer.Name, v.Buyer.Rep, v.Buyer.Addr, v.Buyer.Email,
		v.SupplyTotal, v.TaxTotal, v.Total, nullID(v.AmendOfID), v.AmendReason, v.ChargeType, v.Remark, v.Source, v.ProviderRef,
		v.CreatedBy, fmtTime(now), fmtTime(now), v.AcceptedAt)
	if err != nil {
		return 0, mapErr(err)
	}
	// 변경 피드(B43 3단계, D13): 생성 줄. 롤백되면 줄도 없다.
	return id, changes.Note(ctx, tx, changes.ResTaxInvoice, id, changes.OpCreated, v.Status)
}

// UpdateDraft 는 작성 중인 매출 건의 내용을 고친다(받는자·작성일·금액·종류·비고). 작성 중이 아니면 영향 행이 0 이라 오류다.
func (SQLTaxInvoice) UpdateDraft(ctx context.Context, tx contract.Tx, v domain.TaxInvoice, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE tax_invoices SET tax_type = ?, issue_date = ?, buyer_biz_no = ?, buyer_branch_no = ?, buyer_name = ?, buyer_rep = ?, buyer_addr = ?, buyer_email = ?,
		supply_total = ?, tax_total = ?, total = ?, charge_type = ?, remark = ?, amend_reason = ?, updated_at = ? WHERE `+contract.SQLEntity(ctx, "tax_invoices")+` AND id = ? AND status = 'draft'`,
		v.TaxType, v.IssueDate, v.Buyer.BizNo, v.Buyer.BranchNo, v.Buyer.Name, v.Buyer.Rep, v.Buyer.Addr, v.Buyer.Email,
		v.SupplyTotal, v.TaxTotal, v.Total, v.ChargeType, v.Remark, v.AmendReason, fmtTime(now), v.ID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 작성 중인 세금계산서만 고칠 수 있습니다", contract.ErrInvalidState)
	}
	return changes.Note(ctx, tx, changes.ResTaxInvoice, v.ID, changes.OpUpdated, domain.TaxDraft)
}

// SetSupplier 는 발행 때 공급자 스냅샷을 회사 정보로 고정한다(작성 중일 때만).
func (SQLTaxInvoice) SetSupplier(ctx context.Context, tx contract.Tx, id int64, p domain.TaxParty) error {
	_, err := tx.ExecContext(ctx, `UPDATE tax_invoices SET supplier_biz_no = ?, supplier_branch_no = ?, supplier_name = ?, supplier_rep = ?, supplier_addr = ?, supplier_email = ? WHERE `+contract.SQLEntity(ctx, "tax_invoices")+` AND id = ? AND status = 'draft'`,
		p.BizNo, p.BranchNo, p.Name, p.Rep, p.Addr, p.Email, id)
	return err
}

// ReplaceLines 는 품목 줄을 통째로 바꾼다(작성 중일 때만: 트리거가 막는다).
func (SQLTaxInvoice) ReplaceLines(ctx context.Context, tx contract.Tx, id int64, lines []domain.TaxLine) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tax_invoice_lines WHERE invoice_id = ?`, id); err != nil {
		return mapErr(err)
	}
	for i, l := range lines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tax_invoice_lines (invoice_id, seq, day, name, spec, qty, unit_price, supply, tax, remark) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, i+1, l.Day, l.Name, l.Spec, l.Qty, l.UnitPrice, l.Supply, l.Tax, l.Remark); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// Transition 은 상태를 from → to 로 바꾼다(조건부 갱신: 이미 다른 상태면 영향 행 0 → 충돌). sets 는 "col = ?" 조각이고 args 가 뒤따른다.
func (SQLTaxInvoice) Transition(ctx context.Context, tx contract.Tx, id int64, from, to string, now time.Time, sets string, args ...any) error {
	if sets != "" {
		sets = ", " + sets
	}
	q := `UPDATE tax_invoices SET status = ?, updated_at = ?` + sets + ` WHERE ` + contract.SQLEntity(ctx, "") + ` AND id = ? AND status = ?`
	all := append([]any{to, fmtTime(now)}, args...)
	all = append(all, id, from)
	res, err := tx.ExecContext(ctx, q, all...)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 세금계산서의 상태가 이미 바뀌었습니다", contract.ErrInvalidState)
	}
	// 변경 피드: 상태 변경 줄(발행 대기·전송됨·국세청 수신·실패·취소).
	return changes.Note(ctx, tx, changes.ResTaxInvoice, id, changes.OpStatusChanged, to)
}

// Exec 는 상태를 바꾸지 않는 칸 갱신(시도 횟수·다음 시도 시각 등)이다. 상태가 같은 건에만 적용한다.
func (SQLTaxInvoice) Exec(ctx context.Context, tx contract.Tx, id int64, status string, now time.Time, sets string, args ...any) error {
	all := append(append([]any{fmtTime(now)}, args...), id, status)
	res, err := tx.ExecContext(ctx, `UPDATE tax_invoices SET updated_at = ?, `+sets+` WHERE `+contract.SQLEntity(ctx, "")+` AND id = ? AND status = ?`, all...)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 세금계산서의 상태가 이미 바뀌었습니다", contract.ErrInvalidState)
	}
	return nil
}

// AddTransmission 은 대행사 호출 기록 한 줄을 더한다(추가 전용).
func (SQLTaxInvoice) AddTransmission(ctx context.Context, tx contract.Tx, id int64, attempt int, kind, code string, httpStatus int, ref string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tax_invoice_transmissions (invoice_id, attempt_no, at, kind, result_code, http_status, provider_ref) VALUES (?,?,?,?,?,?,?)`,
		id, attempt, fmtTime(now), kind, code, httpStatus, ref)
	return err
}

// Get 은 한 건(줄·전송 기록 포함)이다. 없으면 ErrNotFound.
func (r SQLTaxInvoice) Get(ctx context.Context, q contract.Tx, id int64) (domain.TaxInvoice, error) {
	v, err := scanTax(q.QueryRowContext(ctx, `SELECT `+taxCols+` FROM tax_invoices t WHERE `+contract.SQLEntity(ctx, "t")+` AND t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if v.Lines, err = r.lines(ctx, q, id); err != nil {
		return v, err
	}
	v.Transmissions, err = r.transmissions(ctx, q, id)
	return v, err
}

func (SQLTaxInvoice) lines(ctx context.Context, q contract.Tx, id int64) ([]domain.TaxLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, day, name, spec, qty, unit_price, supply, tax, remark FROM tax_invoice_lines WHERE invoice_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaxLine{}
	for rows.Next() {
		var l domain.TaxLine
		if err := rows.Scan(&l.Seq, &l.Day, &l.Name, &l.Spec, &l.Qty, &l.UnitPrice, &l.Supply, &l.Tax, &l.Remark); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (SQLTaxInvoice) transmissions(ctx context.Context, q contract.Tx, id int64) ([]domain.TaxTransmission, error) {
	rows, err := q.QueryContext(ctx, `SELECT attempt_no, at, kind, result_code, http_status, provider_ref FROM tax_invoice_transmissions WHERE invoice_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaxTransmission{}
	for rows.Next() {
		var t domain.TaxTransmission
		var at string
		if err := rows.Scan(&t.AttemptNo, &at, &t.Kind, &t.Code, &t.HTTPStatus, &t.ProviderRef); err != nil {
			return nil, err
		}
		t.At, t.CodeLabel = parseTime(at), domain.TaxCodeLabels[t.Code]
		out = append(out, t)
	}
	return out, rows.Err()
}

// IDByApproval 은 승인번호로 건을 찾는다(없으면 0).
func (SQLTaxInvoice) IDByApproval(ctx context.Context, q contract.Tx, approvalNo string) (int64, string, error) {
	var id int64
	var no string
	err := q.QueryRowContext(ctx, `SELECT id, no FROM tax_invoices WHERE entity_id > 0 AND approval_no = ?`, approvalNo).Scan(&id, &no)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return id, no, err
}

// List 는 목록(작성일 내림차순, 같으면 번호 내림차순)과 조건에 맞는 전체 건수다. 줄·전송 기록은 싣지 않는다.
func (SQLTaxInvoice) List(ctx context.Context, q contract.Tx, f domain.TaxInvoiceFilter) ([]domain.TaxInvoice, int, error) {
	var conds []string
	var args []any
	add := func(c string, a ...any) { conds = append(conds, c); args = append(args, a...) }
	if f.Direction != "" {
		add(`t.direction = ?`, f.Direction)
	}
	if f.Status != "" {
		add(`t.status = ?`, f.Status)
	}
	if f.From != "" {
		add(`t.issue_date >= ?`, f.From)
	}
	if f.To != "" {
		add(`t.issue_date <= ?`, f.To)
	}
	if f.ApprovalNo != "" {
		add(`t.approval_no = ?`, f.ApprovalNo)
	}
	if f.Counterparty != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Counterparty) + "%"
		add(`((t.direction = 'sale' AND t.buyer_name LIKE ? ESCAPE '\') OR (t.direction = 'purchase' AND t.supplier_name LIKE ? ESCAPE '\'))`, like, like)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := q.QueryRowContext(ctx, contract.EntitySQL(ctx, `SELECT COUNT(*) FROM (SELECT * FROM tax_invoices WHERE {entity_id}) t`)+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, `SELECT `+taxCols+` FROM (SELECT * FROM tax_invoices WHERE {entity_id}) t`)+where+` ORDER BY t.issue_date DESC, t.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.TaxInvoice{}
	for rows.Next() {
		v, err := scanTax(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// ClaimDue 는 보낼 때가 된 건의 번호를 조건부 갱신으로 가져간다: issued(발행 요청)와 sent(수신 조회) 중 next_try_at 이 지났고
// 아직 가져가지 않았거나 15분 넘게 끝나지 않은 건. 가져간 건수만큼만 돌려준다(다중 프로세스 안전).
func (SQLTaxInvoice) ClaimDue(ctx context.Context, tx contract.Tx, now time.Time, limit int) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tax_invoices WHERE `+contract.SQLEntity(ctx, "tax_invoices")+` AND status IN ('issued','sent') AND next_try_at <> '' AND next_try_at <= ? AND (claimed_at = '' OR claimed_at < ?) ORDER BY id LIMIT ?`,
		fmtTime(now), fmtTime(now.Add(-15*time.Minute)), limit)
	if err != nil {
		return nil, err
	}
	var cand []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		cand = append(cand, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var got []int64
	for _, id := range cand {
		res, err := tx.ExecContext(ctx, `UPDATE tax_invoices SET claimed_at = ? WHERE `+contract.SQLEntity(ctx, "tax_invoices")+` AND id = ? AND status IN ('issued','sent') AND (claimed_at = '' OR claimed_at < ?)`,
			fmtTime(now), id, fmtTime(now.Add(-15*time.Minute)))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			got = append(got, id)
		}
	}
	return got, nil
}

package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLAuthority 는 전결 규정 표(authority_rules)의 SQLite 구현이다(B23 단계 3).
type SQLAuthority struct{}

const authoritySelect = `SELECT r.id, COALESCE(f.code,''), COALESCE(f.name,''), r.min_amount, COALESCE(r.max_amount,0), r.final_level, r.policy_slug, r.clause_no, r.active
	FROM authority_rules r LEFT JOIN approval_forms f ON f.id = r.form_id`

func scanAuthority(rows *sql.Rows) ([]domain.AuthorityRule, error) {
	defer rows.Close()
	var out []domain.AuthorityRule
	for rows.Next() {
		var r domain.AuthorityRule
		var active int
		if err := rows.Scan(&r.ID, &r.FormCode, &r.FormName, &r.MinAmount, &r.MaxAmount, &r.FinalLevel, &r.PolicySlug, &r.ClauseNo, &active); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// List 는 규정 전부다(양식 지정 규정이 먼저, 금액 오름차순).
func (SQLAuthority) List(ctx context.Context, q contract.Tx) ([]domain.AuthorityRule, error) {
	rows, err := q.QueryContext(ctx, authoritySelect+` ORDER BY (r.form_id IS NULL), f.code, r.min_amount, r.id`)
	if err != nil {
		return nil, err
	}
	return scanAuthority(rows)
}

// Get 은 규정 한 건이다. 없으면 ErrNotFound.
func (a SQLAuthority) Get(ctx context.Context, q contract.Tx, id int64) (domain.AuthorityRule, error) {
	rows, err := q.QueryContext(ctx, authoritySelect+` WHERE r.id = ?`, id)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	list, err := scanAuthority(rows)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	if len(list) == 0 {
		return domain.AuthorityRule{}, contract.ErrNotFound
	}
	return list[0], nil
}

// Find 는 양식과 금액에 맞는 사용 중 규정이다. 양식 지정 규정이 전체 규정보다 먼저다. 없으면 ErrNotFound.
func (SQLAuthority) Find(ctx context.Context, q contract.Tx, formID, amount int64) (domain.AuthorityRule, error) {
	rows, err := q.QueryContext(ctx, authoritySelect+` WHERE r.active = 1 AND (r.form_id = ? OR r.form_id IS NULL)
		AND r.min_amount <= ? AND (r.max_amount IS NULL OR ? < r.max_amount) ORDER BY (r.form_id IS NULL), r.min_amount DESC, r.id LIMIT 1`, formID, amount, amount)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	list, err := scanAuthority(rows)
	if err != nil {
		return domain.AuthorityRule{}, err
	}
	if len(list) == 0 {
		return domain.AuthorityRule{}, contract.ErrNotFound
	}
	return list[0], nil
}

func nullAmount(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

// Insert 는 규정을 더한다. formID 0 이면 전체 양식.
func (SQLAuthority) Insert(ctx context.Context, q contract.Tx, formID int64, in domain.AuthorityRuleInput, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO authority_rules (form_id, min_amount, max_amount, final_level, policy_slug, clause_no, active, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, nullID(formID), in.MinAmount, nullAmount(in.MaxAmount), in.FinalLevel, in.PolicySlug, in.ClauseNo, boolInt(in.Active), fmtTime(now), fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// Update 는 규정을 고친다.
func (SQLAuthority) Update(ctx context.Context, q contract.Tx, formID int64, in domain.AuthorityRuleInput, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE authority_rules SET form_id = ?, min_amount = ?, max_amount = ?, final_level = ?, policy_slug = ?, clause_no = ?, active = ?, updated_at = ? WHERE id = ?`,
		nullID(formID), in.MinAmount, nullAmount(in.MaxAmount), in.FinalLevel, in.PolicySlug, in.ClauseNo, boolInt(in.Active), fmtTime(now), in.ID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return contract.ErrNotFound
	}
	return nil
}

// Delete 는 규정을 지운다. 진행 중 문서는 스냅샷이라 영향이 없다.
func (SQLAuthority) Delete(ctx context.Context, q contract.Tx, id int64) error {
	res, err := q.ExecContext(ctx, `DELETE FROM authority_rules WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return contract.ErrNotFound
	}
	return nil
}

// FormIDByCode 는 양식 코드의 번호다. 없으면 ErrNotFound.
func (SQLAuthority) FormIDByCode(ctx context.Context, q contract.Tx, code string) (int64, string, error) {
	var id int64
	var kind string
	err := q.QueryRowContext(ctx, `SELECT id, kind FROM approval_forms WHERE code = ?`, code).Scan(&id, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", contract.ErrNotFound
	}
	return id, kind, err
}

// AuthorityJSON 은 문서의 현재 차수에 저장된 전결 판정 JSON 이다(없으면 "").
func (SQLAuthority) AuthorityJSON(ctx context.Context, q contract.Tx, prID int64, round int) (string, error) {
	var s string
	err := q.QueryRowContext(ctx, `SELECT authority_json FROM approval_lines WHERE pr_id = ? AND round = ?`, prID, round).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

var _ = domain.AuthorityRule{}

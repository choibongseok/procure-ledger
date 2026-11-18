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

// FormRow 는 approval_forms 한 행이다.
type FormRow struct {
	ID                 int64
	Code               string
	Category           string
	Name               string
	Kind               domain.FormKind
	Active             bool
	RequiredPermission string
	Searchable         bool // 내용 검색에 포함(B47, 기본 켬)
}

// VersionRow 는 approval_form_versions 한 행이다(정의는 풀어서 담는다).
type VersionRow struct {
	ID          int64
	FormID      int64
	FormCode    string
	Version     int
	Status      string
	Definition  domain.FormDefinition
	CreatedAt   string
	PublishedAt string
}

// SQLForms 는 양식 저장소다(B23). 게시된 버전의 불변성은 DB 트리거가 최후로 막는다.
type SQLForms struct{}

const formCols = `id, code, category, name, kind, active, required_permission, searchable`

func scanForm(s rowScanner) (FormRow, error) {
	var f FormRow
	var kind string
	var active, searchable int
	err := s.Scan(&f.ID, &f.Code, &f.Category, &f.Name, &kind, &active, &f.RequiredPermission, &searchable)
	f.Kind, f.Active, f.Searchable = domain.FormKind(kind), active == 1, searchable == 1
	return f, err
}

// Forms 는 모든 양식이다(id 순).
func (SQLForms) Forms(ctx context.Context, q contract.Tx) ([]FormRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+formCols+` FROM approval_forms ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FormRow
	for rows.Next() {
		f, err := scanForm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FormByCode 는 코드로 찾는다. 없으면 contract.ErrNotFound.
func (SQLForms) FormByCode(ctx context.Context, q contract.Tx, code string) (FormRow, error) {
	f, err := scanForm(q.QueryRowContext(ctx, `SELECT `+formCols+` FROM approval_forms WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return f, contract.ErrNotFound
	}
	return f, err
}

func scanVersion(s rowScanner) (VersionRow, error) {
	var v VersionRow
	var def string
	var pub sql.NullString
	if err := s.Scan(&v.ID, &v.FormID, &v.FormCode, &v.Version, &v.Status, &def, &v.CreatedAt, &pub); err != nil {
		return v, err
	}
	v.PublishedAt = pub.String
	if err := json.Unmarshal([]byte(def), &v.Definition); err != nil {
		return v, err
	}
	return v, nil
}

const versionCols = `v.id, v.form_id, f.code, v.version, v.status, v.definition, v.created_at, v.published_at`

// LatestPublished 는 양식의 최신 게시 버전이다. 없으면 contract.ErrNotFound.
func (SQLForms) LatestPublished(ctx context.Context, q contract.Tx, formID int64) (VersionRow, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, `SELECT `+versionCols+` FROM approval_form_versions v JOIN approval_forms f ON f.id = v.form_id
		WHERE v.form_id = ? AND v.status = 'published' ORDER BY v.version DESC LIMIT 1`, formID))
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	return v, err
}

// VersionByID 는 버전 한 개다. 없으면 contract.ErrNotFound.
func (SQLForms) VersionByID(ctx context.Context, q contract.Tx, id int64) (VersionRow, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, `SELECT `+versionCols+` FROM approval_form_versions v JOIN approval_forms f ON f.id = v.form_id WHERE v.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	return v, err
}

// VersionSummary 는 관리 목록용 요약이다(최신 번호·상태, 게시 버전 수, 초안 유무).
func (SQLForms) VersionSummary(ctx context.Context, q contract.Tx, formID int64) (latest int, latestStatus string, published int, hasDraft bool, err error) {
	rows, err := q.QueryContext(ctx, `SELECT version, status FROM approval_form_versions WHERE form_id = ? ORDER BY version`, formID)
	if err != nil {
		return 0, "", 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var st string
		if err := rows.Scan(&v, &st); err != nil {
			return 0, "", 0, false, err
		}
		latest, latestStatus = v, st
		switch st {
		case "published":
			published++
		case "draft":
			hasDraft = true
		}
	}
	return latest, latestStatus, published, hasDraft, rows.Err()
}

// InsertDraft 는 새 초안 버전을 만든다(번호는 가장 큰 버전 + 1).
func (SQLForms) InsertDraft(ctx context.Context, q contract.Tx, formID int64, def domain.FormDefinition, now time.Time) (int64, error) {
	b, err := json.Marshal(def)
	if err != nil {
		return 0, err
	}
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO approval_form_versions (form_id, version, status, definition, created_at)
		VALUES (?, (SELECT COALESCE(MAX(version),0)+1 FROM approval_form_versions WHERE form_id = ?), 'draft', ?, ?)`,
		formID, formID, string(b), fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// UpdateDraft 는 초안의 내용을 바꾼다. 게시된 버전이면 DB 트리거가 거부한다.
func (SQLForms) UpdateDraft(ctx context.Context, q contract.Tx, id int64, def domain.FormDefinition) error {
	b, err := json.Marshal(def)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE approval_form_versions SET definition = ? WHERE id = ?`, string(b), id)
	return mapErr(err)
}

// Publish 는 초안을 게시하고 같은 양식의 이전 게시 버전을 퇴역시킨다.
func (SQLForms) Publish(ctx context.Context, q contract.Tx, formID, id int64, now time.Time) error {
	if _, err := q.ExecContext(ctx, `UPDATE approval_form_versions SET status = 'retired' WHERE form_id = ? AND status = 'published'`, formID); err != nil {
		return mapErr(err)
	}
	_, err := q.ExecContext(ctx, `UPDATE approval_form_versions SET status = 'published', published_at = ? WHERE id = ? AND status = 'draft'`, fmtTime(now), id)
	return mapErr(err)
}

// SetActive 는 양식의 사용 여부를 바꾼다.
func (SQLForms) SetActive(ctx context.Context, q contract.Tx, formID int64, active bool) error {
	_, err := q.ExecContext(ctx, `UPDATE approval_forms SET active = ? WHERE id = ?`, boolInt(active), formID)
	return err
}

// NextSeq 는 범위 (양식, 부서 조직, 연도)의 일련번호를 하나 올려 돌려준다. 같은 쓰기 트랜잭션 안에서만 부른다.
func (SQLForms) NextSeq(ctx context.Context, q contract.Tx, formID, scopeUnitID int64, year int) (int, error) {
	n, err := NextDocSeq(ctx, q, contract.WriteEntityID(ctx), formID, scopeUnitID, year) // 법인별 카운터(B44)
	return int(n), err
}

// PeekSeq 는 다음에 쓸 일련번호(마지막 + 1)를 올리지 않고 읽는다(번호 규칙 미리보기).
func (SQLForms) PeekSeq(ctx context.Context, q contract.Tx, formID, scopeUnitID int64, year int) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT last_seq FROM document_counters WHERE entity_id = ? AND form_id = ? AND scope_unit_id = ? AND year = ?`, contract.WriteEntityID(ctx), formID, scopeUnitID, year).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	return n + 1, err
}

// LatestDraft 는 양식의 작성 중 버전이다. 없으면 contract.ErrNotFound.
func (SQLForms) LatestDraft(ctx context.Context, q contract.Tx, formID int64) (VersionRow, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, `SELECT `+versionCols+` FROM approval_form_versions v JOIN approval_forms f ON f.id = v.form_id
		WHERE v.form_id = ? AND v.status = 'draft' ORDER BY v.version DESC LIMIT 1`, formID))
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	return v, err
}

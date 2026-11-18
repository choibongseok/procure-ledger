package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// PersonalLineService 는 개인 결재선(B23 명세 3.9)이다. 본인 것만 보고 고친다(남의 것은 찾을 수 없음).
type PersonalLineService struct{ Base }

// NewPersonalLineService 는 개인 결재선 서비스를 만든다.
func NewPersonalLineService(b Base) *PersonalLineService { return &PersonalLineService{Base: b} }

var _ domain.PersonalLineService = (*PersonalLineService)(nil)

const (
	maxPersonalLines = 30
	maxPersonalName  = 40
)

func (s *PersonalLineService) List(ctx context.Context) ([]domain.PersonalLine, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, form_code, edits_json FROM personal_lines WHERE user_id = ? ORDER BY name`, p.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PersonalLine{}
	for rows.Next() {
		var l domain.PersonalLine
		var raw string
		if err := rows.Scan(&l.ID, &l.Name, &l.FormCode, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &l.Edits)
		out = append(out, l)
	}
	return out, rows.Err()
}

func checkPersonalName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > maxPersonalName {
		return "", validation("결재선 이름은 1자 이상 %d자 이하여야 합니다", maxPersonalName)
	}
	return n, nil
}

// Save 는 새 개인 결재선을 저장한다. 이름은 사용자 안에서 유일하고 최대 30개다. 허용하는 역할만 저장한다(합의·참조·수신, 결재선 자동 단계는 저장하지 않는다).
func (s *PersonalLineService) Save(ctx context.Context, in domain.PersonalLineInput) (domain.PersonalLine, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "personal_line.save", audit.TargetPersonalLine)
	if err != nil {
		return domain.PersonalLine{}, err
	}
	name, err := checkPersonalName(in.Name)
	if err != nil {
		return domain.PersonalLine{}, err
	}
	for _, e := range in.Edits {
		switch e.Role {
		case "agree", "cc", "receive", "approve", "remove":
		default:
			return domain.PersonalLine{}, validation("알 수 없는 결재선 역할입니다(%s)", e.Role)
		}
	}
	raw, _ := json.Marshal(in.Edits)
	var out domain.PersonalLine
	err = s.write(ctx, p, "personal_line.save", audit.TargetPersonalLine, 0, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_lines WHERE user_id = ?`, p.UserID).Scan(&n); err != nil {
			return err
		}
		var existing int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM personal_lines WHERE user_id = ? AND name = ?`, p.UserID, name).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := s.now().Format("2006-01-02T15:04:05Z")
		if existing != 0 {
			// 같은 이름은 덮어쓴다(수정).
			// 양식 지정은 입력이 비어 있으면 기존 값을 보존한다(빈 값으로 지우지 않는다).
			if _, err := tx.ExecContext(ctx, `UPDATE personal_lines SET form_code = CASE WHEN ? = '' THEN form_code ELSE ? END, edits_json = ?, updated_at = ? WHERE id = ?`, in.FormCode, in.FormCode, string(raw), now, existing); err != nil {
				return err
			}
			var kept string
			_ = tx.QueryRowContext(ctx, `SELECT form_code FROM personal_lines WHERE id = ?`, existing).Scan(&kept)
			out = domain.PersonalLine{ID: existing, Name: name, FormCode: kept, Edits: in.Edits}
		} else {
			if n >= maxPersonalLines {
				return validation("개인 결재선은 %d개까지 저장할 수 있습니다", maxPersonalLines)
			}
			id, err := dbutil.InsertID(ctx, tx, `INSERT INTO personal_lines (user_id, name, form_code, edits_json, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
				p.UserID, name, in.FormCode, string(raw), now, now)
			if err != nil {
				return err
			}
			out = domain.PersonalLine{ID: id, Name: name, FormCode: in.FormCode, Edits: in.Edits}
		}
		return s.record(ctx, tx, p, "personal_line.save", audit.TargetPersonalLine, out.ID,
			audit.DiffItem{Key: "name", New: name}, audit.DiffItem{Key: "form_code", New: in.FormCode}, audit.DiffItem{Key: "steps", New: fmt.Sprint(len(in.Edits))})
	})
	return out, err
}

// Delete 는 개인 결재선을 지운다(본인 것만, 남의 것은 찾을 수 없음).
func (s *PersonalLineService) Delete(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "personal_line.delete", audit.TargetPersonalLine)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "personal_line.delete", audit.TargetPersonalLine, id, func(tx contract.Tx) error {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM personal_lines WHERE id = ? AND user_id = ?`, id, p.UserID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM personal_lines WHERE id = ?`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "personal_line.delete", audit.TargetPersonalLine, id, audit.DiffItem{Key: "name", Old: name})
	})
}

// Load 는 개인 결재선을 불러온다. 대상자가 퇴사·비활성이면 그 줄을 빼고 안내를 돌려준다(필수 단계는 불러오기로 바뀌지 않는다).
func (s *PersonalLineService) Load(ctx context.Context, id int64) (domain.PersonalLine, []string, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		return domain.PersonalLine{}, nil, err
	}
	var l domain.PersonalLine
	var raw string
	err = s.DB.QueryRowContext(ctx, `SELECT id, name, form_code, edits_json FROM personal_lines WHERE id = ? AND user_id = ?`, id, p.UserID).Scan(&l.ID, &l.Name, &l.FormCode, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil, contract.ErrNotFound
	}
	if err != nil {
		return l, nil, err
	}
	var edits []domain.LineEdit
	_ = json.Unmarshal([]byte(raw), &edits)
	kept, notes := s.pruneEdits(ctx, edits)
	l.Edits = kept
	return l, notes, nil
}

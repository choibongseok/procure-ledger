package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 설치 마법사(B38)가 첫 관리자를 만드는 데 쓰는 함수들이다. 마법사 서비스(internal/setup)가 한 트랜잭션 안에서 부른다.
// 이 함수들은 감사를 남기지 않는다(마법사의 setup.complete 한 건이 같은 트랜잭션에서 남긴다).

// FirstAdminInput 은 마법사의 "첫 관리자" 입력이다.
type FirstAdminInput struct {
	Name     string
	Email    string
	Password string
}

// FieldError 는 필드 하나의 입력 오류다. contract.ErrValidation 이다.
type FieldError struct{ Field, Msg string }

func (e FieldError) Error() string { return e.Msg }
func (e FieldError) Unwrap() error { return contract.ErrValidation }

// ValidateFirstAdminAll 은 입력을 정리(공백 제거, 이메일 소문자)하고 필드별 오류(name·email·password → 문구)를 모두 돌려준다. 오류가 없으면 nil 이다.
// 비밀번호는 기본 정책(10자 이상 등)을 따르고, 비밀번호 값은 오류 문구에 넣지 않는다.
func ValidateFirstAdminAll(in *FirstAdminInput) map[string]string {
	in.Name, in.Email = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Email))
	errs := map[string]string{}
	switch {
	case in.Name == "":
		errs["name"] = "이름을 입력하세요."
	case utf8.RuneCountInString(in.Name) > 50:
		errs["name"] = "이름은 50자 이내로 입력하세요."
	}
	switch {
	case in.Email == "":
		errs["email"] = "이메일을 입력하세요."
	case len(in.Email) > 254:
		errs["email"] = "이메일은 254자 이내로 입력하세요."
	default:
		if a, err := mail.ParseAddress(in.Email); err != nil || a.Address != in.Email || a.Name != "" || !strings.Contains(in.Email[strings.Index(in.Email, "@"):], ".") {
			errs["email"] = "이메일 형식이 올바르지 않습니다."
		}
	}
	if err := DefaultPasswordPolicy().Check(in.Password, in.Email, in.Name); err != nil {
		errs["password"] = err.Error()
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ValidateFirstAdmin 은 ValidateFirstAdminAll 의 첫 오류를 FieldError 로 돌려준다(이름 → 이메일 → 비밀번호 순).
func ValidateFirstAdmin(in *FirstAdminInput) error {
	errs := ValidateFirstAdminAll(in)
	for _, f := range []string{"name", "email", "password"} {
		if m, ok := errs[f]; ok {
			return FieldError{f, m}
		}
	}
	return nil
}

// HashPassword 는 비밀번호를 argon2id 로 해시한다(트랜잭션 밖에서 불러 연결을 오래 잡지 않게 한다).
func (s *Service) HashPassword(ctx context.Context, password string) (string, error) {
	return s.hasher.Hash(ctx, password)
}

// CreateFirstAdminTx 는 tx 안에서 첫 관리자를 만든다: 역할 admin 의 활성 사용자, 이메일 로그인 자격(첫 로그인에서 비밀번호 변경은 강제하지 않는다),
// 비상 관리자 지정. 역할 부여와 실효 권한 캐시는 users 삽입 트리거가 같은 트랜잭션에서 맞춘다. hash 는 HashPassword 의 결과다.
// 이메일이 이미 있으면 contract.ErrValidation(FieldError)이다.
func (s *Service) CreateFirstAdminTx(ctx context.Context, tx contract.Tx, in FirstAdminInput, hash string, deptID int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO users (email, name, role, department_id, active) VALUES (?, ?, 'admin', ?, 1) RETURNING id`,
		in.Email, in.Name, deptID).Scan(&id)
	if err != nil {
		if dbutil.IsUnique(err) {
			return 0, FieldError{"email", "이미 등록된 이메일입니다."}
		}
		return 0, err
	}
	pol, err := loadPolicy(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := s.setPassword(ctx, tx, id, hash, "", pol.Password, false); err != nil {
		return 0, err
	}
	pol.EmergencyAdmins = append(pol.EmergencyAdmins, id)
	cj, err := json.Marshal(pol)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_policy (id, config_json, updated_by, updated_at) VALUES (1,?,?,?)
		ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		string(cj), id, s.now().UTC().Format(timeLayout)); err != nil {
		return 0, fmt.Errorf("비상 관리자 지정: %w", err)
	}
	return id, nil
}

// setupPending 은 설치 마법사가 열려 있는지다(setup_state.status='pending'). 읽지 못하면 false 다(마법사를 열어 두는 쪽이 위험하다).
func (s *Service) setupPending(ctx context.Context) bool {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM setup_state WHERE id = 1`).Scan(&status); err != nil {
		return false
	}
	return status == "pending"
}

// demoMode 는 시연 환경(설치 때 데모 데이터를 선택)인지다. 읽지 못하면 false 다.
func (s *Service) demoMode(ctx context.Context) bool {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'setup.demo_mode'`).Scan(&v); err != nil {
		return false
	}
	return v == "1"
}

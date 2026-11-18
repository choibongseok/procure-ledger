package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// DevAccount 는 dev IdP 가 로그인시켜 주는 시드 계정이자 ERP 사용자(users)의 시드다.
// 역할 5종과 결재선(팀장 → 부서장 → 재무)을 모두 태울 수 있게 구성했다.
type DevAccount struct {
	Email      string
	Name       string
	Role       contract.Role
	Position   string // manager 의 결재선 단계: team_lead | dept_head
	Department string
	// OrgUnit 은 조직도(개발용 시드)에서 이 계정의 주소속 조직 이름이다. 로그인 화면 목록이 보인다.
	// 상단 바는 실제 조직도 값을 읽으므로 둘이 어긋나지 않게 internal/org 의 테스트가 대조한다.
	OrgUnit string
}

// Subject 는 dev IdP 의 sub 값이다.
func (a DevAccount) Subject() string { return "dev|" + strings.SplitN(a.Email, "@", 2)[0] }

// DevDepartments 는 시드 부서다.
var DevDepartments = []string{"구매팀", "경영지원팀", "영업팀"}

// DevAccounts 는 시드 계정 목록이다. 비밀번호는 없다(dev IdP 는 login_hint 로 바로 로그인).
//
//	구매팀 PR 의 결재선: lead@pl.test(팀장, 팀장이 올린 요청은 lead2@pl.test 가 결재) → head@pl.test(부서장) → finance@pl.test(재무)
//	타 부서 격리 확인용: staff2@pl.test(영업팀)
var DevAccounts = []DevAccount{
	{"admin@pl.test", "관리자", contract.RoleAdmin, "", "경영지원팀", "경영지원팀"},
	{"finance@pl.test", "재무 담당", contract.RoleFinance, "", "경영지원팀", "재무팀"},
	{"finance2@pl.test", "재무 담당2", contract.RoleFinance, "", "경영지원팀", "재무팀"}, // 재무가 올린 전표의 재무 단계를 맡는 두 번째 재무(자기 결재 금지)
	{"lead@pl.test", "구매팀장", contract.RoleManager, "team_lead", "구매팀", "구매1팀"},
	{"lead2@pl.test", "구매부팀장", contract.RoleManager, "team_lead", "구매팀", "구매2팀"},
	{"head@pl.test", "구매부서장", contract.RoleManager, "dept_head", "구매팀", "구매팀"},
	{"staff@pl.test", "구매직원", contract.RoleStaff, "", "구매팀", "구매1팀"},
	{"staff2@pl.test", "영업직원", contract.RoleStaff, "", "영업팀", "영업1팀"},
	{"auditor@pl.test", "감사 담당", contract.RoleAuditor, "", "경영지원팀", "감사팀"},
}

// SeedDevUsers 는 시드 부서·사용자를 멱등하게 넣는다(이미 있으면 건드리지 않는다). dev IdP 가 켜졌을 때만 부른다.
func SeedDevUsers(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range DevDepartments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO departments (name) VALUES (?) ON CONFLICT DO NOTHING`, d); err != nil {
			return err
		}
	}
	for _, a := range DevAccounts {
		var pos any
		if a.Position != "" {
			pos = a.Position
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (oidc_sub, email, name, role, position, department_id)
			VALUES (?, ?, ?, ?, ?, (SELECT id FROM departments WHERE name = ?)) ON CONFLICT DO NOTHING`,
			a.Subject(), a.Email, a.Name, string(a.Role), pos, a.Department); err != nil {
			return fmt.Errorf("시드 사용자 %s: %w", a.Email, err)
		}
	}
	return tx.Commit()
}

// devLookup 은 dev IdP 가 시드 8개 밖의 사용자(개발용 조직 시드의 사람들)를 이메일로 찾는다. 사용 중지된 계정은 찾지 않는다.
func (s *Service) devLookup(ctx context.Context, email string) (DevAccount, bool) {
	var a DevAccount
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT email, name, role FROM users WHERE lower(email) = lower(?) AND active = 1`, strings.TrimSpace(email)).Scan(&a.Email, &a.Name, &role)
	if err != nil {
		return DevAccount{}, false
	}
	a.Role = contract.Role(role)
	return a, true
}

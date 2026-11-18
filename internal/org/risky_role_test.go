package org

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33 재판정 1: 입사 발령과 조직 CSV 로 위험 권한이 든 역할을 지정할 수 없다(역할 관리 화면의 2인 확인으로만). 비위험 역할은 그대로다.
func TestHireRejectsRolesWithRiskyPermissions(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	if _, err := e.db.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_risky','위험 보조','custom','a','a'), ('r_safe','조회 보조','custom','a','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'role:manage' FROM roles WHERE key='r_risky' UNION ALL SELECT id, 'report:read' FROM roles WHERE key='r_safe'`); err != nil {
		t.Fatal(err)
	}
	hire := func(email string, role contract.Role) error {
		_, err := e.svc.Register(e.ctx, e.admin, Command{Kind: KindHire, Name: "입사자", Email: email, ToUnit: t1, Rank: 1, Role: role, Effective: "2026-03-10", Reason: "입사"})
		return err
	}
	for name, role := range map[string]contract.Role{"관리자 템플릿": contract.RoleAdmin, "재무 템플릿": contract.RoleFinance, "위험 권한이 든 사용자 정의 역할": "r_risky"} {
		err := hire("risky-"+string(role)+"@pl.test", role)
		var rr *access.RiskyRoleError
		if !errors.As(err, &rr) || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "역할 관리 화면에서 부여(2인 확인)") {
			t.Errorf("%s: 거부되어야 함: %v", name, err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE email LIKE 'risky-%'`); n != 0 {
		t.Fatalf("거부된 입사인데 사용자 %d명이 만들어짐", n)
	}
	// 거부 감사(롤백된 트랜잭션 밖): 입사 동작에 denied 로 남는다.
	var denied int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.hire' AND result='denied'`).Scan(&denied); err != nil || denied != 3 {
		t.Fatalf("입사 거부 감사 %d건 %v", denied, err)
	}
	// 비위험 역할(기본 직원·결재자, 위험 권한이 없는 사용자 정의 역할)은 그대로 입사한다.
	for _, role := range []contract.Role{contract.RoleStaff, contract.RoleManager, "r_safe", ""} {
		if err := hire("ok-"+string(role)+"@pl.test", role); err != nil {
			t.Errorf("%q 입사: %v", role, err)
		}
	}
}

func TestImportCSVRejectsRolesWithRiskyPermissions(t *testing.T) {
	e, _, _ := importEnv(t)
	if _, err := e.svc.db.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_risky','위험 보조','custom','a','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'user:manage' FROM roles WHERE key='r_risky'`); err != nil {
		t.Fatal(err)
	}
	units := "조직코드,조직명,종류,상위조직코드,조직장사번\nDATA,데이터팀,팀,DEP,E301\n"
	for _, role := range []string{"관리자", "재무", "위험 보조"} {
		people := "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE301,한지우,e301@pl.test,사원,DATA," + role + ",2026-03-10\n"
		pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, people))
		if err != nil {
			t.Fatal(err)
		}
		if len(pv.Errors) == 0 || !strings.Contains(pv.Errors[0].Msg, "위험 권한이 들어 있는 역할은 가져오기로 지정할 수 없습니다") {
			t.Fatalf("%s: 미리보기에서 거부되어야 함: %+v", role, pv.Errors)
		}
		// 미리보기를 건너뛰고 반영을 시도해도 같은 규칙으로 막히고 거부 감사가 남는다.
		if _, err := e.svc.ApplyImport(e.ctx, e.admin, inputOf(units, people), "forged", false); err == nil {
			t.Fatalf("%s: 반영이 통과함", role)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE email = 'e301@pl.test'`); n != 0 {
		t.Fatalf("거부된 가져오기인데 사용자가 만들어짐: %d", n)
	}
}

// 기존 사람 행의 권한역할 열은 반영되지 않으므로 미리보기가 "무시됨"을 알린다.
func TestImportWarnsThatRoleColumnIsIgnoredForExistingPeople(t *testing.T) {
	e, _, _ := importEnv(t)
	units := "조직코드,조직명,종류,상위조직코드,조직장사번\nDATA,데이터팀,팀,DEP,E301\n"
	hire := "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE301,한지우,e301@pl.test,사원,DATA,직원,2026-03-10\n"
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, hire))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, inputOf(units, hire), pv.PreviewID, false); err != nil {
		t.Fatal(err)
	}
	again := "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE301,한지우,e301@pl.test,사원,DATA,결재자,2026-03-10\n"
	pv2, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, again))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range pv2.Warnings {
		found = found || strings.Contains(w, "권한역할 열은 기존 사람에게는 적용되지 않아 무시됩니다")
	}
	if !found {
		t.Fatalf("무시됨 안내가 없음: %v", pv2.Warnings)
	}
}

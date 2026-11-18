package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const uSalesHead = 12 // 영업팀 부서장(필요한 테스트만 addSalesHead 로 추가한다)

// addSalesHead 는 영업팀(부서)에 부서장 사용자를 추가한다. 시드 영업팀은 부서장이 공석이라 영업 건의 부서장 단계는 대표까지 올라간다.
func (e *env) addSalesHead(t *testing.T) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, position, department_id) VALUES (12,'h12@t','영업부서장','manager','dept_head',2)`); err != nil {
		t.Fatal(err)
	}
	users[uSalesHead] = contract.Principal{UserID: uSalesHead, Name: "영업부서장", Role: contract.RoleManager, DepartmentID: deptSales}
	e.org.AddPerson(uSalesHead, "영업부서장", "부장", unitSales, "", "", "")
	e.org.SetHead(unitSales, uSalesHead)
}

// deactivate 는 사용자를 퇴사 처리한다(users.active 와 조직 재직 상태 모두).
func (e *env) deactivate(t *testing.T, id int64) {
	t.Helper()
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	e.org.Leave(id)
}

// stepApprovers 는 건 현재 차수의 결재선을 "이름 > 이름" 으로 줄인다(재무 단계는 "재무").
func (e *env) lineNames(t *testing.T, by, prID int64) []string {
	t.Helper()
	pr, err := e.purchase.Get(as(by), prID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range pr.Steps {
		if s.Title == "재무" || s.Kind == "finance" {
			out = append(out, "재무")
			continue
		}
		out = append(out, s.AssigneeName)
	}
	return out
}

const uUnreg = 13 // 조직에 등록되지 않은 사용자(계정만 있다)

func (e *env) addUnregistered(t *testing.T) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, position, department_id) VALUES (13,'unreg@t','미등록','staff',NULL,1)`); err != nil {
		t.Fatal(err)
	}
	users[uUnreg] = contract.Principal{UserID: uUnreg, Name: "미등록", Role: contract.RoleStaff, DepartmentID: deptBuy}
	e.org.AddUnregistered(uUnreg)
}

package service_test

import (
	"context"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"math/rand"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

const (
	unitFin1 = 15 // 재무1팀(재무팀 아래 팀): 팀장 14
	uFinLead = 14
	uFinStaf = 16
)

func (e *env) addFinTeam(t *testing.T) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO users (id, email, name, role, position, department_id) VALUES (14,'fl@t','재무팀장','manager','team_lead',3)`,
		`INSERT INTO users (id, email, name, role, position, department_id) VALUES (16,'fs@t','재무직원','staff',NULL,3)`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	users[uFinLead] = contract.Principal{UserID: uFinLead, Name: "재무팀장", Role: contract.RoleManager, DepartmentID: deptFin}
	users[uFinStaf] = contract.Principal{UserID: uFinStaf, Name: "재무직원", Role: contract.RoleStaff, DepartmentID: deptFin}
	e.org.AddUnit(unitFin1, "FIN1", "재무1팀", contract.OrgTeam, unitFin)
	e.org.AddPerson(uFinLead, "재무팀장", "과장", unitFin1, "", "", "")
	e.org.AddPerson(uFinStaf, "재무직원", "사원", unitFin1, "", "", "")
	e.org.SetHead(unitFin1, uFinLead)
}

// AC-41 겸직 조직장은 겸직 조직 범위의 건도 본다. 겸직 구성원(조직장 아님)은 보지 못한다.
func TestConcurrentHeadSeesAndConcurrentMemberDoesNot(t *testing.T) {
	e := newEnv(t)
	e.addFinTeam(t)
	pr := e.submitted(t, uFinStaf, 500_000) // 재무1팀 직원 건: 팀장 단계는 재무팀장
	// 영업팀장(영업1팀 조직장)이 재무팀(부서)의 조직장을 겸직한다. 영업직원은 재무팀의 겸직 구성원일 뿐이다.
	e.org.AddConcurrent(uLead2, unitFin).SetHead(unitFin, uLead2)
	e.org.AddConcurrent(uStaff2, unitFin)
	if !visible(t, e, uLead2, pr.ID) || !inList(t, e, uLead2, pr.ID) {
		t.Fatal("겸직 조직장이 겸직 조직 범위 건을 못 봄")
	}
	if visible(t, e, uStaff2, pr.ID) || inList(t, e, uStaff2, pr.ID) {
		t.Fatal("겸직 구성원이 건을 봄")
	}
}

// failingNotes 는 알림 삽입을 실패시키려고 알림 표를 막는다(트리거로 INSERT 거부).
// AC-141 알림 생성은 재지정과 같은 트랜잭션이다. 알림 삽입이 실패하면 재지정도 없다.
func TestReassignRollsBackWhenNotificationInsertFails(t *testing.T) {
	dbtest.SkipPG(t, "SQLite 트리거·PRAGMA 를 직접 쓴다")
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	e.deactivate(t, uLead)
	if _, err := e.db.Exec(`CREATE TRIGGER trg_test_block_notes BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT, '알림 삽입 실패(시험)'); END`); err != nil {
		t.Fatal(err)
	}
	before := count(t, e.db, `SELECT COUNT(*) FROM approval_step_reassignments`)
	histBefore := count(t, e.db, `SELECT COUNT(*) FROM approval_history`)
	_, err := e.reassigner().ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uLead3, Cause: "leave", Reason: "퇴사"})
	if err == nil {
		t.Fatal("알림 삽입이 실패했는데 재지정이 성공함")
	}
	if !strings.Contains(err.Error(), "알림 삽입 실패(시험)") {
		t.Fatalf("다른 이유로 실패함(알림 삽입 경로를 타지 않음): %v", err)
	}
	if count(t, e.db, `SELECT COUNT(*) FROM approval_step_reassignments`) != before || count(t, e.db, `SELECT COUNT(*) FROM approval_history`) != histBefore {
		t.Fatal("재지정 흔적이 남음(같은 트랜잭션이 아님)")
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM audit_logs WHERE action = 'approval.reassign' AND result = 'success'`); n != 0 {
		t.Fatalf("성공 감사 기록이 남음: %d", n)
	}
}

// AC-39 상신의 감사 기록이 실패하면 스냅샷도 저장되지 않는다. 스냅샷 행(선·단계·배정자)이 모두 없다.
func TestSubmitSnapshotAbsentWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	d := e.draft(t, uStaff, 500_000)
	e.rec.FailWith(errors.New("감사 기록 실패(시험)"))
	if _, err := e.purchase.Submit(as(uStaff), d.ID); err == nil {
		t.Fatal("감사 기록이 실패했는데 상신이 성공함")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM approval_lines WHERE pr_id = ?`,
		`SELECT COUNT(*) FROM approval_steps WHERE pr_id = ?`,
		`SELECT COUNT(*) FROM approval_step_assignees WHERE pr_id = ?`,
		`SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`,
	} {
		if n := count(t, e.db, q, d.ID); n != 0 {
			t.Fatalf("%s: 스냅샷 행이 남음(%d)", q, n)
		}
	}
	if got, _ := e.purchase.Get(as(uStaff), d.ID); got.Status != domain.PRDraft {
		t.Fatalf("상태가 바뀜: %s", got.Status)
	}
}

// AC-137 재지정 직전·직후의 결재 경합: 재지정이 먼저면 옛 결재자의 결재는 거부되고, 결재가 먼저면 재지정이 거부된다(순차 호출).
func TestReassignDecideRace(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := e.reassigner().ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uLead3, Cause: "transfer", Reason: "전보", ConfirmActive: true}); err != nil {
		t.Fatal(err)
	}
	_, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true})
	if err == nil {
		t.Fatal("재지정된 옛 결재자의 결재가 통과함")
	}
	if _, err := e.approval.Decide(as(uLead3), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("새 결재자: %v", err)
	}
	// 결재가 먼저 끝난 건은 재지정할 수 없다.
	_, err = e.reassigner().ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uLead, Cause: "other", Reason: "되돌리기", ConfirmActive: true})
	if err == nil {
		t.Fatal("끝난 단계가 재지정됨")
	}
}

// AC-12 한 사람이 같은 결재선의 두 단계에 배정되는 경우가 없다. 조직 구조를 여러 개 무작위로 만들어 확인한다.
func TestRandomOrgNeverAssignsOnePersonToTwoSteps(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewSource(seed))
		e := newEnv(t)
		o := fake.NewOrg()
		o.AddUnit(unitCompany, "PL", "PL 주식회사", contract.OrgCompany, 0)
		o.AddUnit(unitBuy, "BUY", "구매팀", contract.OrgDepartment, unitCompany)
		o.AddUnit(unitBuy1, "BUY1", "구매1팀", contract.OrgTeam, unitBuy)
		o.AddUnit(unitFin, "FIN", "재무팀", contract.OrgDepartment, unitCompany)
		// 무작위로 조직장을 겸직·공석·대행으로 배치한다. 같은 사람이 여러 층의 조직장이 될 수 있다.
		people := []int64{uStaff, uLead, uHead, uLead3, uCEO}
		for _, id := range append(people, uFinance, uFinance2) {
			o.AddPerson(id, users[id].Name, "과장", unitBuy1, "", "", "")
		}
		pick := func() int64 { return people[1+rng.Intn(len(people)-1)] }
		for _, u := range []int64{unitBuy1, unitBuy, unitCompany} {
			switch rng.Intn(4) {
			case 0: // 공석
			case 1:
				o.SetHead(u, pick()).SetActing(u, pick())
			default:
				o.SetHead(u, pick())
			}
		}
		e.org = o
		for _, amount := range []int64{300_000, 2_000_000, 20_000_000, 200_000_000} {
			pv, err := e.liner().PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: amount})
			if err != nil {
				t.Fatal(err)
			}
			seen := map[int64]bool{}
			for _, st := range pv.Steps {
				if st.Omitted || st.Missing || st.ApproverID == 0 {
					continue
				}
				if seen[st.ApproverID] {
					t.Fatalf("seed %d 금액 %d: 한 사람(%d)이 두 단계에 배정됨: %+v", seed, amount, st.ApproverID, pv.Steps)
				}
				seen[st.ApproverID] = true
				if st.ApproverID == uStaff {
					t.Fatalf("seed %d: 요청자가 자기 결재선에 배정됨", seed)
				}
			}
		}
	}
}

// AC-45b 열람 범위 밖 건은 웹·MCP 채널 모두 "찾을 수 없음"이고 존재하지 않는 번호와 문구가 같다(존재 여부를 알려 주지 않는다).
func TestOutOfScopeIsNotFoundOnWebAndMCPWithSameText(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff2, 500_000) // 영업팀 건
	mcp := users[uLead]
	mcp.Channel = contract.ChannelMCP
	for name, ctx := range map[string]context.Context{"web": as(uLead), "mcp": contract.WithPrincipal(context.Background(), mcp)} {
		_, outErr := e.purchase.Get(ctx, pr.ID)
		_, noneErr := e.purchase.Get(ctx, 999_999)
		if !errors.Is(outErr, contract.ErrNotFound) || !errors.Is(noneErr, contract.ErrNotFound) {
			t.Fatalf("%s: 찾을 수 없음이어야 함: %v / %v", name, outErr, noneErr)
		}
		if outErr.Error() != noneErr.Error() {
			t.Fatalf("%s: 존재 여부가 문구로 드러남: %q vs %q", name, outErr, noneErr)
		}
		if _, err := e.approval.Decide(ctx, pr.ID, domain.Decision{Approve: true}); !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("%s: 결재도 찾을 수 없음이어야 함: %v", name, err)
		}
	}
}

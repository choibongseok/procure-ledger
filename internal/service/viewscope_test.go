package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func visible(t *testing.T, e *env, by, prID int64) bool {
	t.Helper()
	_, err := e.purchase.Get(as(by), prID)
	if err != nil && !isErr(err, contract.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func inList(t *testing.T, e *env, by, prID int64) bool {
	t.Helper()
	list, err := e.purchase.List(as(by), domain.PRFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == prID {
			return true
		}
	}
	return false
}

// 조직장은 자기 조직과 하위 조직 전체의 구매요청을 본다. 같은 부서 동료는 서로 본다. 그 밖은 404(AC-40·45).
func TestHeadSeesDescendantUnits(t *testing.T) {
	e := newEnv(t)
	buy := e.submitted(t, uStaff, 500_000)    // 구매1팀 건
	sales := e.submitted(t, uStaff2, 500_000) // 영업1팀 건
	// 대표(회사 조직장)는 하위 전체를 본다. 구매팀 부서장은 구매팀 건만, 팀장은 자기 팀 건만 본다.
	for _, pr := range []int64{buy.ID, sales.ID} {
		if !visible(t, e, uCEO, pr) || !inList(t, e, uCEO, pr) {
			t.Fatalf("대표가 건 %d 를 못 봄", pr)
		}
	}
	if !visible(t, e, uHead, buy.ID) || visible(t, e, uHead, sales.ID) {
		t.Fatal("부서장은 구매팀 건만 보아야 함")
	}
	if visible(t, e, uLead3, buy.ID) == false {
		// 같은 구매팀(부서) 소속이라 부서 번호 규칙으로 본다(호환 투영).
		t.Fatal("같은 부서 동료는 서로 본다")
	}
	if visible(t, e, uLead3, sales.ID) || inList(t, e, uLead3, sales.ID) {
		t.Fatal("타 부서 건은 보이지 않아야 함")
	}
	// 전체 열람 역할.
	for _, by := range []int64{uFinance, uAuditor, uAdmin} {
		if !visible(t, e, by, sales.ID) {
			t.Fatalf("전체 열람 역할 %d", by)
		}
	}
}

// 직무대행 중인 조직의 건을 대행자가 보고 대행이 끝나면 못 본다. 조직장에서 물러난 사람은 새로 열람하지 못한다(AC-42·43).
func TestActingAndFormerHeadScope(t *testing.T) {
	e := newEnv(t)
	sales := e.submitted(t, uStaff2, 500_000)
	if visible(t, e, uLead3, sales.ID) {
		t.Fatal("대행 전에는 보이지 않아야 함")
	}
	e.org.SetActing(unitSales, uLead3) // 영업팀(부서) 부서장 공석 중 직무대행
	if !visible(t, e, uLead3, sales.ID) {
		t.Fatal("직무대행자는 그 조직 범위를 본다")
	}
	e.org.SetActing(unitSales, 0)
	if visible(t, e, uLead3, sales.ID) {
		t.Fatal("대행이 끝나면 보이지 않아야 함")
	}
	// 조직장에서 물러난 사람: 대표를 비우면 대표는 범위가 사라진다(재무팀 소속이라 부서 규칙으로도 안 보인다).
	if !visible(t, e, uCEO, sales.ID) {
		t.Fatal("재직 중인 대표는 본다")
	}
	e.org.SetHead(unitCompany, 0)
	if visible(t, e, uCEO, sales.ID) {
		t.Fatal("조직장에서 물러나면 새로 열람하지 못한다")
	}
}

// 귀속 조직은 상신 시점에 고정된다: 요청자가 전보해도 옛 조직의 조직장이 보고 새 조직의 조직장은 못 본다(AC-44).
func TestOwnerUnitIsFixedAtSubmit(t *testing.T) {
	e := newEnv(t)
	e.addSalesHead(t)
	pr := e.submitted(t, uStaff2, 500_000)
	if got, _ := e.purchase.Get(as(uStaff2), pr.ID); got.OwnerUnitID != unitSales1 || got.RequesterUnitPath != "영업팀 > 영업1팀" || got.LineAsOf != "2026-11-10" {
		t.Fatalf("스냅샷: %+v", got)
	}
	// 영업직원이 구매1팀으로 전보한다(조직 변경 + 호환 투영 값).
	e.org.AddPerson(uStaff2, "영업직원", "대리", unitBuy1, "", "", "")
	if _, err := e.db.Exec(`UPDATE users SET department_id = 1 WHERE id = ?`, uStaff2); err != nil {
		t.Fatal(err)
	}
	if !visible(t, e, uSalesHead, pr.ID) {
		t.Fatal("옛 조직의 조직장은 계속 본다")
	}
	if visible(t, e, uHead, pr.ID) {
		t.Fatal("새 조직(구매팀)의 조직장은 옛 건을 못 본다")
	}
	// 상신 이후 조직 이름이 바뀌어도 스냅샷 문자열은 그대로다(AC-31).
	e.org.AddUnit(unitSales1, "SAL1", "영업1팀(개편)", contract.OrgTeam, unitSales)
	if got, _ := e.purchase.Get(as(uStaff2), pr.ID); got.RequesterUnitPath != "영업팀 > 영업1팀" {
		t.Fatalf("스냅샷 문자열이 바뀜: %q", got.RequesterUnitPath)
	}
}

// 상신 스냅샷: 결재선·요청자 귀속 조직이 저장되고, 감사 기록에 line·requester_unit·line_as_of 가 있다. 재상신하면 다시 만든다(AC-30·36·37·38).
func TestSubmitStoresSnapshotAndAudit(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 2_000_000)
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_lines WHERE pr_id = ? AND round = 1 AND requester_unit_path = '구매팀 > 구매1팀'`, pr.ID); n != 1 {
		t.Fatalf("스냅샷 행 %d", n)
	}
	got, _ := e.purchase.Get(as(uStaff), pr.ID)
	if len(got.Steps) != 2 || got.Steps[0].UnitPath != "구매팀 > 구매1팀" || got.Steps[0].Title != "팀장" || got.Steps[0].AssigneeName != "팀장" ||
		got.Steps[1].UnitPath != "구매팀" || got.Steps[1].Title != "부서장" {
		t.Fatalf("단계 스냅샷: %+v", got.Steps)
	}
	rec, ok := lastRecord(e, "purchase_request.submit")
	if !ok || !hasDiff(rec, "line", "1:구매1팀 팀장 팀장 / 2:구매팀 부서장 부서장") || !hasDiff(rec, "requester_unit", "구매팀 > 구매1팀") || !hasDiff(rec, "line_as_of", "2026-11-10") {
		t.Fatalf("상신 감사 기록: %+v", rec)
	}
	// 스냅샷은 수정할 수 없다.
	if _, err := e.db.Exec(`UPDATE approval_steps SET unit_path = 'x' WHERE pr_id = ?`, pr.ID); err == nil {
		t.Fatal("스냅샷 열 수정이 허용됨")
	}
	// 상신 뒤 팀장이 바뀌어도 배정자는 그대로, 새 팀장은 결재할 수 없다(AC-32).
	e.org.AddConcurrent(uLead3, unitBuy1)
	e.org.SetHead(unitBuy1, uLead3)
	if p, _ := e.approval.ListPending(as(uLead3)); len(p) != 0 {
		t.Fatalf("새 조직장 결재함: %+v", p)
	}
	_, err := e.approval.Decide(as(uLead3), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrForbidden)
	cur, _ := e.purchase.Get(as(uStaff), pr.ID)
	if cur.Steps[0].HeadChangedTo != "구매팀장2" {
		t.Fatalf("조직장 교체 안내: %+v", cur.Steps[0])
	}
	if _, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("배정자는 결재 가능: %v", err)
	}
	// 반려 뒤 재상신: 차수가 오르고 결재선이 그때의 조직으로 다시 만들어진다.
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Comment: "보완"}); err != nil {
		t.Fatal(err)
	}
	re, err := e.purchase.Submit(as(uStaff), pr.ID)
	if err != nil || re.Round != 2 || re.Steps[0].AssigneeID != uLead3 {
		t.Fatalf("재상신: %+v %v", re, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_lines WHERE pr_id = ?`, pr.ID); n != 2 {
		t.Fatalf("차수별 스냅샷 %d", n)
	}
}

// 임시저장 건의 귀속 부서·조직은 저장할 때마다 현재 소속으로 갱신된다(AC-37).
func TestDraftOwnerFollowsRequester(t *testing.T) {
	e := newEnv(t)
	pr := e.draft(t, uStaff2, 100_000)
	if pr.OwnerUnitID != unitSales1 || pr.DepartmentID != deptSales {
		t.Fatalf("초안 귀속: %+v", pr)
	}
	e.org.AddPerson(uStaff2, "영업직원", "대리", unitBuy1, "", "", "")
	if _, err := e.db.Exec(`UPDATE users SET department_id = 1 WHERE id = ?`, uStaff2); err != nil {
		t.Fatal(err)
	}
	moved := users[uStaff2]
	moved.DepartmentID = deptBuy
	users[uStaff2] = moved
	defer func() { moved.DepartmentID = deptSales; users[uStaff2] = moved }()
	got, err := e.purchase.UpdateDraft(as(uStaff2), pr.ID, domain.DraftInput{VendorID: e.vendor(t).ID, Title: "수정", Reason: "x",
		Lines: []domain.PRLineInput{{ItemID: e.item(t, "MOVE-1", 0, false).ID, Quantity: 1, UnitPrice: 100_000}}})
	if err != nil || got.OwnerUnitID != unitBuy1 || got.DepartmentID != deptBuy {
		t.Fatalf("수정 뒤 귀속: %+v %v", got, err)
	}
	// 상신하면 그 시점의 귀속이 결재선 스냅샷과 함께 고정된다.
	sub, err := e.purchase.Submit(as(uStaff2), pr.ID)
	if err != nil || sub.OwnerUnitID != unitBuy1 || sub.RequesterUnitPath != "구매팀 > 구매1팀" {
		t.Fatalf("상신 뒤 귀속: %+v %v", sub, err)
	}
}

// 발주·입고는 구매요청의 열람 범위를 따른다(AC-45a).
func TestPurchaseOrderAndReceiptFollowRequestScope(t *testing.T) {
	e := newEnv(t)
	sales := e.submitted(t, uStaff2, 500_000)
	approve(t, e, uLead2, sales.ID)
	got, _ := e.purchase.Get(as(uStaff2), sales.ID)
	po := got.PurchaseOrderID
	if po == 0 {
		t.Fatal("발주가 생성되어야 함")
	}
	if _, err := e.inventory.GetPurchaseOrder(as(uCEO), po); err != nil {
		t.Fatalf("대표(조직장 범위) 발주 조회: %v", err)
	}
	if _, err := e.inventory.GetPurchaseOrder(as(uLead3), po); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("범위 밖 발주: %v", err)
	}
	list, _ := e.inventory.ListPurchaseOrders(as(uCEO), domain.POFilter{})
	if len(list) != 1 {
		t.Fatalf("대표의 발주 목록: %+v", list)
	}
	if list, _ := e.inventory.ListPurchaseOrders(as(uLead3), domain.POFilter{}); len(list) != 0 {
		t.Fatalf("범위 밖 발주 목록: %+v", list)
	}
}

// 결재선 미리보기: 본인은 항상, 타인은 열람 범위일 때만, 금액 0 이하는 400(AC-70).
func TestPreviewLinePermissionAndInput(t *testing.T) {
	e := newEnv(t)
	l := e.liner()
	own, err := l.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 500_000})
	if err != nil || !own.Complete || len(own.Steps) != 1 || own.Steps[0].ApproverName != "팀장" || own.UnitPath != "구매팀 > 구매1팀" {
		t.Fatalf("본인 미리보기: %+v %v", own, err)
	}
	// 같은 부서 동료(uLead)가 직원 것을 묻는다. 다른 부서 직원 것은 거부.
	if _, err := l.PreviewLine(as(uLead), domain.LinePreviewInput{RequesterID: uStaff, Amount: 500_000}); err != nil {
		t.Fatalf("같은 부서: %v", err)
	}
	_, err = l.PreviewLine(as(uLead), domain.LinePreviewInput{RequesterID: uStaff2, Amount: 500_000})
	is(t, err, contract.ErrForbidden)
	if !strings.Contains(err.Error(), "이 사람의 결재선은 볼 수 없습니다") {
		t.Fatalf("문구: %v", err)
	}
	// 조직장(대표)·전체 열람 역할은 된다.
	for _, by := range []int64{uCEO, uAuditor, uFinance} {
		if _, err := l.PreviewLine(as(by), domain.LinePreviewInput{RequesterID: uStaff2, Amount: 500_000}); err != nil {
			t.Fatalf("사용자 %d: %v", by, err)
		}
	}
	for _, amount := range []int64{0, -1} {
		_, err = l.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: amount})
		is(t, err, contract.ErrValidation)
	}
	_, err = l.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 1, AsOf: "2026/11/10"})
	is(t, err, contract.ErrValidation)
	_, err = l.PreviewLine(as(uStaff), domain.LinePreviewInput{RequesterID: 999, Amount: 1})
	is(t, err, contract.ErrNotFound)
	// 조직 미등록 요청자는 오류가 아니라 Complete=false (AC-10).
	e.addUnregistered(t)
	pv, err := l.PreviewLine(as(uUnreg), domain.LinePreviewInput{Amount: 500_000})
	if err != nil || pv.Complete || len(pv.Problems) != 1 {
		t.Fatalf("미등록: %+v %v", pv, err)
	}
	// 미리보기와 상신이 저장한 결재선이 같다(AC-14).
	pre, _ := l.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 2_000_000})
	pr := e.submitted(t, uStaff, 2_000_000)
	got, _ := e.purchase.Get(as(uStaff), pr.ID)
	if len(pre.Steps) != len(got.Steps) {
		t.Fatalf("단계 수 %d != %d", len(pre.Steps), len(got.Steps))
	}
	for i, s := range pre.Steps {
		if s.ApproverID != got.Steps[i].AssigneeID || s.UnitPath != got.Steps[i].UnitPath {
			t.Fatalf("%d단계 미리보기 %+v 와 스냅샷 %+v 가 다름", i+1, s, got.Steps[i])
		}
	}
}

// 조직 미등록 사용자의 상신은 불가하지만 임시저장은 된다(C4).
func TestUnregisteredRequesterCanDraftButNotSubmit(t *testing.T) {
	e := newEnv(t)
	e.addUnregistered(t)
	pr := e.draft(t, uUnreg, 100_000)
	if pr.OwnerUnitID != 0 {
		t.Fatalf("미등록 사용자의 귀속 조직은 비어야 함: %d", pr.OwnerUnitID)
	}
	_, err := e.purchase.Submit(as(uUnreg), pr.ID)
	is(t, err, contract.ErrValidation)
	if !strings.Contains(err.Error(), "조직에 등록되지 않아 결재선을 만들 수 없습니다") {
		t.Fatalf("문구: %v", err)
	}
}

// 퇴사한 사람의 과거 기록에는 이름 옆에 "(퇴사)" 가 붙고, 재직자와 DB 의 이름·결재선 스냅샷 문자열은 그대로다(AC-51 확장).
func TestLeftUsersGetSuffixInRecords(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead, uLead3)
	pr := e.submitted(t, uStaff, 500_000) // 1단계 uLead
	approve(t, e, uLead, pr.ID)
	e.deactivate(t, uLead)  // 결재한 팀장 퇴사
	e.deactivate(t, uStaff) // 요청자 퇴사
	got, err := e.purchase.Get(as(uAdmin), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequesterName != "직원 (퇴사)" || got.Steps[0].AssigneeName != "팀장 (퇴사)" {
		t.Fatalf("요청자·결재자 이름: %q %q", got.RequesterName, got.Steps[0].AssigneeName)
	}
	list, _ := e.purchase.List(as(uAdmin), domain.PRFilter{})
	if len(list) != 1 || list[0].RequesterName != "직원 (퇴사)" {
		t.Fatalf("목록 요청자 이름: %+v", list)
	}
	hist, _ := e.approval.History(as(uAdmin), pr.ID)
	var names []string
	for _, h := range hist {
		names = append(names, h.ActorName)
	}
	if names[0] != "직원 (퇴사)" || names[1] != "팀장 (퇴사)" {
		t.Fatalf("이력 처리자 이름: %v", names)
	}
	_, received, _ := e.delegation.ListMine(as(uLead3))
	if len(received) != 1 || received[0].DelegatorName != "팀장 (퇴사)" || received[0].DelegateName != "구매팀장2" {
		t.Fatalf("위임 이름: %+v", received)
	}
	// 재직자에게는 붙지 않고 DB 의 이름과 스냅샷 문자열은 바뀌지 않는다.
	if n := count(t, e.db, `SELECT COUNT(*) FROM users WHERE name LIKE '%(퇴사)%'`); n != 0 {
		t.Fatalf("DB 이름이 바뀜: %d", n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_step_assignees WHERE name_at LIKE '%(퇴사)%'`); n != 0 {
		t.Fatalf("스냅샷 이름이 바뀜: %d", n)
	}
}

package service_test

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// delegate 는 by 가 to 에게 [now-1h, now+24h] 동안 위임하는 입력을 만든다.
func (e *env) delegateIn(to int64) domain.DelegationInput {
	return domain.DelegationInput{DelegateID: to, From: e.now.Add(-time.Hour), To: e.now.Add(24 * time.Hour)}
}

func (e *env) mustDelegate(t *testing.T, by, to int64) domain.Delegation {
	t.Helper()
	d, err := e.delegation.Create(as(by), e.delegateIn(to))
	if err != nil {
		t.Fatalf("위임 지정(%d→%d): %v", by, to, err)
	}
	return d
}

func hasDiff(rec audit.Record, key, val string) bool {
	for _, d := range rec.Diff {
		if d.Key == key && (d.New == val || d.Old == val) {
			return true
		}
	}
	return false
}

func lastRecord(e *env, action string) (audit.Record, bool) {
	rs := e.rec.Records()
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Action == action {
			return rs[i], true
		}
	}
	return audit.Record{}, false
}

// 결재선은 조직도로만 만든다. 위임은 상신을 풀지 못하고, 조직장 보임·직무대행 발령이 푼다(명세 3.6).
// 대표가 공석이면 영업1팀 팀장(uLead2) 본인 요청은 위로 올릴 곳이 없어 상신할 수 없다.
func TestActingAssignmentUnblocksSubmit(t *testing.T) {
	e := newEnv(t)
	e.org.SetHead(unitCompany, 0) // 대표 공석
	pr := e.draft(t, uLead2, 500_000)
	_, err := e.purchase.Submit(as(uLead2), pr.ID)
	is(t, err, contract.ErrValidation)
	// 위임은 상신 사전 조건에 영향이 없다.
	e.mustDelegate(t, uLead2, uHead)
	_, err = e.purchase.Submit(as(uLead2), pr.ID)
	is(t, err, contract.ErrValidation)
	// 대표 직무대행 발령(부서장 uHead)이 있으면 상신된다.
	e.org.SetActing(unitCompany, uHead)
	got, err := e.purchase.Submit(as(uLead2), pr.ID)
	if err != nil {
		t.Fatalf("직무대행 발령 뒤 상신: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].AssigneeID != uHead || !got.Steps[0].Acting {
		t.Fatalf("직무대행자가 배정돼야 함: %+v", got.Steps)
	}
}

// 위임받은 사람이 위임자의 단계를 대신 결재한다. 영업1팀 직원(uStaff2)의 요청은 팀장(uLead2)의 단계이고 그가 부서장(uHead)에게 맡긴다.
func TestDelegationDecideOnBehalf(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 500_000)

	pending, err := e.approval.ListPending(as(uHead))
	if err != nil || len(pending) != 1 || pending[0].ID != pr.ID || pending[0].DelegatedFromID != uLead2 {
		t.Fatalf("수임자 결재함: %+v %v", pending, err)
	}
	// 위임받은 건은 열람 범위 밖이어도 열람·이력 조회가 된다.
	if got, err := e.purchase.Get(as(uHead), pr.ID); err != nil || got.ID != pr.ID {
		t.Fatalf("수임자 열람: %v", err)
	}
	if _, err := e.approval.History(as(uHead), pr.ID); err != nil {
		t.Fatalf("수임자 이력 조회: %v", err)
	}

	res, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	if err != nil || res.Request.Status != domain.PROrdered {
		t.Fatalf("위임 결재: %+v %v", res, err)
	}
	hist, _ := e.approval.History(as(uStaff2), pr.ID)
	last := hist[len(hist)-1]
	if last.Action != domain.ActionApprove || last.ActorID != uHead || last.OnBehalfOfID != uLead2 {
		t.Fatalf("이력에 대신 결재가 남아야 함: %+v", last)
	}
	if last.ActorName == "" || last.OnBehalfOfName == "" {
		t.Fatalf("이력에 처리자·위임자 이름이 있어야 함: %+v", last)
	}
	rec, ok := lastRecord(e, "approval.approve")
	if !ok || !hasDiff(rec, "on_behalf_of", "8") || rec.ActorID != uHead {
		t.Fatalf("위임 결재 감사 기록: %+v", rec)
	}
	// 종결된 건은 위임 덕분에 열려 있던 열람이 끝난다. 대신 결재한 사실은 이력·감사 로그에만 남는다.
	if _, err := e.purchase.Get(as(uHead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("결재 뒤 열람이 남음: %v", err)
	}
}

func TestDelegationRejectOnBehalf(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 500_000)
	_, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false})
	is(t, err, contract.ErrValidation) // 사유 필수는 위임에도 그대로
	res, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: false, Comment: "예산 초과"})
	if err != nil || res.Request.Status != domain.PRRejected {
		t.Fatalf("위임 반려: %+v %v", res, err)
	}
	rec, ok := lastRecord(e, "approval.reject")
	if !ok || !hasDiff(rec, "on_behalf_of", "8") {
		t.Fatalf("위임 반려 감사 기록: %+v", rec)
	}
}

// 기간 밖·해제 후에는 위임 결재가 거부되고 결재함에도 보이지 않는다.
func TestDelegationWindowAndRevoke(t *testing.T) {
	e := newEnv(t)
	d := e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 500_000)

	// 기간 이후
	e.now = e.now.Add(48 * time.Hour)
	_, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 0 {
		t.Fatalf("기간이 끝났는데 결재함에 보임: %+v", p)
	}
	if _, err := e.purchase.Get(as(uHead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("기간이 끝났는데 열람됨: %v", err)
	}

	// 기간 이전
	e.now = e.now.Add(-96 * time.Hour)
	_, err = e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)

	// 기간 안에서 해제하면 곧바로 거부된다.
	e.now = e.now.Add(48 * time.Hour)
	if err := e.delegation.Revoke(as(uLead2), d.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 0 {
		t.Fatalf("해제했는데 결재함에 보임: %+v", p)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE on_behalf_of IS NOT NULL`); n != 0 {
		t.Fatalf("거부됐는데 이력이 남음: %d", n)
	}
}

// 요청자가 수임자인 건은 위임으로도 결재할 수 없다.
func TestDelegationSelfApprovalForbidden(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead3, uLead) // 구매2팀 팀장 → 구매1팀 팀장
	pr := e.submitted(t, uLead, 500_000)
	// 구매1팀 팀장(요청자)의 1단계는 부서장이 맡는다. 요청자가 수임자라고 결재권이 생기지 않는다.
	_, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrSelfApproval)
	if p, _ := e.approval.ListPending(as(uLead)); len(p) != 0 {
		t.Fatalf("본인 요청이 결재함에 보임: %+v", p)
	}
	if !e.rec.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) {
		t.Error("자기 결재 시도가 감사에 남지 않음")
	}
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("배정된 결재자는 결재 가능: %v", err)
	}
}

// 직책 서열 규칙(B11): 같은 층 이상의 조직장에게만, 재무는 재무에게만. 서열은 조직장인 조직의 가장 높은 층이다.
func TestDelegationRankRules(t *testing.T) {
	cases := []struct {
		name     string
		from, to int64
		ok       bool
	}{
		{"팀장→팀장", uLead, uLead3, true},
		{"팀장→부서장", uLead, uHead, true},
		{"팀장→타 부서 팀장", uLead, uLead2, true},
		{"팀장→대표", uLead, uCEO, true},
		{"부서장→대표", uHead, uCEO, true},
		{"부서장→팀장(더 낮은 층)", uHead, uLead, false},
		{"부서장→재무", uHead, uFinance, false},
		{"대표→부서장(더 낮은 층)", uCEO, uHead, false},
		{"재무→재무", uFinance, uFinance2, true},
		{"재무→부서장", uFinance, uHead, false},
		{"팀장→직원", uLead, uStaff, false},
		{"팀장→재무", uLead, uFinance, false},
		{"팀장→관리자", uLead, uAdmin, false},
		{"팀장→자기 자신", uLead, uLead, false},
		{"팀장→없는 사용자", uLead, 999, false},
	}
	for _, c := range cases {
		e := newEnv(t)
		_, err := e.delegation.Create(as(c.from), e.delegateIn(c.to))
		if c.ok && err != nil {
			t.Errorf("%s: 허용돼야 함: %v", c.name, err)
		}
		if !c.ok {
			is(t, err, contract.ErrValidation)
			if n := count(t, e.db, `SELECT COUNT(*) FROM approval_delegations`); n != 0 {
				t.Errorf("%s: 거부됐는데 행이 남음", c.name)
			}
		}
	}
	// 부서장 → 부서장(같은 층)은 허용
	e := newEnv(t)
	e.addSalesHead(t)
	if _, err := e.delegation.Create(as(uHead), e.delegateIn(uSalesHead)); err != nil {
		t.Fatalf("부서장→부서장: %v", err)
	}
	// 조직장이 아닌 manager 는 결재 단계를 맡지 않아 위임할 수 없다.
	e.org.SetHead(unitBuy1, 0)
	_, err := e.delegation.Create(as(uLead), e.delegateIn(uLead3))
	is(t, err, contract.ErrForbidden)
}

func TestDelegationPeriodValidation(t *testing.T) {
	e := newEnv(t)
	for name, in := range map[string]domain.DelegationInput{
		"기간 없음":      {DelegateID: uLead3},
		"종료가 시작보다 앞": {DelegateID: uLead3, From: e.now.Add(time.Hour), To: e.now},
		"이미 끝난 기간":   {DelegateID: uLead3, From: e.now.Add(-48 * time.Hour), To: e.now.Add(-24 * time.Hour)},
	} {
		_, err := e.delegation.Create(as(uLead), in)
		if !isErr(err, contract.ErrValidation) {
			t.Errorf("%s: 400 기대, %v", name, err)
		}
	}
}

// 권한 없는 역할은 위임을 지정·해제·조회할 수 없고 거부가 감사에 남는다.
func TestDelegationRequiresPermission(t *testing.T) {
	e := newEnv(t)
	d := e.mustDelegate(t, uLead, uLead3)
	for _, by := range []int64{uStaff, uAuditor, uAdmin} {
		_, err := e.delegation.Create(as(by), e.delegateIn(uLead3))
		is(t, err, contract.ErrForbidden)
		is(t, e.delegation.Revoke(as(by), d.ID), contract.ErrForbidden)
		_, _, err = e.delegation.ListMine(as(by))
		is(t, err, contract.ErrForbidden)
		_, err = e.delegation.Candidates(as(by))
		is(t, err, contract.ErrForbidden)
	}
	if !e.rec.Has("delegation.create", audit.TargetDelegation, audit.ResultDenied) ||
		!e.rec.Has("delegation.revoke", audit.TargetDelegation, audit.ResultDenied) {
		t.Error("거부가 감사에 남지 않음")
	}
	_, err := e.delegation.Create(contractNoUser(), e.delegateIn(uLead3))
	is(t, err, contract.ErrUnauthenticated)
}

// 해제는 위임자 본인만, 한 번만. 지정·해제 모두 감사에 남는다.
func TestDelegationRevokeRulesAndAudit(t *testing.T) {
	e := newEnv(t)
	d := e.mustDelegate(t, uLead, uHead)
	rec, ok := lastRecord(e, "delegation.create")
	if !ok || rec.TargetType != audit.TargetDelegation || !hasDiff(rec, "delegate_id", "3") || !hasDiff(rec, "from", "2026-11-10T08:00:00Z") {
		t.Fatalf("지정 감사 기록: %+v", rec)
	}

	// 수임자나 남은 해제할 수 없다(존재를 숨김).
	is(t, e.delegation.Revoke(as(uHead), d.ID), contract.ErrNotFound)
	is(t, e.delegation.Revoke(as(uLead3), d.ID), contract.ErrNotFound)
	is(t, e.delegation.Revoke(as(uLead), 9999), contract.ErrNotFound)

	if err := e.delegation.Revoke(as(uLead), d.ID); err != nil {
		t.Fatal(err)
	}
	rec, ok = lastRecord(e, "delegation.revoke")
	if !ok || rec.ActorID != uLead || rec.TargetID != "1" {
		t.Fatalf("해제 감사 기록: %+v", rec)
	}
	is(t, e.delegation.Revoke(as(uLead), d.ID), contract.ErrInvalidState)

	given, received, err := e.delegation.ListMine(as(uLead))
	if err != nil || len(given) != 1 || given[0].RevokedAt == nil || len(received) != 0 {
		t.Fatalf("내 위임 목록: %+v %+v %v", given, received, err)
	}
	_, received, _ = e.delegation.ListMine(as(uHead))
	if len(received) != 1 || received[0].DelegatorID != uLead {
		t.Fatalf("받은 위임 목록: %+v", received)
	}
}

// 감사 기록이 실패하면 위임 지정·해제·위임 결재 모두 롤백된다.
func TestDelegationAuditFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.rec.FailWith(errBoom)
	_, err := e.delegation.Create(as(uLead), e.delegateIn(uLead3))
	is(t, err, contract.ErrAuditFailed)
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_delegations`); n != 0 {
		t.Fatalf("감사 실패인데 위임 %d건이 남음", n)
	}
	e.rec.FailWith(nil)

	d := e.mustDelegate(t, uLead2, uHead)
	e.rec.FailWith(errBoom)
	is(t, e.delegation.Revoke(as(uLead2), d.ID), contract.ErrAuditFailed)
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_delegations WHERE revoked_at IS NOT NULL`); n != 0 {
		t.Fatal("감사 실패인데 해제가 남음")
	}
	e.rec.FailWith(nil)

	pr := e.submitted(t, uStaff2, 500_000)
	e.rec.FailWith(errBoom)
	_, err = e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrAuditFailed)
	got, _ := e.purchase.Get(as(uStaff2), pr.ID)
	if got.Status != domain.PRSubmitted || got.PurchaseOrderID != 0 {
		t.Fatalf("위임 결재가 롤백되지 않음: %+v", got)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE action='approve'`); n != 0 {
		t.Fatalf("이력이 남음: %d", n)
	}
}

// 위임이 없는 타 조직 건은 여전히 보이지 않고 결재할 수 없다. 위임은 위임자가 현재 결재자인 단계에만 열린다.
func TestDelegationKeepsDepartmentIsolation(t *testing.T) {
	e := newEnv(t)
	e.addSalesHead(t)
	// uLead2(영업1팀 팀장) → uLead(구매1팀 팀장) 위임. 영업팀 건(요청자 uStaff2)과 구매팀 건(요청자 uStaff).
	e.mustDelegate(t, uLead2, uLead)
	sales := e.submitted(t, uStaff2, 500_000)
	buy := e.submitted(t, uStaff, 500_000)

	pending, _ := e.approval.ListPending(as(uLead))
	if len(pending) != 2 {
		t.Fatalf("구매팀 건 + 위임받은 영업팀 건 2건 기대: %+v", pending)
	}
	for _, p := range pending {
		if p.ID == sales.ID && p.DelegatedFromID != uLead2 {
			t.Fatalf("영업팀 건은 위임 표시여야 함: %+v", p)
		}
		if p.ID == buy.ID && p.DelegatedFromID != 0 {
			t.Fatalf("본인 팀 건은 위임 표시가 아님: %+v", p)
		}
	}

	// 위임받지 않은 다른 타 조직 건: 구매2팀 팀장 uLead3 은 영업팀 건에 손댈 수 없다.
	if p, _ := e.approval.ListPending(as(uLead3)); len(p) != 0 {
		t.Fatalf("uLead3 결재함: %+v", p)
	}
	_, err := e.approval.Decide(as(uLead3), sales.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrNotFound)
	if _, err := e.purchase.Get(as(uLead3), sales.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("위임받지 않았는데 타 부서 건이 열림: %v", err)
	}
	if _, err := e.approval.History(as(uLead3), sales.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("위임받지 않았는데 타 부서 이력이 열림: %v", err)
	}

	// 위임은 위임자의 단계에만 열린다: 영업팀 건의 부서장 단계는 uLead 가 대신할 수 없다.
	big := e.submitted(t, uStaff2, 2_000_000) // 팀장(uLead2) → 영업부서장
	if _, err := e.approval.Decide(as(uLead), big.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("팀장 단계 위임 결재: %v", err)
	}
	_, err = e.approval.Decide(as(uLead), big.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrForbidden)
}

func TestDelegationCandidates(t *testing.T) {
	e := newEnv(t)
	cs, err := e.delegation.Candidates(as(uLead))
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]domain.DelegateCandidate{}
	for _, c := range cs {
		got[c.ID] = c
	}
	// 팀장은 같은 층 이상의 조직장에게(타 부서 포함). 본인·직원·재무·감사·관리자는 제외.
	for _, id := range []int64{uLead3, uHead, uLead2, uCEO} {
		if _, ok := got[id]; !ok {
			t.Errorf("후보에 %d 가 있어야 함", id)
		}
	}
	for _, id := range []int64{uLead, uStaff, uFinance, uAuditor, uAdmin} {
		if _, ok := got[id]; ok {
			t.Errorf("후보에 %d 가 없어야 함", id)
		}
	}
	// 후보 라벨은 조직도의 직책 이름과 조직 이름이다(users.position 이 아니다).
	if c := got[uHead]; c.Title != "부서장" || c.UnitName != "구매팀" {
		t.Errorf("부서장 후보 라벨: %+v", c)
	}
	if c := got[uCEO]; c.Title != "대표" || c.UnitName != "PL 주식회사" {
		t.Errorf("대표 후보 라벨: %+v", c)
	}
}

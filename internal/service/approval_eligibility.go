package service

import (
	"context"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 결재 후보·위임 판정의 유일한 정의다(B21 3.6). 결재 화면·결재함·상신·회수·위임 해제·재지정·소멸 목록이 모두 이 파일의 함수를 쓴다.
//
//   - 직책 단계의 직접 후보 = 상신 때 정해진 배정자(관리자 재지정이 있으면 마지막 재지정의 새 결재자). 재무 단계는 재직 중인 finance 역할 전원(풀).
//   - 후보 = 직접 후보(재직·결재 권한) ∪ 그 직접 후보가 맡긴 유효한 위임의 수임자 − 요청자 − 이 차수에서 이미 다른 단계를 결재한 사람.
//   - 한 사람이 같은 차수의 두 단계를 결재할 수 없다는 규칙의 정의도 여기(canAssignDistinct, 결재 시점 DecidedIDs 제외)다.

// eligibility 는 후보 판정에 필요한 저장소와 조직 조회다.
type eligibility struct {
	approvals   repository.ApprovalRepository
	delegations repository.DelegationRepository
	purchases   repository.PurchaseRepository
	lines       repository.SQLLines
	org         contract.OrgDirectory
}

func isPool(k domain.StepKind) bool {
	ki, ok := kindOf(k)
	return ok && ki.Pool
}

// usable 은 이 사용자가 지금 결재할 수 있는 상태인지다: 재직(users.active)·결재 권한 역할·조직 기준 재직(퇴사 효력일 이후가 아님)·휴직 중이 아님(B50 8.3).
// 휴직 중인 사람은 직접 후보·위임 수임자·재무 풀 어느 쪽으로도 결재할 수 없다(후보 계산이 모두 이 함수를 거친다). 휴직은 로그인을 막지 않는다.
func (e eligibility) usable(ctx context.Context, q contract.Tx, id int64, day contract.Day) (bool, error) {
	return e.usableFor(ctx, q, id, day, domain.RoleApprove)
}

// usableFor 는 단계 역할(approve·agree)에 맞는 권한(approval:decide·approval:agree)으로 usable 을 판정한다.
func (e eligibility) usableFor(ctx context.Context, q contract.Tx, id int64, day contract.Day, role string) (bool, error) {
	perm := contract.PermApprovalDecide
	if role == domain.RoleAgree {
		perm = contract.PermApprovalAgree
	}
	u, err := e.lines.User(ctx, q, id)
	if errors.Is(err, contract.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !u.Active || !u.Can(perm) {
		return false, nil
	}
	if e.org == nil {
		return true, nil
	}
	if f, ok := ctx.Value(orgFactsKey{}).(*orgFacts); ok {
		return !f.left[id] && !f.absent[id], nil // 트랜잭션 안에서는 미리 읽어 둔 조직 기준 퇴사·휴직 상태만 쓴다
	}
	p, err := e.org.Person(ctx, id, day)
	switch {
	case errors.Is(err, contract.ErrNotInOrg), errors.Is(err, contract.ErrNotFound):
		return true, nil // 조직에 등록되지 않은 사용자는 users 상태만 본다
	case err != nil:
		return false, err
	}
	return p.Active && !p.OnLeave, nil
}

// onLeave 는 사용자가 지금 휴직 중인지다(결재자 재지정 문구가 "재직 중이 아님"과 "휴직 중"을 가르는 데 쓴다). usableFor 와 같은 조회 방식이다.
func (e eligibility) onLeave(ctx context.Context, id int64, day contract.Day) bool {
	if f, ok := ctx.Value(orgFactsKey{}).(*orgFacts); ok {
		return f.absent[id]
	}
	if e.org == nil {
		return false
	}
	p, err := e.org.Person(ctx, id, day)
	return err == nil && p.Active && p.OnLeave
}

// direct 는 단계의 직접 후보(재직·결재 권한 조건을 통과한 사람)다.
func (e eligibility) direct(ctx context.Context, q contract.Tx, ref repository.StepRef, day contract.Day) ([]int64, error) {
	var ids []int64
	if isPool(ref.Kind) {
		pool, err := e.lines.ActiveByPerm(ctx, q, contract.PermFinanceStage)
		if err != nil {
			return nil, err
		}
		for _, u := range pool {
			ids = append(ids, u.ID)
		}
	} else {
		var err error
		ids, err = e.approvals.StepAssignees(ctx, q, ref.PRID, ref.Round, ref.Seq)
		if err != nil {
			return nil, err
		}
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		ok, err := e.usableFor(ctx, q, id, day, ref.Role)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// approversOf 는 ref 단계를 결재할 수 있는 사람이다: 직접 후보와 그들의 유효한 위임 수임자에서 요청자와(skipDecided 이면) 이 차수에서
// 이미 다른 단계를 결재한 사람을 뺀다. excludeDelegation 은 해제를 가정할 위임이다(0 이면 없음).
func (e eligibility) approversOf(ctx context.Context, q contract.Tx, ref repository.StepRef, now time.Time, excludeDelegation int64, skipDecided bool) ([]int64, error) {
	day := contract.DayOf(now)
	direct, err := e.direct(ctx, q, ref, day)
	if err != nil {
		return nil, err
	}
	cands := append([]int64{}, direct...)
	for _, d := range direct {
		if ref.Role == domain.RoleAgree {
			break // 합의는 위임할 수 없다(명세 3.8, D11)
		}
		dels, err := e.delegations.ActiveFrom(ctx, q, d, now, excludeDelegation)
		if err != nil {
			return nil, err
		}
		for _, dg := range dels {
			ok, err := e.usable(ctx, q, dg.DelegateID, day)
			if err != nil {
				return nil, err
			}
			if ok {
				cands = append(cands, dg.DelegateID)
			}
		}
	}
	skip := map[int64]bool{ref.RequesterID: true}
	if skipDecided {
		decided, err := e.delegations.DecidedIDs(ctx, q, ref.PRID)
		if err != nil {
			return nil, err
		}
		for _, id := range decided {
			skip[id] = true
		}
	}
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range cands {
		if !skip[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// delegatorMatches 는 위임자 delegatorID 가 ref 단계의 유효한 처리자인지다. 후보 계산(direct)과 같은 usable 조건(재직·결재 권한·조직 재직)을
// 거치므로, 결재 권한을 잃거나 퇴사한 위임자의 수임자는 그 단계를 대신 결재할 수 없다(위임은 무효로만 취급하며 자동 해제하지 않는다).
func (e eligibility) delegatorMatches(ctx context.Context, q contract.Tx, ref repository.StepRef, delegatorID int64, day contract.Day) (bool, error) {
	if isPool(ref.Kind) {
		u, err := e.lines.User(ctx, q, delegatorID)
		if errors.Is(err, contract.ErrNotFound) {
			return false, nil
		}
		if err != nil || !u.Active || !u.Can(contract.PermFinanceStage) {
			return false, err
		}
		return e.usable(ctx, q, delegatorID, day)
	}
	ids, err := e.approvals.StepAssignees(ctx, q, ref.PRID, ref.Round, ref.Seq)
	if err != nil {
		return false, err
	}
	if !hasID(ids, delegatorID) {
		return false, nil
	}
	return e.usable(ctx, q, delegatorID, day)
}

// findActing 은 delegateID 가 ref 단계를 대신 결재할 수 있게 해 주는 유효한 위임 중 가장 먼저 지정된 것이다.
// 요청자 본인이 위임자인 위임은 인정하지 않는다(요청자는 자기 건의 결재 단계를 맡지 않는다).
func (e eligibility) findActing(ctx context.Context, q contract.Tx, delegateID int64, ref repository.StepRef, now time.Time) (repository.DelegationRec, bool, error) {
	dels, err := e.delegations.ActiveTo(ctx, q, delegateID, now)
	if err != nil {
		return repository.DelegationRec{}, false, err
	}
	for _, d := range dels {
		if d.DelegatorID == ref.RequesterID {
			continue
		}
		ok, err := e.delegatorMatches(ctx, q, ref, d.DelegatorID, contract.DayOf(now))
		if err != nil {
			return repository.DelegationRec{}, false, err
		}
		if ok {
			return d, true, nil
		}
	}
	return repository.DelegationRec{}, false, nil
}

// canView 는 위임으로 prID 를 볼 수 있는지다. 위임이 유효한 동안(기간 안, 해제 전)에 한해 열린다:
// 지금 차례인 단계를 대신 결재할 수 있거나, 그 위임으로 이 차수의 단계를 이미 대신 결재한 진행 중 건.
func (e eligibility) canView(ctx context.Context, q contract.Tx, prID, userID int64, now time.Time) (bool, error) {
	rec, err := e.purchases.GetPR(ctx, q, prID, repository.ScopeAll)
	if errors.Is(err, contract.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if (rec.Status != domain.PRSubmitted && rec.Status != domain.PRInReview) || rec.RequesterID == userID {
		return false, nil
	}
	dels, err := e.delegations.ActiveTo(ctx, q, userID, now)
	if err != nil || len(dels) == 0 {
		return false, err
	}
	onBehalf, err := e.approvals.DecidedOnBehalf(ctx, q, prID, rec.Round, userID)
	if err != nil {
		return false, err
	}
	for _, d := range dels {
		if hasID(onBehalf, d.DelegatorID) {
			return true, nil
		}
	}
	open, err := e.approvals.OpenSteps(ctx, q, prID, rec.Round)
	if err != nil {
		return false, err
	}
	for _, ref := range open {
		if ref.Status != domain.StepPending {
			continue
		}
		for _, d := range dels {
			if d.DelegatorID == rec.RequesterID {
				continue
			}
			if ok, err := e.delegatorMatches(ctx, q, ref, d.DelegatorID, contract.DayOf(now)); err != nil || ok {
				return ok, err
			}
		}
	}
	return false, nil
}

// canAssignDistinct 는 각 단계마다 후보 한 명씩, 서로 다른 사람을 고를 수 있는지다(단계는 최대 몇 개라 완전 탐색).
func canAssignDistinct(sets [][]int64) bool {
	used := map[int64]bool{}
	var rec func(i int) bool
	rec = func(i int) bool {
		if i == len(sets) {
			return true
		}
		for _, id := range sets[i] {
			if used[id] {
				continue
			}
			used[id] = true
			if rec(i + 1) {
				return true
			}
			used[id] = false
		}
		return false
	}
	return rec(0)
}

func hasID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// remainingAssignable 은 아직 결재가 남은 단계들(open)을 서로 다른 결재자가 맡을 수 있는지다. 결재 시점 검사와 위임 해제 검사가 쓴다.
// actor 는 이 결재를 막 마쳐서 남은 단계를 맡을 수 없게 되는 사람이다(0 이면 없음). excludeDelegation 은 해제를 가정할 위임이다.
func (e eligibility) remainingAssignable(ctx context.Context, q contract.Tx, open []repository.StepRef, now time.Time, actor, excludeDelegation int64) (bool, error) {
	sets := make([][]int64, len(open))
	for i, ref := range open {
		ids, err := e.approversOf(ctx, q, ref, now, excludeDelegation, true)
		if err != nil {
			return false, err
		}
		kept := []int64{}
		for _, id := range ids {
			if id != actor {
				kept = append(kept, id)
			}
		}
		sets[i] = kept
	}
	return canAssignDistinct(sets), nil
}

// rankFinance 는 재무 역할의 서열 값이다. 재무는 직책 서열과 별개 축이라 재무→재무 위임만 허용되고, 비교에는 쓰이지 않는다.
const rankFinance = 1 << 20

// headRank 는 사용자가 조직장인 조직 중 가장 높은 층(팀 1 … 대표 4)과 그 조직이다. 조직장이 아니면 0 이다(직무대행은 제외).
func (e eligibility) headRank(ctx context.Context, userID int64, day contract.Day) (int, contract.OrgUnit, error) {
	if e.org == nil {
		return 0, contract.OrgUnit{}, errors.New("조직 서비스가 연결되지 않았습니다")
	}
	units, err := e.org.UnitsHeadedBy(ctx, userID, day)
	if err != nil {
		if errors.Is(err, contract.ErrNotInOrg) || errors.Is(err, contract.ErrNotFound) {
			return 0, contract.OrgUnit{}, nil
		}
		return 0, contract.OrgUnit{}, err
	}
	best, bu := 0, contract.OrgUnit{}
	for _, u := range units {
		if l := u.Kind.Level(); l > best {
			best, bu = l, u
		}
	}
	return best, bu, nil
}

// rankOf 는 결재 직책 서열의 유일한 정의다: 재무 단계 구조 권한(approval:finance_stage)을 가진 사람은 rankFinance, 그 밖은 조직장인 조직의 가장 높은 층(조직장이 아니면 0).
func (e eligibility) rankOf(ctx context.Context, u repository.UserRec, day contract.Day) (int, error) {
	if u.Can(contract.PermFinanceStage) {
		return rankFinance, nil
	}
	r, _, err := e.headRank(ctx, u.ID, day)
	return r, err
}

// orgFacts 는 쓰기 트랜잭션에 들어가기 전에 조직 조회에서 읽어 둔 값이다. DB 연결이 하나라서 트랜잭션 안에서 조직 서비스(자기 DB 핸들로 읽는다)를
// 부르면 멈추므로, 결재 후보 판정이 필요한 쓰기 메서드는 트랜잭션 밖에서 prefetchOrgFacts 로 ctx 에 실어 보낸다.
type orgFacts struct {
	left   map[int64]bool // 조직 기준 오늘 퇴사 상태인 결재 권한 역할 사용자
	absent map[int64]bool // 조직 기준 오늘 휴직 중인 결재 권한 역할 사용자(B50)
	ver    string         // 읽기 전 SQLLines.OrgVersion 지문. 트랜잭션 시작 때 다시 읽어 다르면 낡은 판정이라 거부한다
}

type orgFactsKey struct{}

// prefetchOrgFacts 는 결재 권한 역할(manager·finance) 사용자 전원의 조직 기준 재직 상태를 읽어 ctx 에 실어 돌려준다. 트랜잭션 밖에서 부른다.
// ctx 에는 가변 포인터를 싣는다: 쓰기가 낡은 판정(조직 지문 불일치)으로 거부되면 write 가 같은 포인터를 다시 읽어 채우고 재시도한다.
func (b Base) prefetchOrgFacts(ctx context.Context) (context.Context, error) {
	if b.Org == nil {
		return ctx, nil
	}
	f, err := b.loadOrgFacts(ctx)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, orgFactsKey{}, &f), nil
}

func (b Base) loadOrgFacts(ctx context.Context) (orgFacts, error) {
	lines := repository.SQLLines{}
	var ids []int64
	seen := map[int64]bool{}
	for _, perm := range []contract.Permission{contract.PermApprovalDecide, contract.PermApprovalAgree} {
		us, err := lines.ActiveByPerm(ctx, b.DB, perm)
		if err != nil {
			return orgFacts{}, err
		}
		for _, u := range us {
			if !seen[u.ID] {
				seen[u.ID] = true
				ids = append(ids, u.ID)
			}
		}
	}
	ver, err := lines.OrgVersion(ctx, b.DB)
	if err != nil {
		return orgFacts{}, err
	}
	people, err := b.Org.Persons(ctx, ids, contract.DayOf(b.now()))
	if err != nil {
		return orgFacts{}, err
	}
	f := orgFacts{left: map[int64]bool{}, absent: map[int64]bool{}, ver: ver}
	for id, p := range people {
		if !p.Active {
			f.left[id] = true
		} else if p.OnLeave {
			f.absent[id] = true
		}
	}
	return f, nil
}

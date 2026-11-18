package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 퇴사한 사람의 과거 기록에는 이름 옆에 "(퇴사)"를 붙인다(B20 명세 3.7·AC-51). 표시 때만 붙이고 DB 의 이름(결재선 스냅샷의 이름 포함)은 그대로 둔다.
// 퇴사 상태 판정은 조직 서비스의 LeftUsers(오늘 KST 기준, 마지막 근무일 당일에는 붙지 않는다)다. 조직 서비스가 이 메서드를 갖지 않거나
// 조회가 실패하면 이름을 그대로 둔다(표시 보조라서 업무를 막지 않는다).

const leftSuffix = " (퇴사)"

// leftLister 는 조직 서비스가 퇴사 상태를 알려 주는 메서드다(구현: internal/org Service).
type leftLister interface {
	LeftUsers(ctx context.Context, ids []int64) (map[int64]bool, error)
}

// leftSet 은 ids 중 오늘 기준 퇴사 상태인 사용자다. 트랜잭션 밖에서만 부른다(조직 서비스가 자기 DB 핸들로 읽는다).
func (b Base) leftSet(ctx context.Context, ids []int64) map[int64]bool {
	ll, ok := b.Org.(leftLister)
	if !ok || len(ids) == 0 {
		return nil
	}
	left, err := ll.LeftUsers(ctx, ids)
	if err != nil {
		return nil
	}
	return left
}

func withLeft(name string, id int64, left map[int64]bool) string {
	if left[id] && name != "" {
		return name + leftSuffix
	}
	return name
}

// markPRNames 는 구매요청 목록의 요청자·현재 결재자 이름에 퇴사 표시를 붙인다.
func (b Base) markPRNames(ctx context.Context, prs []domain.PurchaseRequest) {
	b.fillDraftUnits(ctx, prs)
	var ids []int64
	for _, p := range prs {
		ids = append(ids, p.RequesterID)
		for _, st := range p.Steps {
			if st.AssigneeID != 0 {
				ids = append(ids, st.AssigneeID)
			}
		}
	}
	left := b.leftSet(ctx, ids)
	if len(left) == 0 {
		return
	}
	for i := range prs {
		prs[i].RequesterName = withLeft(prs[i].RequesterName, prs[i].RequesterID, left)
		for j := range prs[i].Steps {
			st := &prs[i].Steps[j]
			st.AssigneeName = withLeft(st.AssigneeName, st.AssigneeID, left)
		}
	}
}

// markHistoryNames 는 결재 이력의 처리자·위임자 이름에 퇴사 표시를 붙인다.
func (b Base) markHistoryNames(ctx context.Context, recs []domain.ApprovalRecord) {
	var ids []int64
	for _, r := range recs {
		ids = append(ids, r.ActorID)
		if r.OnBehalfOfID != 0 {
			ids = append(ids, r.OnBehalfOfID)
		}
	}
	left := b.leftSet(ctx, ids)
	if len(left) == 0 {
		return
	}
	for i := range recs {
		recs[i].ActorName = withLeft(recs[i].ActorName, recs[i].ActorID, left)
		recs[i].OnBehalfOfName = withLeft(recs[i].OnBehalfOfName, recs[i].OnBehalfOfID, left)
	}
}

// markDelegationNames 는 위임 목록의 위임자·수임자 이름에 퇴사 표시를 붙인다.
func (b Base) markDelegationNames(ctx context.Context, ds []domain.Delegation) {
	var ids []int64
	for _, d := range ds {
		ids = append(ids, d.DelegatorID, d.DelegateID)
	}
	left := b.leftSet(ctx, ids)
	if len(left) == 0 {
		return
	}
	for i := range ds {
		ds[i].DelegatorName = withLeft(ds[i].DelegatorName, ds[i].DelegatorID, left)
		ds[i].DelegateName = withLeft(ds[i].DelegateName, ds[i].DelegateID, left)
	}
}

// fillDraftUnits 는 아직 상신하지 않은 건(상신 당시 조직이 없다)의 소속 칸을 요청자의 현재 소속 조직 경로로 채운다(명세 7.4).
// 트랜잭션 밖에서만 부른다. 조직 조회가 실패하면 비워 둔다(표시 보조).
func (b Base) fillDraftUnits(ctx context.Context, prs []domain.PurchaseRequest) {
	if b.Org == nil {
		return
	}
	day := contract.DayOf(b.now())
	paths := map[int64]string{}
	for i := range prs {
		p := &prs[i]
		if p.RequesterUnitPath != "" || p.OwnerUnitID == 0 || p.Status != domain.PRDraft {
			continue
		}
		txt, ok := paths[p.OwnerUnitID]
		if !ok {
			if path, err := b.Org.Path(ctx, p.OwnerUnitID, day); err == nil {
				txt = contract.OrgPathText(path)
			}
			paths[p.OwnerUnitID] = txt
		}
		p.RequesterUnitPath = txt
	}
}

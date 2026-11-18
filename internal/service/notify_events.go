package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B23 단계 2: 업무 사건 알림(앱 안 알림만). 모두 쓰기 트랜잭션 안에서 만들고 실패하면 호출자 트랜잭션이 함께 롤백된다.
// 알림 받을 사람 계산은 DB 만 읽는다(트랜잭션 안에서 조직 서비스를 부르면 멈추므로 조직 조회가 필요한 수신 담당자는 미리 구해 넘긴다).

// 알림 종류(열린 문자열). 화면은 제목·본문을 그대로 보인다.
const (
	noteApprovalPending    = "approval_pending"
	noteApprovalRejected   = "approval_rejected"
	noteApprovalDone       = "approval_done"
	noteAgreementObjected  = "agreement_objected"
	noteDocumentCC         = "document_cc"
	noteReceiptArrived     = "receipt_arrived"
	noteReceiptReturned    = "receipt_returned"
	noteReceiptDone        = "receipt_done"
	noteSubmissionCanceled = "submission_cancelled"
)

// docLink 는 문서 상세 링크다. 구매 품의서는 구매요청 화면, 양식 문서는 전자결재 화면이다.
func docLink(kind string, id int64) string {
	if kind != "" && kind != string(domain.FormKindPurchase) {
		return fmt.Sprintf("/docs/%d", id)
	}
	return fmt.Sprintf("/purchase-requests/%d", id)
}

func pendingLink(kind string, id int64) string {
	if kind != "" && kind != string(domain.FormKindPurchase) {
		return fmt.Sprintf("/docs/%d", id)
	}
	return fmt.Sprintf("/approvals/%d", id)
}

// notifyUsers 는 ids(중복 제거, skip 제외)에게 같은 알림을 만든다. 만든 건수를 돌려준다.
func (b Base) notifyUsers(ctx context.Context, tx contract.Tx, ids []int64, skip int64, kind, title, body, link string, refID int64) (int, error) {
	seen := map[int64]bool{skip: true}
	n := 0
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		if err := b.notify(ctx, tx, repository.NotificationInsert{UserID: id, Kind: kind, Title: title, Body: body, Link: link, RefType: "purchase_request", RefID: refID}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// pendingHandlers 는 지금 처리할 차례인 단계(결재·합의)를 맡은 사람과 그의 유효한 위임 수임자(결재 단계만), 재무 단계는 재직 재무 담당 풀이다.
// 결재 후보 판정(approversOf)과 달리 조직 조회를 하지 않는 근사다(알림 대상 계산 전용, 권한 판정에는 쓰지 않는다).
func (b Base) pendingHandlers(ctx context.Context, tx contract.Tx, prID int64, round int) ([]int64, error) {
	steps, err := repository.SQLApprovals{}.PendingSteps(ctx, tx, prID, round)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, st := range steps {
		if isPool(st.Kind) {
			pool, err := repository.SQLLines{}.ActiveByPerm(ctx, tx, contract.PermFinanceStage)
			if err != nil {
				return nil, err
			}
			for _, u := range pool {
				out = append(out, u.ID)
			}
			continue
		}
		ids, err := repository.SQLApprovals{}.StepAssignees(ctx, tx, prID, round, st.Seq)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
		if st.Role == domain.RoleAgree {
			continue // 합의는 위임할 수 없다
		}
		for _, d := range ids {
			dels, err := repository.SQLDelegations{}.ActiveFrom(ctx, tx, d, b.now(), 0)
			if err != nil {
				return nil, err
			}
			for _, dg := range dels {
				out = append(out, dg.DelegateID)
			}
		}
	}
	return out, nil
}

// ccUsers 는 현재 차수의 참조 대상 사용자들이다.
func (b Base) ccUsers(ctx context.Context, tx contract.Tx, prID int64, round int) ([]int64, error) {
	recs, err := repository.SQLApprovals{}.ListRecipients(ctx, tx, prID, round)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, r := range recs {
		if r.Kind == "cc" && r.TargetType == "user" {
			out = append(out, r.TargetID)
		}
	}
	return out, nil
}

// notifyPending 은 새로 차례가 된 결재·합의 처리자에게 알린다.
func (b Base) notifyPending(ctx context.Context, tx contract.Tx, rec repository.PRRecord, actor int64) (int, error) {
	ids, err := b.pendingHandlers(ctx, tx, rec.ID, rec.Round)
	if err != nil {
		return 0, err
	}
	return b.notifyUsers(ctx, tx, ids, actor, noteApprovalPending, "결재할 문서가 도착했습니다",
		fmt.Sprintf("「%s」(%s) 문서가 지금 처리할 차례입니다. 기안: %s", rec.Title, rec.No, rec.RequesterName), pendingLink(rec.FormKind, rec.ID), rec.ID)
}

// notifyCC 는 참조 대상에게 알린다(상신 때).
func (b Base) notifyCC(ctx context.Context, tx contract.Tx, rec repository.PRRecord, actor int64) (int, error) {
	ids, err := b.ccUsers(ctx, tx, rec.ID, rec.Round)
	if err != nil {
		return 0, err
	}
	return b.notifyUsers(ctx, tx, ids, actor, noteDocumentCC, "참조 문서가 도착했습니다",
		fmt.Sprintf("「%s」(%s) 문서의 참조로 지정되었습니다. 기안: %s", rec.Title, rec.No, rec.RequesterName), docLink(rec.FormKind, rec.ID), rec.ID)
}

// stepActors 는 현재 차수에서 이미 처리한(결재·합의) 사람들이다.
func (b Base) stepActors(ctx context.Context, tx contract.Tx, prID int64, round int) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT approver_id FROM approval_steps WHERE pr_id = ? AND round = ? AND approver_id IS NOT NULL`, prID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// receiptHandlers 는 수신처 조직의 접수 담당자(조직장·직무대행은 호출자가 미리 구해 head 로 넘긴다)와 수신 담당자 전원이다.
func (b Base) receiptHandlers(ctx context.Context, tx contract.Tx, unitID int64, heads []int64) ([]int64, error) {
	rs, err := repository.SQLReceipts{}.Receivers(ctx, tx, unitID)
	if err != nil {
		return nil, err
	}
	out := append(append([]int64{}, heads...), rs...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// unitHeadIDs 는 조직의 조직장(공석이면 직무대행자)이다. 조직 조회가 있으므로 트랜잭션 밖에서 부른다. 없으면 빈 목록.
func (b Base) unitHeadIDs(ctx context.Context, unitID int64) []int64 {
	if b.Org == nil {
		return nil
	}
	h, err := b.Org.Head(ctx, unitID, contract.DayOf(b.now()))
	if err != nil || h.Vacant || h.UserID == 0 {
		return nil
	}
	return []int64{h.UserID}
}

// receiveUnits 는 사용자가 접수 권한이 있는 수신처(조직장·직무대행인 조직, 수신 담당자인 조직)다. 조직 조회가 있으므로 트랜잭션 밖에서 부른다.
func (b Base) receiveUnits(ctx context.Context, userID int64) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	add := func(id int64) {
		if id != 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if b.Org != nil {
		day := contract.DayOf(b.now())
		units, err := b.Org.UnitsHeadedBy(ctx, userID, day)
		if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
			return nil, err
		}
		for _, u := range units {
			add(u.ID)
		}
		if person, err := b.Org.Person(ctx, userID, day); err == nil {
			for _, pl := range person.Acting {
				add(pl.Unit.ID)
			}
		}
	}
	ids, err := repository.SQLReceipts{}.UnitsOf(ctx, b.DB, userID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		add(id)
	}
	return out, nil
}

// notifySubmitted 는 상신(재상신 포함) 때 첫 차례 결재자와 참조 대상에게 알린다. 같은 트랜잭션에서 DB 만 읽는다.
func (b Base) notifySubmitted(ctx context.Context, tx contract.Tx, rec repository.PRRecord, actor int64) error {
	if _, err := b.notifyPending(ctx, tx, rec, actor); err != nil {
		return err
	}
	_, err := b.notifyCC(ctx, tx, rec, actor)
	return err
}

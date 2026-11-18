package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ReceiptService 는 수신 처리(접수 → 담당자 지정 → 처리 완료, 반송)와 수신 담당자 관리다(B23 단계 2, 명세 3.11).
// 접수 권한은 수신처 조직의 조직장(직무대행 포함)과 그 조직의 수신 담당자이고 서비스가 판정한다(권한 선언은 receipt:handle).
type ReceiptService struct {
	Base
	Receipts repository.SQLReceipts
	Purchase repository.PurchaseRepository
}

var _ domain.DocReceiptService = (*ReceiptService)(nil)

// NewReceiptService 는 SQLite 저장소로 수신 서비스를 만든다.
func NewReceiptService(b Base) *ReceiptService {
	return &ReceiptService{Base: b, Purchase: repository.SQLPurchases{}}
}

const errNoReceiveRight = "접수할 권한이 없습니다. 수신처의 조직장이나 수신 담당자만 접수할 수 있습니다"

func hasUnit(units []int64, id int64) bool {
	for _, u := range units {
		if u == id {
			return true
		}
	}
	return false
}

// List 는 수신 건 조회다. DocID 가 있으면 그 문서(열람 범위 안)의 수신 건, 없으면 내가 접수 권한이 있는 수신처의 건이다.
func (s *ReceiptService) List(ctx context.Context, f domain.DocReceiptFilter) ([]domain.DocReceipt, error) {
	p, err := contract.Authorize(ctx, contract.PermInboxRead)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if f.DocID != 0 {
		if _, err := s.Purchase.GetPR(ctx, s.DB, f.DocID, scope); err != nil {
			return nil, err
		}
		out, err := s.Receipts.ListForDoc(ctx, s.DB, f.DocID)
		if out == nil {
			out = []domain.DocReceipt{}
		}
		if err == nil {
			s.markCanHandle(out, scope.ReceiveUnits, p.UserID)
		}
		return out, err
	}
	out, err := s.Receipts.ListForUnits(ctx, s.DB, scope.ReceiveUnits, f.Tab, f.Limit)
	if out == nil {
		out = []domain.DocReceipt{}
	}
	if err == nil {
		s.markCanHandle(out, scope.ReceiveUnits, p.UserID)
	}
	return out, err
}

// markCanHandle 는 수신 건마다 호출자가 처리할 수 있는지 채운다(처리 판정과 같은 조건: 수신처 접수 권한 또는 지정된 담당자).
func (s *ReceiptService) markCanHandle(list []domain.DocReceipt, units []int64, userID int64) {
	for i := range list {
		list[i].CanHandle = hasUnit(units, list[i].UnitID) || list[i].AssigneeID == userID
	}
}

// handled 는 호출자가 수신 건의 수신처를 접수할 수 있는지 확인하고 수신 건을 돌려준다. 권한이 없으면 접수 권한 문구로 거부한다.
func (s *ReceiptService) handled(ctx context.Context, p contract.Principal, id int64, action string) (domain.DocReceipt, error) {
	rc, err := s.Receipts.Get(ctx, s.DB, id)
	if err != nil {
		return rc, err
	}
	units, err := s.receiveUnits(ctx, p.UserID)
	if err != nil {
		return rc, err
	}
	if !hasUnit(units, rc.UnitID) {
		s.recordDenied(ctx, p, action, audit.TargetDocReceipt, id, "접수 권한 없음")
		return rc, fmt.Errorf("%w: %s", contract.ErrForbidden, errNoReceiveRight)
	}
	return rc, nil
}

func stateErr(msg string) error {
	return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg)
}

func (s *ReceiptService) change(ctx context.Context, p contract.Principal, action string, rc domain.DocReceipt, fn func(tx contract.Tx) error, diff []audit.DiffItem, notify func(tx contract.Tx) error) (domain.DocReceipt, error) {
	var out domain.DocReceipt
	err := s.write(ctx, p, action, audit.TargetDocReceipt, rc.ID, func(tx contract.Tx) error {
		if err := fn(tx); err != nil {
			if errors.Is(err, contract.ErrInvalidState) {
				return stateErr("수신 건의 상태가 바뀌었습니다. 화면을 새로 고치세요.")
			}
			return err
		}
		if notify != nil {
			if err := notify(tx); err != nil {
				return err
			}
		}
		var gerr error
		out, gerr = s.Receipts.Get(ctx, tx, rc.ID)
		if gerr != nil {
			return gerr
		}
		return s.record(ctx, tx, p, action, audit.TargetDocReceipt, rc.ID, diff...)
	})
	return out, err
}

// Accept 는 접수 대기 건을 접수한다.
func (s *ReceiptService) Accept(ctx context.Context, receiptID int64) (domain.DocReceipt, error) {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.accept", audit.TargetDocReceipt)
	if err != nil {
		return domain.DocReceipt{}, err
	}
	rc, err := s.handled(ctx, p, receiptID, "doc_receipt.accept")
	if err != nil {
		return rc, err
	}
	if rc.Status != "pending" {
		return rc, stateErr("접수 대기 상태가 아닙니다.")
	}
	return s.change(ctx, p, "doc_receipt.accept", rc, func(tx contract.Tx) error {
		return s.Receipts.Accept(ctx, tx, rc.ID, p.UserID, s.now())
	}, []audit.DiffItem{{Key: "doc_no", New: rc.DocNo}, {Key: "unit", New: rc.UnitName}}, nil)
}

// Assign 는 접수한 건의 담당자를 그 조직 구성원(재직자) 중에서 정한다. 이미 지정됐으면 바꾼다.
func (s *ReceiptService) Assign(ctx context.Context, receiptID, userID int64) (domain.DocReceipt, error) {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.assign", audit.TargetDocReceipt)
	if err != nil {
		return domain.DocReceipt{}, err
	}
	rc, err := s.handled(ctx, p, receiptID, "doc_receipt.assign")
	if err != nil {
		return rc, err
	}
	if rc.Status != "received" && rc.Status != "assigned" {
		return rc, stateErr("접수한 건만 담당자를 지정할 수 있습니다.")
	}
	u, err := repository.SQLLines{}.User(ctx, s.DB, userID)
	if errors.Is(err, contract.ErrNotFound) || err == nil && !u.Active {
		return rc, validation("재직 중인 담당자를 선택하세요")
	}
	if err != nil {
		return rc, err
	}
	if s.Org != nil {
		members, merr := s.Org.MembersOf(ctx, rc.UnitID, contract.DayOf(s.now()), true)
		if merr != nil {
			return rc, merr
		}
		if !hasUnit(members, userID) {
			return rc, validation("수신처 조직의 구성원만 담당자로 지정할 수 있습니다")
		}
	}
	return s.change(ctx, p, "doc_receipt.assign", rc, func(tx contract.Tx) error {
		return s.Receipts.Assign(ctx, tx, rc.ID, userID, p.UserID, s.now())
	}, []audit.DiffItem{{Key: "doc_no", New: rc.DocNo}, {Key: "unit", New: rc.UnitName}, {Key: "assignee", New: u.Name}}, nil)
}

// Complete 는 접수·지정된 건을 처리 완료한다(접수 담당자 또는 지정된 담당자). 기안자에게 알린다.
func (s *ReceiptService) Complete(ctx context.Context, receiptID int64, comment string) (domain.DocReceipt, error) {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.complete", audit.TargetDocReceipt)
	if err != nil {
		return domain.DocReceipt{}, err
	}
	rc, err := s.Receipts.Get(ctx, s.DB, receiptID)
	if err != nil {
		return rc, err
	}
	if rc.AssigneeID != p.UserID || rc.Status != "assigned" {
		if rc, err = s.handled(ctx, p, receiptID, "doc_receipt.complete"); err != nil {
			return rc, err
		}
	}
	if rc.Status != "received" && rc.Status != "assigned" {
		return rc, stateErr("접수한 건만 처리 완료할 수 있습니다.")
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 500 {
		return rc, validation("의견은 500자 이내로 입력하세요")
	}
	return s.change(ctx, p, "doc_receipt.complete", rc, func(tx contract.Tx) error {
		return s.Receipts.Complete(ctx, tx, rc.ID, p.UserID, comment, s.now())
	}, []audit.DiffItem{{Key: "doc_no", New: rc.DocNo}, {Key: "unit", New: rc.UnitName}, {Key: "comment", New: comment}},
		func(tx contract.Tx) error {
			_, err := s.notifyUsers(ctx, tx, []int64{rc.RequesterID}, p.UserID, noteReceiptDone, "수신 처리가 완료되었습니다",
				fmt.Sprintf("「%s」(%s) 문서의 수신이 %s에서 처리 완료되었습니다.", rc.DocTitle, rc.DocNo, rc.UnitName), fmt.Sprintf("/docs/%d", rc.DocID), rc.DocID)
			return err
		})
}

// Return 은 끝나지 않은 건을 반송한다(사유 필수). 기안자에게 알린다. 재송부는 없다.
func (s *ReceiptService) Return(ctx context.Context, receiptID int64, reason string) (domain.DocReceipt, error) {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.return", audit.TargetDocReceipt)
	if err != nil {
		return domain.DocReceipt{}, err
	}
	rc, err := s.handled(ctx, p, receiptID, "doc_receipt.return")
	if err != nil {
		return rc, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return rc, validation("반송 사유를 입력해야 합니다")
	}
	if utf8.RuneCountInString(reason) > 500 {
		return rc, validation("반송 사유는 500자 이내로 입력하세요")
	}
	if rc.Status == "done" || rc.Status == "returned" {
		return rc, stateErr("이미 처리가 끝난 수신 건입니다.")
	}
	return s.change(ctx, p, "doc_receipt.return", rc, func(tx contract.Tx) error {
		return s.Receipts.Return(ctx, tx, rc.ID, p.UserID, reason, s.now())
	}, []audit.DiffItem{{Key: "doc_no", New: rc.DocNo}, {Key: "unit", New: rc.UnitName}, {Key: "reason", New: reason}},
		func(tx contract.Tx) error {
			_, err := s.notifyUsers(ctx, tx, []int64{rc.RequesterID}, p.UserID, noteReceiptReturned, "수신이 반송되었습니다",
				fmt.Sprintf("「%s」(%s) 문서가 %s에서 반송되었습니다. 사유: %s", rc.DocTitle, rc.DocNo, rc.UnitName, reason), fmt.Sprintf("/docs/%d", rc.DocID), rc.DocID)
			return err
		})
}

// Receivers 는 수신처의 수신 담당자 목록이다(그 수신처를 접수할 수 있는 사람만 본다).
func (s *ReceiptService) Receivers(ctx context.Context, unitID int64) ([]domain.UnitReceiver, error) {
	p, err := contract.Authorize(ctx, contract.PermReceiptHandle)
	if err != nil {
		return nil, err
	}
	units, err := s.receiveUnits(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !hasUnit(units, unitID) {
		return nil, fmt.Errorf("%w: 이 조직의 수신 담당자를 볼 권한이 없습니다", contract.ErrForbidden)
	}
	ids, err := s.Receipts.Receivers(ctx, s.DB, unitID)
	if err != nil {
		return nil, err
	}
	out := []domain.UnitReceiver{}
	for _, id := range ids {
		if u, err := (repository.SQLLines{}).User(ctx, s.DB, id); err == nil && u.Active {
			out = append(out, domain.UnitReceiver{UserID: id, Name: u.Name}) // 퇴사자는 목록에서 빠진다
		}
	}
	return out, nil
}

// headOf 는 호출자가 조직의 조직장(직무대행 포함)인지 확인한다.
func (s *ReceiptService) headOf(ctx context.Context, p contract.Principal, unitID int64) error {
	for _, h := range s.unitHeadIDs(ctx, unitID) {
		if h == p.UserID {
			return nil
		}
	}
	return fmt.Errorf("%w: 그 조직의 조직장만 수신 담당자를 관리할 수 있습니다", contract.ErrForbidden)
}

// AddReceiver 는 조직장이 자기 조직 구성원(재직자)을 수신 담당자로 지정한다.
func (s *ReceiptService) AddReceiver(ctx context.Context, unitID, userID int64) error {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.receiver_add", audit.TargetDocReceipt)
	if err != nil {
		return err
	}
	if err := s.headOf(ctx, p, unitID); err != nil {
		s.recordDenied(ctx, p, "doc_receipt.receiver_add", audit.TargetDocReceipt, unitID, "조직장 아님")
		return err
	}
	u, err := repository.SQLLines{}.User(ctx, s.DB, userID)
	if errors.Is(err, contract.ErrNotFound) || err == nil && !u.Active {
		return validation("재직 중인 사용자를 선택하세요")
	}
	if err != nil {
		return err
	}
	unitName := fmt.Sprint(unitID)
	if s.Org != nil {
		members, merr := s.Org.MembersOf(ctx, unitID, contract.DayOf(s.now()), true) // 그 조직과 하위 조직의 재직 구성원(담당자 지정과 같은 규칙)
		if merr != nil {
			return merr
		}
		if !hasUnit(members, userID) {
			return validation("그 조직과 하위 조직의 구성원만 수신 담당자로 지정할 수 있습니다")
		}
		if un, err := s.Org.Unit(ctx, unitID, contract.DayOf(s.now())); err == nil {
			unitName = un.Name
		}
	}
	return s.write(ctx, p, "doc_receipt.receiver_add", audit.TargetDocReceipt, unitID, func(tx contract.Tx) error {
		if err := s.Receipts.AddReceiver(ctx, tx, unitID, userID, p.UserID, s.now()); err != nil {
			if errors.Is(err, contract.ErrInvalidState) {
				return fmt.Errorf("%w: 이미 수신 담당자입니다", contract.ErrInvalidState)
			}
			return err
		}
		return s.record(ctx, tx, p, "doc_receipt.receiver_add", audit.TargetDocReceipt, unitID,
			audit.DiffItem{Key: "unit", New: unitName}, audit.DiffItem{Key: "user", New: u.Name})
	})
}

// RemoveReceiver 는 조직장이 수신 담당자를 뺀다.
func (s *ReceiptService) RemoveReceiver(ctx context.Context, unitID, userID int64) error {
	p, err := s.authorize(ctx, contract.PermReceiptHandle, "doc_receipt.receiver_remove", audit.TargetDocReceipt)
	if err != nil {
		return err
	}
	if err := s.headOf(ctx, p, unitID); err != nil {
		s.recordDenied(ctx, p, "doc_receipt.receiver_remove", audit.TargetDocReceipt, unitID, "조직장 아님")
		return err
	}
	name := fmt.Sprint(userID)
	if u, err := (repository.SQLLines{}).User(ctx, s.DB, userID); err == nil {
		name = u.Name
	}
	return s.write(ctx, p, "doc_receipt.receiver_remove", audit.TargetDocReceipt, unitID, func(tx contract.Tx) error {
		if err := s.Receipts.RemoveReceiver(ctx, tx, unitID, userID); err != nil {
			if errors.Is(err, contract.ErrInvalidState) {
				return contract.ErrNotFound
			}
			return err
		}
		return s.record(ctx, tx, p, "doc_receipt.receiver_remove", audit.TargetDocReceipt, unitID, audit.DiffItem{Key: "user", New: name})
	})
}

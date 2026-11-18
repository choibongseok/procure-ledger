package service

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// RecordPrint 는 문서를 종이로 내보낸다는 사실을 감사에 남긴다(B23 단계 3, 명세 3.14). 열람 범위 안의 문서만 되고 구매 품의서도 같다.
// 출력 화면을 여는 것만으로는 남기지 않고 인쇄 버튼을 눌렀을 때 부른다.
func (s *ApprovalDocService) RecordPrint(ctx context.Context, id int64) error {
	p, err := contract.Authorize(ctx, contract.PermDraftRead)
	if err != nil {
		return err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return err
	}
	rec, err := s.Purchases.GetPR(ctx, s.DB, id, scope)
	if err != nil {
		return err
	}
	if rec.DisposedAt != "" {
		return fmt.Errorf("%w: 폐기된 문서는 출력할 수 없습니다.", contract.ErrInvalidState)
	}
	if err := s.auditPrivilegedView(ctx, p, scope, rec); err != nil {
		return err
	}
	return s.write(ctx, p, "document.print", audit.TargetApprovalDoc, id, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, "document.print", audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "doc_no", New: rec.No}, audit.DiffItem{Key: "status", New: fmt.Sprint(rec.Status)})
	})
}

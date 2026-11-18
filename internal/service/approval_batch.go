package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const maxBatchItems = 50

// batchMessage 는 거부 오류에서 화면에 보일 한국어 사유만 뽑는다("권한이 없습니다: 지금은 …" → "지금은 …").
func batchMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && i+2 < len(msg) {
		return msg[i+2:]
	}
	return msg
}

// DecideBatch 는 내 차례인 문서를 한꺼번에 승인·동의한다(결재 대기함, B23 단계 2). 최대 50건이고 승인만 있다(일괄 반려는 없다).
// 건마다 기존 Decide·Agree 를 그대로 부르므로 권한·상태 전이·감사 기록이 건마다 따로이고, 한 건의 실패(이미 처리됨·내 차례 아님·뒤 단계가 멈춤)가 다른 건을 막지 않는다.
// 요약 한 건(approval.batch)을 따로 감사에 남긴다. 의견은 공통 한 칸이다.
func (s *ApprovalService) DecideBatch(ctx context.Context, items []domain.BatchItem, comment string) ([]domain.BatchResult, error) {
	p, err := s.authorize(ctx, contract.PermApprovalAgree, "approval.batch", audit.TargetApproval)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, validation("처리할 문서를 선택하세요")
	}
	if len(items) > maxBatchItems {
		return nil, validation("한 번에 %d건까지 처리할 수 있습니다", maxBatchItems)
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 1000 {
		return nil, validation("의견은 1000자 이내로 입력하세요")
	}
	seen := map[int64]bool{}
	out := make([]domain.BatchResult, 0, len(items))
	ok := 0
	for _, it := range items {
		if seen[it.DocID] {
			continue
		}
		seen[it.DocID] = true
		var res domain.DecisionResult
		var derr error
		if p.Can(contract.PermApprovalDecide) {
			res, derr = s.Decide(ctx, it.DocID, domain.Decision{Approve: true, Comment: comment})
		} else {
			res, derr = s.Agree(ctx, it.DocID, domain.Decision{Approve: true, Comment: comment})
		}
		if derr != nil {
			out = append(out, domain.BatchResult{DocID: it.DocID, Message: batchMessage(derr)})
			continue
		}
		ok++
		out = append(out, domain.BatchResult{DocID: it.DocID, OK: true, Message: "처리했습니다", Status: res.Request.Status})
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "approval.batch", TargetType: audit.TargetApproval, TargetID: "batch", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "count", New: fmt.Sprint(len(out))}, {Key: "succeeded", New: fmt.Sprint(ok)}, {Key: "failed", New: fmt.Sprint(len(out) - ok)}}})
	}); err != nil {
		return out, err
	}
	return out, nil
}

package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 보존 연한 만료 문서 폐기(B29 ③, 6절).
// 서버는 자동으로 폐기하지 않는다. 관리자(retention:dispose)가 목록에서 골라 사유와 함께 승인할 때만 지운다.

const maxRetentionReason = 100

// legalHoldForms 는 재무·회계 증빙과 얽혀 연한이 지나도 폐기하지 않는 시스템 양식 코드다(구매 품의서·지출 결의서·재고 조정 전표).
// 양식 정의의 legal_hold 로 다른 양식도 폐기 불가로 만들 수 있다. 결재 완료 때 둘을 합쳐 문서에 스냅샷한다.
var legalHoldForms = map[string]bool{"PUR": true, "EXP": true, "ADJ": true}

// isLegalHold 는 양식 코드와 정의로 법정 보존(폐기 불가) 여부를 정한다.
func isLegalHold(code string, def domain.FormDefinition) bool {
	return def.LegalHold || legalHoldForms[code]
}

// RetentionService 는 domain.RetentionService 의 구현이다.
type RetentionService struct {
	Base
	Repo repository.SQLRetention
}

var _ domain.RetentionService = (*RetentionService)(nil)

func NewRetentionService(b Base) *RetentionService { return &RetentionService{Base: b} }

// today 는 KST 오늘 날짜(YYYY-MM-DD)다. 보존 기한은 그 날까지 보존이므로 기한이 오늘보다 이전이면 후보다.
func (s *RetentionService) today() string { return string(contract.DayOf(s.now())) }

func (s *RetentionService) Candidates(ctx context.Context, limit, offset int) ([]domain.RetentionCandidate, int, error) {
	if _, err := s.authorize(ctx, contract.PermRetentionRead, "doc.dispose_list", audit.TargetApprovalDoc); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.Repo.Candidates(ctx, s.DB, s.today(), limit, offset)
}

func (s *RetentionService) Holds(ctx context.Context) ([]domain.RetentionCandidate, error) {
	if _, err := s.authorize(ctx, contract.PermRetentionRead, "doc.dispose_list", audit.TargetApprovalDoc); err != nil {
		return nil, err
	}
	return s.Repo.Holds(ctx, s.DB, s.today())
}

func (s *RetentionService) ExpiredCount(ctx context.Context) (int, error) {
	if _, err := s.authorize(ctx, contract.PermRetentionRead, "doc.dispose_list", audit.TargetApprovalDoc); err != nil {
		return 0, err
	}
	return s.Repo.Count(ctx, s.DB, s.today())
}

func cleanReason(raw, what string) (string, error) {
	r := strings.TrimSpace(raw)
	if r == "" {
		return "", validation("%s 사유를 입력하세요.", what)
	}
	if utf8.RuneCountInString(r) > maxRetentionReason {
		return "", validation("%s 사유는 %d자 이내로 입력해 주세요.", what, maxRetentionReason)
	}
	return r, nil
}

// Dispose 는 선택한 문서를 폐기한다. 요청 전체가 한 트랜잭션이라 하나라도 후보가 아니거나 감사 기록이 실패하면 아무것도 지워지지 않는다.
// 후보 조건은 이 트랜잭션 안에서 다시 판정한다(그 사이 보류가 걸리거나 보존 기한이 바뀐 경우).
func (s *RetentionService) Dispose(ctx context.Context, in domain.DisposeInput) (int, error) {
	p, err := s.authorize(ctx, contract.PermRetentionDispose, "doc.dispose", audit.TargetApprovalDoc)
	if err != nil {
		return 0, err
	}
	if len(in.IDs) == 0 {
		return 0, validation("폐기할 문서를 선택하세요.")
	}
	if len(in.IDs) > domain.MaxDisposeBatch {
		return 0, validation("한 번에 %d건까지 폐기할 수 있습니다. 나누어 선택하세요.", domain.MaxDisposeBatch)
	}
	reason, err := cleanReason(in.Reason, "폐기")
	if err != nil {
		return 0, err
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(in.IDs))
	for _, id := range in.IDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	err = s.write(ctx, p, "doc.dispose", audit.TargetApprovalDoc, 0, func(tx contract.Tx) error {
		cands, err := s.Repo.CandidateInfo(ctx, tx, s.today(), ids)
		if err != nil {
			return err
		}
		var bad []string
		for _, id := range ids {
			if _, ok := cands[id]; !ok {
				no := fmt.Sprint(id)
				if c, e := s.Repo.DocNo(ctx, tx, id); e == nil {
					no = c.No
				}
				bad = append(bad, no)
			}
		}
		if len(bad) > 0 {
			return validation("폐기할 수 없는 문서가 포함되어 있습니다(%s). 목록을 새로 고친 뒤 다시 선택하세요.", strings.Join(firstN(bad, 5), ", "))
		}
		now := s.now()
		for _, id := range ids {
			c := cands[id]
			if err := s.Repo.Dispose(ctx, tx, id, p.UserID, reason, now); err != nil {
				return err
			}
			if err := s.record(ctx, tx, p, "doc.dispose", audit.TargetApprovalDoc, id,
				audit.DiffItem{Key: "doc_no", New: c.No},
				audit.DiffItem{Key: "form", New: c.FormName},
				audit.DiffItem{Key: "retention_until", New: c.RetentionUntil},
				audit.DiffItem{Key: "reason", New: reason}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("외 %d건", len(s)-n))
}

// Hold 는 문서를 폐기 후보에서 뺀다(법적 분쟁·감사 진행 중 등). 사유 필수.
func (s *RetentionService) Hold(ctx context.Context, id int64, reason string) error {
	p, err := s.authorize(ctx, contract.PermRetentionDispose, "doc.dispose_hold", audit.TargetApprovalDoc)
	if err != nil {
		return err
	}
	r, err := cleanReason(reason, "폐기 보류")
	if err != nil {
		return err
	}
	return s.write(ctx, p, "doc.dispose_hold", audit.TargetApprovalDoc, id, func(tx contract.Tx) error {
		doc, err := s.Repo.DocNo(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.SetHold(ctx, tx, id, true, r); err != nil {
			return holdErr(err)
		}
		return s.record(ctx, tx, p, "doc.dispose_hold", audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "doc_no", New: doc.No}, audit.DiffItem{Key: "reason", New: r})
	})
}

// ClearHold 는 폐기 보류를 푼다. 사유는 선택이다(비우면 "보류 해제").
func (s *RetentionService) ClearHold(ctx context.Context, id int64, reason string) error {
	p, err := s.authorize(ctx, contract.PermRetentionDispose, "doc.dispose_hold_clear", audit.TargetApprovalDoc)
	if err != nil {
		return err
	}
	r := strings.TrimSpace(reason)
	if utf8.RuneCountInString(r) > maxRetentionReason {
		return validation("보류 해제 사유는 %d자 이내로 입력해 주세요.", maxRetentionReason)
	}
	if r == "" {
		r = "보류 해제"
	}
	return s.write(ctx, p, "doc.dispose_hold_clear", audit.TargetApprovalDoc, id, func(tx contract.Tx) error {
		doc, err := s.Repo.DocNo(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.SetHold(ctx, tx, id, false, ""); err != nil {
			return holdErr(err)
		}
		return s.record(ctx, tx, p, "doc.dispose_hold_clear", audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "doc_no", New: doc.No}, audit.DiffItem{Key: "reason", New: r})
	})
}

func holdErr(err error) error {
	if err == contract.ErrInvalidState {
		return fmt.Errorf("%w: 이 문서는 폐기 보류를 바꿀 수 없는 상태입니다. 목록을 새로 고쳐 현재 상태를 확인하세요", contract.ErrInvalidState)
	}
	return err
}

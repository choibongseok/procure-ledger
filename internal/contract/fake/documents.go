package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// Documents 는 domain.DocumentService 의 가짜다. 대조 규칙은 흉내 내지 않고 미리 정한 Outcome 을 돌려준다
// (실제 규칙은 ai 의 internal/docverify 가 결정적 코드로 구현한다). 권한 검사와 감사 기록은 실제와 같다.
type Documents struct {
	mu       sync.Mutex
	rec      audit.Recorder
	verdicts map[int64]domain.Verdict
	next     int64

	// Outcome 은 Verify 가 돌려줄 판정이다. 기본은 일치.
	Outcome domain.Outcome
}

// NewDocuments 는 감사 기록기를 받아 만든다.
func NewDocuments(rec audit.Recorder) *Documents {
	return &Documents{rec: rec, verdicts: map[int64]domain.Verdict{}, Outcome: domain.OutcomeMatch}
}

func (d *Documents) record(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id int64, res audit.Result) error {
	return d.rec.Record(ctx, nil, audit.Entry{Actor: p, Action: action, TargetType: tt, TargetID: fmt.Sprint(id), Result: res})
}

// Verify 는 domain.DocumentService 를 구현한다.
func (d *Documents) Verify(ctx context.Context, in domain.VerifyInput) (domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentVerify)
	if err != nil {
		if p.UserID != 0 {
			_ = d.record(ctx, p, "document.verify", audit.TargetDocument, 0, audit.ResultDenied)
		}
		return domain.Verdict{}, err
	}
	if in.POID == 0 || in.Text == "" && len(in.Image) == 0 {
		return domain.Verdict{}, fmt.Errorf("%w: 발주와 문서 내용이 필요합니다", contract.ErrValidation)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	v := domain.Verdict{ID: d.next, DocumentID: d.next, POID: in.POID, ReceiptID: in.ReceiptID, Outcome: d.Outcome, CreatedAt: time.Now().UTC()}
	if err := d.record(ctx, p, "document.verify", audit.TargetVerdict, v.ID, audit.ResultSuccess); err != nil {
		d.next--
		return domain.Verdict{}, err
	}
	d.verdicts[v.ID] = v
	return v, nil
}

// GetVerdict 는 domain.DocumentService 를 구현한다.
func (d *Documents) GetVerdict(ctx context.Context, id int64) (domain.Verdict, error) {
	if _, err := contract.Authorize(ctx, contract.PermDocumentRead); err != nil {
		return domain.Verdict{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.verdicts[id]
	if !ok {
		return domain.Verdict{}, contract.ErrNotFound
	}
	return v, nil
}

// Confirm 은 domain.DocumentService 를 구현한다. 재무(document:confirm)만 가능하다.
func (d *Documents) Confirm(ctx context.Context, verdictID int64, in domain.ConfirmInput) (domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentConfirm)
	if err != nil {
		if p.UserID != 0 {
			_ = d.record(ctx, p, "verdict.confirm", audit.TargetVerdict, verdictID, audit.ResultDenied)
		}
		return domain.Verdict{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.verdicts[verdictID]
	if !ok {
		return domain.Verdict{}, contract.ErrNotFound
	}
	if err := d.record(ctx, p, "verdict.confirm", audit.TargetVerdict, verdictID, audit.ResultSuccess); err != nil {
		return domain.Verdict{}, err
	}
	v.ConfirmedBy, v.ConfirmedOutcome = p.UserID, in.Outcome
	d.verdicts[verdictID] = v
	return v, nil
}

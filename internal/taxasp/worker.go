package taxasp

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// Worker 는 전송·상태 조회 작업이다(명세 8.3). 서버 주기 작업이고 복구 리허설 모드에서는 시작하지 않는다.
//   - issued(발행 대기) 건은 조건부 갱신으로 가져가 Provider.Issue 를 부르고, 결과를 TaxDispatchStore 로 반영한다(전이는 서비스의 전이 표를 그대로 탄다).
//   - sent(전송됨) 건은 Provider.Status 로 국세청 수신 여부를 조회한다.
//   - 대행사 설정이 없거나 꺼져 있으면 아무것도 가져가지 않는다(건은 발행 대기로 남는다).
//   - 한 번에 가져가는 수(Batch)·동시 호출(Concurrency)·분당 호출(PerMin)·건당 응답 시간(CallTimeout)으로 묶는다. 한도를 넘은 일은 다음 주기로 미룬다(실패로 세지 않는다).
type Worker struct {
	Store    domain.TaxDispatchStore
	Settings SettingsSource
	Registry *Registry
	Now      func() time.Time

	Batch       int           // 기본 20
	Concurrency int           // 기본 4
	PerMin      int           // 기본 60
	CallTimeout time.Duration // 기본 15초
	Interval    time.Duration // Run 의 주기. 기본 20초
	Rehearsal   bool          // 참이면 어떤 호출도 하지 않는다

	mu    sync.Mutex
	calls []time.Time // 최근 1분의 호출 시각
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func def[T comparable](v, d T) T {
	var zero T
	if v == zero {
		return d
	}
	return v
}

// budget 은 이번 주기에 더 부를 수 있는 호출 수다.
func (w *Worker) budget(now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	cut := now.Add(-time.Minute)
	i := 0
	for i < len(w.calls) && !w.calls[i].After(cut) {
		i++
	}
	w.calls = w.calls[i:]
	return def(w.PerMin, 60) - len(w.calls)
}

func (w *Worker) spend(now time.Time) {
	w.mu.Lock()
	w.calls = append(w.calls, now)
	w.mu.Unlock()
}

// Tick 은 한 주기를 돈다. 처리한 일의 수를 돌려준다.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	if w.Rehearsal {
		return 0, nil
	}
	now := w.now()
	limit := min(def(w.Batch, 20), w.budget(now))
	if limit <= 0 {
		return 0, nil
	}
	prov, err := w.Registry.Resolve(ctx, w.Settings)
	if errors.Is(err, ErrNotConfigured) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	jobs, err := w.Store.ClaimDue(ctx, now, limit)
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	sem := make(chan struct{}, def(w.Concurrency, 4))
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		w.spend(now)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := w.handle(ctx, prov, j); err != nil {
				log.Printf("taxasp: 세금계산서 %s 처리 반영 실패: %v", j.No, err)
			}
		}()
	}
	wg.Wait()
	return len(jobs), nil
}

func (w *Worker) handle(ctx context.Context, prov Provider, j domain.TaxDispatchJob) error {
	cctx, cancel := context.WithTimeout(ctx, def(w.CallTimeout, 15*time.Second))
	defer cancel()
	switch j.Kind {
	case "issue":
		res, err := prov.Issue(cctx, IssueRequest{IdempotencyKey: j.Invoice.No, Invoice: toInvoice(j.Invoice)})
		if err != nil {
			e := AsError(err)
			if e.Retryable {
				return w.Store.Retry(ctx, j.ID, e.Code, e.HTTP)
			}
			return w.Store.Fail(ctx, j.ID, e.Code, e.HTTP)
		}
		return w.Store.Submitted(ctx, j.ID, res.Ref, res.HTTPStatus)
	default: // status
		res, err := prov.Status(cctx, j.ProviderRef)
		if err != nil {
			e := AsError(err)
			if e.Retryable {
				return w.Store.StillPending(ctx, j.ID) // 조회 실패는 접수 실패가 아니다: 다음 조회에서 다시 본다(7일 한도)
			}
			return w.Store.Fail(ctx, j.ID, e.Code, e.HTTP)
		}
		switch res.Kind {
		case StatusAccepted:
			return w.Store.Accepted(ctx, j.ID, res.ApprovalNo)
		case StatusRejected:
			code := res.Code
			if _, ok := domain.TaxCodeLabels[code]; !ok || code == "" {
				code = "provider_rejected"
			}
			return w.Store.Fail(ctx, j.ID, code, res.HTTPStatus)
		default:
			return w.Store.StillPending(ctx, j.ID)
		}
	}
}

// Run 은 ctx 가 끝날 때까지 주기마다 Tick 을 돈다.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(def(w.Interval, 20*time.Second))
	defer t.Stop()
	for {
		if _, err := w.Tick(ctx); err != nil {
			log.Printf("taxasp: 전송 작업 주기 오류: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func toInvoice(v domain.TaxInvoice) Invoice {
	p := func(x domain.TaxParty) Party {
		return Party{BizNo: x.BizNo, BranchNo: x.BranchNo, Name: x.Name, Rep: x.Rep, Addr: x.Addr, Email: x.Email}
	}
	inv := Invoice{No: v.No, TaxType: v.TaxType, IssueDate: v.IssueDate, Supplier: p(v.Supplier), Buyer: p(v.Buyer), SupplyTotal: v.SupplyTotal, TaxTotal: v.TaxTotal, Total: v.Total,
		ChargeType: v.ChargeType, Remark: v.Remark, AmendReason: v.AmendReason, AmendOfApprovalNo: v.AmendOfApprovalNo}
	for _, l := range v.Lines {
		inv.Lines = append(inv.Lines, Line{Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Tax: l.Tax, Remark: l.Remark})
	}
	return inv
}

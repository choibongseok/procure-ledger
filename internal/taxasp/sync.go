package taxasp

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// Syncer 는 수신 동기화 작업이다(명세 6.1, D11). 하루 한 번 자동으로, 재무 담당자가 눌러서 수동으로 돈다.
//   - 읽는 창은 "마지막으로 끝까지 읽은 날 - 겹침(기본 3일)" ~ 오늘이다. 겹쳐 읽어 늦게 올라온 건을 놓치지 않고, 같은 승인번호는 서비스가 건너뛴다.
//   - 쪽(Limit)마다 반영하고 커서를 저장한다. 한 번에 읽는 쪽 수(MaxPages)를 넘으면 partial 로 멈추고 다음 실행이 같은 창을 이어서 읽는다.
//   - 대행사 오류는 failed 로 남기고(비밀·본문 없이 코드만) 다음 실행에서 다시 한다. 설정이 없으면 not_configured, 회사 사업자번호가 없으면 no_company.
//   - 같은 승인번호에 내용이 다르면 서비스가 충돌로 세고 덮어쓰지 않는다. 형식 오류·타사 건은 그 건만 세고 건너뛴다.
type Syncer struct {
	Store    domain.TaxSyncStore
	Settings SettingsSource
	Registry *Registry
	Now      func() time.Time

	Overlap     int           // 겹침 일수. 기본 3
	InitialDays int           // 처음 실행할 때 거슬러 올라갈 일수. 기본 30
	PageLimit   int           // 쪽 크기. 기본 100
	MaxPages    int           // 한 번에 읽는 최대 쪽 수. 기본 20
	CallTimeout time.Duration // 호출당 시간. 기본 20초
	Interval    time.Duration // Run 이 확인하는 주기. 기본 10분
	Rehearsal   bool          // 참이면 어떤 호출도 하지 않는다
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func day(t time.Time) string { return t.In(contract.KST).Format("2006-01-02") }

func toItem(r Received) domain.TaxReceivedItem {
	p := func(x Party) domain.TaxParty {
		return domain.TaxParty{BizNo: x.BizNo, BranchNo: x.BranchNo, Name: x.Name, Rep: x.Rep, Addr: x.Addr, Email: x.Email}
	}
	inv := r.Invoice
	it := domain.TaxReceivedItem{ApprovalNo: r.ApprovalNo, TaxType: inv.TaxType, IssueDate: inv.IssueDate, Supplier: p(inv.Supplier), Buyer: p(inv.Buyer),
		SupplyTotal: inv.SupplyTotal, TaxTotal: inv.TaxTotal, ChargeType: inv.ChargeType, Remark: inv.Remark, AmendReason: inv.AmendReason, AmendOfApprovalNo: inv.AmendOfApprovalNo}
	for i, l := range inv.Lines {
		it.Lines = append(it.Lines, domain.TaxLine{Seq: i + 1, Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Tax: l.Tax, Remark: l.Remark})
	}
	return it
}

// RunOnce 는 동기화를 한 번 한다. 결과 코드(ok·partial·failed·not_configured·no_company·rehearsal)와 건수를 돌려준다. 사람이 읽는 문장은 domain.TaxSyncResultLabels.
func (s *Syncer) RunOnce(ctx context.Context) (string, domain.TaxSyncCounts, error) {
	var total domain.TaxSyncCounts
	if s.Rehearsal {
		return "rehearsal", total, nil
	}
	st, err := s.Store.SyncState(ctx)
	if err != nil {
		return "", total, err
	}
	finish := func(res string) (string, domain.TaxSyncCounts, error) {
		return res, total, s.Store.FinishSync(ctx, st, total, res)
	}
	biz, err := s.Store.CompanyBizNo(ctx)
	if err != nil {
		return "", total, err
	}
	if len(biz) != 10 {
		return finish("no_company")
	}
	if oerr := s.Store.CheckSyncOwner(ctx); oerr != nil {
		if errors.Is(oerr, domain.ErrTaxSyncOwner) {
			st.Cursor = "" // 멈춘 창을 이어 읽지 않고 담당자가 다시 켠 뒤 처음부터 겹침 창으로 읽는다
			return finish("owner_inactive")
		}
		return "", total, oerr
	}
	prov, err := s.Registry.Resolve(ctx, s.Settings)
	if errors.Is(err, ErrNotConfigured) {
		return finish("not_configured")
	}
	if err != nil {
		return "", total, err
	}
	if !prov.Capabilities().ListReceived {
		return finish("not_configured")
	}
	today := s.now().In(contract.KST)
	if st.Cursor == "" || st.WinFrom == "" || st.WinTo == "" {
		from := today.AddDate(0, 0, -def(s.InitialDays, 30))
		if t, perr := time.ParseInLocation("2006-01-02", st.LastDone, contract.KST); perr == nil {
			from = t.AddDate(0, 0, -def(s.Overlap, 3))
		}
		st.WinFrom, st.WinTo, st.Cursor = day(from), day(today), ""
	}
	for page := 0; page < def(s.MaxPages, 20); page++ {
		cctx, cancel := context.WithTimeout(ctx, def(s.CallTimeout, 20*time.Second))
		res, lerr := prov.ListReceived(cctx, ListQuery{From: st.WinFrom, To: st.WinTo, Cursor: st.Cursor, Limit: def(s.PageLimit, 100)})
		cancel()
		if lerr != nil {
			log.Printf("taxasp: 수신 목록 조회 실패 코드=%s", AsError(lerr).Code)
			return finish("failed")
		}
		items := make([]domain.TaxReceivedItem, 0, len(res.Items))
		for _, r := range res.Items {
			items = append(items, toItem(r))
		}
		c, aerr := s.Store.ApplyReceived(ctx, items)
		if errors.Is(aerr, domain.ErrTaxSyncOwner) {
			st.Cursor = ""
			return finish("owner_inactive")
		}
		if aerr != nil {
			log.Printf("taxasp: 수신 건 반영 실패: %v", aerr)
			return finish("failed")
		}
		total.Add(c)
		if !res.HasMore {
			st.LastDone, st.Cursor = st.WinTo, ""
			return finish("ok")
		}
		st.Cursor = res.NextCursor
		if err := s.Store.SaveSyncState(ctx, st); err != nil {
			return "", total, err
		}
	}
	return finish("partial")
}

// due 는 오늘(KST) 아직 한 번도 돌지 않았거나 끝까지 읽지 못한 창이 남아 있는지다.
func (s *Syncer) due(ctx context.Context) bool {
	st, err := s.Store.SyncState(ctx)
	if err != nil {
		return false
	}
	return st.Cursor != "" || st.LastRunAt.IsZero() || day(st.LastRunAt) != day(s.now())
}

// Run 은 ctx 가 끝날 때까지 주기마다 확인해 하루 한 번(또는 이어 읽을 창이 남았을 때) 동기화한다.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(def(s.Interval, 10*time.Minute))
	defer t.Stop()
	for {
		// 법인마다(대행사 연동이 켜진 법인) 그 법인 context 로 한 번씩 돈다. 켜진 법인이 없으면 현재 법인 없는 context 로 한 번(이전 동작).
		ctxs := []context.Context{ctx}
		if ids, err := s.Store.SyncEntities(ctx); err == nil && len(ids) > 0 {
			ctxs = ctxs[:0]
			for _, id := range ids {
				ctxs = append(ctxs, contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: id}))
			}
		}
		for _, ectx := range ctxs {
			if !s.due(ectx) {
				continue
			}
			if res, c, err := s.RunOnce(ectx); err != nil {
				log.Printf("taxasp: 수신 동기화 오류: %v", err)
			} else {
				log.Printf("taxasp: 수신 동기화 결과=%s 신규=%d 중복=%d 충돌=%d", res, c.Inserted, c.Same, c.Conflict)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

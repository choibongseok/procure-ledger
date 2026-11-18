// Package mock 은 대행사(ASP) 결정적 모의 구현이다(B43 D18, 명세 8.2). 운영 대행사가 아니다: 시험과 로컬 시연용이다.
//
// 승인번호는 결정적이다: 작성일(YYYYMMDD) + 8자리(공급자 사업자번호·내부 번호의 해시) + 8자리 일련번호 = 24자리 숫자.
// 시나리오(Config.MockMode)
//   - none: 즉시 접수, 즉시 국세청 수신
//   - transient: 같은 멱등 키의 처음 2번의 Issue 가 unavailable(일시 오류), 그 뒤 성공
//   - permanent: Issue 가 rejected_validation(영구 오류)
//   - delayed: 접수 뒤 Status 가 처음 2번 pending, 그 뒤 accepted
//   - conflict: ListReceived 가 같은 승인번호를 두 번째 조회부터 다른 금액으로 돌려준다(동기화 충돌 시험용)
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/choibongseok/procure-ledger/internal/taxasp"
)

// Store 는 모의 대행사의 상태다(프로세스 안, 어댑터 인스턴스들이 공유한다).
type Store struct {
	mu         sync.Mutex
	seq        int64
	byKey      map[string]string // 멱등 키 → 참조
	tries      map[string]int    // 멱등 키 → Issue 호출 수(transient)
	refs       map[string]*record
	received   []taxasp.Received
	lists      map[string]int // 승인번호 → 조회된 횟수(conflict)
	demoSeeded bool
}

type record struct {
	approval string
	polls    int
}

// NewStore 는 빈 상태를 만든다.
func NewStore() *Store {
	return &Store{byKey: map[string]string{}, tries: map[string]int{}, refs: map[string]*record{}, lists: map[string]int{}}
}

// Seed 는 수신 목록에 나올 가상 수신 건을 넣는다(승인번호가 24자리가 아니면 채워 넣지 않고 그대로 둔다: 형식 오류 시험용).
func (s *Store) Seed(items ...taxasp.Received) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, items...)
}

// Provider 는 모의 어댑터다.
type Provider struct {
	st    *Store
	mode  string
	seed  string // "demo" 면 Configure 가 시연용 수신 건을 한 번 채운다
	buyer string
}

var _ taxasp.Provider = (*Provider)(nil)

// Factory 는 Registry 에 등록할 공장이다(같은 Store 를 공유).
func Factory(st *Store) func() taxasp.Provider {
	return func() taxasp.Provider { return &Provider{st: st, mode: "none"} }
}

func (p *Provider) Name() string { return "mock" }

func (p *Provider) Capabilities() taxasp.Capabilities {
	return taxasp.Capabilities{ServerSigning: false, ListReceived: true, StatusPoll: true, AmendIssue: true}
}

func (p *Provider) Configure(_ context.Context, c taxasp.Config) error {
	switch c.MockMode {
	case "", "none", "transient", "permanent", "delayed", "conflict":
		p.mode = c.MockMode
		if p.mode == "" {
			p.mode = "none"
		}
		if c.MockSeed == "demo" && len(c.MockBuyer) == 10 {
			p.st.seedDemo(c.MockBuyer)
		}
		return nil
	}
	return taxasp.Err("auth", 0)
}

func approvalNo(date, supplierBiz, no string, seq int64) string {
	h := sha256.Sum256([]byte(supplierBiz + "|" + no))
	d := []byte(date)
	compact := make([]byte, 0, 8)
	for _, c := range d {
		if c >= '0' && c <= '9' {
			compact = append(compact, c)
		}
	}
	for len(compact) < 8 {
		compact = append(compact, '0')
	}
	return fmt.Sprintf("%s%08d%08d", compact[:8], binary.BigEndian.Uint32(h[:4])%100000000, seq%100000000)
}

func (p *Provider) Issue(ctx context.Context, req taxasp.IssueRequest) (taxasp.IssueResult, error) {
	if err := ctx.Err(); err != nil {
		return taxasp.IssueResult{}, taxasp.Err("timeout", 0)
	}
	if req.IdempotencyKey == "" || req.Invoice.No == "" || len(req.Invoice.Buyer.BizNo) != 10 || len(req.Invoice.Supplier.BizNo) != 10 || req.Invoice.Total != req.Invoice.SupplyTotal+req.Invoice.TaxTotal {
		return taxasp.IssueResult{}, taxasp.Err("rejected_validation", 422)
	}
	st := p.st
	st.mu.Lock()
	defer st.mu.Unlock()
	if ref, ok := st.byKey[req.IdempotencyKey]; ok {
		return taxasp.IssueResult{Ref: ref, HTTPStatus: 200}, nil
	}
	st.tries[req.IdempotencyKey]++
	switch p.mode {
	case "permanent":
		return taxasp.IssueResult{}, taxasp.Err("rejected_validation", 422)
	case "transient":
		if st.tries[req.IdempotencyKey] <= 2 {
			return taxasp.IssueResult{}, taxasp.Err("unavailable", 503)
		}
	}
	st.seq++
	ref := "MOCK-" + strconv.FormatInt(st.seq, 10)
	st.byKey[req.IdempotencyKey] = ref
	st.refs[ref] = &record{approval: approvalNo(req.Invoice.IssueDate, req.Invoice.Supplier.BizNo, req.Invoice.No, st.seq)}
	return taxasp.IssueResult{Ref: ref, HTTPStatus: 202}, nil
}

func (p *Provider) Status(ctx context.Context, ref string) (taxasp.StatusResult, error) {
	if err := ctx.Err(); err != nil {
		return taxasp.StatusResult{}, taxasp.Err("timeout", 0)
	}
	st := p.st
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.refs[ref]
	if r == nil {
		return taxasp.StatusResult{}, taxasp.Err("rejected_validation", 404)
	}
	r.polls++
	if p.mode == "delayed" && r.polls <= 2 {
		return taxasp.StatusResult{Kind: taxasp.StatusPending, HTTPStatus: 200}, nil
	}
	return taxasp.StatusResult{Kind: taxasp.StatusAccepted, ApprovalNo: r.approval, HTTPStatus: 200}, nil
}

func (p *Provider) ListReceived(ctx context.Context, q taxasp.ListQuery) (taxasp.ReceivedPage, error) {
	if err := ctx.Err(); err != nil {
		return taxasp.ReceivedPage{}, taxasp.Err("timeout", 0)
	}
	st := p.st
	st.mu.Lock()
	defer st.mu.Unlock()
	var items []taxasp.Received
	for _, it := range st.received {
		d := it.Invoice.IssueDate
		if (q.From != "" && d < q.From) || (q.To != "" && d > q.To) {
			continue
		}
		cp := it
		if p.mode == "conflict" {
			st.lists[it.ApprovalNo]++
			if st.lists[it.ApprovalNo] >= 2 {
				cp.Invoice.SupplyTotal++
				cp.Invoice.Total++
			}
		}
		items = append(items, cp)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ApprovalNo < items[j].ApprovalNo })
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	start := 0
	if q.Cursor != "" {
		n, err := strconv.Atoi(q.Cursor)
		if err != nil || n < 0 || n > len(items) {
			return taxasp.ReceivedPage{}, taxasp.Err("rejected_validation", 400)
		}
		start = n
	}
	end := start + limit
	page := taxasp.ReceivedPage{}
	if end < len(items) {
		page.HasMore, page.NextCursor = true, strconv.Itoa(end)
	} else {
		end = len(items)
	}
	page.Items = items[start:end]
	return page, nil
}

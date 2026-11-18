// (internal/handler/purchase*).
package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterPurchase 는 구매요청 라우트(/api/purchase-requests...)를 등록한다.
// 호출: cmd/server/routes_erp.go. d.Purchase 는 domain.PurchaseService 이다.
// 권한은 등록 시 선언만 하고, 핸들러 안에서 역할을 비교하지 않는다.
func RegisterPurchase(r contract.Registrar, d deps.Deps) {
	if d.Purchase == nil {
		return
	}
	h := purchaseHandler{svc: d.Purchase}
	r.HandleFunc("POST", "/api/purchase-requests", contract.PermPurchaseCreate, h.create)
	r.HandleFunc("GET", "/api/purchase-requests", contract.PermPurchaseRead, h.list)
	if _, ok := d.Purchase.(mySummarizer); ok {
		r.HandleFunc("GET", "/api/purchase-requests/my-summary", contract.PermPurchaseRead, h.mySummary)
	}
	r.HandleFunc("GET", "/api/purchase-requests/{id}", contract.PermPurchaseRead, h.get)
	r.HandleFunc("PUT", "/api/purchase-requests/{id}", contract.PermPurchaseCreate, h.update)
	r.HandleFunc("POST", "/api/purchase-requests/{id}/submit", contract.PermPurchaseCreate, h.submit)
	if _, ok := d.Purchase.(withdrawer); ok {
		r.HandleFunc("POST", "/api/purchase-requests/{id}/withdraw", contract.PermPurchaseCreate, h.withdraw)
	}
}

// mySummarizer 는 계약 밖 확장이다: 홈 화면 "내 요청 진행" 카드의 요약(service.PurchaseService 가 구현).
type mySummarizer interface {
	MyRequestSummary(ctx context.Context) (map[domain.PRStatus]int, int, error)
}

// withdrawer 는 계약 밖 확장이다: 결재자 없이 멈춘 건의 회수와 "결재자 없음" 판정(service.PurchaseService 가 구현).
type withdrawer interface {
	Withdraw(ctx context.Context, id int64) (domain.PurchaseRequest, error)
	StalledIDs(ctx context.Context, prs []domain.PurchaseRequest) (map[int64]bool, error)
}

type purchaseHandler struct{ svc domain.PurchaseService }

// dtos 는 목록·단건 응답에 "결재자 없음" 표시를 붙인다.
func (h purchaseHandler) dtos(ctx context.Context, prs []domain.PurchaseRequest) ([]prDTO, error) {
	out := toPRDTOs(prs)
	if w, ok := h.svc.(withdrawer); ok {
		stalled, err := w.StalledIDs(ctx, prs)
		if err != nil {
			return nil, err
		}
		for i := range out {
			out[i].NoApprover = stalled[out[i].ID]
		}
	}
	return out, nil
}

func (h purchaseHandler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.(withdrawer).Withdraw(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

type draftBody struct {
	VendorID int64  `json:"vendorId"`
	Title    string `json:"title"`
	Reason   string `json:"reason"`
	Lines    []struct {
		ItemID    int64 `json:"itemId"`
		Quantity  int64 `json:"quantity"`
		UnitPrice int64 `json:"unitPrice"`
	} `json:"lines"`
	Edits         []lineEditDTO `json:"edits"`
	DueDate       string        `json:"dueDate"`
	DeliveryPlace string        `json:"deliveryPlace"`
}

func (b draftBody) input() domain.DraftInput {
	in := domain.DraftInput{VendorID: b.VendorID, Title: b.Title, Reason: b.Reason, Edits: toEdits(b.Edits), DueDate: b.DueDate, DeliveryPlace: b.DeliveryPlace}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.PRLineInput{ItemID: l.ItemID, Quantity: l.Quantity, UnitPrice: l.UnitPrice})
	}
	return in
}

func (h purchaseHandler) create(w http.ResponseWriter, r *http.Request) {
	var b draftBody
	if !decode(w, r, &b) {
		return
	}
	pr, err := h.svc.CreateDraft(r.Context(), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPRDTO(pr))
}

func (h purchaseHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b draftBody
	if !decode(w, r, &b) {
		return
	}
	pr, err := h.svc.UpdateDraft(r.Context(), id, b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h purchaseHandler) submit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.Submit(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h purchaseHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	ds, err := h.dtos(r.Context(), []domain.PurchaseRequest{pr})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ds[0])
}

func (h purchaseHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.PRFilter{
		Status: domain.PRStatus(q.Get("status")), Limit: queryInt(r, "limit", 50),
		AfterID: int64(queryInt(r, "afterId", 0)), Offset: queryInt(r, "offset", 0), DepartmentID: int64(queryInt(r, "departmentId", 0)),
		RequesterID: int64(queryInt(r, "requesterId", 0)), From: q.Get("from"), To: q.Get("to"), Query: strings.TrimSpace(q.Get("q")),
		Sort: q.Get("sort"), Order: q.Get("order"),
	}
	// 정렬을 안 주면 작성일 내림차순(최신 먼저). afterId 커서는 id 오름차순이라 커서를 쓸 때는 기본 정렬을 걸지 않는다.
	if f.Sort == "" && f.AfterID == 0 {
		f.Sort = "created_at"
		if f.Order == "" {
			f.Order = "desc"
		}
	} else if f.Sort != "" && f.Order == "" {
		f.Order = "asc"
	}
	prs, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	ds, err := h.dtos(r.Context(), prs)
	if err != nil {
		writeErr(w, err)
		return
	}
	if c, ok := h.svc.(prCounter); ok {
		n, err := c.Count(r.Context(), f)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set(totalCountHeader, strconv.Itoa(n))
	}
	writeJSON(w, http.StatusOK, ds)
}

// totalCountHeader 는 목록 응답의 전체 건수 헤더다(본문은 배열 그대로라 기존 호출자가 깨지지 않는다). 쪽 이동은 offset+limit, afterId 는 호환용이다.
const totalCountHeader = "X-Total-Count"

// prCounter 는 계약 밖 확장이다: 같은 조건의 전체 건수(service.PurchaseService 가 구현).
type prCounter interface {
	Count(ctx context.Context, f domain.PRFilter) (int, error)
}

// mySummary 는 내가 올린 요청의 상태별 건수다. 요청자 본인 것만 센다.
func (h purchaseHandler) mySummary(w http.ResponseWriter, r *http.Request) {
	counts, stalled, err := h.svc.(mySummarizer).MyRequestSummary(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	byStatus := map[string]int{}
	total := 0
	for st, n := range counts {
		byStatus[string(st)] = n
		total += n
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "byStatus": byStatus, "noApprover": stalled})
}

// 월 마감 라우트(B27 단계 2, 9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterMonthClose 는 /api/inventory/months 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterMonthClose(r contract.Registrar, d deps.Deps) {
	if d.MonthClose == nil {
		return
	}
	h := monthCloseHandler{svc: d.MonthClose}
	r.HandleFunc("GET", "/api/inventory/months", contract.PermInventoryRead, h.list)
	r.HandleFunc("GET", "/api/inventory/months/{month}/precheck", contract.PermInventoryRead, h.precheck)
	r.HandleFunc("GET", "/api/inventory/months/{month}/status", contract.PermInventoryRead, h.status)
	r.HandleFunc("POST", "/api/inventory/months/{month}/close", contract.PermInventoryClose, h.close)
	r.HandleFunc("POST", "/api/inventory/months/{month}/release-request", contract.PermInventoryClose, h.release)
}

type monthCloseHandler struct{ svc domain.MonthCloseService }

func monthRowDTO(m domain.MonthRow) map[string]any {
	out := map[string]any{"month": m.Month, "state": m.State, "version": m.Version, "closedBy": m.ClosedBy, "closable": m.Closable, "releasable": m.Releasable}
	if !m.ClosedAt.IsZero() {
		out["closedAt"] = ts(m.ClosedAt)
	}
	if m.CloseAmount != nil { // 재고 평가 조회 권한이 있을 때만 서비스가 채운다
		out["closeAmount"] = *m.CloseAmount
	}
	return out
}

func (h monthCloseHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, m := range rows {
		out[i] = monthRowDTO(m)
	}
	writeJSON(w, http.StatusOK, out)
}

func itemsDTO(in []domain.PrecheckItem) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, x := range in {
		out[i] = map[string]any{"title": x.Title, "detail": x.Detail, "docId": x.DocID}
	}
	return out
}

func (h monthCloseHandler) precheck(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Precheck(r.Context(), r.PathValue("month"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": p.Month, "closable": p.Closable, "reason": p.Reason, "blockers": itemsDTO(p.Blockers), "warnings": itemsDTO(p.Warnings)})
}

func (h monthCloseHandler) status(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Status(r.Context(), r.PathValue("month"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"month": s.Month, "state": s.State, "version": s.Version, "blockers": s.Blockers, "warnings": s.Warnings}
	if !s.ClosedAt.IsZero() {
		out["closedAt"] = ts(s.ClosedAt)
	}
	if s.CloseAmount != nil {
		out["closeAmount"] = *s.CloseAmount
	}
	writeJSON(w, http.StatusOK, out)
}

func (h monthCloseHandler) close(w http.ResponseWriter, r *http.Request) {
	row, err := h.svc.Close(r.Context(), r.PathValue("month"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, monthRowDTO(row))
}

func (h monthCloseHandler) release(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	id, err := h.svc.RequestRelease(r.Context(), r.PathValue("month"), b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"docId": id})
}

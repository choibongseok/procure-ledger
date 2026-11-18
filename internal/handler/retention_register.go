// 보존 연한 만료 문서 폐기 API(B29 ③, 6·10절).
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterRetention 은 폐기 후보·보류 조회(retention:read)와 폐기·보류(retention:dispose) 라우트를 등록한다. 모든 라우트가 권한을 선언한다.
func RegisterRetention(r contract.Registrar, d deps.Deps) {
	if d.Retention == nil {
		return
	}
	h := retentionHandler{svc: d.Retention}
	r.HandleFunc("GET", "/api/retention/candidates", contract.PermRetentionRead, h.candidates)
	r.HandleFunc("GET", "/api/retention/holds", contract.PermRetentionRead, h.holds)
	r.HandleFunc("GET", "/api/retention/count", contract.PermRetentionRead, h.count)
	r.HandleFunc("POST", "/api/retention/dispose", contract.PermRetentionDispose, h.dispose)
	r.HandleFunc("POST", "/api/retention/hold", contract.PermRetentionDispose, h.hold)
	r.HandleFunc("POST", "/api/retention/hold/{id}/clear", contract.PermRetentionDispose, h.clearHold)
}

type retentionHandler struct{ svc domain.RetentionService }

type candidateDTO struct {
	ID             int64  `json:"id"`
	No             string `json:"no"`
	Title          string `json:"title"`
	FormCode       string `json:"formCode"`
	FormName       string `json:"formName"`
	RequesterName  string `json:"requesterName"`
	CompletedAt    string `json:"completedAt"`
	RetentionUntil string `json:"retentionUntil"`
	DaysOver       int    `json:"daysOver"`
	Held           bool   `json:"held"`
	HoldReason     string `json:"holdReason,omitempty"`
}

func toCandidateDTOs(in []domain.RetentionCandidate) []candidateDTO {
	out := make([]candidateDTO, len(in))
	for i, c := range in {
		out[i] = candidateDTO{ID: c.ID, No: c.No, Title: c.Title, FormCode: c.FormCode, FormName: c.FormName, RequesterName: c.RequesterName,
			CompletedAt: c.CompletedAt, RetentionUntil: c.RetentionUntil, DaysOver: c.DaysOver, Held: c.Held, HoldReason: c.HoldReason}
	}
	return out
}

func (h retentionHandler) candidates(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, total, err := h.svc.Candidates(r.Context(), limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": toCandidateDTOs(rows), "total": total})
}

func (h retentionHandler) holds(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Holds(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": toCandidateDTOs(rows)})
}

func (h retentionHandler) count(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.ExpiredCount(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

func (h retentionHandler) dispose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs    []int64 `json:"ids"`
		Reason string  `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := h.svc.Dispose(r.Context(), domain.DisposeInput{IDs: b.IDs, Reason: b.Reason})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disposed": n})
}

func (h retentionHandler) hold(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.Hold(r.Context(), b.ID, b.Reason); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": true})
}

func (h retentionHandler) clearHold(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	// 해제 사유는 선택이라 본문이 없거나 읽을 수 없어도 그대로 진행한다.
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := h.svc.ClearHold(r.Context(), id, b.Reason); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": false})
}

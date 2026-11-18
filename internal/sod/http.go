package sod

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

var kst = time.FixedZone("KST", 9*3600)

// Register 는 권한 분리 화면 API 를 선언과 함께 등록한다. 모드 변경·예외 요청·승인은 로그인 세션만 쓴다(토큰 403).
func (e *Engine) Register(reg contract.Registrar) {
	reg.HandleFunc("GET", "/api/admin/sod/rules", contract.PermSoDRead, e.hRules)
	reg.HandleFunc("PUT", "/api/admin/sod/rules/{code}", contract.PermSoDManage, rbac.SessionOnly(e.audit, e.txs, e.hSetMode))
	reg.HandleFunc("GET", "/api/admin/sod/violations", contract.PermSoDRead, e.hViolations)
	reg.HandleFunc("GET", "/api/admin/sod/exceptions", contract.PermSoDRead, e.hExceptions)
	reg.HandleFunc("POST", "/api/admin/sod/exceptions", contract.PermSoDManage, rbac.SessionOnly(e.audit, e.txs, e.hRequest))
	reg.HandleFunc("POST", "/api/admin/sod/exceptions/{id}/approve", contract.PermSoDExceptionApprove, rbac.SessionOnly(e.audit, e.txs, e.hDecide(true)))
	reg.HandleFunc("POST", "/api/admin/sod/exceptions/{id}/reject", contract.PermSoDExceptionApprove, rbac.SessionOnly(e.audit, e.txs, e.hDecide(false)))
	reg.HandleFunc("POST", "/api/admin/sod/exceptions/{id}/revoke", contract.PermSoDExceptionApprove, rbac.SessionOnly(e.audit, e.txs, e.hRevoke))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": strings.TrimPrefix(err.Error(), contract.ErrInvalidState.Error()+": ")})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": ")})
	default:
		log.Printf("sod: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func decode(w http.ResponseWriter, req *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, verr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func (e *Engine) hRules(w http.ResponseWriter, req *http.Request) {
	out, err := e.Rules(req.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *Engine) hSetMode(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decode(w, req, &body) {
		return
	}
	if err := e.SetMode(req.Context(), req.PathValue("code"), body.Mode); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dayParam(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, kst)
}

func (e *Engine) hViolations(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	f := ViolationFilter{Rule: q.Get("rule"), Result: q.Get("result")}
	var err error
	if f.From, err = dayParam(q.Get("from")); err != nil {
		writeErr(w, verr("시작 날짜 형식이 올바르지 않습니다"))
		return
	}
	if to, err := dayParam(q.Get("to")); err != nil {
		writeErr(w, verr("종료 날짜 형식이 올바르지 않습니다"))
		return
	} else if !to.IsZero() {
		f.To = to.AddDate(0, 0, 1)
	}
	f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	out, err := e.Violations(req.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *Engine) hExceptions(w http.ResponseWriter, req *http.Request) {
	out, err := e.Exceptions(req.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *Engine) hRequest(w http.ResponseWriter, req *http.Request) {
	var in ExceptionInput
	if !decode(w, req, &in) {
		return
	}
	id, err := e.RequestException(req.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (e *Engine) hDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		var body struct {
			Note string `json:"note"`
		}
		if req.ContentLength != 0 && !decode(w, req, &body) {
			return
		}
		if err := e.DecideException(req.Context(), id, approve, body.Note); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (e *Engine) hRevoke(w http.ResponseWriter, req *http.Request) {
	id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err := e.RevokeException(req.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

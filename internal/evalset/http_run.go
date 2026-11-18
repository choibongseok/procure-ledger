package evalset

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// RegisterRuns 는 실행·결과·비교·기준선 API 를 선언과 함께 등록한다(명세 10절).
//
//	POST /api/admin/eval/sets/{code}/runs        eval:run    실행 시작 {version, provider, model, confirmCost} → 202
//	GET  /api/admin/eval/runs?setCode=&limit=    eval:read   세트 실행 이력
//	GET  /api/admin/eval/runs/{id}               eval:read   실행 상세(문항 결과)
//	GET  /api/admin/eval/run-compare?a=&b=       eval:read   두 실행 비교(a 가 기준)
//	PUT  /api/admin/eval/sets/{code}/baseline    eval:write  기준선 지정·해제 {runId}
//	GET  /api/admin/eval/run-options             eval:run    실행에 고를 수 있는 공급자
//	GET  /api/admin/eval/gates                   eval:read   저장 전 평가 지정(용도 패턴 → 세트)
//	PUT  /api/admin/eval/gates                   eval:write  지정 저장 {purposePattern, setCode, enabled}
func (s *Service) RegisterRuns(reg contract.Registrar) {
	reg.HandleFunc("POST", "/api/admin/eval/sets/{code}/runs", contract.PermEvalRun, s.hStartRun)
	reg.HandleFunc("GET", "/api/admin/eval/runs", contract.PermEvalRead, s.hListRuns)
	reg.HandleFunc("GET", "/api/admin/eval/runs/{id}", contract.PermEvalRead, s.hGetRun)
	reg.HandleFunc("GET", "/api/admin/eval/run-compare", contract.PermEvalRead, s.hCompare)
	reg.HandleFunc("PUT", "/api/admin/eval/sets/{code}/baseline", contract.PermEvalWrite, s.hBaseline)
	reg.HandleFunc("GET", "/api/admin/eval/run-options", contract.PermEvalRun, s.hRunOptions)
	reg.HandleFunc("GET", "/api/admin/eval/gates", contract.PermEvalRead, s.hListGates)
	reg.HandleFunc("PUT", "/api/admin/eval/gates", contract.PermEvalWrite, s.hSaveGate)
}

func (s *Service) hStartRun(w http.ResponseWriter, r *http.Request) {
	var in RunInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Start(r.Context(), r.PathValue("code"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Service) hListRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.ListRuns(r.Context(), r.URL.Query().Get("setCode"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *Service) hGetRun(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	out, err := s.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hCompare(w http.ResponseWriter, r *http.Request) {
	a, _ := strconv.ParseInt(r.URL.Query().Get("a"), 10, 64)
	b, _ := strconv.ParseInt(r.URL.Query().Get("b"), 10, 64)
	out, err := s.Compare(r.Context(), a, b)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hBaseline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID int64 `json:"runId"`
	}
	if !decode(w, r, &in) {
		return
	}
	warn, err := s.SetBaseline(r.Context(), r.PathValue("code"), in.RunID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warn})
}

func (s *Service) hRunOptions(w http.ResponseWriter, r *http.Request) {
	out, err := s.Providers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Service) hListGates(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListGates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gates": out})
}

func (s *Service) hSaveGate(w http.ResponseWriter, r *http.Request) {
	var in GateInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SaveGate(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

package aicost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 AI 비용 API 를 권한 선언과 함께 등록한다(명세 12절).
//
//	GET  /api/admin/ai-cost               cost:read      전사 월 집계 ?month=&entity=&department=&purpose=&provider=
//	GET  /api/ai-cost/my                  report:read    우리 부서 월 결과·추이(부서장, 본인 부서만)
//	PUT  /api/admin/ai-cost/fx            cost:allocate  월 환율 {month, usdKrwX1000}
//	POST /api/admin/ai-cost/adjustments   cost:allocate  실제 청구액 {month, provider, actualKrw, note}
//	GET  /api/admin/ai-cost/rules         cost:read      배부 규칙 세트 버전 목록
//	GET  /api/admin/ai-cost/rules/{version} cost:read    규칙 세트 한 버전
//	POST /api/admin/ai-cost/rules         cost:allocate  새 규칙 세트 버전 {validFromMonth, note, pools}
//	POST /api/admin/ai-cost/rules/default cost:allocate  규칙이 없을 때 기본 규칙(v1) 만들기
//	GET  /api/admin/ai-cost/preview       cost:read      배부 미리보기 ?month=&entity=&version=&compare=
//	GET  /api/admin/ai-cost/departments   cost:read      규칙 대상으로 고를 수 있는 부서(법인 범위 안)
//	GET  /api/admin/ai-cost/run           cost:read      월·법인의 배부 실행(확정 결과·전표·해제 요청) ?month=&entity=
//	POST /api/admin/ai-cost/runs          cost:allocate  월 배부 확정 {month, entity, version}(전표 초안 함께 생성)
//	POST /api/admin/ai-cost/runs/{id}/journal  cost:allocate  취소된 전표 다시 만들기
//	POST /api/admin/ai-cost/runs/{id}/release-request|release-confirm|release-reject  cost:allocate  확정 해제 요청·확인·반려(세션 전용)
//	GET|PUT /api/admin/ai-cost/budgets    cost:read|allocate  부서별 AI 비용 예산 ?month=&entity= / {month, departmentId, amountKrw}
//	GET  /api/admin/ai-cost/export        cost:read      월 배부 결과 CSV ?month=&entity=
//	GET|PUT /api/admin/ai-cost/settings   cost:read|allocate  비용 전표의 차변 계정 {expenseAccount}
func (s *Service) Register(reg contract.Registrar) {
	reg.HandleFunc("GET", "/api/admin/ai-cost", contract.PermCostRead, s.hMonthly)
	reg.HandleFunc("GET", "/api/ai-cost/my", contract.PermReportRead, s.hMy)
	reg.HandleFunc("PUT", "/api/admin/ai-cost/fx", contract.PermCostAllocate, s.hFX)
	reg.HandleFunc("POST", "/api/admin/ai-cost/adjustments", contract.PermCostAllocate, s.hAdjust)
	reg.HandleFunc("GET", "/api/admin/ai-cost/rules", contract.PermCostRead, s.hRules)
	reg.HandleFunc("GET", "/api/admin/ai-cost/rules/{version}", contract.PermCostRead, s.hRule)
	reg.HandleFunc("POST", "/api/admin/ai-cost/rules", contract.PermCostAllocate, s.hSaveRule)
	reg.HandleFunc("POST", "/api/admin/ai-cost/rules/default", contract.PermCostAllocate, s.hDefaultRule)
	reg.HandleFunc("GET", "/api/admin/ai-cost/preview", contract.PermCostRead, s.hPreview)
	reg.HandleFunc("GET", "/api/admin/ai-cost/departments", contract.PermCostRead, s.hDepartments)
	so := func(f http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, f) }
	reg.HandleFunc("GET", "/api/admin/ai-cost/run", contract.PermCostRead, s.hRun)
	reg.HandleFunc("POST", "/api/admin/ai-cost/runs", contract.PermCostAllocate, s.hConfirm)
	reg.HandleFunc("POST", "/api/admin/ai-cost/runs/{id}/journal", contract.PermCostAllocate, s.hRegenerate)
	reg.HandleFunc("POST", "/api/admin/ai-cost/runs/{id}/release-request", contract.PermCostAllocate, so(s.hReleaseRequest))
	reg.HandleFunc("POST", "/api/admin/ai-cost/runs/{id}/release-confirm", contract.PermCostAllocate, so(s.hReleaseConfirm))
	reg.HandleFunc("POST", "/api/admin/ai-cost/runs/{id}/release-reject", contract.PermCostAllocate, so(s.hReleaseReject))
	reg.HandleFunc("GET", "/api/admin/ai-cost/budgets", contract.PermCostRead, s.hBudgets)
	reg.HandleFunc("PUT", "/api/admin/ai-cost/budgets", contract.PermCostAllocate, s.hSetBudget)
	reg.HandleFunc("GET", "/api/admin/ai-cost/export", contract.PermCostRead, s.hExport)
	reg.HandleFunc("GET", "/api/admin/ai-cost/settings", contract.PermCostRead, s.hSettings)
	reg.HandleFunc("PUT", "/api/admin/ai-cost/settings", contract.PermCostAllocate, s.hSetSettings)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	trim := func(p error) string {
		msg := err.Error()
		if len(msg) > len(p.Error())+2 && msg[:len(p.Error())] == p.Error() {
			return msg[len(p.Error())+2:]
		}
		return msg
	}
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": trim(contract.ErrNotFound)})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": trim(contract.ErrInvalidState)})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": trim(contract.ErrValidation)})
	default:
		log.Printf("aicost: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func qint(r *http.Request, k string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(k), 10, 64)
	return n
}

// entityParam 은 entity 매개변수를 법인 번호로 해석한다(B44 12절: API 는 법인 코드를 받는다). 비면 0(현재 법인), 숫자만이면 번호(전부터 받던 형식),
// 그 밖에는 법인 코드(대소문자 무시)이며 모르는 코드는 400 이다. 번호든 코드든 호출자의 읽기 범위 밖이면 서비스가 403 으로 거부한다.
func (s *Service) entityParam(ctx context.Context, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n, nil
	}
	var id int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ?`, strings.ToUpper(raw)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, valErr("알 수 없는 법인 코드입니다")
	}
	return id, err
}

func (s *Service) qentity(r *http.Request) (int64, error) {
	return s.entityParam(r.Context(), r.URL.Query().Get("entity"))
}

func (s *Service) hMonthly(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ent, err := s.qentity(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.Monthly(r.Context(), Filter{Month: q.Get("month"), EntityID: ent, DepartmentID: qint(r, "department"), Purpose: q.Get("purpose"), Provider: q.Get("provider")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hMy(w http.ResponseWriter, r *http.Request) {
	out, err := s.MyDepartment(r.Context(), r.URL.Query().Get("month"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hFX(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month       string `json:"month"`
		UsdKrwX1000 int64  `json:"usdKrwX1000"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.SetFX(r.Context(), in.Month, in.UsdKrwX1000); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": in.Month, "usdKrwX1000": in.UsdKrwX1000})
}

func (s *Service) hAdjust(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month     string `json:"month"`
		Provider  string `json:"provider"`
		ActualKRW int64  `json:"actualKrw"`
		Note      string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.AddAdjustment(r.Context(), in.Month, in.Provider, in.ActualKRW, in.Note); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": in.Month, "provider": in.Provider, "actualKrw": in.ActualKRW})
}

func (s *Service) hRules(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListRuleSets(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (s *Service) hRule(w http.ResponseWriter, r *http.Request) {
	v, _ := strconv.Atoi(r.PathValue("version"))
	out, err := s.GetRuleSet(r.Context(), v)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hSaveRule(w http.ResponseWriter, r *http.Request) {
	var in RuleSetInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SaveRuleSet(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hDefaultRule(w http.ResponseWriter, r *http.Request) {
	out, err := s.CreateDefaultRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, _ := strconv.Atoi(q.Get("version"))
	c, _ := strconv.Atoi(q.Get("compare"))
	ent, err := s.qentity(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.PreviewAllocation(r.Context(), PreviewInput{Month: q.Get("month"), EntityID: ent, Version: v, Compare: c})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hDepartments(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListDepartments(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	ents, err := s.ListEntities(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"departments": out, "entities": ents})
}

func (s *Service) hRun(w http.ResponseWriter, r *http.Request) {
	ent, err := s.qentity(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.GetRun(r.Context(), r.URL.Query().Get("month"), ent)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": out})
}

func (s *Service) hConfirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month   string          `json:"month"`
		Entity  json.RawMessage `json:"entity"` // 법인 번호(숫자) 또는 법인 코드(글자)
		Version int             `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	raw := strings.Trim(strings.TrimSpace(string(in.Entity)), `"`)
	if raw == "null" {
		raw = ""
	}
	ent, err := s.entityParam(r.Context(), raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.ConfirmRun(r.Context(), ConfirmInput{Month: in.Month, EntityID: ent, Version: in.Version})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func runID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Service) hRegenerate(w http.ResponseWriter, r *http.Request) {
	out, err := s.RegenerateJournal(r.Context(), runID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hReleaseRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.RequestRelease(r.Context(), runID(r), in.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hReleaseConfirm(w http.ResponseWriter, r *http.Request) {
	out, err := s.ConfirmRelease(r.Context(), runID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hReleaseReject(w http.ResponseWriter, r *http.Request) {
	out, err := s.RejectRelease(r.Context(), runID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hBudgets(w http.ResponseWriter, r *http.Request) {
	ent, err := s.qentity(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.ListBudgets(r.Context(), r.URL.Query().Get("month"), ent)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": out})
}

func (s *Service) hSetBudget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month        string `json:"month"`
		DepartmentID int64  `json:"departmentId"`
		AmountKRW    int64  `json:"amountKrw"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.SetBudget(r.Context(), in.Month, in.DepartmentID, in.AmountKRW); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": in.Month, "departmentId": in.DepartmentID, "amountKrw": in.AmountKRW})
}

func (s *Service) hExport(w http.ResponseWriter, r *http.Request) {
	ent, err := s.qentity(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, body, err := s.ExportCSV(r.Context(), r.URL.Query().Get("month"), ent)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"; filename*=UTF-8''`+name)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Service) hSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.GetSettings(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hSetSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpenseAccount string `json:"expenseAccount"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.SetExpenseAccount(r.Context(), in.ExpenseAccount); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"expenseAccount": in.ExpenseAccount})
}

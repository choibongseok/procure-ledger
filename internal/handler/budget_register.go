// 부서·월 예산 라우트(B25 ①, 8절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterBudget 은 /api/budgets 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterBudget(r contract.Registrar, d deps.Deps) {
	if d.Budget == nil {
		return
	}
	h := budgetHandler{svc: d.Budget}
	r.HandleFunc("GET", "/api/budgets", contract.PermBudgetRead, h.list)
	r.HandleFunc("PUT", "/api/budgets", contract.PermBudgetManage, h.set)
	r.HandleFunc("GET", "/api/budgets/revisions", contract.PermBudgetRead, h.revisions)
	r.HandleFunc("GET", "/api/budgets/check", contract.PermPurchaseRead, h.check)
}

type budgetHandler struct{ svc domain.BudgetService }

// flexAmount 는 금액을 숫자나 쉼표 섞인 문자열("1,500,000")로 받는다. 읽지 못하면 ok=false 다(서비스 검사 전에 400 으로 거절한다).
type flexAmount struct {
	v  int64
	ok bool
}

func (f *flexAmount) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	s = strings.Trim(s, `"`)
	s = strings.ReplaceAll(strings.ReplaceAll(s, ",", ""), " ", "")
	n, err := strconv.ParseInt(s, 10, 64)
	f.v, f.ok = n, err == nil && s != ""
	return nil
}

type budgetSnapDTO struct {
	State      string `json:"state"`
	Month      string `json:"month"`
	Budget     int64  `json:"budget"`
	Used       int64  `json:"used"`
	InProgress int64  `json:"inProgress"`
	Amount     int64  `json:"amount"`
	Over       int64  `json:"over"`
	// 예산 초과로 재무 단계를 실제로 더했는지(이미 있던 경우 false).
	FinanceAdded *bool `json:"financeAdded"` // nil(null)=알 수 없음(옛 스냅샷)
}

func toBudgetSnapDTO(s *domain.BudgetSnapshot) *budgetSnapDTO {
	if s == nil {
		return nil
	}
	return &budgetSnapDTO{State: s.State, Month: s.Month, Budget: s.Budget, Used: s.Used, InProgress: s.InProgress, Amount: s.Amount, Over: s.Over, FinanceAdded: s.FinanceAdded}
}

func (h budgetHandler) list(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	rows, err := h.svc.List(r.Context(), month)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, x := range rows {
		out[i] = map[string]any{"departmentId": x.DepartmentID, "departmentName": x.DepartmentName, "month": x.Month,
			"amount": x.Amount, "used": x.Used, "inProgress": x.InProgress, "balance": x.Balance, "usagePct": x.UsagePct, "badge": x.Badge}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h budgetHandler) set(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DepartmentID int64      `json:"departmentId"`
		Month        string     `json:"month"`
		Amount       flexAmount `json:"amount"`
		Reason       string     `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !b.Amount.ok {
		http.Error(w, "월 예산은 0 이상의 숫자로 입력하세요. 쉼표(,)는 넣어도 됩니다.", http.StatusBadRequest)
		return
	}
	got, err := h.svc.Set(r.Context(), domain.BudgetInput{DepartmentID: b.DepartmentID, Month: b.Month, Amount: b.Amount.v, Reason: b.Reason})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": got.ID, "departmentId": got.DepartmentID, "departmentName": got.DepartmentName, "month": got.Month, "amount": got.Amount})
}

func (h budgetHandler) revisions(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Revisions(r.Context(), r.URL.Query().Get("month"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		m := map[string]any{"departmentName": x.DepartmentName, "month": x.Month, "newAmount": x.NewAmount, "reason": x.Reason, "actorName": x.ActorName, "at": ts(x.At)}
		if x.OldAmount != nil {
			m["oldAmount"] = *x.OldAmount
		}
		out[i] = m
	}
	writeJSON(w, http.StatusOK, out)
}

func (h budgetHandler) check(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	amount, _ := strconv.ParseInt(q.Get("amount"), 10, 64)
	dept, _ := strconv.ParseInt(q.Get("departmentId"), 10, 64)
	prID, _ := strconv.ParseInt(q.Get("prId"), 10, 64)
	c, err := h.svc.Check(r.Context(), domain.BudgetCheckInput{Amount: amount, DepartmentID: dept, Month: q.Get("month"), PRID: prID})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": c.Month, "departmentId": c.DepartmentID, "departmentName": c.DepartmentName,
		"registered": c.Registered, "budget": c.Budget, "used": c.Used, "inProgress": c.InProgress, "balance": c.Balance,
		"amount": c.Amount, "over": c.Over, "after": c.After, "state": c.State})
}

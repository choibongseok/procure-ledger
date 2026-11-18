// 근태·휴가(B50) 라우트: 휴가 종류·연차 규칙(설정), 잔여·원장, 휴가 신청·취소, 인사 담당 조정. 권한·열람 범위는 서비스가 판정한다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/module"
)

// RegisterLeave 는 휴가 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
// 사용자 화면용 라우트는 모듈 게이트(꺼지면 권한이 있어도 404)를 지나고, 설정 라우트(/api/admin/attendance)는 모듈을 켜기 전에 규칙을 저장해야 하므로 게이트 밖이다.
//
//	GET  /api/attendance/leave/types                       attendance:self    신청에 쓸 수 있는 휴가 종류
//	GET  /api/attendance/leave/balance?userId=             attendance:self    잔여(본인, 인사 담당·직속 조직장은 범위 안 사람)
//	GET  /api/attendance/leave/ledger?userId=&limit=       attendance:self    잔여 원장
//	POST /api/attendance/leave/preview                     attendance:self    신청 전 계산(쓰지 않는다)
//	POST /api/attendance/leave/requests                    attendance:self    휴가 신청(문서 작성·예약·상신)
//	GET  /api/attendance/leave/requests                    attendance:self    내 신청 목록
//	GET  /api/attendance/leave/requests/{id}               attendance:self    신청 한 건
//	POST /api/attendance/leave/requests/{id}/cancel        attendance:self    취소
//	POST /api/attendance/leave/adjust                      attendance:hr      잔여 조정·초기 이관(사유 필수)
//	GET/POST /api/admin/attendance/policies, DELETE .../{id}   attendance:policy  연차 규칙(효력일 이력)
//	GET/POST /api/admin/attendance/types, PUT .../{id}         attendance:policy  휴가 종류
func RegisterLeave(r contract.Registrar, d deps.Deps) {
	if d.Leave == nil {
		return
	}
	h := leaveHandler{svc: d.Leave}
	gate := func(f http.HandlerFunc) http.HandlerFunc {
		if d.Modules == nil {
			return f
		}
		return d.Modules.Gate(module.KeyAttendance, f)
	}
	self := contract.PermAttendanceSelf
	r.HandleFunc("GET", "/api/attendance/leave/types", self, gate(h.types))
	r.HandleFunc("GET", "/api/attendance/leave/balance", self, gate(h.balance))
	r.HandleFunc("GET", "/api/attendance/leave/ledger", self, gate(h.ledger))
	r.HandleFunc("POST", "/api/attendance/leave/preview", self, gate(h.preview))
	r.HandleFunc("POST", "/api/attendance/leave/requests", self, gate(h.request))
	r.HandleFunc("GET", "/api/attendance/leave/requests", self, gate(h.mine))
	r.HandleFunc("GET", "/api/attendance/leave/requests/{id}", self, gate(h.get))
	r.HandleFunc("POST", "/api/attendance/leave/requests/{id}/cancel", self, gate(h.cancel))
	r.HandleFunc("POST", "/api/attendance/leave/adjust", contract.PermAttendanceHR, gate(h.adjust))
	pol := contract.PermAttendancePolicy
	r.HandleFunc("GET", "/api/admin/attendance/policies", pol, h.policies)
	r.HandleFunc("POST", "/api/admin/attendance/policies", pol, h.savePolicy)
	r.HandleFunc("DELETE", "/api/admin/attendance/policies/{id}", pol, h.cancelPolicy)
	r.HandleFunc("GET", "/api/admin/attendance/types", pol, h.adminTypes)
	r.HandleFunc("POST", "/api/admin/attendance/types", pol, h.saveType)
	r.HandleFunc("PUT", "/api/admin/attendance/types/{id}", pol, h.saveType)
}

type leaveHandler struct{ svc domain.LeaveService }

func typeJSON(t domain.LeaveType) map[string]any {
	return map[string]any{"id": t.ID, "code": t.Code, "name": t.Name, "unit": t.Unit, "balance": t.Balance, "balanceLabel": domain.LeaveBalanceLabels[t.Balance],
		"paid": t.Paid, "proofRequired": t.ProofRequired, "maxDays": t.MaxDays, "system": t.System, "active": t.Active}
}

func policyJSON(p domain.LeavePolicy) map[string]any {
	return map[string]any{"id": p.ID, "effectiveFrom": p.EffectiveFrom, "basis": p.Basis, "workdayMinutes": p.WorkdayMinutes, "hourUnitMinutes": p.HourUnitMinutes,
		"attendanceRule": p.AttendanceRule, "carryMaxMinutes": p.CarryMaxMinutes, "carryValidMonths": p.CarryValidMonths, "expireMonths": p.ExpireMonths,
		"firstYearMode": p.FirstYearMode, "createdByName": p.CreatedByName, "createdAt": p.CreatedAt, "canceled": p.Canceled}
}

func requestJSON(q domain.LeaveRequest) map[string]any {
	return map[string]any{"id": q.ID, "docId": q.DocID, "docNo": q.DocNo, "userId": q.UserID, "userName": q.UserName, "leaveCode": q.LeaveCode, "leaveName": q.LeaveName,
		"startDate": q.StartDate, "endDate": q.EndDate, "part": q.Part, "startTime": q.StartTime, "endTime": q.EndTime, "minutes": q.Minutes, "status": q.Status,
		"statusLabel": domain.LeaveStatusLabels[q.Status], "reason": q.Reason, "hasReason": q.HasReason, "createdAt": q.CreatedAt, "delegateUserId": q.DelegateUserID, "note": q.Note,
		"hasAbsence": q.AbsenceDelegationID != 0, "proofAttachmentId": q.ProofAttachmentID}
}

func (h leaveHandler) types(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.Types(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(ts))
	for i, t := range ts {
		out[i] = typeJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

func (h leaveHandler) adminTypes(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.AdminTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(ts))
	for i, t := range ts {
		out[i] = typeJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

type typeBody struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
	Balance       string `json:"balance"`
	Paid          bool   `json:"paid"`
	ProofRequired bool   `json:"proofRequired"`
	MaxDays       int    `json:"maxDays"`
	Active        bool   `json:"active"`
}

func (h leaveHandler) saveType(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.PathValue("id") != "" {
		var ok bool
		if id, ok = pathID(w, r, "id"); !ok {
			return
		}
	}
	var in typeBody
	if !decode(w, r, &in) {
		return
	}
	t, err := h.svc.SaveType(r.Context(), id, domain.LeaveTypeInput{Code: in.Code, Name: in.Name, Unit: in.Unit, Balance: in.Balance, Paid: in.Paid, ProofRequired: in.ProofRequired, MaxDays: in.MaxDays, Active: in.Active})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, typeJSON(t))
}

func (h leaveHandler) policies(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.Policies(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(ps))
	for i, p := range ps {
		out[i] = policyJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
}

type policyBody struct {
	EffectiveFrom    string `json:"effectiveFrom"`
	Basis            string `json:"basis"`
	WorkdayMinutes   int    `json:"workdayMinutes"`
	HourUnitMinutes  int    `json:"hourUnitMinutes"`
	AttendanceRule   bool   `json:"attendanceRule"`
	CarryMaxMinutes  int    `json:"carryMaxMinutes"`
	CarryValidMonths int    `json:"carryValidMonths"`
	ExpireMonths     int    `json:"expireMonths"`
	FirstYearMode    string `json:"firstYearMode"`
}

func (h leaveHandler) savePolicy(w http.ResponseWriter, r *http.Request) {
	var in policyBody
	if !decode(w, r, &in) {
		return
	}
	p, err := h.svc.SavePolicy(r.Context(), domain.LeavePolicyInput(in))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, policyJSON(p))
}

func (h leaveHandler) cancelPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.CancelPolicy(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h leaveHandler) balance(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	b, err := h.svc.Balance(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	lines := make([]map[string]any, len(b.Lines))
	for i, l := range b.Lines {
		lines[i] = map[string]any{"balance": l.Balance, "label": domain.LeaveBalanceLabels[l.Balance], "minutes": l.Minutes, "reserved": l.Reserved, "expiringMinutes": l.ExpiringMinutes, "expiringOn": l.ExpiringOn}
	}
	writeJSON(w, http.StatusOK, map[string]any{"userId": b.UserID, "workdayMinutes": b.WorkdayMinutes, "lines": lines})
}

func (h leaveHandler) ledger(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	ls, err := h.svc.Ledger(r.Context(), uid, queryInt(r, "limit", 100))
	if err != nil {
		writeErr(w, err)
		return
	}
	kinds := map[string]string{"grant": "발생", "carry": "이월", "expire": "소멸", "reserve": "예약", "release": "복원", "use": "사용", "adjust": "조정", "opening": "이관"}
	out := make([]map[string]any, len(ls))
	for i, l := range ls {
		out[i] = map[string]any{"id": l.ID, "balance": l.Balance, "balanceLabel": domain.LeaveBalanceLabels[l.Balance], "kind": l.Kind, "kindLabel": kinds[l.Kind], "minutes": l.Minutes,
			"reason": l.Reason, "expiresOn": l.ExpiresOn, "createdAt": l.CreatedAt, "createdByName": l.CreatedByName}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

type leaveBody struct {
	LeaveCode         string `json:"leaveCode"`
	StartDate         string `json:"startDate"`
	EndDate           string `json:"endDate"`
	Part              string `json:"part"`
	StartTime         string `json:"startTime"`
	EndTime           string `json:"endTime"`
	Reason            string `json:"reason"`
	DelegateUserID    int64  `json:"delegateUserId"`
	ProofAttachmentID int64  `json:"proofAttachmentId"`
}

func (b leaveBody) input() domain.LeaveInput { return domain.LeaveInput(b) }

func (h leaveHandler) preview(w http.ResponseWriter, r *http.Request) {
	var in leaveBody
	if !decode(w, r, &in) {
		return
	}
	p, err := h.svc.Preview(r.Context(), in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	days := make([]map[string]any, len(p.Days))
	for i, d := range p.Days {
		days[i] = map[string]any{"date": d.Date, "minutes": d.Minutes, "skipped": d.Skipped}
	}
	writeJSON(w, http.StatusOK, map[string]any{"minutes": p.Minutes, "days": days, "balance": p.Balance, "balanceLabel": domain.LeaveBalanceLabels[p.Balance],
		"balanceBefore": p.BalanceBefore, "balanceAfter": p.BalanceAfter, "workdayMinutes": p.WorkdayMinutes, "warnings": p.Warnings})
}

func (h leaveHandler) request(w http.ResponseWriter, r *http.Request) {
	var in leaveBody
	if !decode(w, r, &in) {
		return
	}
	q, err := h.svc.Request(r.Context(), in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, requestJSON(q))
}

func (h leaveHandler) mine(w http.ResponseWriter, r *http.Request) {
	qs, err := h.svc.MyRequests(r.Context(), queryInt(r, "limit", 50))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(qs))
	for i, q := range qs {
		out[i] = requestJSON(q)
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (h leaveHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	q, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, requestJSON(q))
}

func (h leaveHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	q, err := h.svc.Cancel(r.Context(), id, in.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, requestJSON(q))
}

func (h leaveHandler) adjust(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID    int64  `json:"userId"`
		Balance   string `json:"balance"`
		Kind      string `json:"kind"`
		Minutes   int64  `json:"minutes"`
		ExpiresOn string `json:"expiresOn"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.Adjust(r.Context(), domain.LeaveAdjustInput{UserID: in.UserID, Balance: in.Balance, Kind: in.Kind, Minutes: in.Minutes, ExpiresOn: in.ExpiresOn, Reason: in.Reason}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

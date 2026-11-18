// 출퇴근·정정·근태 마감·근무 유형(B50 5단계) 라우트. 권한·열람 범위는 서비스가 판정한다.
package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/module"
)

// RegisterAttendance 는 출퇴근 라우트를 등록한다. 모듈이 꺼지면 권한이 있어도 404 이고, 근무 유형 설정 라우트(/api/admin/attendance)는 모듈을 켜기 전에도 쓴다.
//
//	GET  /api/attendance/today                          attendance:self   오늘 기록·이번 주·안내
//	GET  /api/attendance/week?userId=&date=             attendance:self   한 주 기록(본인, 직속 조직장·인사 담당은 범위 안 사람)
//	POST /api/attendance/check-in, /check-out           attendance:self   출근·퇴근(서버 시각, 본문 시각은 무시)
//	GET  /api/attendance/corrections                    attendance:self   내 정정 줄과 내가 처리할 대기 요청
//	POST /api/attendance/corrections                    attendance:self   정정 요청(사유 필수)
//	POST /api/attendance/corrections/{id}/decide        attendance:self   승인·반려(직속 조직장·인사 담당, 본인 요청 불가)
//	POST /api/attendance/admin/correct                  attendance:hr     직접 정정(사유 필수)
//	GET/POST /api/attendance/admin/closes, POST .../{month}/reopen   attendance:hr   근태 마감·해제
//	GET  /api/attendance/work-types                     attendance:self   근무 유형 목록
//	GET/POST /api/attendance/admin/assignments          attendance:hr     근무 유형 배정 이력·배정
//	GET/POST /api/admin/attendance/work-types, PUT .../{id}   attendance:policy   근무 유형 설정
func RegisterAttendance(r contract.Registrar, d deps.Deps) {
	if d.Attendance == nil {
		return
	}
	h := attendanceHandler{svc: d.Attendance}
	gate := func(f http.HandlerFunc) http.HandlerFunc {
		if d.Modules == nil {
			return f
		}
		return d.Modules.Gate(module.KeyAttendance, f)
	}
	self, hr := contract.PermAttendanceSelf, contract.PermAttendanceHR
	r.HandleFunc("GET", "/api/attendance/today", self, gate(h.today))
	r.HandleFunc("GET", "/api/attendance/week", self, gate(h.week))
	r.HandleFunc("POST", "/api/attendance/check-in", self, gate(h.checkIn))
	r.HandleFunc("POST", "/api/attendance/check-out", self, gate(h.checkOut))
	r.HandleFunc("GET", "/api/attendance/corrections", self, gate(h.corrections))
	r.HandleFunc("POST", "/api/attendance/corrections", self, gate(h.requestCorrection))
	r.HandleFunc("POST", "/api/attendance/corrections/{id}/decide", self, gate(h.decide))
	r.HandleFunc("POST", "/api/attendance/admin/correct", hr, gate(h.directCorrect))
	r.HandleFunc("GET", "/api/attendance/admin/closes", hr, gate(h.closes))
	r.HandleFunc("POST", "/api/attendance/admin/closes", hr, gate(h.closeMonth))
	r.HandleFunc("POST", "/api/attendance/admin/closes/{month}/reopen", hr, gate(h.reopen))
	r.HandleFunc("GET", "/api/attendance/work-types", self, gate(h.workTypes))
	r.HandleFunc("GET", "/api/attendance/admin/assignments", hr, gate(h.assignments))
	r.HandleFunc("POST", "/api/attendance/admin/assignments", hr, gate(h.assign))
	pol := contract.PermAttendancePolicy
	r.HandleFunc("GET", "/api/admin/attendance/work-types", pol, h.workTypes)
	r.HandleFunc("POST", "/api/admin/attendance/work-types", pol, h.saveWorkType)
	r.HandleFunc("PUT", "/api/admin/attendance/work-types/{id}", pol, h.saveWorkType)
	registerAttendanceBoard(r, d, h, gate)
}

type attendanceHandler struct{ svc domain.AttendanceService }

func dayJSON(d domain.AttendanceDay) map[string]any {
	return map[string]any{"date": d.Date, "checkIn": d.CheckIn, "checkOut": d.CheckOut, "source": d.Source, "workMinutes": d.WorkMinutes, "missing": d.Missing, "late": d.Late,
		"earlyLeave": d.EarlyLeave, "coreMiss": d.CoreMiss, "holidayNote": d.HolidayNote, "leaveNote": d.LeaveNote, "correctionId": d.CorrectionID}
}

func weekJSON(w domain.AttendanceWeek) map[string]any {
	days := make([]map[string]any, len(w.Days))
	for i, d := range w.Days {
		days[i] = dayJSON(d)
	}
	return map[string]any{"weekStart": w.WeekStart, "weekEnd": w.WeekEnd, "days": days, "totalMinutes": w.TotalMinutes, "level": w.Level, "levelLabel": domain.WeekLevelLabels[w.Level],
		"weeklyTarget": w.WeeklyTarget, "belowTarget": w.BelowTarget, "workTypeName": w.WorkTypeName, "cautionMinutes": w.CautionMinute, "overMinutes": w.OverMinute}
}

func todayJSON(t domain.AttendanceToday) map[string]any {
	warnings := t.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{"today": dayJSON(t.Today), "week": weekJSON(t.Week), "missingYesterday": t.MissingYesterday, "warnings": warnings}
}

func correctionJSON(c domain.Correction) map[string]any {
	return map[string]any{"id": c.ID, "userId": c.UserID, "userName": c.UserName, "workDate": c.WorkDate, "oldIn": clockOf(c.OldIn), "oldOut": clockOf(c.OldOut), "newIn": clockOf(c.NewIn), "newOut": clockOf(c.NewOut),
		"reason": c.Reason, "requestedName": c.RequestedName, "status": c.Status, "statusLabel": domain.CorrectionStatusLabels[c.Status], "approvedName": c.ApprovedName,
		"decideNote": c.DecideNote, "createdAt": c.CreatedAt, "decidedAt": c.DecidedAt}
}

func workTypeJSON(w domain.WorkType) map[string]any {
	return map[string]any{"id": w.ID, "code": w.Code, "name": w.Name, "kind": w.Kind, "kindLabel": domain.WorkKindLabels[w.Kind], "startTime": w.StartTime, "endTime": w.EndTime,
		"breakStart": w.BreakStart, "breakEnd": w.BreakEnd, "coreStart": w.CoreStart, "coreEnd": w.CoreEnd, "weeklyMinutes": w.WeeklyMinutes, "active": w.Active, "default": w.Default}
}

func (h attendanceHandler) today(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.Today(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, todayJSON(t))
}

func (h attendanceHandler) week(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	wk, err := h.svc.WeekOf(r.Context(), uid, r.URL.Query().Get("date"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, weekJSON(wk))
}

// checkIn·checkOut 은 요청 본문을 읽지 않는다(클라이언트가 보낸 시각을 받지 않는다).
func (h attendanceHandler) checkIn(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.CheckIn(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, todayJSON(t))
}

func (h attendanceHandler) checkOut(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.CheckOut(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, todayJSON(t))
}

func (h attendanceHandler) corrections(w http.ResponseWriter, r *http.Request) {
	mine, pending, err := h.svc.Corrections(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	conv := func(in []domain.Correction) []map[string]any {
		out := make([]map[string]any, len(in))
		for i, c := range in {
			out[i] = correctionJSON(c)
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"mine": conv(mine), "toDecide": conv(pending)})
}

type correctionBody struct {
	UserID   int64  `json:"userId"`
	WorkDate string `json:"workDate"`
	CheckIn  string `json:"checkIn"`
	CheckOut string `json:"checkOut"`
	Reason   string `json:"reason"`
}

func (b correctionBody) input() domain.CorrectionInput { return domain.CorrectionInput(b) }

func (h attendanceHandler) requestCorrection(w http.ResponseWriter, r *http.Request) {
	var in correctionBody
	if !decode(w, r, &in) {
		return
	}
	in.UserID = 0 // 본인 요청이다
	c, err := h.svc.RequestCorrection(r.Context(), in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, correctionJSON(c))
}

func (h attendanceHandler) directCorrect(w http.ResponseWriter, r *http.Request) {
	var in correctionBody
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.DirectCorrect(r.Context(), in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, correctionJSON(c))
}

func (h attendanceHandler) decide(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.DecideCorrection(r.Context(), id, in.Approve, in.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, correctionJSON(c))
}

func (h attendanceHandler) closes(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.Closes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = map[string]any{"month": c.Month, "closed": c.Closed, "closedBy": c.ClosedBy, "closedAt": c.ClosedAt, "reopenedBy": c.ReopenedBy, "reopenedAt": c.ReopenedAt, "reopenReason": c.ReopenReason}
	}
	writeJSON(w, http.StatusOK, map[string]any{"closes": out})
}

func (h attendanceHandler) closeMonth(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month string `json:"month"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.CloseMonth(r.Context(), in.Month); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h attendanceHandler) reopen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.ReopenMonth(r.Context(), r.PathValue("month"), in.Reason); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h attendanceHandler) workTypes(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.WorkTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(ts))
	for i, t := range ts {
		out[i] = workTypeJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workTypes": out})
}

type workTypeBody struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	StartTime     string `json:"startTime"`
	EndTime       string `json:"endTime"`
	BreakStart    string `json:"breakStart"`
	BreakEnd      string `json:"breakEnd"`
	CoreStart     string `json:"coreStart"`
	CoreEnd       string `json:"coreEnd"`
	WeeklyMinutes int    `json:"weeklyMinutes"`
	Active        bool   `json:"active"`
}

func (h attendanceHandler) saveWorkType(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.PathValue("id") != "" {
		var ok bool
		if id, ok = pathID(w, r, "id"); !ok {
			return
		}
	}
	var in workTypeBody
	if !decode(w, r, &in) {
		return
	}
	t, err := h.svc.SaveWorkType(r.Context(), id, domain.WorkTypeInput(in))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workTypeJSON(t))
}

func (h attendanceHandler) assignments(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	as, err := h.svc.Assignments(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(as))
	for i, a := range as {
		out[i] = map[string]any{"id": a.ID, "userId": a.UserID, "workTypeId": a.WorkTypeID, "typeName": a.TypeName, "validFrom": a.ValidFrom, "createdBy": a.CreatedBy}
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": out})
}

func (h attendanceHandler) assign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID     int64  `json:"userId"`
		WorkTypeID int64  `json:"workTypeId"`
		ValidFrom  string `json:"validFrom"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.Assign(r.Context(), in.UserID, in.WorkTypeID, in.ValidFrom); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clockOf 는 저장된 시각(RFC3339 UTC)을 KST "HH:MM" 으로 바꾼다. 비었거나 읽을 수 없으면 "".
func clockOf(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.In(contract.KST).Format("15:04")
}

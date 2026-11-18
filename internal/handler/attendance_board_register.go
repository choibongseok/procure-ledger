// 부서 달력·팀 근태 현황·월 근태 내보내기(B50 6단계) 라우트. 열람 범위는 서비스의 attendanceViewScope 한 곳이 정한다.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// registerAttendanceBoard 는 달력·현황·내보내기 라우트를 등록한다.
//
//	GET /api/attendance/calendar?month=YYYY-MM&unitId=   attendance:self    부서 달력(범위는 서비스)
//	GET /api/attendance/team?unitId=                     attendance:self    팀 근태 현황(조직장·인사 담당만, 서비스가 판정)
//	GET /api/attendance/export?month=YYYY-MM&format=csv  attendance:export  월 근태 내보내기(공통 내보내기 틀: BOM·수식 주입 방지·행 상한·감사)
func registerAttendanceBoard(r contract.Registrar, d deps.Deps, h attendanceHandler, gate func(http.HandlerFunc) http.HandlerFunc) {
	self := contract.PermAttendanceSelf
	r.HandleFunc("GET", "/api/attendance/calendar", self, gate(h.calendar))
	r.HandleFunc("GET", "/api/attendance/team", self, gate(h.team))
	if d.Export != nil {
		export.Register(r, "/api/attendance/export", contract.PermAttendanceExport, d.Export, &monthSource{h: h})
	}
}

func (h attendanceHandler) calendar(w http.ResponseWriter, r *http.Request) {
	unit, _ := strconv.ParseInt(r.URL.Query().Get("unitId"), 10, 64)
	c, err := h.svc.Calendar(r.Context(), r.URL.Query().Get("month"), unit)
	if err != nil {
		writeErr(w, err)
		return
	}
	days := make([]map[string]any, len(c.Days))
	for i, d := range c.Days {
		days[i] = map[string]any{"date": d.Date, "weekend": d.Weekend, "holiday": d.Holiday}
	}
	people := make([]map[string]any, len(c.People))
	for i, p := range c.People {
		es := make([]map[string]any, len(p.Entries))
		for j, e := range p.Entries {
			es[j] = map[string]any{"start": e.Start, "end": e.End, "label": e.Label, "part": e.Part}
		}
		people[i] = map[string]any{"userId": p.UserID, "name": p.Name, "unit": p.Unit, "entries": es}
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": c.Month, "days": days, "people": people, "scope": c.Scope})
}

func (h attendanceHandler) team(w http.ResponseWriter, r *http.Request) {
	unit, _ := strconv.ParseInt(r.URL.Query().Get("unitId"), 10, 64)
	t, err := h.svc.TeamStatus(r.Context(), unit)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(t.Rows))
	for i, x := range t.Rows {
		rows[i] = map[string]any{"userId": x.UserID, "name": x.Name, "unit": x.Unit, "state": x.State, "checkIn": x.CheckIn, "checkOut": x.CheckOut, "late": x.Late,
			"weekMinutes": x.WeekMinutes, "weekLevel": x.WeekLevel, "missing": x.Missing}
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": t.Date, "scope": t.Scope, "total": t.Total, "present": t.Present, "notYet": t.NotYet, "onLeave": t.OnLeave, "absent": t.Absent, "pending": t.Pending,
		"weekOver": t.WeekOver, "weekCaution": t.WeekCaut, "rows": rows, "truncated": t.Truncated, "cautionMinutes": t.CautionMin, "overMinutes": t.OverMin})
}

// monthSource 는 월 근태 내보내기의 원천이다. 급여 금액·수당은 없고 휴가·휴직 사유 원문도 없다(구분별 일수만). 사번은 서비스가 열람 기록을 남긴 뒤에만 나간다.
type monthSource struct{ h attendanceHandler }

var monthColumns = []export.Column{
	{Label: "사번"}, {Label: "이름"}, {Label: "소속"}, {Label: "근무 유형"},
	{Label: "소정 근무일 수(일)", Kind: export.KindInt}, {Label: "출근 일수(일)", Kind: export.KindInt}, {Label: "지각(회)", Kind: export.KindInt}, {Label: "조퇴(회)", Kind: export.KindInt},
	{Label: "총 근로시간(시간)", Kind: export.KindInt}, {Label: "총 근로시간(분)", Kind: export.KindInt},
	{Label: "연차 사용(일)"}, {Label: "대체휴가 사용(일)"}, {Label: "병가(일)"}, {Label: "그 밖 휴가(일)"},
	{Label: "휴직(일)", Kind: export.KindInt}, {Label: "결근(일)", Kind: export.KindInt}, {Label: "52시간 초과 주 수", Kind: export.KindInt}, {Label: "확정 여부"},
}

func (s *monthSource) Dataset() export.Dataset {
	return export.Dataset{Code: "attendance_month", Label: "월 근태", Columns: monthColumns,
		Privacy: &export.PrivacySpec{Category: privacy.CatEmployeeNo, Fields: []string{privacy.FieldEmployeeNo}, ServiceNotes: true}}
}

func (s *monthSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Month("월", q.Get("month"))}
}

func (s *monthSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	if m := q.Get("month"); len(m) != 7 {
		return 0, false, fmt.Errorf("%w: 달을 YYYY-MM 으로 입력하세요", contract.ErrValidation)
	}
	n, err := s.h.svc.MonthScopeCount(ctx)
	return n, false, err
}

// dayText 는 일수를 "1", "0.5", "1.25" 꼴로 쓴다(끝의 0 을 지운다).
func dayText(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func (s *monthSource) Rows(ctx context.Context, q url.Values, _ bool, _ int) ([][]export.Cell, error) {
	rep, err := s.h.svc.MonthReport(ctx, q.Get("month"))
	if err != nil {
		return nil, err
	}
	state := "미확정"
	if rep.Closed {
		state = "확정"
	}
	out := make([][]export.Cell, 0, len(rep.Rows))
	for _, x := range rep.Rows {
		out = append(out, []export.Cell{x.EmployeeNo, x.Name, x.Unit, x.WorkType, x.WorkDays, x.AttendDays, x.Late, x.EarlyLeave, x.WorkMinutes / 60, x.WorkMinutes % 60,
			dayText(x.AnnualDays), dayText(x.CompDays), dayText(x.SickDays), dayText(x.OtherLeaveDays), x.LeaveOfAbsence, x.AbsentDays, x.OverWeeks, state})
	}
	return out, nil
}

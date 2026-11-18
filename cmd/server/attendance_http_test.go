package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// B50 5단계: 출퇴근 라우트는 모듈이 꺼져 있으면 404, 켜지면 열린다. 체크인은 본문의 시각을 무시하고 서버 시각만 쓴다. 근태 마감·직접 정정은 인사 담당 권한이 있어야 한다.
func TestAttendanceHTTPModuleGateServerTimeAndPermissions(t *testing.T) {
	e := newERPOrgEnv(t)
	for _, p := range []string{"/api/attendance/today", "/api/attendance/week", "/api/attendance/corrections", "/api/attendance/work-types"} {
		if code, _ := e.do("staff", "GET", p, ""); code != 404 {
			t.Errorf("꺼진 모듈 %s: %d", p, code)
		}
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/check-in", `{}`); code != 404 {
		t.Errorf("꺼진 모듈 출근: %d", code)
	}
	// 근무 유형 설정은 관리자만, 모듈을 켜기 전에도 쓴다.
	body := `{"code":"early","name":"조기 근무","kind":"fixed","startTime":"08:00","endTime":"17:00","breakStart":"12:00","breakEnd":"13:00","active":true}`
	if code, _ := e.do("staff", "POST", "/api/admin/attendance/work-types", body); code != 403 {
		t.Errorf("직원의 유형 저장: %d", code)
	}
	if code, b := e.do("admin", "POST", "/api/admin/attendance/work-types", body); code != 200 {
		t.Fatalf("유형 저장: %d %s", code, b)
	}
	y := time.Now().In(contract.KST).Year()
	for _, d := range []string{fmt.Sprintf("%d-12-25", y), fmt.Sprintf("%d-12-25", y+1)} {
		e.do("admin", "POST", "/api/company/holidays", `{"items":[{"date":"`+d+`","name":"휴일"}]}`)
	}
	if code, b := e.do("admin", "POST", "/api/admin/attendance/policies", goodPolicy); code != 201 {
		t.Fatalf("규칙 저장: %d %s", code, b)
	}
	if did, err := e.d.Org.(*org.Service).Backfill(context.Background()); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	// 본문에 시각을 실어 보내도 서버 시각이 쓰인다(오늘 기록의 출근 시각이 본문 값 03:00 이 아니다).
	in := e.must("staff", "POST", "/api/attendance/check-in", `{"checkIn":"03:00","at":"2000-01-01T03:00:00Z"}`, 200)
	today, _ := in["today"].(map[string]any)
	if today["checkIn"] == "03:00" || today["checkIn"] == "" || today["checkIn"] == nil {
		t.Fatalf("출근 기록: %v", today)
	}
	if code, b := e.do("staff", "POST", "/api/attendance/check-in", `{}`); code != 400 || !strings.Contains(b, "이미 출근") {
		t.Fatalf("두 번째 출근: %d %s", code, b)
	}
	e.must("staff", "POST", "/api/attendance/check-out", `{}`, 200)
	week := e.must("staff", "GET", "/api/attendance/week", "", 200)
	if days, _ := week["days"].([]any); len(days) != 7 {
		t.Fatalf("주 기록: %v", week)
	}
	// 남의 주 기록은 직원에게 404, 마감·직접 정정·배정은 인사 권한 없이는 403.
	if code, _ := e.do("staff", "GET", "/api/attendance/week?userId="+fmt.Sprint(e.userID("admin@pl.test")), ""); code != 404 {
		t.Errorf("남의 주 기록: %d", code)
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/admin/closes", `{"month":"2020-01"}`); code != 403 {
		t.Errorf("직원의 마감: %d", code)
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/admin/correct", `{"userId":1,"workDate":"2026-01-02","checkIn":"09:00","reason":"x"}`); code != 403 {
		t.Errorf("직원의 직접 정정: %d", code)
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/admin/assignments", `{"userId":1,"workTypeId":1,"validFrom":"2026-01-01"}`); code != 403 {
		t.Errorf("직원의 배정: %d", code)
	}
	past := time.Now().In(contract.KST).AddDate(0, 0, -3).Format("2006-01-02")
	// 정정 요청: 사유가 없으면 400, 있으면 201 이고 본인이 승인하면 403.
	if code, _ := e.do("staff", "POST", "/api/attendance/corrections", `{"workDate":"`+past+`","checkIn":"09:00"}`); code != 400 {
		t.Errorf("사유 없는 정정: %d", code)
	}
	c := e.must("staff", "POST", "/api/attendance/corrections", `{"workDate":"`+past+`","checkIn":"09:00","checkOut":"18:00","reason":"퇴근 누락"}`, 201)
	if code, _ := e.do("staff", "POST", fmt.Sprintf("/api/attendance/corrections/%v/decide", c["id"]), `{"approve":true}`); code != 403 {
		t.Errorf("본인 승인: %d", code)
	}
	if c["statusLabel"] != "승인 대기" {
		t.Errorf("상태 문구: %v", c["statusLabel"])
	}
	// 달력은 누구나(범위는 서비스), 팀 현황은 조직장·인사 담당만, 월 내보내기는 attendance:export 권한자만이다.
	month := time.Now().In(contract.KST).AddDate(0, -1, 0).Format("2006-01")
	cal := e.must("staff", "GET", "/api/attendance/calendar?month="+month, "", 200)
	if cal["month"] != month {
		t.Errorf("달력: %v", cal)
	}
	if code, _ := e.do("staff", "GET", "/api/attendance/team", ""); code != 403 {
		t.Errorf("직원의 팀 현황: %d", code)
	}
	if code, _ := e.do("staff", "GET", "/api/attendance/export?format=csv&month="+month, ""); code != 403 {
		t.Errorf("직원의 월 내보내기: %d", code)
	}
	code, body := e.do("admin", "GET", "/api/attendance/export?format=csv&month="+month, "")
	if code != 200 || !strings.Contains(body, "소정 근무일 수") || !strings.Contains(body, "미확정") || strings.Contains(body, "사유") {
		t.Errorf("관리자의 월 내보내기: %d %.200s", code, body)
	}
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":false}`, 200)
	// 공통 내보내기 틀은 원천의 "없음"을 조건 오류(400)로 돌려준다: 파일은 만들어지지 않는다.
	if code, _ := e.do("admin", "GET", "/api/attendance/export?format=csv&month="+month, ""); code != 400 {
		t.Errorf("끈 뒤 월 내보내기: %d", code)
	}
	if code, _ := e.do("staff", "GET", "/api/attendance/today", ""); code != 404 {
		t.Errorf("끈 뒤: %d", code)
	}
}

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

const goodPolicy = `{"effectiveFrom":"2026-01-01","basis":"hire","workdayMinutes":480,"hourUnitMinutes":60,"attendanceRule":true,"carryMaxMinutes":0,"carryValidMonths":12,"expireMonths":12,"firstYearMode":"monthly"}`

// B50 2단계: 휴가 API 는 모듈이 꺼져 있으면 권한이 있어도 404 이고, 켜지면 열린다. 규칙·종류 설정 API 는 관리자만이며 모듈이 꺼져 있어도 쓸 수 있다.
func TestLeaveHTTPModuleGateAndPermissions(t *testing.T) {
	e := newERPOrgEnv(t)
	for _, p := range []string{"/api/attendance/leave/types", "/api/attendance/leave/balance", "/api/attendance/leave/requests", "/api/attendance/leave/ledger"} {
		if code, _ := e.do("staff", "GET", p, ""); code != 404 {
			t.Errorf("꺼진 모듈 %s: %d", p, code)
		}
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/leave/preview", `{}`); code != 404 {
		t.Errorf("꺼진 모듈 미리보기: %d", code)
	}
	// 설정 API: 직원 403, 관리자 200(꺼져 있어도). 종류는 처음 읽을 때 시스템 종류 6개가 만들어진다.
	if code, _ := e.do("staff", "GET", "/api/admin/attendance/policies", ""); code != 403 {
		t.Errorf("직원의 규칙 조회: %d", code)
	}
	ts := e.must("admin", "GET", "/api/admin/attendance/types", "", 200)
	if got, _ := ts["types"].([]any); len(got) != 6 {
		t.Fatalf("시스템 종류: %v", ts)
	}
	bad := strings.Replace(goodPolicy, `"expireMonths":12`, `"expireMonths":6`, 1)
	if code, body := e.do("admin", "POST", "/api/admin/attendance/policies", bad); code != 400 || !strings.Contains(body, "법정 기준") {
		t.Fatalf("법정 하한 위반: %d %s", code, body)
	}
	// 모듈을 켜면 열린다(점검표를 채운다).
	y := time.Now().In(contract.KST).Year()
	for _, d := range []string{fmt.Sprintf("%d-12-25", y), fmt.Sprintf("%d-12-25", y+1)} {
		e.do("admin", "POST", "/api/company/holidays", `{"items":[{"date":"`+d+`","name":"휴일"}]}`)
	}
	if code, body := e.do("admin", "POST", "/api/admin/attendance/policies", goodPolicy); code != 201 {
		t.Fatalf("규칙 저장: %d %s", code, body)
	}
	if did, err := e.d.Org.(*org.Service).Backfill(context.Background()); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	types := e.must("staff", "GET", "/api/attendance/leave/types", "", 200)
	if got, _ := types["types"].([]any); len(got) != 6 {
		t.Fatalf("신청용 종류: %v", types)
	}
	bal := e.must("staff", "GET", "/api/attendance/leave/balance", "", 200)
	if bal["workdayMinutes"] != float64(480) {
		t.Fatalf("잔여: %v", bal)
	}
	// 남의 잔여는 직원에게 404, 직원은 조정·규칙 저장을 못 한다.
	if code, _ := e.do("staff", "GET", "/api/attendance/leave/balance?userId="+fmt.Sprint(e.userID("admin@pl.test")), ""); code != 404 {
		t.Errorf("남의 잔여: %d", code)
	}
	if code, _ := e.do("staff", "POST", "/api/attendance/leave/adjust", `{"userId":1,"balance":"annual","kind":"adjust","minutes":480,"reason":"x"}`); code != 403 {
		t.Errorf("직원의 조정: %d", code)
	}
	if code, _ := e.do("staff", "POST", "/api/admin/attendance/policies", `{}`); code != 403 {
		t.Errorf("직원의 규칙 저장: %d", code)
	}
	// 잔여가 없으면 신청 미리보기는 400 과 한국어 문구다.
	if code, body := e.do("staff", "POST", "/api/attendance/leave/preview", `{"leaveCode":"annual","startDate":"2099-11-16","endDate":"2099-11-16"}`); code != 400 || !strings.Contains(body, "잔여") {
		t.Errorf("잔여 없는 미리보기: %d %s", code, body)
	}
	// 끄면 다시 404.
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":false}`, 200)
	if code, _ := e.do("staff", "GET", "/api/attendance/leave/types", ""); code != 404 {
		t.Errorf("끈 뒤 종류 목록: %d", code)
	}
}

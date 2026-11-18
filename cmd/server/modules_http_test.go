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

// B50 4절: 모듈 관리 API 의 권한·점검표·켜기·꺼짐 상태.
func TestModulesHTTPEnableFlow(t *testing.T) {
	e := newERPOrgEnv(t)
	// 권한: 관리자만. 직원·재무·감사자는 403.
	for _, who := range []string{"staff", "finance", "auditor"} {
		if code, _ := e.do(who, "GET", "/api/admin/modules", ""); code != 403 {
			t.Errorf("%s 목록: %d", who, code)
		}
		if code, _ := e.do(who, "PUT", "/api/admin/modules/attendance", `{"enabled":true}`); code != 403 {
			t.Errorf("%s 켜기: %d", who, code)
		}
	}
	out := e.must("admin", "GET", "/api/admin/modules", "", 200)
	mods, _ := out["modules"].([]any)
	if len(mods) != 1 || mods[0].(map[string]any)["key"] != "attendance" || mods[0].(map[string]any)["enabled"] != false {
		t.Fatalf("모듈 목록: %v", out)
	}
	// 점검표: 공휴일이 없으면 준비되지 않았고 켜기가 409 와 한국어 문구로 거부된다.
	chk := e.must("admin", "GET", "/api/admin/modules/attendance/checklist", "", 200)
	if chk["ready"] != false {
		t.Fatalf("공휴일 없이 준비됨: %v", chk)
	}
	if code, body := e.do("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`); code != 409 || !strings.Contains(body, "필수 점검 항목") {
		t.Fatalf("점검 전 켜기: %d %s", code, body)
	}
	if code, _ := e.do("admin", "PUT", "/api/admin/modules/attendance", `{}`); code != 400 {
		t.Fatalf("본문 없음: %d", code)
	}
	if code, _ := e.do("admin", "GET", "/api/admin/modules/nope/checklist", ""); code != 404 {
		t.Fatalf("모르는 모듈: %d", code)
	}
	// 공휴일을 등록하면 켜진다.
	y := time.Now().In(contract.KST).Year()
	for _, d := range []string{fmt.Sprintf("%d-12-25", y), fmt.Sprintf("%d-12-25", y+1)} {
		e.do("admin", "POST", "/api/company/holidays", `{"items":[{"date":"`+d+`","name":"휴일"}]}`)
	}
	// 연차 규칙이 없으면 공휴일이 있어도 준비되지 않는다(규칙 화면 API 는 모듈이 꺼져 있어도 쓸 수 있다).
	if code, body := e.do("admin", "POST", "/api/admin/attendance/policies", `{"effectiveFrom":"2026-01-01","basis":"hire","workdayMinutes":480,"hourUnitMinutes":60,"attendanceRule":true,"carryMaxMinutes":0,"carryValidMonths":12,"expireMonths":12,"firstYearMode":"monthly"}`); code != 201 {
		t.Fatalf("규칙 저장: %d %s", code, body)
	}
	// 조직에 등록되지 않은 사용자가 있으면 공휴일이 있어도 준비되지 않는다. 조직 등록(백필) 뒤에 켜진다.
	chk = e.must("admin", "GET", "/api/admin/modules/attendance/checklist", "", 200)
	if chk["ready"] != false {
		t.Fatalf("조직 미등록 사용자가 있는데 준비됨: %v", chk)
	}
	if did, err := e.d.Org.(*org.Service).Backfill(context.Background()); err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	chk = e.must("admin", "GET", "/api/admin/modules/attendance/checklist", "", 200)
	if chk["ready"] != true {
		t.Fatalf("점검표가 준비되지 않음: %v", chk)
	}
	on := e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	if on["enabled"] != true {
		t.Fatalf("켜기: %v", on)
	}
	out = e.must("admin", "GET", "/api/admin/modules", "", 200)
	if out["modules"].([]any)[0].(map[string]any)["enabled"] != true {
		t.Fatalf("켠 뒤 목록: %v", out)
	}
	off := e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":false}`, 200)
	if off["enabled"] != false {
		t.Fatalf("끄기: %v", off)
	}
	// 인사 담당 역할 템플릿.
	tpl := e.must("admin", "GET", "/api/admin/modules/attendance/hr-template", "", 200)
	perms, _ := tpl["permissions"].([]any)
	if tpl["name"] != "인사 담당" || len(perms) != 3 {
		t.Fatalf("템플릿: %v", tpl)
	}
	// 감사: 켜기·끄기 두 건이 모듈 대상으로 남는다.
	if n := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'module.set' AND target_type = 'module' AND target_id = 'attendance'`); n != 2 {
		t.Fatalf("감사 %d건", n)
	}
}

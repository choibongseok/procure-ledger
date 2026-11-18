package main

import (
	"encoding/json"
	"testing"
)

// B44 3단계: 연결 재무 집계 화면의 API 와 권한, 내보내기의 법인 필터, 사업장 선택기 목록, 부가세의 사업장 매개변수.
func TestConsolidatedAndEntityFilterHTTP(t *testing.T) {
	e := newERPOrgEnv(t)
	// 권한 consolidated:read 가 없는 직원은 거부된다.
	if code, _ := e.do("staff", "GET", "/api/finance/consolidated?unit=month&month=2026-11", ""); code != 403 {
		t.Errorf("직원: %d", code)
	}
	out := e.must("auditor", "GET", "/api/finance/consolidated?unit=month&month=2026-11", "", 200)
	ents, _ := out["entities"].([]any)
	if len(ents) != 1 {
		t.Fatalf("법인이 하나뿐인 설치의 열: %v", out["entities"])
	}
	if out["partialScope"] != false {
		t.Errorf("일부 법인 안내: %v", out["partialScope"])
	}
	if code, _ := e.do("auditor", "GET", "/api/finance/consolidated?unit=week", ""); code != 400 {
		t.Errorf("잘못된 단위: %d", code)
	}
	// 내보내기: 법인 필터 매개변수. 모르는 코드는 400, 범위 안의 코드는 통과한다.
	for path, want := range map[string]int{
		"/api/finance/consolidated/export/trial?format=csv&month=2026-11":                        200,
		"/api/finance/consolidated/export/trial?format=csv&month=2026-11&entity=MAIN":            200,
		"/api/finance/consolidated/export/trial?format=csv&month=2026-11&entity=NOPE":            400,
		"/api/finance/consolidated/export/intercompany?format=csv&month=2026-11":                 200,
		"/api/finance/trial-balance/export?format=csv&from=2026-11-01&to=2026-11-30&entity=MAIN": 200,
	} {
		if code, body := e.do("auditor", "GET", path, ""); code != want {
			t.Errorf("%s: %d(기대 %d) %s", path, code, want, body)
		}
	}
	// 사업장 선택기 목록(로그인한 사람이면 읽는다): 기본 법인의 주사업장 하나.
	sites := e.must("staff", "GET", "/api/entity/sites", "", 200)
	rows, _ := sites["sites"].([]any)
	if len(rows) != 1 {
		t.Fatalf("사업장: %v", sites)
	}
	// 부가세: 없는 사업장은 400.
	if code, _ := e.do("auditor", "GET", "/api/finance/vat-purchase?unit=month&month=2026-11&siteId=999", ""); code != 400 {
		t.Errorf("없는 사업장: %d", code)
	}
	var one map[string]any
	code, body := e.do("auditor", "GET", "/api/finance/vat-purchase?unit=month&month=2026-11", "")
	if code != 200 {
		t.Fatalf("부가세: %d", code)
	}
	_ = json.Unmarshal([]byte(body), &one)
	if _, has := one["sites"]; has {
		t.Errorf("사업장이 하나뿐이면 사업장 표시가 없어야 한다: %v", one["sites"])
	}
}

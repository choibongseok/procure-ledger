package main

import "testing"

// B44 13.1: 감사 로그 조회의 entity(법인 코드) 매개변수. 모르는 코드는 400, 범위 안의 코드와 "*" 는 통과한다.
func TestAuditLogsEntityParam(t *testing.T) {
	e := newOrgEnv(t)
	for path, want := range map[string]int{
		"/api/audit-logs?entity=MAIN": 200,
		"/api/audit-logs?entity=*":    200,
		"/api/audit-logs?entity=NOPE": 400,
	} {
		if code, body := e.do("auditor", "GET", path, ""); code != want {
			t.Errorf("%s: %d(기대 %d) %s", path, code, want, body)
		}
	}
}

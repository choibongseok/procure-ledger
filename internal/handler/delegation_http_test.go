package handler_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func TestHTTPDelegationFlow(t *testing.T) {
	s := newServer(t)
	now := time.Now().UTC()
	body := func(to string) map[string]any {
		return map[string]any{"delegateId": 3, "from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)}
	}

	// 권한 없는 역할은 지정할 수 없다(라우트 선언으로 403).
	for _, u := range []string{"staff", "auditor", "admin"} {
		s.must(u, "POST", "/api/approvals/delegations", body(""), 403)
	}
	// 기간 없음·직책 규칙 위반은 400.
	s.must("lead", "POST", "/api/approvals/delegations", map[string]any{"delegateId": 3}, 400)
	code, _, raw := s.do("lead", "POST", "/api/approvals/delegations", map[string]any{
		"delegateId": 1, "from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Add(time.Hour).Format(time.RFC3339)})
	if code != 400 || !strings.Contains(raw, "같은 직책 이상") {
		t.Fatalf("직책 규칙 위반: %d %s", code, raw)
	}

	d := s.must("lead", "POST", "/api/approvals/delegations", body(""), 201)
	if d["delegateName"] != "부서장" || d["revokedAt"] != nil {
		t.Fatalf("위임: %+v", d)
	}
	if !s.rec.Has("delegation.create", audit.TargetDelegation, audit.ResultSuccess) {
		t.Error("위임 지정 감사 기록 없음")
	}
	list := s.must("lead", "GET", "/api/approvals/delegations", nil, 200)
	if len(list["given"].([]any)) != 1 {
		t.Fatalf("내가 맡긴 위임: %+v", list)
	}
	if got := s.must("head", "GET", "/api/approvals/delegations", nil, 200); len(got["received"].([]any)) != 1 {
		t.Fatalf("받은 위임: %+v", got)
	}

	// 남의 위임은 해제할 수 없다. 본인은 해제할 수 있고 한 번만이다.
	s.must("head", "DELETE", "/api/approvals/delegations/"+id(d), nil, 404)
	s.must("lead", "DELETE", "/api/approvals/delegations/"+id(d), nil, 204)
	s.must("lead", "DELETE", "/api/approvals/delegations/"+id(d), nil, 409)
	if !s.rec.Has("delegation.revoke", audit.TargetDelegation, audit.ResultSuccess) {
		t.Error("위임 해제 감사 기록 없음")
	}
}

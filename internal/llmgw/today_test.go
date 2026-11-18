package llmgw_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 오늘 사용량: 본인 범위는 로그인 누구나, 전사 합계는 llm:usage_read 가 있을 때만. 한도는 가장 좁은 범위 기준이다.
func TestTodaySummaryScopesAndLimit(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	staff := as(contract.RoleStaff, 1, 10)
	other := as(contract.RoleStaff, 2, 20)
	if _, err := e.gw.Complete(staff, req("안녕")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(other, req("반가워")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES ('role','staff',100000)`); err != nil {
		t.Fatal(err)
	}

	mine, err := e.st.TodaySummary(staff)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Mine.Calls != 1 || mine.All != nil {
		t.Fatalf("staff 는 본인 범위만: %+v", mine)
	}
	if mine.Limit == nil || mine.Limit.Scope != "role" || mine.Limit.DailyLimit != 100000 || mine.Limit.Used != nil || mine.Limit.NearLimit {
		t.Fatalf("권한 없는 호출자에게는 범위 합계를 주지 않는다: %+v", mine.Limit)
	}

	aud, err := e.st.TodaySummary(as(contract.RoleAuditor, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if aud.All == nil || aud.All.Calls != 2 || aud.Mine.Calls != 0 {
		t.Fatalf("auditor 는 전사 합계와 본인: %+v", aud)
	}
	if aud.Limit != nil {
		t.Fatalf("auditor 에게 적용되는 한도 없음: %+v", aud.Limit)
	}
}

// 같은 역할의 다른 사용자가 쓴 총량은 집계값으로 새지 않는다. 임박 여부(한 비트)만 알린다.
func TestTodaySummaryDoesNotLeakRoleTotalToUnprivileged(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	staff, staff2 := as(contract.RoleStaff, 1, 10), as(contract.RoleStaff, 2, 20)
	if _, err := e.gw.Complete(staff, req("안녕")); err != nil {
		t.Fatal(err)
	}
	var used int64
	if err := e.db.QueryRow(`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM llm_usage WHERE status='ok'`).Scan(&used); err != nil || used == 0 {
		t.Fatalf("전제: %d %v", used, err)
	}
	// 한도를 사용량의 90% 근처로 잡아 임박 상태로 만든다.
	if _, err := e.db.Exec(`INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES ('role','staff',?)`, used*100/90); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.TodaySummary(staff2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mine.Tokens != 0 || got.Limit == nil || got.Limit.Used != nil || !got.Limit.NearLimit {
		t.Fatalf("mine=0, 합계 비공개, 임박 true 여야 함: %+v %+v", got, got.Limit)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "usedTokens") {
		t.Fatalf("응답에 범위 합계가 나감: %s", b)
	}
	// 권한이 있는 호출자(재무)는 범위 합계를 본다.
	if _, err := e.db.Exec(`INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES ('role','finance',1000000)`); err != nil {
		t.Fatal(err)
	}
	fin, err := e.st.TodaySummary(as(contract.RoleFinance, 3, 10))
	if err != nil || fin.Limit == nil || fin.Limit.Used == nil {
		t.Fatalf("권한 보유자는 범위 합계를 본다: %+v %v", fin, err)
	}
}

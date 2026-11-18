package llmgw_test

import (
	"context"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// B33: 모델 허용 규칙과 일일 한도의 역할 범위는 역할 이름 하나가 아니라 사용자가 가진 역할 키 전체(사용자 정의 역할 포함)로 판정한다.

// customUser 는 사용자 정의 역할 r_<번호> 를 사람에게 직접 부여하고(실효 권한 캐시까지) Principal 을 돌려준다. 호환용 Role 은 비어 있다.
func (e *env) customUser(t *testing.T, userID, roleNo int64, dept int64) context.Context {
	t.Helper()
	key := "r_" + itoa(roleNo)
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (?, ?, '신입', 'staff', ?)`, userID, "c"+itoa(userID)+"@pl.test", dept); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO roles (id, key, name, description, kind, created_at, updated_at) VALUES (?, ?, ?, '', 'custom', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z') ON CONFLICT DO NOTHING`, 100+roleNo, key, "사용자 정의 "+itoa(roleNo)); err != nil {
		t.Fatal(err)
	}
	gid := dbtest.InsertID(t, e.db, `INSERT INTO role_grants (role_id, target_type, target_id, created_at) VALUES (?, 'user', ?, '2026-11-10T00:00:00Z')`, 100+roleNo, userID)
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (?, 'policy:search', ?, ?)`, userID, 100+roleNo, gid); err != nil {
		t.Fatal(err)
	}
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: userID, Name: "신입", Role: "", DepartmentID: dept, Channel: contract.ChannelWeb,
		Perms: contract.NewPermSet(contract.PermPolicySearch), RoleNames: []string{"사용자 정의 " + itoa(roleNo)}})
}

func itoa(n int64) string {
	s := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestModelRuleAppliesToCustomRoleUsers(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	user := e.customUser(t, 50, 7, 10)
	// 사용자 정의 역할에 거부 규칙을 걸면 그 역할 사용자는 거부되고, 다른 사람은 그대로 허용된다.
	e.allow(t, "r_7", 0, "mock*", 0)
	if _, err := e.gw.Complete(user, req("안녕")); !errors.Is(err, contract.ErrModelNotAllowed) {
		t.Fatalf("사용자 정의 역할 거부 규칙이 걸려야 함: %v", err)
	}
	if e.spy.calls != 0 {
		t.Fatal("거부된 호출이 공급자에게 갔다")
	}
	if _, err := e.gw.Complete(as(contract.RoleFinance, 2, 20), req("안녕")); err != nil {
		t.Fatalf("다른 사람은 허용: %v", err)
	}
	// 같은 구체성에서는 거부가 이기고, 더 구체적인 역할+부서 허용 규칙은 이긴다.
	e.allow(t, "r_7", 10, "mock*", 1)
	if _, err := e.gw.Complete(user, req("안녕")); err != nil {
		t.Fatalf("역할+부서 허용 규칙이 이겨야 함: %v", err)
	}
	// 템플릿 사용자의 기존 동작은 그대로다: staff 규칙은 staff 에게만 걸린다.
	e.allow(t, "staff", 0, "mock*", 0)
	if _, err := e.gw.Complete(as(contract.RoleStaff, 1, 10), req("안녕")); !errors.Is(err, contract.ErrModelNotAllowed) {
		t.Fatalf("staff 규칙: %v", err)
	}
}

func TestQuotaAppliesToCustomRoleUsersAndSharesUsage(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	admin := as(contract.RoleAdmin, 9, 1)
	a := e.customUser(t, 50, 7, 10)
	b := e.customUser(t, 51, 7, 10) // 같은 사용자 정의 역할의 다른 사람
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "role", ScopeValue: "r_7", DailyLimit: 50}); err != nil {
		t.Fatalf("사용자 정의 역할 한도 등록: %v", err)
	}
	long := smallReq(strings.Repeat("가나다라 ", 30)) // 예상 입력 38 + 최대 출력 10 = 48 <= 50
	if _, err := e.gw.Complete(a, long); err != nil {
		t.Fatalf("첫 호출은 한도 이내: %v", err)
	}
	// 역할 한도는 그 역할 사람들의 합산이다: 다른 사람의 호출은 남은 한도를 넘어 거부된다.
	if _, err := e.gw.Complete(b, smallReq("두 번째")); !errors.Is(err, contract.ErrQuotaExceeded) {
		t.Fatalf("같은 역할의 합산 한도: %v", err)
	}
	// 사용량 행의 대표 역할은 사용자 정의 역할 키다.
	var role string
	if err := e.db.QueryRow(`SELECT role FROM llm_usage WHERE user_id = 50 AND status = 'ok' LIMIT 1`).Scan(&role); err != nil || role != "r_7" {
		t.Fatalf("대표 역할 %q: %v", role, err)
	}
	// 오늘 사용량 요약과 역할별 한도 표에도 나온다.
	today, err := e.st.TodaySummary(a)
	if err != nil || today.Limit == nil || today.Limit.Scope != "role" || today.Limit.DailyLimit != 50 {
		t.Fatalf("오늘 요약: %+v %v", today.Limit, err)
	}
	limits, err := e.st.RoleLimits(contract.WithPrincipal(context.Background(), contract.Principal{UserID: 50, Role: "", DepartmentID: 10, Channel: contract.ChannelWeb,
		Perms: contract.NewPermSet(contract.PermLLMUsageRead, contract.PermPolicySearch)}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range limits {
		if l.Role == "r_7" && l.DailyLimit != nil && *l.DailyLimit == 50 && l.UsedTokens > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("역할별 한도 표에 본인 사용자 정의 역할 줄이 없음: %+v", limits)
	}
}

func TestPolicyAndQuotaInputAcceptCustomRoleKeysOnly(t *testing.T) {
	e := newEnv(t)
	admin := as(contract.RoleAdmin, 9, 1)
	e.customUser(t, 50, 7, 10)
	if _, err := e.st.AddPolicy(admin, llmgw.Policy{Role: "r_7", ModelPattern: "mock*", Allowed: true}); err != nil {
		t.Fatalf("사용자 정의 역할 키 규칙: %v", err)
	}
	for _, bad := range []string{"r_999", "슈퍼관리자", "Admin "} {
		if _, err := e.st.AddPolicy(admin, llmgw.Policy{Role: bad, ModelPattern: "mock*", Allowed: true}); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%q 는 거절: %v", bad, err)
		}
		if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "role", ScopeValue: bad, DailyLimit: 10}); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%q 한도는 거절: %v", bad, err)
		}
	}
	// 지운 역할은 새 규칙에 쓸 수 없다.
	if _, err := e.db.Exec(`UPDATE roles SET deleted_at = '2026-11-11T00:00:00Z' WHERE key = 'r_7'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AddPolicy(admin, llmgw.Policy{Role: "r_7", ModelPattern: "other*", Allowed: true}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("지운 역할: %v", err)
	}
	// 기본 템플릿 키는 그대로 허용된다.
	if _, err := e.st.AddPolicy(admin, llmgw.Policy{Role: "auditor", ModelPattern: "mock*", Allowed: true}); err != nil {
		t.Fatalf("템플릿 키: %v", err)
	}
}

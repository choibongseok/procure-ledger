package llmgw_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	cAudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 가짜가 아닌 실제 감사 기록기(internal/audit + SQLite)에서도 정책·한도 관리의 거부와 성공,
// 이미지 차단·전송이 기록되는지 본다. 가짜는 빈 대상 ID 같은 입력을 받아 주던 시절이 있어 실제로 한 번 더 확인한다.
func newRealAuditEnv(t *testing.T) (*env, *audit.Store) {
	t.Helper()
	e := newEnv(t)
	real := audit.New(e.db)
	e.st = &llmgw.Store{DB: e.db, Txs: e.txs, Audit: real, Now: func() time.Time { return e.now }}
	gw, err := llmgw.New(llmgw.Options{Provider: e.spy, DefaultModel: "mock-1", DB: e.db, Txs: e.txs, Audit: real, Masker: fake.Masker{},
		Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.gw = gw
	return e, real
}

func realRecords(t *testing.T, a *audit.Store, action string) []cAudit.Record {
	t.Helper()
	rs, err := a.List(context.Background(), cAudit.Filter{Action: action, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestPolicyAndQuotaManagementAuditedInRealRecorder(t *testing.T) {
	e, real := newRealAuditEnv(t)
	staff, admin := as(contract.RoleStaff, 1, 10), as(contract.RoleAdmin, 9, 1)

	// 거부: 세 관리 동작 모두 실제 기록기에 denied 로 남는다.
	if _, err := e.st.AddPolicy(staff, llmgw.Policy{ModelPattern: "mock*", Allowed: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("add: %v", err)
	}
	if err := e.st.SetQuota(staff, llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 10}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("quota: %v", err)
	}
	if err := e.st.DeletePolicy(staff, 1); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("delete: %v", err)
	}
	for _, action := range []string{"llm_policy.add", "llm_quota.set", "llm_policy.delete"} {
		rs := realRecords(t, real, action)
		if len(rs) != 1 || rs[0].Result != cAudit.ResultDenied || rs[0].ActorRole != contract.RoleStaff || rs[0].TargetID == "" {
			t.Fatalf("%s 거부가 실제 기록기에 남아야 함: %+v", action, rs)
		}
	}
	if n, _ := e.st.ListPolicies(context.Background()); len(n) != 0 {
		t.Fatal("거부된 요청이 정책을 바꿨다")
	}

	// 성공: 같은 동작이 success 로 남고 체인이 유효하다.
	id, err := e.st.AddPolicy(admin, llmgw.Policy{ModelPattern: "mock*", Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeletePolicy(admin, id); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"llm_policy.add", "llm_quota.set", "llm_policy.delete"} {
		rs := realRecords(t, real, action)
		if len(rs) != 2 {
			t.Fatalf("%s 거부+성공 2건이어야 함: %+v", action, rs)
		}
	}
	if res, err := real.Verify(context.Background()); err != nil || !res.OK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

func TestImageGuardAuditedInRealRecorder(t *testing.T) {
	e, real := newRealAuditEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	imgReq := llm.Request{Purpose: "test", Messages: []llm.Message{{Role: "user", Content: "x"}}, Images: []llm.Image{{MediaType: "image/png", Data: []byte("img")}}}

	// 권한·설정 차단은 llm_image.blocked(denied) 로 남고 공급자는 호출되지 않는다.
	if _, err := e.gw.Complete(as(contract.RoleStaff, 1, 10), imgReq); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(as(contract.RoleFinance, 3, 10), imgReq); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal(err)
	}
	if rs := realRecords(t, real, llmgw.ActionImageBlocked); len(rs) != 2 || e.spy.calls != 0 {
		t.Fatalf("차단 2건·공급자 미호출이어야 함: %+v calls=%d", rs, e.spy.calls)
	}
	// 켜면 전송 감사가 남는다.
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value) VALUES (?, 'true') ON CONFLICT(key) DO UPDATE SET value = 'true'`, llmgw.SettingImageTransmission); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(as(contract.RoleFinance, 3, 10), imgReq); err != nil {
		t.Fatal(err)
	}
	if rs := realRecords(t, real, llmgw.ActionImageSent); len(rs) != 1 || rs[0].Result != cAudit.ResultSuccess {
		t.Fatalf("전송 감사: %+v", rs)
	}
	if res, err := real.Verify(context.Background()); err != nil || !res.OK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

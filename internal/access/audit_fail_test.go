package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	cat "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

type failRec struct {
	cat.Recorder
	fail bool
}

func (f *failRec) Record(ctx context.Context, tx contract.Tx, e cat.Entry) error {
	if f.fail {
		return contract.ErrAuditFailed
	}
	return f.Recorder.Record(ctx, tx, e)
}

// AC-1340: role.* 쓰기는 감사 기록과 같은 트랜잭션이다. 기록이 실패하면 요청이 실패하고 역할·부여·요청이 하나도 바뀌지 않는다.
func TestRoleWritesRollBackWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	rec := &failRec{Recorder: audit.New(e.db)}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	svc := New(e.db, contract.SQLTxManager{DB: e.db}, rec, func() time.Time { return now })
	ctx := context.Background()
	s := e.staff(t, "af@x")
	// 준비(감사 성공): 변경될 역할·부여·위험 요청 한 벌.
	role, err := svc.CreateRole(ctx, e.admin, "시험 역할", "", []string{"report:read"})
	if err != nil {
		t.Fatal(err)
	}
	risky, err := svc.CreateRole(ctx, e.admin, "위험 시험", "", []string{"audit:verify"})
	if err != nil {
		t.Fatal(err)
	}
	del, err := svc.CreateRole(ctx, e.admin, "삭제용", "", []string{"report:read"}) // 부여가 없어야 지울 수 있다
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	if err != nil {
		t.Fatal(err)
	}
	req, err := svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID, Reason: "시험"})
	if err != nil || req.Request == nil {
		t.Fatalf("위험 요청: %+v %v", req, err)
	}
	snap := func() [6]int {
		return [6]int{e.n(t, `SELECT COUNT(*) FROM roles`), e.n(t, `SELECT COUNT(*) FROM role_grants`), e.n(t, `SELECT COUNT(*) FROM role_permissions`),
			e.n(t, `SELECT COUNT(*) FROM role_change_requests WHERE status='pending'`), e.n(t, `SELECT SUM(version) FROM roles`), e.n(t, `SELECT COUNT(*) FROM user_effective_permissions`)}
	}
	before := snap()
	rec.fail = true
	cur, _ := loadRole(ctx, e.db, role.ID)
	steps := map[string]error{}
	_, steps["create"] = svc.CreateRole(ctx, e.admin, "새 역할", "", []string{"report:read"})
	_, steps["clone"] = svc.CloneRole(ctx, e.admin, role.ID, "복제")
	_, steps["update"] = svc.UpdateRole(ctx, e.admin, role.ID, cur.Version, "시험 역할", "설명", []string{"report:read", "budget:read"}, "")
	steps["delete"] = svc.DeleteRole(ctx, e.admin, del.ID)
	_, steps["grant"] = svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: e.admin.UserID})
	steps["revoke"] = svc.RevokeGrant(ctx, e.admin, g.GrantID)
	steps["approve"] = svc.Approve(ctx, e.boss, req.Request.ID)
	steps["reject"] = svc.Reject(ctx, e.boss, req.Request.ID, "사유")
	for name, err := range steps {
		if !errors.Is(err, contract.ErrAuditFailed) {
			t.Errorf("%s: 감사 실패는 요청 실패여야 함: %v", name, err)
		}
	}
	if after := snap(); after != before {
		t.Fatalf("감사 실패인데 데이터가 바뀜: %v → %v", before, after)
	}
	// 감사가 돌아오면 같은 요청이 성공한다(상태가 어긋나지 않았다).
	rec.fail = false
	if err := svc.RevokeGrant(ctx, e.admin, g.GrantID); err != nil {
		t.Fatalf("복구 뒤 회수: %v", err)
	}
}

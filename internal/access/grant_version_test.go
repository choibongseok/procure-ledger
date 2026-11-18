package access

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 부여 요청 뒤 역할 권한 구성이 바뀌면 그 요청은 승인할 수 없다(요청 시점의 역할 버전을 저장하고 승인 때 비교한다).
func TestGrantRequestRejectedWhenRoleChangedMeanwhile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "gv@x")
	role, err := e.svc.CreateRole(ctx, e.admin, "검증 보조", "", []string{"audit:verify", "audit:read"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: s.UserID, Reason: "시험"})
	if err != nil || res.Request == nil {
		t.Fatalf("%+v %v", res, err)
	}
	// 요청 뒤에 역할의 권한 구성이 바뀐다(위험 권한은 더하지 않아 즉시 적용되는 변경).
	cur, _ := loadRole(ctx, e.db, role.ID)
	if ch, err := e.svc.UpdateRole(ctx, e.admin, role.ID, cur.Version, "검증 보조", "", []string{"audit:verify"}, ""); err != nil || ch.Role == nil {
		t.Fatalf("역할 수정: %+v %v", ch, err)
	}
	err = e.svc.Approve(ctx, e.boss, res.Request.ID)
	if !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "그 사이 역할이 바뀌어") {
		t.Fatalf("역할이 바뀐 뒤 승인: %v", err)
	}
	if e.perms(t, s.UserID).Has(contract.PermAuditVerify) {
		t.Fatal("거절됐는데 적용됨")
	}
}

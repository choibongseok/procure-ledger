package auth_test

import (
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 토큰으로 인증된 Principal 은 TokenID 를 갖고, TokenLookup 은 이름·앞자리·소유자만 돌려준다(폐기된 토큰 포함).
func TestTokenIDAndLookup(t *testing.T) {
	svc, admin, _ := newSvc(t, nil)
	ctx := context.Background()
	raw, info, err := svc.Issue(ctx, admin, "모니터링용", 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.AuthenticateToken(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.TokenID != info.ID || p.TokenID == 0 {
		t.Fatalf("TokenID %d, 발급 번호 %d", p.TokenID, info.ID)
	}
	if err := svc.Revoke(ctx, admin, info.ID); err != nil {
		t.Fatal(err)
	}
	var lookup contract.TokenLookup = svc
	got, err := lookup.TokenInfos(ctx, []int64{info.ID, 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("없는 번호는 빠져야 한다: %+v", got)
	}
	ti := got[info.ID]
	if ti.Name != "모니터링용" || ti.Prefix != raw[:8] || ti.OwnerName == "" || ti.RevokedAt == nil {
		t.Fatalf("조회 결과: %+v", ti)
	}
	if ti.Prefix == raw {
		t.Fatal("앞자리만 나가야 한다")
	}
	if m, err := lookup.TokenInfos(ctx, nil); err != nil || len(m) != 0 {
		t.Fatalf("빈 입력: %v %v", m, err)
	}
}

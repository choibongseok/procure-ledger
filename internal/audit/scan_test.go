package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Scan 은 상한 없이 조건에 맞는 기록을 훑고, 읽기만 하며 체인을 건드리지 않는다.
func TestScanReadsAllWithoutTouchingChain(t *testing.T) {
	s, txs := setup(t)
	fill(t, s, txs, 600)
	ctx := context.Background()
	before, _ := s.Verify(ctx)
	n := 0
	err := s.Scan(ctx, caudit.Filter{Channel: contract.ChannelWeb}, func(caudit.Record) error { n++; return nil })
	if err != nil || n != 600 {
		t.Fatalf("Scan %d건 err=%v (List 상한 500 을 넘어야 한다)", n, err)
	}
	m := 0
	_ = s.Scan(ctx, caudit.Filter{Channel: contract.ChannelMCP}, func(caudit.Record) error { m++; return nil })
	if m != 0 {
		t.Fatalf("채널 필터: %d", m)
	}
	stop := errors.New("stop")
	if err := s.Scan(ctx, caudit.Filter{}, func(caudit.Record) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("fn 오류 전파: %v", err)
	}
	after, _ := s.Verify(ctx)
	if !after.OK || after.Checked != before.Checked || after.HeadHash != before.HeadHash {
		t.Fatalf("Scan 뒤 체인 변화: %+v -> %+v", before, after)
	}
}

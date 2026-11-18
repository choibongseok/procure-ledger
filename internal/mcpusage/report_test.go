package mcpusage

import (
	"context"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

type fakeScan struct {
	recs []audit.Record
	got  audit.Filter
}

func (f *fakeScan) Scan(_ context.Context, flt audit.Filter, fn func(audit.Record) error) error {
	f.got = flt
	for _, r := range f.recs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

type fakeTokens map[int64]contract.TokenInfo

func (f fakeTokens) TokenInfos(_ context.Context, ids []int64) (map[int64]contract.TokenInfo, error) {
	out := map[int64]contract.TokenInfo{}
	for _, id := range ids {
		if t, ok := f[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func rec(action string, res audit.Result, detail string, ms int64, tok int64, at time.Time) audit.Record {
	r := audit.Record{Channel: contract.ChannelMCP, Action: action, Result: res, Detail: detail, At: at}
	if ms >= 0 {
		r.Diff = append(r.Diff, audit.DiffItem{Key: audit.DiffKeyDurationMS, New: itoa(ms)})
	}
	if tok != 0 {
		r.Diff = append(r.Diff, audit.DiffItem{Key: audit.DiffKeyTokenID, New: itoa(tok)})
	}
	return r
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// 호출·권한 거부·인증 실패·한도 초과·오류가 섞인 데이터의 집계, 쓰기 도구의 중복 기록과 다른 채널 기록의 제외, 지연 백분위.
func TestReportAggregatesMixedOutcomes(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	revoked := at
	other := rec("mcp.get_stock", audit.ResultSuccess, "x", 5, 1, at)
	other.Channel = contract.ChannelWeb
	sc := &fakeScan{recs: []audit.Record{
		// search_items: 성공 4건(10,20,30,40ms), 권한 거부 1건, 한도 초과 1건
		rec("mcp.search_items", audit.ResultSuccess, "tool=search_items", 10, 1, at),
		rec("mcp.search_items", audit.ResultSuccess, "tool=search_items", 20, 1, at.Add(time.Minute)),
		rec("mcp.search_items", audit.ResultSuccess, "tool=search_items", 30, 2, at),
		rec("mcp.search_items", audit.ResultSuccess, "tool=search_items", 40, 2, at),
		rec("mcp.search_items", audit.ResultDenied, "tool=search_items required=x", 2, 2, at),
		rec("mcp.search_items", audit.ResultDenied, "tool=search_items rate_limited limit=4", 1, 2, at),
		// 쓰기 도구: 실행 전 기록(duration 없음)은 세지 않고 결과 기록만 센다.
		rec("mcp.approve_or_reject", audit.ResultSuccess, "tool=approve_or_reject requested", -1, 1, at),
		rec("mcp.approve_or_reject.result", audit.ResultError, "tool=approve_or_reject failed", 100, 1, at),
		// 인증 실패(토큰 번호 없음)
		rec("mcp.get_stock", audit.ResultDenied, "tool=get_stock auth=invalid", 3, 0, at),
		other, // 다른 채널
		rec("purchase_request.create", audit.ResultSuccess, "", 1, 1, at), // mcp. 동작이 아님
	}}
	svc := &Service{Scan: sc, Tokens: fakeTokens{
		1: {ID: 1, Name: "내 노트북", Prefix: "plp_ab12", OwnerName: "김직원"},
		2: {ID: 2, Name: "구형", Prefix: "plp_cd34", OwnerName: "이팀장", RevokedAt: &revoked},
	}, RatePerMin: func() int { return 4 }}
	from, to := at.Add(-24*time.Hour), at.Add(24*time.Hour)
	rep, err := svc.Report(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if sc.got.Channel != contract.ChannelMCP || !sc.got.From.Equal(from) || !sc.got.To.Equal(to) {
		t.Fatalf("채널·기간 필터: %+v", sc.got)
	}
	if rep.RatePerMin != 4 {
		t.Fatalf("한도 표시: %d", rep.RatePerMin)
	}
	tot := rep.Total
	if tot.Calls != 8 || tot.Success != 4 || tot.Denied != 1 || tot.RateLimited != 1 || tot.AuthFailed != 1 || tot.Errors != 1 {
		t.Fatalf("합계: %+v", tot)
	}
	if tot.RejectRate != 0.5 {
		t.Fatalf("거부율: %v", tot.RejectRate)
	}
	byTool := map[string]ToolRow{}
	for _, r := range rep.Tools {
		byTool[r.Tool] = r
	}
	if len(byTool) != 3 {
		t.Fatalf("도구 3종(search_items, approve_or_reject, get_stock): %v", rep.Tools)
	}
	s := byTool["search_items"]
	if s.Calls != 6 || s.Success != 4 || s.Denied != 1 || s.RateLimited != 1 || s.MaxMS != 40 || s.P50MS != 10 || s.AvgMS != 17 {
		t.Fatalf("search_items: %+v", s)
	}
	if rep.Tools[0].Tool != "search_items" {
		t.Fatalf("호출 많은 순: %v", rep.Tools)
	}
	if a := byTool["approve_or_reject"]; a.Calls != 1 || a.Errors != 1 || a.RejectRate != 1 || a.P95MS != 100 {
		t.Fatalf("쓰기 도구는 결과 기록 1건만: %+v", a)
	}
	if len(rep.Tokens) != 3 {
		t.Fatalf("토큰 3종(1, 2, 알 수 없음): %+v", rep.Tokens)
	}
	for _, tk := range rep.Tokens {
		switch tk.TokenID {
		case 1:
			if tk.Name != "내 노트북" || tk.Prefix != "plp_ab12" || tk.Owner != "김직원" || tk.Revoked || tk.Calls != 3 || tk.Errors != 1 || !tk.LastAt.Equal(at.Add(time.Minute)) {
				t.Fatalf("토큰 1: %+v", tk)
			}
		case 2:
			if !tk.Revoked || tk.Calls != 4 || tk.Denied != 1 || tk.RateLimited != 1 || tk.Success != 2 {
				t.Fatalf("토큰 2: %+v", tk)
			}
		case 0:
			if tk.Name != "" || tk.AuthFailed != 1 || tk.Calls != 1 {
				t.Fatalf("알 수 없는 토큰: %+v", tk)
			}
		}
	}
}

func TestReportEmptyPeriod(t *testing.T) {
	svc := &Service{Scan: &fakeScan{}}
	rep, err := svc.Report(context.Background(), time.Unix(0, 0), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total.Calls != 0 || rep.Total.RejectRate != 0 || rep.Tools == nil || rep.Tokens == nil {
		t.Fatalf("빈 기간: %+v", rep)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if percentile(v, 50) != 5 || percentile(v, 95) != 10 || percentile(v, 10) != 1 || percentile(nil, 50) != 0 {
		t.Fatal("nearest-rank 백분위")
	}
}

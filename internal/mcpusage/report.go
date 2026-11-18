package mcpusage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Stats 는 호출 묶음 하나의 집계다. 거부는 종류별로 나눠 센다.
type Stats struct {
	Calls       int     `json:"calls"`
	Success     int     `json:"success"`
	Denied      int     `json:"denied"`      // 권한 거부
	AuthFailed  int     `json:"authFailed"`  // 폐기·만료 토큰
	RateLimited int     `json:"rateLimited"` // 분당 한도 초과
	Errors      int     `json:"errors"`      // 도구 실행 오류
	RejectRate  float64 `json:"rejectRate"`  // 성공이 아닌 호출 / 전체 호출(0~1)
	AvgMS       int64   `json:"avgMs"`
	P50MS       int64   `json:"p50Ms"`
	P95MS       int64   `json:"p95Ms"`
	MaxMS       int64   `json:"maxMs"`
}

// ToolRow 는 도구별 집계다. Tool 은 도구 이름이고 화면이 한국어 라벨로 바꾼다.
type ToolRow struct {
	Tool string `json:"tool"`
	Stats
}

// TokenRow 는 개인 토큰별 사용량이다. 토큰은 이름·앞자리·소유자로만 나타내고 원문·해시는 없다.
// TokenID 0 은 토큰을 알 수 없는 호출(인증 실패)이다.
type TokenRow struct {
	TokenID int64     `json:"tokenId"`
	Name    string    `json:"name"`
	Prefix  string    `json:"prefix"`
	Owner   string    `json:"owner"`
	Revoked bool      `json:"revoked"`
	LastAt  time.Time `json:"lastAt"`
	Stats
}

// Report 는 한 기간의 집계 결과다. From 은 포함, To 는 미포함이다.
type Report struct {
	From              time.Time  `json:"from"`
	To                time.Time  `json:"to"`
	RatePerMin        int        `json:"ratePerMin"`        // 설정된 토큰별 분당 한도. 0 이면 한도 없음
	RatePerUserPerMin int        `json:"ratePerUserPerMin"` // 설정된 사용자별 합산 분당 한도(B17). 0 이면 없음
	Total             Stats      `json:"total"`
	Tools             []ToolRow  `json:"tools"`
	Tokens            []TokenRow `json:"tokens"`
}

// Service 는 감사 로그에서 사용량을 집계한다. 쓰기는 하지 않는다.
type Service struct {
	Scan              audit.Scanner
	Tokens            contract.TokenLookup
	RatePerMin        func() int // 설정된 토큰별 한도. nil 이면 0
	RatePerUserPerMin func() int // 설정된 사용자별 합산 한도. nil 이면 0
	// DB 가 있으면 아직 요약 기록이 안 남은 한도 창의 거부 횟수도 센다(창이 열려 있거나 정산 전). 없으면 감사 로그만 센다.
	DB *sql.DB
}

type acc struct {
	Stats
	durs []int64
	last time.Time
}

func (a *acc) add(outcome string, ms int64, at time.Time) {
	a.Calls++
	switch outcome {
	case "success":
		a.Success++
	case "denied":
		a.Denied++
	case "auth":
		a.AuthFailed++
	case "rate":
		a.RateLimited++
	default:
		a.Errors++
	}
	a.durs = append(a.durs, ms)
	if at.After(a.last) {
		a.last = at
	}
}

// addRateExtra 는 창 요약(B17)이 알려 주는, 첫 거부 기록 말고 더 있던 한도 초과 거부를 센다. 소요 시간은 모르므로 지연 집계에는 넣지 않는다.
func (a *acc) addRateExtra(n int, at time.Time) {
	a.Calls += n
	a.RateLimited += n
	if at.After(a.last) {
		a.last = at
	}
}

func (a *acc) finish() Stats {
	s := a.Stats
	if s.Calls > 0 {
		s.RejectRate = float64(s.Calls-s.Success) / float64(s.Calls)
	}
	if len(a.durs) > 0 { // 요약 기록만 있는 묶음은 소요 시간을 모른다
		sort.Slice(a.durs, func(i, j int) bool { return a.durs[i] < a.durs[j] })
		var sum int64
		for _, d := range a.durs {
			sum += d
		}
		s.AvgMS = sum / int64(len(a.durs))
		s.P50MS = percentile(a.durs, 50)
		s.P95MS = percentile(a.durs, 95)
		s.MaxMS = a.durs[len(a.durs)-1]
	}
	return s
}

// percentile 은 정렬된 값에서 가장 가까운 순위(nearest-rank) 백분위다.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func diffValue(items []audit.DiffItem, key string) (string, bool) {
	for _, it := range items {
		if it.Key == key {
			return it.New, true
		}
	}
	return "", false
}

// classify 는 호출 종료 기록의 결과를 집계 종류로 나눈다.
func classify(r audit.Record) string {
	switch r.Result {
	case audit.ResultSuccess:
		return "success"
	case audit.ResultDenied:
		switch {
		case strings.Contains(r.Detail, "rate_limited"):
			return "rate"
		case strings.Contains(r.Detail, "auth=invalid"):
			return "auth"
		}
		return "denied"
	}
	return "error"
}

type pendingWindow struct {
	token, window int64
	denied        int
}

// pendingWindows 는 기간 안에서 아직 요약 기록이 없는(거부 2회 이상) 토큰·분 창이다. 첫 거부 1건은 이미 감사 로그에 있다.
func (s *Service) pendingWindows(ctx context.Context, from, to time.Time) ([]pendingWindow, error) {
	if s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT token_id, window_start, denied_count FROM mcp_rate_window
		 WHERE denied_count >= 2 AND summarized = 0 AND window_start >= ? AND window_start < ?`, from.Unix()/60, (to.Unix()+59)/60)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingWindow
	for rows.Next() {
		var w pendingWindow
		if err := rows.Scan(&w.token, &w.window, &w.denied); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SummaryTool 은 창 요약 기록의 "도구" 이름이다(어느 도구의 거부인지 모른다).
const SummaryTool = "rate_limited_summary"

func toolOf(action string) string {
	return strings.TrimSuffix(strings.TrimPrefix(action, "mcp."), ".result")
}

// Report 는 [from, to) 기간의 MCP 호출을 집계한다. 호출 1건은 duration_ms 가 있는 종료 기록 1건이다
// (쓰기 도구의 실행 전 "requested" 기록에는 없어 두 번 세지 않는다). 감사 로그를 읽기만 한다.
func (s *Service) Report(ctx context.Context, from, to time.Time) (Report, error) {
	rep := Report{From: from, To: to, Tools: []ToolRow{}, Tokens: []TokenRow{}}
	if s.RatePerMin != nil {
		rep.RatePerMin = s.RatePerMin()
	}
	if s.RatePerUserPerMin != nil {
		rep.RatePerUserPerMin = s.RatePerUserPerMin()
	}
	total := &acc{}
	tools := map[string]*acc{}
	tokens := map[int64]*acc{}
	// 요약 전 창은 감사 로그를 읽기 전에 먼저 읽는다. 그 사이 정산돼도 아래에서 같은 (토큰, 창)의 요약 기록이 보이면 건너뛰므로 두 번 세지 않는다.
	pending, err := s.pendingWindows(ctx, from, to)
	if err != nil {
		return Report{}, fmt.Errorf("한도 창: %w", err)
	}
	summarized := map[[2]int64]bool{}
	err = s.Scan.Scan(ctx, audit.Filter{Channel: contract.ChannelMCP, From: from, To: to}, func(r audit.Record) error {
		if r.Channel != contract.ChannelMCP || !strings.HasPrefix(r.Action, "mcp.") {
			return nil
		}
		dv, ok := diffValue(r.Diff, audit.DiffKeyDurationMS)
		if !ok {
			// 창 요약 기록: 거부 총횟수(첫 거부 포함)에서 이미 세어진 첫 거부 1건을 뺀 만큼을 더한다. 도구는 알 수 없어 별도 행으로 둔다.
			if r.Action == SummaryAction {
				cv, ok := diffValue(r.Diff, audit.DiffKeyDeniedCount)
				n, _ := strconv.Atoi(cv)
				if !ok || n < 2 {
					return nil
				}
				var tid int64
				if tv, ok := diffValue(r.Diff, audit.DiffKeyTokenID); ok {
					tid, _ = strconv.ParseInt(tv, 10, 64)
				}
				if wv, ok := diffValue(r.Diff, audit.DiffKeyWindowStart); ok {
					w, _ := strconv.ParseInt(wv, 10, 64)
					summarized[[2]int64{tid, w}] = true
				}
				total.addRateExtra(n-1, r.At)
				if tools[SummaryTool] == nil {
					tools[SummaryTool] = &acc{}
				}
				tools[SummaryTool].addRateExtra(n-1, r.At)
				if tokens[tid] == nil {
					tokens[tid] = &acc{}
				}
				tokens[tid].addRateExtra(n-1, r.At)
			}
			return nil
		}
		ms, _ := strconv.ParseInt(dv, 10, 64)
		var tid int64
		if tv, ok := diffValue(r.Diff, audit.DiffKeyTokenID); ok {
			tid, _ = strconv.ParseInt(tv, 10, 64)
		}
		out := classify(r)
		total.add(out, ms, r.At)
		tool := toolOf(r.Action)
		if tools[tool] == nil {
			tools[tool] = &acc{}
		}
		tools[tool].add(out, ms, r.At)
		if tokens[tid] == nil {
			tokens[tid] = &acc{}
		}
		tokens[tid].add(out, ms, r.At)
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("사용량 집계: %w", err)
	}
	for _, w := range pending {
		if summarized[[2]int64{w.token, w.window}] {
			continue
		}
		at := time.Unix(w.window*60, 0)
		total.addRateExtra(w.denied-1, at)
		if tools[SummaryTool] == nil {
			tools[SummaryTool] = &acc{}
		}
		tools[SummaryTool].addRateExtra(w.denied-1, at)
		if tokens[w.token] == nil {
			tokens[w.token] = &acc{}
		}
		tokens[w.token].addRateExtra(w.denied-1, at)
	}
	rep.Total = total.finish()
	for name, a := range tools {
		rep.Tools = append(rep.Tools, ToolRow{Tool: name, Stats: a.finish()})
	}
	sort.Slice(rep.Tools, func(i, j int) bool {
		if rep.Tools[i].Calls != rep.Tools[j].Calls {
			return rep.Tools[i].Calls > rep.Tools[j].Calls
		}
		return rep.Tools[i].Tool < rep.Tools[j].Tool
	})
	var ids []int64
	for id := range tokens {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	infos := map[int64]contract.TokenInfo{}
	if len(ids) > 0 && s.Tokens != nil {
		infos, err = s.Tokens.TokenInfos(ctx, ids)
		if err != nil {
			return Report{}, fmt.Errorf("토큰 정보: %w", err)
		}
	}
	for id, a := range tokens {
		row := TokenRow{TokenID: id, LastAt: a.last, Stats: a.finish()}
		if info, ok := infos[id]; ok {
			row.Name, row.Prefix, row.Owner, row.Revoked = info.Name, info.Prefix, info.OwnerName, info.RevokedAt != nil
		}
		rep.Tokens = append(rep.Tokens, row)
	}
	sort.Slice(rep.Tokens, func(i, j int) bool {
		if rep.Tokens[i].Calls != rep.Tokens[j].Calls {
			return rep.Tokens[i].Calls > rep.Tokens[j].Calls
		}
		return rep.Tokens[i].TokenID < rep.Tokens[j].TokenID
	})
	return rep, nil
}

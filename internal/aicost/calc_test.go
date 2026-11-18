package aicost

import (
	"math/big"
	"math/rand"
	"testing"
)

func rat(n, d int64) *big.Rat { return big.NewRat(n, d) }

func lineOf(r calcResult, dept int64) calcLine {
	for _, l := range r.Lines {
		if l.Dept == dept {
			return l
		}
	}
	return calcLine{}
}

// AC-4903: 무작위 풀·대상·금액 1,000회에서 부서별 직접+공용 합이 집계 총액과 정확히 같고, 같은 입력은 같은 결과다.
func TestCalculateSumsMatchTotalProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(49))
	purposes := []string{"docverify.extract", "knowledge.answer", "eval.run", "mcp.select_tool"}
	providers := []string{"p1", "p2"}
	for it := 0; it < 1000; it++ {
		nd := 1 + rng.Intn(6)
		var rows []calcRow
		for i := 0; i < 1+rng.Intn(12); i++ {
			dept := int64(rng.Intn(nd + 1)) // 0 = 부서 미지정
			rows = append(rows, calcRow{Dept: dept, Purpose: purposes[rng.Intn(len(purposes))], Provider: providers[rng.Intn(2)],
				Amount: rat(rng.Int63n(5_000_000), 1+rng.Int63n(997))})
		}
		hc := map[int64]int64{}
		for d := 1; d <= nd; d++ {
			hc[int64(d)] = int64(rng.Intn(9))
		}
		pools := []PoolInput{
			{Name: "a", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, TargetsAll: true},
			{Name: "b", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true},
		}
		if rng.Intn(2) == 0 {
			var ts []PoolTarget
			left := 100
			for d := 1; d <= nd; d++ {
				w := left
				if d < nd {
					w = 1 + rng.Intn(left-(nd-d))
				}
				left -= w
				ts = append(ts, PoolTarget{DepartmentID: int64(d), WeightPct: w})
			}
			pools = append(pools, PoolInput{Name: "c", Selector: Selector{Purposes: []string{"mcp.select_tool"}}, Driver: DriverFixed, Targets: ts})
		}
		in := calcInput{Rows: rows, Pools: pools, Headcount: hc}
		r1, r2 := calculate(in), calculate(in)
		if len(r1.Lines) != len(r2.Lines) || r1.TotalKRW != r2.TotalKRW {
			t.Fatalf("반복 %d: 결정적이지 않음", it)
		}
		for i := range r1.Lines {
			if r1.Lines[i] != r2.Lines[i] {
				t.Fatalf("반복 %d: 결정적이지 않음", it)
			}
		}
		if len(r1.Blockers) > 0 {
			continue // 대상이 없거나 부서 미지정 금액이 풀에 안 들어간 경우는 확정이 막히는 입력이다
		}
		var sum int64
		for _, l := range r1.Lines {
			if l.DirectKRW+l.SharedKRW != l.TotalKRW {
				t.Fatalf("반복 %d: 부서 %d 직접+공용이 합과 다름: %+v", it, l.Dept, l)
			}
			sum += l.TotalKRW
		}
		if sum != r1.TotalKRW {
			t.Fatalf("반복 %d: 부서별 합 %d != 총액 %d (%+v)", it, sum, r1.TotalKRW, r1.Lines)
		}
	}
}

// 최대 잔여법: 100원을 3부서에 같은 비율로 나누면 34·33·33(동률은 부서 번호 순으로 1원씩).
func TestLargestRemainderTieBreak(t *testing.T) {
	r := calculate(calcInput{
		Rows:      []calcRow{{Dept: 0, Purpose: "x", Provider: "p", Amount: rat(100, 1)}},
		Pools:     []PoolInput{{Name: "미지정", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true}},
		Headcount: map[int64]int64{1: 5, 2: 5, 3: 5},
	})
	if len(r.Blockers) != 0 {
		t.Fatal(r.Blockers)
	}
	got := []int64{lineOf(r, 1).SharedKRW, lineOf(r, 2).SharedKRW, lineOf(r, 3).SharedKRW}
	if got[0] != 34 || got[1] != 33 || got[2] != 33 || r.TotalKRW != 100 {
		t.Fatalf("나머지 배분 %v 합 %d", got, r.TotalKRW)
	}
}

// 인원 비율·사용량 비율·고정 비율의 계산과 직접 비용의 귀속.
func TestDriversAndDirectCosts(t *testing.T) {
	rows := []calcRow{
		{Dept: 1, Purpose: "docverify.extract", Provider: "p", Amount: rat(6000, 1)}, // 직접
		{Dept: 2, Purpose: "docverify.extract", Provider: "p", Amount: rat(2000, 1)}, // 직접
		{Dept: 1, Purpose: "eval.run", Provider: "p", Amount: rat(1000, 1)},          // 평가 풀(사용량 비율: 6000:2000)
		{Dept: 0, Purpose: "mcp.select_tool", Provider: "p", Amount: rat(900, 1)},    // 부서 미지정 풀(인원 비율 1:2)
		{Dept: 3, Purpose: "knowledge.answer", Provider: "p", Amount: rat(500, 1)},   // 고정 풀
	}
	pools := []PoolInput{
		{Name: "평가", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, Targets: []PoolTarget{{DepartmentID: 1}, {DepartmentID: 2}}},
		{Name: "미지정", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true},
		{Name: "지식", Selector: Selector{Purposes: []string{"knowledge.answer"}}, Driver: DriverFixed, Targets: []PoolTarget{{DepartmentID: 1, WeightPct: 40}, {DepartmentID: 2, WeightPct: 60}}},
	}
	r := calculate(calcInput{Rows: rows, Pools: pools, Headcount: map[int64]int64{1: 1, 2: 2}})
	if len(r.Blockers) != 0 {
		t.Fatal(r.Blockers)
	}
	d1, d2 := lineOf(r, 1), lineOf(r, 2)
	// 직접: 부서1 6000, 부서2 2000. 공용: 평가 1000 → 750/250, 미지정 900 → 300/600, 지식 500(부서3 호출) → 200/300.
	if d1.DirectKRW != 6000 || d2.DirectKRW != 2000 || d1.SharedKRW != 750+300+200 || d2.SharedKRW != 250+600+300 {
		t.Fatalf("부서1 %+v 부서2 %+v", d1, d2)
	}
	if r.TotalKRW != 10400 || d1.TotalKRW+d2.TotalKRW != 10400 {
		t.Fatalf("합계 %d", r.TotalKRW)
	}
	if lineOf(r, 3).Dept != 0 { // 부서 3 의 호출은 풀로 들어갔으므로 직접 줄이 없다
		t.Fatalf("부서 3 줄이 있다: %+v", lineOf(r, 3))
	}
	if len(r.Pools) != 3 || r.Pools[0].FallbackUsed {
		t.Fatalf("풀 결과 %+v", r.Pools)
	}
}

func TestUsageDriverFallsBackToHeadcountAndBlockers(t *testing.T) {
	// 사용량 기준인데 대상의 직접 비용이 모두 0 이면 인원 비율로 대체하고 표시한다.
	r := calculate(calcInput{
		Rows:      []calcRow{{Dept: 1, Purpose: "eval.run", Provider: "p", Amount: rat(300, 1)}},
		Pools:     []PoolInput{{Name: "평가", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, TargetsAll: true}},
		Headcount: map[int64]int64{1: 1, 2: 2},
	})
	if len(r.Blockers) != 0 || len(r.Pools) != 1 || !r.Pools[0].FallbackUsed || lineOf(r, 1).SharedKRW != 100 || lineOf(r, 2).SharedKRW != 200 || len(r.Warnings) == 0 {
		t.Fatalf("대체 기준: %+v", r)
	}
	// 부서가 없는 호출을 받을 풀이 없으면 확정을 막는다.
	r = calculate(calcInput{Rows: []calcRow{{Dept: 0, Purpose: "x", Provider: "p", Amount: rat(10, 1)}}, Headcount: map[int64]int64{1: 1}})
	if len(r.Blockers) != 1 || r.UnassignedKR != 10 {
		t.Fatalf("미귀속: %+v", r)
	}
	// 대상의 월말 인원이 모두 0 이면 막는다.
	r = calculate(calcInput{
		Rows:      []calcRow{{Dept: 0, Purpose: "x", Provider: "p", Amount: rat(10, 1)}},
		Pools:     []PoolInput{{Name: "미지정", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true}},
		Headcount: map[int64]int64{1: 0},
	})
	if len(r.Blockers) == 0 {
		t.Fatal("대상 없음을 막아야 한다")
	}
	// 다른 법인의 고정 비율 대상은 빠지고 남은 대상 비율로 나눈다.
	r = calculate(calcInput{
		Rows:      []calcRow{{Dept: 0, Purpose: "x", Provider: "p", Amount: rat(100, 1)}},
		Pools:     []PoolInput{{Name: "고정", Selector: Selector{UnassignedDept: true}, Driver: DriverFixed, Targets: []PoolTarget{{DepartmentID: 1, WeightPct: 30}, {DepartmentID: 9, WeightPct: 70}}}},
		Headcount: map[int64]int64{},
		InEntity:  func(d int64) bool { return d != 9 },
	})
	if len(r.Blockers) != 0 || lineOf(r, 1).SharedKRW != 100 {
		t.Fatalf("법인 밖 대상: %+v", r)
	}
}

func TestMoneyHelpers(t *testing.T) {
	if thousandsKRW(1234567) != "1,234,567" || thousandsKRW(-1000) != "-1,000" || thousandsKRW(0) != "0" {
		t.Fatal("천 단위")
	}
	if ratRound(rat(1, 2)) != 1 || ratRound(rat(49, 100)) != 0 || ratRound(rat(250, 1)) != 250 {
		t.Fatal("반올림")
	}
}

// B49 판정 후속 1: 풀별 표시 금액의 합이 부서 공용 합계(SharedKRW)와 같다. 풀마다 따로 반올림하면 합이 1원 어긋난다(0.6 + 0.6 → 1 + 1 ≠ 1).
func TestPoolAmountsSumToSharedKRW(t *testing.T) {
	r := calculate(calcInput{
		Rows: []calcRow{
			{Dept: 1, Purpose: "docverify.extract", Provider: "p", Amount: rat(201, 2)}, // 직접 100.5
			{Dept: 0, Purpose: "eval.run", Provider: "p", Amount: rat(3, 5)},            // 풀 A 0.6
			{Dept: 0, Purpose: "mcp.select_tool", Provider: "p", Amount: rat(3, 5)},     // 풀 B 0.6
		},
		Pools: []PoolInput{
			{Name: "A", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverHeadcount, TargetsAll: true},
			{Name: "B", Selector: Selector{Purposes: []string{"mcp.select_tool"}}, Driver: DriverHeadcount, TargetsAll: true},
		},
		Headcount: map[int64]int64{1: 1},
	})
	if len(r.Blockers) != 0 {
		t.Fatal(r.Blockers)
	}
	var poolSum int64
	for _, p := range r.Pools {
		poolSum += p.AmountKRW
		var shareSum int64
		for _, s := range p.Shares {
			shareSum += s.ShareKRW
		}
		if shareSum != p.AmountKRW {
			t.Fatalf("풀 %s: 부서 몫 합 %d != 풀 금액 %d", p.Name, shareSum, p.AmountKRW)
		}
	}
	if r.SharedKRW != 1 || poolSum != r.SharedKRW || r.TotalKRW != 102 || r.DirectKRW != 101 {
		t.Fatalf("공용 %d 풀 합 %d 총 %d 직접 %d", r.SharedKRW, poolSum, r.TotalKRW, r.DirectKRW)
	}
}

// 속성: 무작위 입력에서 막는 문제가 없으면 풀별 표시 금액의 합 == 공용 합계, 풀 안 부서 몫의 합 == 그 풀 금액.
func TestPoolAmountsSumToSharedKRWProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(4905))
	purposes := []string{"eval.run", "mcp.select_tool", "knowledge.answer", "docverify.extract"}
	for it := 0; it < 1000; it++ {
		nd := 1 + rng.Intn(5)
		var rows []calcRow
		for i := 0; i < 1+rng.Intn(12); i++ {
			rows = append(rows, calcRow{Dept: int64(rng.Intn(nd + 1)), Purpose: purposes[rng.Intn(len(purposes))], Provider: "p", Amount: rat(rng.Int63n(5_000_000), 1+rng.Int63n(997))})
		}
		hc := map[int64]int64{}
		for d := 1; d <= nd; d++ {
			hc[int64(d)] = int64(rng.Intn(9))
		}
		pools := []PoolInput{
			{Name: "a", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, TargetsAll: true},
			{Name: "b", Selector: Selector{Purposes: []string{"mcp.select_tool"}}, Driver: DriverHeadcount, TargetsAll: true},
			{Name: "c", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true},
		}
		res := calculate(calcInput{Rows: rows, Pools: pools, Headcount: hc})
		if len(res.Blockers) > 0 {
			continue
		}
		var poolSum int64
		for _, p := range res.Pools {
			poolSum += p.AmountKRW
			var s int64
			for _, sh := range p.Shares {
				s += sh.ShareKRW
			}
			if s != p.AmountKRW {
				t.Fatalf("반복 %d 풀 %s: 부서 몫 합 %d != 풀 금액 %d", it, p.Name, s, p.AmountKRW)
			}
		}
		if poolSum != res.SharedKRW {
			t.Fatalf("반복 %d: 풀 금액 합 %d != 공용 합계 %d", it, poolSum, res.SharedKRW)
		}
	}
}

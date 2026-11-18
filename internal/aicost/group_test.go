package aicost

import (
	"math/big"
	"math/rand"
	"testing"
)

func gRow(entity, dept int64, purpose string, amount int64) groupRow {
	return groupRow{Entity: entity, calcRow: calcRow{Dept: dept, Purpose: purpose, Provider: "p", Amount: rat(amount, 1)}}
}

var evalGroup = PoolInput{Name: "평가(법인 간)", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverHeadcount, Scope: ScopeGroup, TargetsAll: true}

// 그룹 풀: 설치 전체 금액을 법인의 월말 인원 비율로 나눈다. 법인 몫의 합은 풀 금액과 같다(법인마다 따로 반올림하지 않는다).
func TestSplitGroupPoolsHeadcountAndLargestRemainder(t *testing.T) {
	res := splitGroupPools(groupInput{
		Rows:     []groupRow{gRow(1, 10, "eval.run", 500), gRow(2, 20, "eval.run", 500), gRow(3, 30, "x", 77)},
		Pools:    []PoolInput{evalGroup},
		Entities: []int64{1, 2, 3},
		EntityHC: map[int64]int64{1: 1, 2: 1, 3: 1},
	})
	if len(res.Blockers) != 0 || res.Totals[0] != 1000 {
		t.Fatalf("%+v", res)
	}
	sh := res.Shares[0]
	if sh[1]+sh[2]+sh[3] != 1000 || sh[1] != 334 || sh[2] != 333 || sh[3] != 333 {
		t.Fatalf("몫 %v (동률은 법인 번호 순으로 1원씩)", sh)
	}
}

func TestSplitGroupPoolsUsageFixedAndBlockers(t *testing.T) {
	// 사용량 기준: 법인의 직접 비용(풀에 안 들어간 부서 있는 호출) 비율.
	res := splitGroupPools(groupInput{
		Rows:     []groupRow{gRow(1, 10, "docverify.extract", 3000), gRow(2, 20, "docverify.extract", 1000), gRow(1, 0, "eval.run", 400)},
		Pools:    []PoolInput{{Name: "g", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, Scope: ScopeGroup, TargetsAll: true}},
		Entities: []int64{1, 2},
		EntityHC: map[int64]int64{1: 5, 2: 5},
	})
	if res.Shares[0][1] != 300 || res.Shares[0][2] != 100 {
		t.Fatalf("사용량 몫 %v", res.Shares[0])
	}
	// 직접 비용이 모두 0 이면 인원 비율로 대체하고 경고한다.
	res = splitGroupPools(groupInput{
		Rows:     []groupRow{gRow(1, 0, "eval.run", 400)},
		Pools:    []PoolInput{{Name: "g", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverUsage, Scope: ScopeGroup, TargetsAll: true}},
		Entities: []int64{1, 2},
		EntityHC: map[int64]int64{1: 1, 2: 3},
	})
	if res.Shares[0][1] != 100 || res.Shares[0][2] != 300 || len(res.Warnings) != 1 {
		t.Fatalf("대체 기준 %+v", res)
	}
	// 고정 비율: 대상의 번호가 법인 번호다.
	res = splitGroupPools(groupInput{
		Rows:     []groupRow{gRow(1, 0, "eval.run", 1000)},
		Pools:    []PoolInput{{Name: "g", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverFixed, Scope: ScopeGroup, Targets: []PoolTarget{{DepartmentID: 1, WeightPct: 25}, {DepartmentID: 2, WeightPct: 75}}}},
		Entities: []int64{1, 2},
	})
	if res.Shares[0][1] != 250 || res.Shares[0][2] != 750 {
		t.Fatalf("고정 몫 %v", res.Shares[0])
	}
	// 나눌 법인이 없으면 막는다(인원 0).
	res = splitGroupPools(groupInput{Rows: []groupRow{gRow(1, 0, "eval.run", 10)}, Pools: []PoolInput{evalGroup}, Entities: []int64{1, 2}, EntityHC: map[int64]int64{}})
	if len(res.Blockers) != 1 {
		t.Fatalf("막는 문제 %+v", res)
	}
	// 법인 안 풀이 먼저 가져간 호출은 그룹 풀에 들어가지 않는다(앞 풀이 이긴다).
	res = splitGroupPools(groupInput{
		Rows:     []groupRow{gRow(1, 10, "eval.run", 900)},
		Pools:    []PoolInput{{Name: "local", Selector: Selector{Purposes: []string{"eval.run"}}, Driver: DriverHeadcount, TargetsAll: true}, evalGroup},
		Entities: []int64{1, 2}, EntityHC: map[int64]int64{1: 1, 2: 1},
	})
	if len(res.Shares) != 0 {
		t.Fatalf("법인 안 풀이 이겨야 한다: %+v", res.Shares)
	}
}

// 법인 계산: 그룹 풀에 들어가는 호출은 법인 계산에서 빠지고, 그 법인의 몫이 부서에 인원 비율로 나뉜다. 부서별 합이 총액과 같다.
func TestCalculateWithInjectedGroupShare(t *testing.T) {
	r := calculate(calcInput{
		Rows:      []calcRow{{Dept: 1, Purpose: "docverify.extract", Provider: "p", Amount: rat(1000, 1)}, {Dept: 1, Purpose: "eval.run", Provider: "p", Amount: rat(9999, 1)}},
		Pools:     []PoolInput{evalGroup},
		Headcount: map[int64]int64{1: 1, 2: 3},
		Injected:  []injectedPool{{Seq: 1, Pool: PoolInput{Name: "평가(법인 간)", Driver: DriverHeadcount, TargetsAll: true}, Amount: rat(400, 1)}},
	})
	if len(r.Blockers) != 0 {
		t.Fatal(r.Blockers)
	}
	d1, d2 := lineOf(r, 1), lineOf(r, 2)
	// 직접 1000(부서 1), 몫 400 → 부서 1: 100, 부서 2: 300. 그룹 풀에 들어간 9,999 원은 이 계산에 없다.
	if d1.DirectKRW != 1000 || d1.SharedKRW != 100 || d2.SharedKRW != 300 || r.TotalKRW != 1400 || d1.TotalKRW+d2.TotalKRW != 1400 {
		t.Fatalf("%+v %+v 총 %d", d1, d2, r.TotalKRW)
	}
	if len(r.Pools) != 1 || !r.Pools[0].Group || r.Pools[0].AmountKRW != 400 || r.Pools[0].Seq != 1 {
		t.Fatalf("풀 %+v", r.Pools)
	}
}

// 속성: 무작위 법인·풀·금액에서 그룹 풀 법인 몫의 합이 풀 금액과 같고, 법인 계산마다 부서별 합이 그 법인 총액과 같다. 같은 입력은 같은 결과다.
func TestGroupSplitConservationProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(4904))
	purposes := []string{"eval.run", "docverify.extract", "knowledge.answer"}
	for it := 0; it < 500; it++ {
		ne := 2 + rng.Intn(3)
		var ents []int64
		hc := map[int64]int64{}
		for e := 1; e <= ne; e++ {
			ents = append(ents, int64(e))
			hc[int64(e)] = int64(rng.Intn(9))
		}
		var rows []groupRow
		total := new(big.Rat)
		groupTotal := new(big.Rat)
		for i := 0; i < 1+rng.Intn(14); i++ {
			e := int64(1 + rng.Intn(ne))
			p := purposes[rng.Intn(len(purposes))]
			amt := rat(rng.Int63n(5_000_000), 1+rng.Int63n(997))
			dept := int64(rng.Intn(4)) + e*10
			if rng.Intn(5) == 0 {
				dept = 0
			}
			rows = append(rows, gRow(e, dept, p, 0))
			rows[len(rows)-1].Amount = amt
			total.Add(total, amt)
			if p == "eval.run" {
				groupTotal.Add(groupTotal, amt)
			}
		}
		drivers := []string{DriverHeadcount, DriverUsage}
		pool := evalGroup
		pool.Driver = drivers[rng.Intn(2)]
		in := groupInput{Rows: rows, Pools: []PoolInput{pool}, Entities: ents, EntityHC: hc}
		a, b := splitGroupPools(in), splitGroupPools(in)
		if len(a.Blockers) > 0 {
			continue
		}
		sum := int64(0)
		for _, v := range a.Shares[0] {
			sum += v
		}
		if groupTotal.Sign() > 0 && sum != ratRound(groupTotal) {
			t.Fatalf("반복 %d: 법인 몫 합 %d != 풀 금액 %d", it, sum, ratRound(groupTotal))
		}
		for e, v := range a.Shares[0] {
			if b.Shares[0][e] != v {
				t.Fatalf("반복 %d: 결정적이지 않음", it)
			}
		}
		// 각 법인의 계산: 법인 행 + 몫.
		for _, e := range ents {
			var crows []calcRow
			for _, r := range rows {
				if r.Entity == e {
					crows = append(crows, r.calcRow)
				}
			}
			var inj []injectedPool
			if sh := a.Shares[0][e]; sh > 0 {
				inj = append(inj, injectedPool{Seq: 1, Pool: PoolInput{Name: "g", Driver: DriverHeadcount, TargetsAll: true}, Amount: rat(sh, 1)})
			}
			ehc := map[int64]int64{}
			for d := int64(0); d < 4; d++ {
				ehc[d+e*10] = int64(rng.Intn(5))
			}
			res := calculate(calcInput{Rows: crows, Pools: []PoolInput{pool, {Name: "미지정", Selector: Selector{UnassignedDept: true}, Driver: DriverHeadcount, TargetsAll: true}}, Headcount: ehc, Injected: inj})
			if len(res.Blockers) > 0 {
				continue
			}
			var s int64
			for _, l := range res.Lines {
				s += l.TotalKRW
			}
			if s != res.TotalKRW {
				t.Fatalf("반복 %d 법인 %d: 부서 합 %d != 총액 %d", it, e, s, res.TotalKRW)
			}
		}
	}
}

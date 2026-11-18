package aicost

import (
	"fmt"
	"math/big"
	"sort"
)

// 배부 계산기(명세 7절): 순수 함수다(DB 를 읽지 않는다). 금액은 정확한 분수(원)로 나누고, 마지막에 최대 잔여법으로 원 단위 정수로 바꾼다.
// 부서별 직접+공용 합이 법인 총액과 언제나 같다. 같은 입력이면 결과가 같다(정렬은 부서 번호 순).

// calcRow 는 법인 한 곳의 사용 집계 한 줄이다. Amount 는 원 단위 정확한 분수다(환율·청구액 보정이 이미 반영됨).
type calcRow struct {
	Dept     int64
	Purpose  string
	Provider string
	Amount   *big.Rat
}

// calcInput 은 계산 입력이다. Headcount 는 월말 기준 부서별 인원이고, InEntity 는 부서가 이 법인에 속하는지다(고정 비율 대상 거름).
type calcInput struct {
	Rows      []calcRow
	Pools     []PoolInput
	Headcount map[int64]int64
	InEntity  func(dept int64) bool
	// Injected 는 법인 간 공용 풀(그룹 풀)이 이 법인에 나눈 몫이다(설치 전체에서 법인 사이에 먼저 나눈 정수 금액). 법인 안에서 부서에 인원·사용량 비율로 다시 나눈다.
	// 그룹 풀에 들어가는 호출은 법인 계산에서 빠진다(설치 전체 단위로 가져가므로).
	Injected []injectedPool
}

// injectedPool 은 그룹 풀의 이 법인 몫이다. Pool 은 법인 안 배분 기준(인원·사용량)과 이름만 쓴다.
type injectedPool struct {
	Seq    int
	Pool   PoolInput
	Amount *big.Rat
}

// PoolShare 는 풀이 대상 한 부서에 나눈 몫이다.
type PoolShare struct {
	DepartmentID int64  `json:"departmentId"`
	Name         string `json:"name"`
	Weight       string `json:"weight"` // 인원 수·직접 비용(원)·퍼센트를 글자로
	ShareKRW     int64  `json:"shareKrw"`
}

// PoolResult 는 풀 한 개의 계산 결과다.
type PoolResult struct {
	Seq          int         `json:"seq"`
	Name         string      `json:"name"`
	Driver       string      `json:"driver"`
	AmountKRW    int64       `json:"amountKrw"`
	Group        bool        `json:"group,omitempty"`        // 법인 간 공용(그룹) 풀의 이 법인 몫
	FallbackUsed bool        `json:"fallbackUsed,omitempty"` // 사용량 기준 풀의 대상 직접 비용이 모두 0 이라 인원 비율로 대체함
	Shares       []PoolShare `json:"shares"`
}

// calcLine 은 부서별 결과다.
type calcLine struct {
	Dept      int64
	DirectKRW int64
	SharedKRW int64
	TotalKRW  int64
}

// calcResult 는 계산 결과다. Blockers 가 있으면 확정할 수 없다.
type calcResult struct {
	Lines        []calcLine
	Pools        []PoolResult
	TotalKRW     int64
	DirectKRW    int64
	SharedKRW    int64
	UnassignedKR int64    // 어느 풀에도 안 들어간 부서 미지정 금액
	Blockers     []string // 확정을 막는 문제
	Warnings     []string
}

func ratFloor(r *big.Rat) *big.Int {
	q := new(big.Int).Quo(r.Num(), r.Denom()) // 양수 분수: 몫은 내림
	return q
}

// ratRound 는 반올림(0.5 는 올림)한 정수다.
func ratRound(r *big.Rat) int64 {
	half := big.NewRat(1, 2)
	return ratFloor(new(big.Rat).Add(r, half)).Int64()
}

// lrRound 는 정확한 분수들을 합이 target 이 되도록 정수로 바꾼다(최대 잔여법, 소수부가 큰 순서, 동률은 키 순).
func lrRound(exact map[int64]*big.Rat, target int64) (map[int64]int64, error) {
	keys := make([]int64, 0, len(exact))
	for k := range exact {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := map[int64]int64{}
	type fr struct {
		k int64
		f *big.Rat
	}
	var fracs []fr
	var sum int64
	for _, k := range keys {
		fl := ratFloor(exact[k])
		out[k] = fl.Int64()
		sum += out[k]
		fracs = append(fracs, fr{k, new(big.Rat).Sub(exact[k], new(big.Rat).SetInt(fl))})
	}
	rem := target - sum
	if rem < 0 || rem > int64(len(keys)) {
		return nil, fmt.Errorf("반올림 잔여 %d 를 %d 곳에 나눌 수 없습니다", rem, len(keys))
	}
	sort.SliceStable(fracs, func(i, j int) bool {
		if c := fracs[i].f.Cmp(fracs[j].f); c != 0 {
			return c > 0
		}
		return fracs[i].k < fracs[j].k
	})
	for i := int64(0); i < rem; i++ {
		out[fracs[i].k]++
	}
	return out, nil
}

func sumRats(m map[int64]*big.Rat) *big.Rat {
	t := new(big.Rat)
	for _, v := range m {
		t.Add(t, v)
	}
	return t
}

// calculate 는 규칙을 적용한다.
func calculate(in calcInput) calcResult {
	res := calcResult{Lines: []calcLine{}, Pools: []PoolResult{}, Blockers: []string{}, Warnings: []string{}}
	poolAmt := make([]*big.Rat, len(in.Pools))
	for i := range poolAmt {
		poolAmt[i] = new(big.Rat)
	}
	direct := map[int64]*big.Rat{}
	unassigned := new(big.Rat)
	total := new(big.Rat)
	for _, r := range in.Rows {
		matched := -1
		for i, p := range in.Pools {
			if p.Selector.matches(r.Dept, r.Purpose, r.Provider) {
				matched = i
				break
			}
		}
		if matched >= 0 && in.Pools[matched].Scope == ScopeGroup {
			continue // 그룹 풀이 설치 전체 단위로 가져간다(이 법인 몫은 Injected 로 온다)
		}
		total.Add(total, r.Amount)
		if matched >= 0 {
			poolAmt[matched].Add(poolAmt[matched], r.Amount)
			continue
		}
		if r.Dept == 0 {
			unassigned.Add(unassigned, r.Amount)
			continue
		}
		if direct[r.Dept] == nil {
			direct[r.Dept] = new(big.Rat)
		}
		direct[r.Dept].Add(direct[r.Dept], r.Amount)
	}
	if unassigned.Sign() > 0 {
		res.UnassignedKR = ratRound(unassigned)
		res.Blockers = append(res.Blockers, fmt.Sprintf("부서가 없는 호출 금액 %s원이 어느 풀에도 들어가지 않았습니다. 부서 미지정 풀이 필요합니다.", thousandsKRW(res.UnassignedKR)))
	}
	// 그룹 풀의 이 법인 몫을 풀로 더한다.
	allPools := append([]PoolInput{}, in.Pools...)
	seqOf := make([]int, 0, len(in.Pools)+len(in.Injected))
	for i := range in.Pools {
		seqOf = append(seqOf, i+1)
	}
	isGroup := make([]bool, len(in.Pools), len(in.Pools)+len(in.Injected))
	for _, ij := range in.Injected {
		if ij.Amount.Sign() <= 0 {
			continue
		}
		poolAmt = append(poolAmt, new(big.Rat).Set(ij.Amount))
		allPools = append(allPools, ij.Pool)
		seqOf = append(seqOf, ij.Seq)
		isGroup = append(isGroup, true)
		total.Add(total, ij.Amount)
	}
	// 풀 대상과 몫.
	shared := map[int64]*big.Rat{}
	eligible := func() []int64 { // 전체 부서: 그 달 인원이나 직접 비용이 있는 부서
		set := map[int64]bool{}
		for d, c := range in.Headcount {
			if c > 0 {
				set[d] = true
			}
		}
		for d, v := range direct {
			if v.Sign() > 0 {
				set[d] = true
			}
		}
		out := make([]int64, 0, len(set))
		for d := range set {
			out = append(out, d)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	type poolCalc struct {
		pr     PoolResult
		exact  *big.Rat // 풀의 정확한 금액(풀별 표시 금액을 부서 공용 합계에 맞출 때 쓴다)
		shares map[int64]*big.Rat
		ws     map[int64]string
	}
	var pcs []poolCalc
	for i, p := range allPools {
		if poolAmt[i].Sign() == 0 {
			continue
		}
		var targets []int64
		fixed := map[int64]int64{}
		if p.TargetsAll {
			targets = eligible()
		} else {
			for _, t := range p.Targets {
				if in.InEntity != nil && !in.InEntity(t.DepartmentID) {
					continue
				}
				targets = append(targets, t.DepartmentID)
				fixed[t.DepartmentID] = int64(t.WeightPct)
			}
			sort.Slice(targets, func(a, b int) bool { return targets[a] < targets[b] })
		}
		pc := poolCalc{pr: PoolResult{Seq: seqOf[i], Name: p.Name, Driver: p.Driver, Group: isGroup[i], AmountKRW: ratRound(poolAmt[i]), Shares: []PoolShare{}}, exact: poolAmt[i], shares: map[int64]*big.Rat{}, ws: map[int64]string{}}
		weights := map[int64]*big.Rat{}
		driver := p.Driver
		build := func(d string) *big.Rat {
			t := new(big.Rat)
			for _, dep := range targets {
				var w *big.Rat
				switch d {
				case DriverHeadcount:
					w = new(big.Rat).SetInt64(in.Headcount[dep])
					pc.ws[dep] = fmt.Sprintf("%d명", in.Headcount[dep])
				case DriverUsage:
					w = new(big.Rat)
					if v := direct[dep]; v != nil {
						w = new(big.Rat).Set(v)
					}
					pc.ws[dep] = thousandsKRW(ratRound(w)) + "원"
				default:
					w = new(big.Rat).SetInt64(fixed[dep])
					pc.ws[dep] = fmt.Sprintf("%d%%", fixed[dep])
				}
				weights[dep] = w
				t.Add(t, w)
			}
			return t
		}
		sumW := build(driver)
		if driver == DriverUsage && sumW.Sign() == 0 {
			pc.pr.FallbackUsed = true
			res.Warnings = append(res.Warnings, fmt.Sprintf("풀 '%s': 대상 부서의 직접 비용이 모두 0 이라 인원 비율로 나눴습니다.", p.Name))
			weights = map[int64]*big.Rat{}
			sumW = build(DriverHeadcount)
		}
		if sumW.Sign() == 0 {
			res.Blockers = append(res.Blockers, fmt.Sprintf("풀 '%s': 나눌 대상 부서가 없습니다(대상 부서의 월말 인원이나 직접 비용이 0 입니다).", p.Name))
			continue
		}
		for _, dep := range targets {
			share := new(big.Rat).Mul(poolAmt[i], weights[dep])
			share.Quo(share, sumW)
			pc.shares[dep] = share
			if shared[dep] == nil {
				shared[dep] = new(big.Rat)
			}
			shared[dep].Add(shared[dep], share)
		}
		pcs = append(pcs, pc)
	}
	// 정수로: 직접 → 공용 순서로 최대 잔여법. 합이 총액과 어긋나면 확정할 수 없다.
	depSet := map[int64]bool{}
	for d := range direct {
		depSet[d] = true
	}
	for d := range shared {
		depSet[d] = true
	}
	deps := make([]int64, 0, len(depSet))
	for d := range depSet {
		deps = append(deps, d)
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i] < deps[j] })
	dEx, sEx := map[int64]*big.Rat{}, map[int64]*big.Rat{}
	allocated := new(big.Rat)
	for _, d := range deps {
		dEx[d], sEx[d] = new(big.Rat), new(big.Rat)
		if v := direct[d]; v != nil {
			dEx[d].Set(v)
		}
		if v := shared[d]; v != nil {
			sEx[d].Set(v)
		}
		allocated.Add(allocated, dEx[d])
		allocated.Add(allocated, sEx[d])
	}
	res.TotalKRW = ratRound(total)
	allocTotal := ratRound(allocated)
	dR, err1 := lrRound(dEx, ratRound(sumRats(dEx)))
	var dSum int64
	for _, v := range dR {
		dSum += v
	}
	sR, err2 := lrRound(sEx, allocTotal-dSum)
	if err1 != nil || err2 != nil {
		res.Blockers = append(res.Blockers, "배부 합계가 집계 총액과 다릅니다. 확정하지 않았습니다.")
		return res
	}
	var tSum int64
	for _, d := range deps {
		l := calcLine{Dept: d, DirectKRW: dR[d], SharedKRW: sR[d], TotalKRW: dR[d] + sR[d]}
		res.Lines = append(res.Lines, l)
		res.DirectKRW += l.DirectKRW
		res.SharedKRW += l.SharedKRW
		tSum += l.TotalKRW
	}
	if len(res.Blockers) == 0 && tSum != res.TotalKRW {
		res.Blockers = append(res.Blockers, "배부 합계가 집계 총액과 다릅니다. 확정하지 않았습니다.")
	}
	// 풀별 표시 금액도 최대 잔여법으로 부서 공용 합계(SharedKRW)에 맞춘다: 풀마다 따로 반올림하면 합이 공용 합계와 1원 어긋날 수 있다.
	if len(pcs) > 0 {
		exact := make(map[int64]*big.Rat, len(pcs))
		for i, pc := range pcs {
			exact[int64(i)] = pc.exact
		}
		if got, err := lrRound(exact, res.SharedKRW); err == nil {
			for i := range pcs {
				pcs[i].pr.AmountKRW = got[int64(i)]
			}
		}
	}
	// 풀별 몫(화면용): 풀 안에서도 최대 잔여법으로 풀 금액에 맞춘다.
	for _, pc := range pcs {
		if r, err := lrRound(pc.shares, pc.pr.AmountKRW); err == nil {
			keys := make([]int64, 0, len(r))
			for k := range r {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
			for _, k := range keys {
				pc.pr.Shares = append(pc.pr.Shares, PoolShare{DepartmentID: k, Weight: pc.ws[k], ShareKRW: r[k]})
			}
		}
		res.Pools = append(res.Pools, pc.pr)
	}
	return res
}

func thousandsKRW(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// ---- 법인 간 공용(그룹) 풀: 설치 전체 금액을 법인 사이에 나눈다(명세 10절, D5) ----

// groupRow 는 어느 법인의 사용 집계 한 줄이다(그룹 풀은 모든 법인의 행을 본다).
type groupRow struct {
	Entity int64
	calcRow
}

// groupInput 은 법인 사이 배분의 입력이다. EntityHC 는 월말 법인별 전체 인원, Entities 는 활성 법인 번호(정렬)다.
type groupInput struct {
	Rows     []groupRow
	Pools    []PoolInput
	Entities []int64
	EntityHC map[int64]int64
}

// groupResult 는 그룹 풀마다 법인별 정수 몫이다. Shares[풀 번호(0부터)][법인] = 원.
type groupResult struct {
	Shares   map[int]map[int64]int64
	Totals   map[int]int64 // 풀 전체 금액(원, 반올림)
	Blockers []string
	Warnings []string
}

// splitGroupPools 는 순수 함수다. 기준: headcount 는 법인의 월말 전체 인원, usage 는 법인의 직접 비용(어느 풀에도 안 들어간 부서 있는 호출), fixed 는 풀이 지정한 법인 비율
// (대상의 DepartmentID 칸이 법인 번호다). 법인별 몫은 최대 잔여법으로 풀 금액에 맞춘다(법인마다 따로 반올림하지 않으므로 법인 합이 설치 합과 같다).
func splitGroupPools(in groupInput) groupResult {
	res := groupResult{Shares: map[int]map[int64]int64{}, Totals: map[int]int64{}, Blockers: []string{}, Warnings: []string{}}
	amt := map[int]*big.Rat{}
	directBy := map[int64]*big.Rat{}
	for _, r := range in.Rows {
		matched := -1
		for i, p := range in.Pools {
			if p.Selector.matches(r.Dept, r.Purpose, r.Provider) {
				matched = i
				break
			}
		}
		switch {
		case matched >= 0 && in.Pools[matched].Scope == ScopeGroup:
			if amt[matched] == nil {
				amt[matched] = new(big.Rat)
			}
			amt[matched].Add(amt[matched], r.Amount)
		case matched < 0 && r.Dept != 0:
			if directBy[r.Entity] == nil {
				directBy[r.Entity] = new(big.Rat)
			}
			directBy[r.Entity].Add(directBy[r.Entity], r.Amount)
		}
	}
	for i, p := range in.Pools {
		g := amt[i]
		if p.Scope != ScopeGroup || g == nil || g.Sign() == 0 {
			continue
		}
		w := map[int64]*big.Rat{}
		sum := new(big.Rat)
		fill := func(driver string) {
			w = map[int64]*big.Rat{}
			sum = new(big.Rat)
			for _, e := range in.Entities {
				var v *big.Rat
				switch driver {
				case DriverHeadcount:
					v = new(big.Rat).SetInt64(in.EntityHC[e])
				case DriverUsage:
					v = new(big.Rat)
					if d := directBy[e]; d != nil {
						v = new(big.Rat).Set(d)
					}
				default:
					v = new(big.Rat)
					for _, t := range p.Targets {
						if t.DepartmentID == e {
							v = new(big.Rat).SetInt64(int64(t.WeightPct))
						}
					}
				}
				w[e] = v
				sum.Add(sum, v)
			}
		}
		fill(p.Driver)
		if p.Driver == DriverUsage && sum.Sign() == 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("풀 '%s'(법인 간): 법인들의 직접 비용이 모두 0 이라 인원 비율로 나눴습니다.", p.Name))
			fill(DriverHeadcount)
		}
		if sum.Sign() == 0 {
			res.Blockers = append(res.Blockers, fmt.Sprintf("풀 '%s'(법인 간): 나눌 법인이 없습니다(법인의 월말 인원이나 직접 비용, 고정 비율 대상이 0 입니다).", p.Name))
			continue
		}
		exact := map[int64]*big.Rat{}
		for _, e := range in.Entities {
			exact[e] = new(big.Rat).Quo(new(big.Rat).Mul(g, w[e]), sum)
		}
		target := ratRound(g)
		got, err := lrRound(exact, target)
		if err != nil {
			res.Blockers = append(res.Blockers, fmt.Sprintf("풀 '%s'(법인 간): 법인별 몫을 풀 금액에 맞출 수 없습니다.", p.Name))
			continue
		}
		res.Shares[i] = got
		res.Totals[i] = target
	}
	return res
}

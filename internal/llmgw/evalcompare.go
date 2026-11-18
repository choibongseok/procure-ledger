package llmgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// SuiteHash 는 케이스 모음(묶음·ID·치명 여부)의 해시다. 같은 골든셋인지 가릴 때 쓴다(결과 통과 여부는 넣지 않는다).
func SuiteHash(cases []EvalCase) string {
	keys := make([]string, 0, len(cases))
	for _, c := range cases {
		k := c.Suite + "/" + c.ID
		if c.Critical {
			k += "!"
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CompareRun 은 비교 표의 한 열(공급자·모델별 최신 실행)이다.
type CompareRun struct {
	ID               int64  `json:"id"`
	At               string `json:"at"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	PassRatePct      int    `json:"passRatePct"`
	CriticalFailures int    `json:"criticalFailures"`
	Regressions      int    `json:"regressions"`         // 기준 실행에서 통과하던 케이스가 이 실행에서 실패한 수
	CriticalRegress  int    `json:"criticalRegressions"` // 그 가운데 치명 케이스 수
	LatencyMS        int64  `json:"latencyMs"`
	CostMicroUSD     int64  `json:"costMicroUsd"`
	Reference        bool   `json:"reference"`
}

// CompareCase 는 비교 표의 한 행(케이스)이다. Results 는 Runs 와 같은 순서이고 값은 pass | fail | "" (그 실행에 없음)이다.
type CompareCase struct {
	Key      string   `json:"key"` // 묶음/ID
	Critical bool     `json:"critical"`
	Results  []string `json:"results"`
}

// Comparison 은 공급자별 골든셋 비교 표다(B45 명세 11절). 같은 suite_hash 인 실행끼리만 놓는다.
type Comparison struct {
	SuiteHash string        `json:"suiteHash"`
	Runs      []CompareRun  `json:"runs"`
	Cases     []CompareCase `json:"cases"`
	Excluded  int           `json:"excluded"` // 골든셋이 달라 표에서 뺀 실행(공급자·모델별 최신 기준) 수
	Warning   string        `json:"warning"`  // 치명 케이스 회귀가 있으면 경고 문구(차단하지 않는다, D12)
}

// CompareEvalRuns 는 공급자·모델별 최신 실행을 모아 비교 표를 만든다. 가장 최근 실행의 골든셋(suite_hash)과 같은 실행만 포함한다.
// 기준 실행은 포함된 실행 중 가장 오래된 것(가장 오래 쓴 공급자)이다. eval:read 가 필요하다.
func (s *Store) CompareEvalRuns(ctx context.Context) (Comparison, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return Comparison{}, err
	}
	runs, err := s.ListEvalRuns(ctx, 200)
	if err != nil {
		return Comparison{}, err
	}
	return buildComparison(runs), nil
}

func buildComparison(all []EvalRun) Comparison {
	out := Comparison{Runs: []CompareRun{}, Cases: []CompareCase{}}
	// all 은 최신 순이다. 공급자·모델별 최신 하나만 남긴다.
	seen := map[string]bool{}
	var latest []EvalRun
	for _, r := range all {
		k := r.Provider + "|" + r.Model
		if seen[k] || r.SuiteHash == "" {
			continue
		}
		seen[k] = true
		latest = append(latest, r)
	}
	if len(latest) == 0 {
		return out
	}
	out.SuiteHash = latest[0].SuiteHash
	var picked []EvalRun
	for _, r := range latest {
		if r.SuiteHash == out.SuiteHash {
			picked = append(picked, r)
		} else {
			out.Excluded++
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].ID < picked[j].ID }) // 가장 오래된 실행이 기준(첫 열)
	status := make([]map[string]bool, len(picked))
	crit := map[string]bool{}
	var order []string
	have := map[string]bool{}
	for i, r := range picked {
		status[i] = map[string]bool{}
		for _, c := range r.Cases {
			k := c.Suite + "/" + c.ID
			status[i][k] = c.Pass
			if c.Critical {
				crit[k] = true
			}
			if !have[k] {
				have[k] = true
				order = append(order, k)
			}
		}
	}
	critRegress := 0
	for i, r := range picked {
		cr := CompareRun{ID: r.ID, At: r.At, Provider: r.Provider, Model: r.Model, PassRatePct: r.PassRatePct, LatencyMS: r.LatencyMS, CostMicroUSD: r.CostMicroUSD, Reference: i == 0}
		for _, c := range r.Cases {
			if c.Critical && !c.Pass {
				cr.CriticalFailures++
			}
		}
		if i > 0 {
			for k, ok := range status[0] {
				if ok {
					if pass, has := status[i][k]; has && !pass {
						cr.Regressions++
						if crit[k] {
							cr.CriticalRegress++
							critRegress++
						}
					}
				}
			}
		}
		out.Runs = append(out.Runs, cr)
	}
	for _, k := range order {
		cc := CompareCase{Key: k, Critical: crit[k], Results: make([]string, len(picked))}
		for i := range picked {
			if pass, has := status[i][k]; has {
				cc.Results[i] = "fail"
				if pass {
					cc.Results[i] = "pass"
				}
			}
		}
		out.Cases = append(out.Cases, cc)
	}
	if critRegress > 0 {
		out.Warning = "기준 공급자에서 통과하던 치명 케이스가 다른 공급자·모델에서 실패했습니다. 라우팅을 바꾸기 전에 확인하세요(저장을 막지는 않습니다)."
	}
	return out
}

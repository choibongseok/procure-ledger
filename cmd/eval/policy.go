package main

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/docs/policies"
	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
)

// 평가용 부서. 이름 → ID.
var evalDepts = map[string]int64{"구매팀": 1, "경영지원팀": 2, "영업팀": 3}

// runPolicy 는 규정 검색 케이스를 실제 서비스(열람 범위·감사 경유)로 돌려 채점한다. LLM 을 쓰지 않아 공급자와 무관하게 결정적이다.
func runPolicy(ctx context.Context, cases []evals.PolicyCase) ([]CaseResult, error) {
	names := map[int64]string{}
	for n, id := range evalDepts {
		names[id] = n
	}
	svc := &policysearch.Service{
		Txs: &fake.TxManager{}, Audit: fake.NewRecorder(), Masker: fake.Masker{},
		Source: func() fs.FS { return policies.FS },
		Depts: func(_ context.Context, id int64) (string, error) {
			if n, ok := names[id]; ok {
				return n, nil
			}
			return "", contract.ErrNotFound
		},
	}
	if err := svc.Init(); err != nil {
		return nil, err
	}
	if p := svc.Problems(); len(p) > 0 {
		return nil, fmt.Errorf("규정 문서 문제: %s", strings.Join(p, "; "))
	}
	var out []CaseResult
	for i, c := range cases {
		cr := CaseResult{Suite: "policy", ID: c.ID, Critical: c.Critical}
		deptID, ok := evalDepts[c.Dept]
		if !ok {
			return nil, fmt.Errorf("케이스 %s: 알 수 없는 부서 %q", c.ID, c.Dept)
		}
		p := contract.Principal{UserID: int64(300 + i), Name: c.Role, Role: contract.Role(c.Role), DepartmentID: deptID, Channel: contract.ChannelWeb}
		pctx := contract.WithPrincipal(ctx, p)
		var hits []domain.PolicyHit
		var err error
		if c.Related {
			hits, err = svc.Related(pctx, domain.PolicyRelatedInput{Amount: c.Amount, Items: c.Items})
		} else {
			hits, err = svc.Search(pctx, c.Query, 20)
		}
		if err != nil {
			cr.Detail = "호출 오류: " + err.Error()
			out = append(out, cr)
			continue
		}
		var problems []string
		keys := make([]string, len(hits))
		for j, h := range hits {
			keys[j] = h.DocSlug + "/" + h.ClauseNo
		}
		topN := c.TopN
		if topN <= 0 {
			topN = 3
		}
		for _, want := range c.ExpectTop {
			rank := -1
			for j, k := range keys {
				if k == want {
					rank = j
					break
				}
			}
			if rank < 0 || rank >= topN {
				problems = append(problems, fmt.Sprintf("%s 가 상위 %d 안에 없음(순위 %d)", want, topN, rank+1))
			}
		}
		for _, none := range c.ExpectNone {
			for _, h := range hits {
				if h.DocSlug == none {
					problems = append(problems, fmt.Sprintf("볼 수 없는 문서 %s 가 결과에 있음", none))
					break
				}
			}
		}
		if c.ExpectEmpty && len(hits) != 0 {
			problems = append(problems, fmt.Sprintf("0건이어야 하는데 %d건", len(hits)))
		}
		if c.SameSetAs != "" {
			ref, rerr := svc.Search(pctx, c.SameSetAs, 20)
			if rerr != nil {
				problems = append(problems, "비교 질의 호출 오류: "+rerr.Error())
			} else if a, b := keySet(hits), keySet(ref); a != b {
				problems = append(problems, fmt.Sprintf("%q 와 결과 집합이 다름(%d건 대 %d건)", c.SameSetAs, len(hits), len(ref)))
			} else if len(ref) == 0 {
				problems = append(problems, "비교 질의의 결과가 비어 있어 시험이 되지 않음")
			}
		}
		cr.Pass = len(problems) == 0
		top := "없음"
		if len(keys) > 0 {
			top = keys[0]
		}
		cr.Detail = fmt.Sprintf("%d건, 1위 %s", len(hits), top)
		if !cr.Pass {
			cr.Detail = strings.Join(problems, "; ") + " / " + cr.Detail
		}
		out = append(out, cr)
	}
	return out, nil
}

// keySet 은 결과의 문서·조항 집합을 정렬한 문자열로 바꿔 순서와 무관하게 비교하게 한다.
func keySet(hits []domain.PolicyHit) string {
	ks := make([]string, len(hits))
	for i, h := range hits {
		ks[i] = h.DocSlug + "/" + h.ClauseNo
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

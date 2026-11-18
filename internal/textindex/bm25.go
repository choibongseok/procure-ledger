package textindex

import (
	"math"
	"sort"
)

// 범용 조각 색인(B47). policysearch 의 조항 색인과 같은 점수 규칙(BM25, 한글 n-gram, 제목 가중, 의미 있는 일치, 상대 하한)을 조각 번호(ID)로 돌린다.
// 만든 뒤에는 읽기 전용이라 동시에 검색해도 된다. 거름(visible)은 질의 조건이라 통계(문서 수·평균 길이·df)도 보이는 조각만으로 계산한다.

// Entry 는 색인할 조각 하나다. Heading 은 본문보다 가중이 크다.
type Entry struct {
	ID            int64
	Heading, Text string
}

// Hit 은 검색 결과 한 건이다. Score 는 순위 비교용이고 밖으로 내보내지 않는다.
type Hit struct {
	ID    int64
	Score int
}

type posting struct {
	entry int
	tf    int
}

// Index 는 조각 단위 역색인이다.
type Index struct {
	ids      []int64
	dl       []int
	postings map[string][]posting
}

// Build 는 조각으로 색인을 만든다. 같은 입력이면(순서 포함) 같은 색인이다.
func Build(entries []Entry) *Index {
	ix := &Index{ids: make([]int64, 0, len(entries)), dl: make([]int, 0, len(entries)), postings: map[string][]posting{}}
	for _, e := range entries {
		toks := Tokenize(e.Text)
		h := Tokenize(e.Heading)
		for i := 0; i < HeadingRepeat; i++ {
			toks = append(toks, h...)
		}
		ei := len(ix.ids)
		ix.ids = append(ix.ids, e.ID)
		ix.dl = append(ix.dl, len(toks))
		tf := map[string]int{}
		for _, t := range toks {
			tf[t]++
		}
		terms := make([]string, 0, len(tf))
		for t := range tf {
			terms = append(terms, t)
		}
		sort.Strings(terms)
		for _, t := range terms {
			ix.postings[t] = append(ix.postings[t], posting{entry: ei, tf: tf[t]})
		}
	}
	return ix
}

// Len 은 색인된 조각 수다.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.ids)
}

// Search 는 visible 이 참인 조각만으로 BM25 검색해 점수 순(같으면 입력 순서)으로 최대 limit 건을 돌려준다.
// 질의의 의미 있는 부분과 맞지 않는 조각(우연한 n-gram 한 개)과 최고 점수의 RelativeCutoff 미만은 뺀다. visible 이 nil 이면 전부 본다.
func (ix *Index) Search(query string, limit int, visible func(id int64) bool) []Hit {
	if ix == nil || limit <= 0 || len(ix.ids) == 0 {
		return nil
	}
	words := QueryWords(query)
	setTerms, allTerms := map[string]bool{}, map[string]bool{}
	var setWords [][]string
	for _, w := range words {
		setWords = append(setWords, w.Set)
		for _, t := range w.Set {
			setTerms[t], allTerms[t] = true, true
		}
		for _, t := range w.Boost {
			allTerms[t] = true
		}
	}
	if len(setTerms) == 0 {
		return nil
	}
	vis := make([]bool, len(ix.ids))
	n, totalLen := 0, 0
	for i, id := range ix.ids {
		if visible == nil || visible(id) {
			vis[i] = true
			n++
			totalLen += ix.dl[i]
		}
	}
	if n == 0 {
		return nil
	}
	avg := float64(totalLen) / float64(n)

	score := func(set map[string]bool) (map[int]float64, map[int]map[string]bool) {
		terms := make([]string, 0, len(set))
		for t := range set {
			terms = append(terms, t)
		}
		sort.Strings(terms)
		scores := map[int]float64{}
		matched := map[int]map[string]bool{}
		for _, t := range terms {
			df := 0
			for _, p := range ix.postings[t] {
				if vis[p.entry] {
					df++
				}
			}
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
			w := TermWeight(t)
			for _, p := range ix.postings[t] {
				if !vis[p.entry] {
					continue
				}
				if matched[p.entry] == nil {
					matched[p.entry] = map[string]bool{}
				}
				matched[p.entry][t] = true
				tf := float64(p.tf)
				dl := float64(ix.dl[p.entry])
				scores[p.entry] += w * idf * tf * (K1 + 1) / (tf + K1*(1-B+B*dl/avg))
			}
		}
		return scores, matched
	}

	setScores, matched := score(setTerms)
	type sc struct {
		ei    int
		milli int
	}
	var cands []sc
	for ei, s := range setScores {
		if !Meaningful(setWords, matched[ei]) {
			continue
		}
		cands = append(cands, sc{ei, int(math.Round(s * 1000))})
	}
	if len(cands) == 0 {
		return nil
	}
	top := 0
	for _, c := range cands {
		if c.milli > top {
			top = c.milli
		}
	}
	cut := int(float64(top) * RelativeCutoff)
	rank := setScores
	if len(allTerms) != len(setTerms) {
		rank, _ = score(allTerms)
	}
	var ranked []sc
	for _, c := range cands {
		if c.milli >= cut {
			ranked = append(ranked, sc{c.ei, int(math.Round(rank[c.ei] * 1000))})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].milli != ranked[j].milli {
			return ranked[i].milli > ranked[j].milli
		}
		return ranked[i].ei < ranked[j].ei
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]Hit, len(ranked))
	for i, r := range ranked {
		out[i] = Hit{ID: ix.ids[r.ei], Score: r.milli}
	}
	return out
}

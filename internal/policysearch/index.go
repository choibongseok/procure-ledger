package policysearch

import (
	"math"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/textindex"
)

// 토큰화·질의 분석은 공용 패키지(textindex)다. 이 파일의 이름은 기존 호출부(시험 포함)를 위한 얇은 별칭이다.

func isHangul(r rune) bool { return textindex.IsHangul(r) }

// Tokenize 는 문자열을 색인·질의용 토큰 목록(중복 포함, 등장 순서)으로 바꾼다.
func Tokenize(s string) []string { return textindex.Tokenize(s) }

func stemOf(w string) (string, bool) { return textindex.Stem(w) }

type posting struct {
	clause int
	tf     int
}

type entry struct {
	doc    int
	clause Clause
	dl     int
}

// Index 는 조항 단위 역색인이다. 만든 뒤에는 읽기 전용이라 동시에 검색해도 된다.
type Index struct {
	docs     []Doc
	entries  []entry
	postings map[string][]posting
	// visible 은 열람 범위 판정이다. 기본은 Doc.Visible 이고, 테스트가 한쪽을 일부러 열어 두고 서비스의 독립 확인이 막는지 볼 때만 바꾼다.
	visible func(Doc, Viewer) bool
}

// Hit 은 검색 결과 한 건이다. Score 는 BM25 점수 × 1000 을 반올림한 정수다.
type Hit struct {
	Doc    Doc
	Clause Clause
	Score  int
}

// Source 는 출처 표기다.
func (h Hit) Source() string { return h.Doc.Source(h.Clause) }

// Viewer 는 열람 범위 판정에 쓰는 호출자 정보다. 부서는 이름으로 비교한다(문서 머리말이 이름을 쓴다).
type Viewer struct {
	// Perms 는 호출자의 실효 권한이다(서비스가 채운다). 문서의 roles 항목은 이 권한으로 판정한다(scope.go).
	Perms contract.PermSet
	Dept  string
}

// Visible 은 문서를 볼 수 있는지다. 권한 AND 부서(문서의 roles 항목은 scope.go 의 규칙으로 권한이 된다). 호출자 권한을 알 수 없으면 아무것도 못 본다.
func (d Doc) Visible(v Viewer) bool {
	perms, ok := v.perms()
	if !ok {
		return false
	}
	roleOK := d.AllRoles
	if !roleOK {
		for _, r := range d.Roles {
			if holdsAny(perms, scopeTokenPerms(r)) {
				roleOK = true
				break
			}
		}
	}
	if !roleOK {
		return false
	}
	if d.AllDepts {
		return true
	}
	for _, x := range d.Depts {
		if x != "" && x == v.Dept {
			return true
		}
	}
	return false
}

// 파라미터(공용 값)
const (
	k1             = textindex.K1
	b              = textindex.B
	headingRepeat  = textindex.HeadingRepeat
	relativeCutoff = textindex.RelativeCutoff
)

// NewIndex 는 문서와 조항으로 색인을 만든다. 입력 순서와 무관하게 같은 색인이 나오도록 문서를 slug 로 정렬한다.
func NewIndex(docs []Doc, clauses map[string][]Clause) *Index {
	ds := append([]Doc(nil), docs...)
	sort.Slice(ds, func(i, j int) bool { return ds[i].Slug < ds[j].Slug })
	ix := &Index{docs: ds, postings: map[string][]posting{}, visible: Doc.Visible}
	for di, d := range ds {
		for _, c := range clauses[d.Slug] {
			toks := Tokenize(c.Text)
			for i := 0; i < headingRepeat; i++ {
				toks = append(toks, Tokenize(c.Heading)...)
			}
			ci := len(ix.entries)
			ix.entries = append(ix.entries, entry{doc: di, clause: c, dl: len(toks)})
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
				ix.postings[t] = append(ix.postings[t], posting{clause: ci, tf: tf[t]})
			}
		}
	}
	return ix
}

// Docs 는 색인된 문서 수가 아니라 문서 목록을 돌려준다(복사본). 관리·테스트용이며 호출자 범위 필터를 거치지 않는다.
func (ix *Index) Docs() []Doc { return append([]Doc(nil), ix.docs...) }

func termWeight(t string) float64 { return textindex.TermWeight(t) }

// Search 는 호출자가 볼 수 있는 문서의 조항만으로 BM25 검색한다(열람 범위가 질의 조건이다).
// 통계(문서 수·평균 길이·df)도 볼 수 있는 조항만으로 계산하므로, 점수에 볼 수 없는 문서의 존재가 섞이지 않는다.
// 같은 색인·같은 입력이면 같은 순서다. 점수가 같으면 문서 slug, 조항 순서로 정한다.
//
// 의미 없는 질의가 우연한 n-gram 한 개로 결과를 내지 않도록, 조항은 질의 단어 중 하나가 강하게(그 단어 n-gram 의 절반 이상) 맞거나
// 서로 다른 두 단어가 맞아야 결과로 인정한다.
func (ix *Index) Search(query string, limit int, v Viewer) []Hit {
	return ix.search(query, limit, v, true)
}

// Find 는 문서 slug 와 조항 번호로 조항 한 건을 찾는다(호출자가 볼 수 있는 문서만). 점수는 0 이다.
func (ix *Index) Find(slug, no string, v Viewer) (Hit, bool) {
	for _, e := range ix.entries {
		d := ix.docs[e.doc]
		if d.Slug == slug && e.clause.No == no && ix.visible(d, v) {
			return Hit{Doc: d, Clause: e.clause}, true
		}
	}
	return Hit{}, false
}

func meaningful(words [][]string, matched map[string]bool) bool {
	return textindex.Meaningful(words, matched)
}

func (ix *Index) search(query string, limit int, v Viewer, strict bool) []Hit {
	if limit <= 0 {
		return nil
	}
	words := textindex.QueryWords(query)
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
	visible := make([]bool, len(ix.entries))
	n, totalLen := 0, 0
	for i, e := range ix.entries {
		if ix.visible(ix.docs[e.doc], v) {
			visible[i] = true
			n++
			totalLen += e.dl
		}
	}
	if n == 0 {
		return nil
	}
	avg := float64(totalLen) / float64(n)

	// score 는 주어진 질의 n-gram 으로 조항마다 BM25 점수와 맞은 n-gram 을 구한다.
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
				if visible[p.clause] {
					df++
				}
			}
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
			w := termWeight(t)
			for _, p := range ix.postings[t] {
				if !visible[p.clause] {
					continue
				}
				if matched[p.clause] == nil {
					matched[p.clause] = map[string]bool{}
				}
				matched[p.clause][t] = true
				tf := float64(p.tf)
				dl := float64(ix.entries[p.clause].dl)
				scores[p.clause] += w * idf * tf * (k1 + 1) / (tf + k1*(1-b+b*dl/avg))
			}
		}
		return scores, matched
	}

	// 결과 집합은 어간 n-gram 만으로 정한다(의미 있는 일치 + 최고 점수 대비 하한).
	setScores, matched := score(setTerms)
	type sc struct {
		ci    int
		milli int
	}
	var cands []sc
	for ci, s := range setScores {
		if strict && !meaningful(setWords, matched[ci]) {
			continue
		}
		cands = append(cands, sc{ci, int(math.Round(s * 1000))})
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
	cut := int(float64(top) * relativeCutoff)
	rankScores := setScores
	if len(allTerms) != len(setTerms) {
		rankScores, _ = score(allTerms) // 순위에는 단어 자체의 정확한 일치를 더한다
	}
	var ranked []sc
	for _, c := range cands {
		if c.milli >= cut {
			ranked = append(ranked, sc{c.ci, int(math.Round(rankScores[c.ci] * 1000))})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].milli != ranked[j].milli {
			return ranked[i].milli > ranked[j].milli
		}
		return ranked[i].ci < ranked[j].ci
	})
	var out []Hit
	for _, r := range ranked {
		if len(out) >= limit {
			break
		}
		e := ix.entries[r.ci]
		out = append(out, Hit{Doc: ix.docs[e.doc], Clause: e.clause, Score: r.milli})
	}
	return out
}

// Package textindex 는 한국어 사내 검색이 함께 쓰는 토큰화·질의 분석·BM25 낱말 가중이다(B47 D14: 규정 검색 policysearch 에서 올렸다).
// 한글은 2·3글자 조각(n-gram)과 조사·어미 제거를 쓰고, 영문·숫자는 소문자 한 토큰이다. 형태소 분석기는 쓰지 않는다.
package textindex

import (
	"sort"
	"strings"
	"unicode"
)

// 토큰화
//
// 공백·구두점으로 나눈 한글 덩어리마다 2글자·3글자 n-gram 을 만든다. 한국어는 조사·어미가 붙어("결재선은", "결재선을")
// 공백 단위 토큰으로는 적중이 약하기 때문이다. 영문·숫자 덩어리는 소문자로 통째로 한 토큰이다.
// 한글 한 글자짜리 덩어리는 잡음이라 버린다.

func IsHangul(r rune) bool { return r >= 0xAC00 && r <= 0xD7A3 }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// tokenizeWords 는 단어(공백·구두점으로 나뉜 덩어리)마다 토큰 목록을 만든다.
func tokenizeWords(s string) [][]string {
	var words [][]string
	var run []rune
	hangul := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		var toks []string
		if hangul {
			for n := 2; n <= 3; n++ {
				for i := 0; i+n <= len(run); i++ {
					toks = append(toks, string(run[i:i+n]))
				}
			}
		} else {
			toks = []string{string(run)}
		}
		if len(toks) > 0 {
			words = append(words, toks)
		}
		run = run[:0]
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case IsHangul(r):
			if !hangul {
				flush()
				hangul = true
			}
			run = append(run, r)
		case isWordRune(r):
			if hangul {
				flush()
				hangul = false
			}
			run = append(run, r)
		default:
			flush()
			hangul = false
		}
	}
	flush()
	return words
}

// Tokenize 는 문자열을 색인·질의용 토큰 목록(중복 포함, 등장 순서)으로 바꾼다.
func Tokenize(s string) []string {
	var out []string
	for _, w := range tokenizeWords(s) {
		out = append(out, w...)
	}
	return out
}

// 파라미터
const (
	K1 = 1.2
	B  = 0.75
	// 제목 토큰은 본문보다 가중이 크다(소제목이 질의와 겹치면 그 조항이 정답일 가능성이 높다).
	HeadingRepeat = 3
	// 최고 점수의 이 비율 미만은 잡음으로 버린다.
	RelativeCutoff = 0.25
	// 3글자 n-gram 은 2글자보다 우연히 겹칠 확률이 낮아 가중을 더 준다.
	TrigramWeight = 1.5
	BigramWeight  = 1.0
)

func TermWeight(t string) float64 {
	if r := []rune(t); len(r) >= 1 && IsHangul(r[0]) && len(r) == 3 {
		return TrigramWeight
	}
	return BigramWeight
}

// Meaningful 은 조항이 질의의 의미 있는 부분과 맞는지다(위 Search 설명).
func Meaningful(words [][]string, matched map[string]bool) bool {
	weak, strong := 0, 0
	for _, w := range words {
		n := 0
		for _, t := range w {
			if matched[t] {
				n++
			}
		}
		switch {
		case n == 0:
		case n*2 >= len(w):
			strong++
		default:
			weak++
		}
	}
	return strong >= 1 || weak >= 2
}

// 질의 단어 끝에서 떼어 볼 조사와 흔한 용언 어미. 긴 것부터 맞춘다.
var suffixes = func() []string {
	s := []string{
		// 조사
		"에서", "에게", "으로", "까지", "부터", "처럼", "보다", "한테", "이나", "라도", "이란", "에는", "에도", "에서는", "에게는", "으로는", "로는", "이라",
		"은", "는", "이", "가", "을", "를", "에", "의", "와", "과", "도", "로", "만",
		// 용언 어미("하다·되다" 계열)
		"하면서", "했습니다", "합니다", "됩니다", "했다", "됐다", "한다", "된다", "하면", "되면", "하는", "되는", "하고", "되고", "하여", "되어", "해서", "돼서",
		"하세요", "하나요", "하는지", "되는지", "했던", "하던", "된", "한", "할", "될", "해", "돼", "하", "되",
	}
	sort.SliceStable(s, func(i, j int) bool { return len([]rune(s[i])) > len([]rune(s[j])) })
	return s
}()

// Stem 는 한글 단어에서 조사·용언 어미를 뗀 어간을 돌려준다. 떼고 나서 2글자 미만이면 떼지 않는다.
func Stem(w string) (string, bool) {
	r := []rune(w)
	if len(r) == 0 || !IsHangul(r[0]) {
		return "", false
	}
	for _, j := range suffixes {
		jr := []rune(j)
		if len(r)-len(jr) >= 2 && string(r[len(r)-len(jr):]) == j {
			return string(r[:len(r)-len(jr)]), true
		}
	}
	return "", false
}

// QWord 는 질의 단어 하나다. set 은 결과 집합을 정하는 n-gram(조사·어미를 뗀 어간), boost 는 순위에만 보태는 n-gram 이다.
// 조사 붙은 질의와 어간 질의가 같은 결과 집합을 내도록 집합은 어간만으로 정한다.
// 3글자 단어("위임은")가 문서에 그대로 나오면 정확한 일치라 순위에만 가산한다.
type QWord struct {
	Set   []string
	Boost []string
}

// QueryWords 는 질의를 단어별로 나눈다. 끝에 조사·어미가 붙은 한글 단어는 어간의 n-gram 만 집합에 쓴다.
// 조사까지 낀 n-gram("선은")은 우연히 겹쳐 순위만 흐리므로 쓰지 않는다.
func QueryWords(q string) []QWord {
	var out []QWord
	var run []rune
	hangul := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		w := string(run)
		var qw QWord
		if hangul {
			if st, ok := Stem(w); ok {
				qw.Set = Ngrams([]rune(st))
				if len(run) == 3 {
					qw.Boost = []string{w}
				}
			} else {
				qw.Set = Ngrams(run)
			}
		} else {
			qw.Set = []string{w}
		}
		if len(qw.Set) > 0 {
			out = append(out, qw)
		}
		run = run[:0]
	}
	for _, r := range strings.ToLower(q) {
		switch {
		case IsHangul(r):
			if !hangul {
				flush()
				hangul = true
			}
			run = append(run, r)
		case isWordRune(r):
			if hangul {
				flush()
				hangul = false
			}
			run = append(run, r)
		default:
			flush()
			hangul = false
		}
	}
	flush()
	return out
}

func Ngrams(run []rune) []string {
	var toks []string
	for n := 2; n <= 3; n++ {
		for i := 0; i+n <= len(run); i++ {
			toks = append(toks, string(run[i:i+n]))
		}
	}
	return toks
}

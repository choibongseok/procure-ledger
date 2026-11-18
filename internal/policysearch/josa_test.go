package policysearch

import (
	"strings"
	"testing"
)

func hitKeys(hs []Hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Doc.Slug+"/"+h.Clause.No)
	}
	return out
}

// 조사가 붙은 짧은 질의에서도 결과가 좁아지지 않고, 정의 조항이 상위에 나온다.
func TestJosaQueriesKeepRecall(t *testing.T) {
	ix := realIndex(t)
	v := vw("admin", "경영지원팀")
	cases := []struct {
		q      string
		want   string
		topN   int
		minHit int
	}{
		{"결재선은", "purchase-approval-line/제2조", 3, 3},
		{"결재선을", "purchase-approval-line/제2조", 3, 3},
		{"입고를", "inventory-receiving/", 3, 3},
		{"입고는", "inventory-receiving/", 3, 3},
	}
	for _, c := range cases {
		for _, vv := range []Viewer{v, vw("staff", "구매팀")} {
			hs := ix.Search(c.q, 10, vv)
			if len(hs) < 2 {
				t.Errorf("%q %+v: 결과가 좁아짐: %v", c.q, vv, hitKeys(hs))
			}
			top := false
			for i, h := range hs {
				top = top || (i < c.topN && strings.HasPrefix(h.Doc.Slug+"/"+h.Clause.No, c.want))
			}
			if !top {
				t.Errorf("%q %+v: 상위 %d 안에 %s 없음: %v", c.q, vv, c.topN, c.want, hitKeys(hs))
			}
		}
		hs := ix.Search(c.q, 10, v)
		if len(hs) < c.minHit {
			t.Errorf("%q: %d건, %d건 이상이어야 함: %v", c.q, len(hs), c.minHit, hitKeys(hs))
		}
		found := false
		for i, h := range hs {
			if i < c.topN && strings.HasPrefix(h.Doc.Slug+"/"+h.Clause.No, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: 상위 %d 안에 %s 가 있어야 함: %v", c.q, c.topN, c.want, hitKeys(hs))
		}
	}
	// 조사를 떼어 보는 것이 의미 없는 질의를 살리지 않는다.
	for _, q := range []string{"하늘은 바람이 구름을", "zzzzqqq없는낱말은", "꿇뷁쏐휪을", "하늘 바람 구름", "은는이가", "하늘이", "고양이가", "바나나를", "자동차는", "컴퓨터로", "구름이 흐르면", "하늘이 맑은"} {
		if hs := ix.Search(q, 10, v); len(hs) != 0 {
			t.Errorf("%q: 0건이어야 함: %v", q, hitKeys(hs))
		}
	}
}

func TestStemOf(t *testing.T) {
	for in, want := range map[string]string{"입고를": "입고", "결재선은": "결재선", "창고에서": "창고", "서류로": "서류", "반려하면": "반려", "입고된": "입고", "반려한": "반려", "반려하": "반려", "반려했다": "반려"} {
		if got, ok := stemOf(in); !ok || got != want {
			t.Errorf("%s -> %s,%v (기대 %s)", in, got, ok, want)
		}
	}
	for _, in := range []string{"입고", "회의", "결과", "abc"} {
		if got, ok := stemOf(in); ok {
			t.Errorf("%s 는 떼지 않아야 하는데 %s", in, got)
		}
	}
}

// 조사·용언 어미가 붙은 질의의 결과 집합은 어간만 쓴 질의와 같다(순서는 달라도 된다).
func TestJosaAndEndingQueriesReturnSameSetAsStem(t *testing.T) {
	ix := realIndex(t)
	pairs := [][2]string{
		{"입고를", "입고"}, {"입고는", "입고"}, {"입고된", "입고"}, {"재고가", "재고"}, {"상신은", "상신"},
		{"반려하면", "반려"}, {"반려한", "반려"}, {"반려하", "반려"}, {"반려했다", "반려"}, {"결재선은", "결재선"}, {"위임은", "위임"},
		{"안전재고가", "안전재고"}, {"승인하는", "승인"}, {"유효기한이", "유효기한"},
	}
	for _, v := range []Viewer{vw("staff", "구매팀"), vw("admin", "경영지원팀"), vw("staff", "영업팀"), vw("finance", "경영지원팀")} {
		for _, p := range pairs {
			got, want := hitKeys(ix.Search(p[0], 50, v)), hitKeys(ix.Search(p[1], 50, v))
			if len(want) == 0 {
				t.Fatalf("%q 어간 질의 결과가 비어 있음(시험 자체가 틀림) %+v", p[1], v)
			}
			if !sameSet(got, want) {
				t.Errorf("%+v %q %v != %q %v", v, p[0], got, p[1], want)
			}
		}
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, c := range m {
		if c != 0 {
			return false
		}
	}
	return true
}

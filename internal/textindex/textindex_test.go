package textindex

import "testing"

func TestTokenizeKoreanNgramsAndStem(t *testing.T) {
	got := Tokenize("결재선은 PR 100만원")
	want := map[string]bool{"결재": true, "재선": true, "선은": true, "결재선": true, "재선은": true, "pr": true, "100": true, "만원": true}
	if len(got) != len(want) {
		t.Fatalf("토큰: %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("예상 밖 토큰 %q: %v", g, got)
		}
	}
	if st, ok := Stem("결재선은"); !ok || st != "결재선" {
		t.Fatalf("조사 제거: %q %v", st, ok)
	}
	if len(Tokenize("가")) != 0 {
		t.Fatal("한 글자 한글은 버린다")
	}
}

func TestIndexSearchVisibleFilterAndRanking(t *testing.T) {
	ix := Build([]Entry{
		{ID: 1, Heading: "장비 교체 절차", Text: "장비를 교체할 때는 먼저 담당자에게 알린다"},
		{ID: 2, Text: "회식 장소 안내"},
		{ID: 3, Text: "장비 교체 절차 장비 교체 절차를 따른다"},
		{ID: 4, Text: "다른 부서의 장비 교체 절차"},
	})
	got := ix.Search("장비 교체 절차", 10, nil)
	if len(got) < 3 || got[0].ID == 2 {
		t.Fatalf("결과: %+v", got)
	}
	for _, h := range got {
		if h.ID == 2 {
			t.Fatalf("무관한 조각이 섞임: %+v", got)
		}
	}
	// 거름: 보이지 않는 조각은 결과에도 통계에도 없다.
	only := ix.Search("장비 교체 절차", 10, func(id int64) bool { return id == 4 })
	if len(only) != 1 || only[0].ID != 4 {
		t.Fatalf("거름: %+v", only)
	}
	if ix.Search("전혀없는낱말", 10, nil) != nil || ix.Search("가", 10, nil) != nil {
		t.Fatal("의미 없는 질의에 결과")
	}
	if got := ix.Search("장비 교체 절차", 2, nil); len(got) != 2 {
		t.Fatalf("limit: %d", len(got))
	}
	var nilIx *Index
	if nilIx.Len() != 0 || nilIx.Search("장비", 5, nil) != nil {
		t.Fatal("nil 색인")
	}
}

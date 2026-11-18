package service

import (
	"reflect"
	"testing"
)

// 부분집합 탐색은 결정적이다: 크기 오름차순, 같으면 위치(입고일이 오래된 순) 사전순.
func TestSubsetSearchDeterministicOrder(t *testing.T) {
	// 30,70,50,50 중 합 100: {30,70}(위치 0,1), {50,50}(위치 2,3). 크기가 같으면 위치가 앞선 {0,1} 이 먼저다.
	exact, _, _ := subsetSearch([]int64{30, 70, 50, 50}, 100)
	if len(exact) != 2 || !reflect.DeepEqual(exact[0].idx, []int{0, 1}) || !reflect.DeepEqual(exact[1].idx, []int{2, 3}) {
		t.Fatalf("정확히 같은 조합: %+v", exact)
	}
	// 단일이 있으면 작은 집합이 먼저.
	exact, _, _ = subsetSearch([]int64{40, 60, 100}, 100)
	if len(exact) != 2 || !reflect.DeepEqual(exact[0].idx, []int{2}) || !reflect.DeepEqual(exact[1].idx, []int{0, 1}) {
		t.Fatalf("작은 집합 우선: %+v", exact)
	}
	// 같은 값 두 입고 중 하나: 오래된(앞선) 것이 먼저.
	exact, _, _ = subsetSearch([]int64{50, 50}, 50)
	if len(exact) != 2 || exact[0].idx[0] != 0 {
		t.Fatalf("오래된 것 우선: %+v", exact)
	}
	// 정확히 같은 조합이 없으면 차이가 가장 작은 조합을 돌려준다(같으면 작은 집합, 앞선 위치).
	exact, nearest, ok := subsetSearch([]int64{30, 50, 70}, 90)
	if len(exact) != 0 || !ok || nearest.sum != 80 && nearest.sum != 100 {
		t.Fatalf("가장 가까운 조합: %+v %+v", exact, nearest)
	}
	if nearest.sum != 100 && nearest.sum != 80 {
		t.Fatal("합이 90±10 이어야 한다")
	}
	// 빈 후보.
	if e, _, ok := subsetSearch(nil, 10); len(e) != 0 || ok {
		t.Fatal("후보가 없으면 결과가 없다")
	}
	// 12건: 전수 탐색이 합 한 가지를 찾는다.
	vals := []int64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048}
	ex, _, _ := subsetSearch(vals, 2047)
	if len(ex) != 1 || ex[0].size != 11 {
		t.Fatalf("2의 거듭제곱 조합은 유일하다: %+v", ex)
	}
}

func TestNormName(t *testing.T) {
	for _, c := range [][2]string{{"(주)한빛상사", "한빛상사"}, {"한빛 상사 주식회사", "한빛상사"}, {"㈜Hanbit", "hanbit"}} {
		if normName(c[0]) != normName(c[1]) {
			t.Errorf("%q ≠ %q", c[0], c[1])
		}
	}
	if normName("한빛상사") == normName("대한물산") {
		t.Error("다른 이름")
	}
}

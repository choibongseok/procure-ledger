package knowledge

import (
	"reflect"
	"testing"
)

func TestVerifyCitations(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		n       int
		text    string
		cited   []int
		dropped int
	}{
		{"유효 인용만 남김", "출장비는 한도 안에서 정산한다. [1]\n지어낸 문장이다. [9]\n번호 없는 문장이다.", 3, "출장비는 한도 안에서 정산한다. [1]", []int{1}, 2},
		{"문장 끝 뒤 번호", "첫째 문장이다. [2] 둘째 문장이다. [1][2]", 2, "첫째 문장이다. [2] 둘째 문장이다. [1][2]", []int{2, 1}, 0},
		{"쉼표 번호 중 하나라도 벗어나면 버림", "근거가 섞인 문장이다 [1, 7].", 3, "", nil, 1},
		{"0 번은 버림", "영번 문장이다 [0].", 3, "", nil, 1},
		{"찾지 못함만 있으면 빈 글", "찾지 못했습니다.", 3, "", nil, 1},
		{"번호만 있는 문장은 버림", "[1]", 3, "", nil, 1},
		{"소수점은 문장 끝이 아님", "한도는 3.5배이다 [2].", 2, "한도는 3.5배이다 [2].", []int{2}, 0},
	}
	for _, c := range cases {
		text, cited, dropped := VerifyCitations(c.raw, c.n)
		if text != c.text || !reflect.DeepEqual(cited, c.cited) || dropped != c.dropped {
			t.Errorf("%s: 글 %q 인용 %v 버림 %d (기대 %q %v %d)", c.name, text, cited, dropped, c.text, c.cited, c.dropped)
		}
	}
}

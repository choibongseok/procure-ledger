package service

import "testing"

// AC-515: 받침 유무와 ㄹ 받침(받침이 있는 글자로 센다), 한글이 아닌 끝 글자.
func TestJosa(t *testing.T) {
	cases := []struct{ got, want string }{
		{topic("스마트유통"), "스마트유통은"},
		{topic("한빛상사"), "한빛상사는"},
		{topic("대한오피스"), "대한오피스는"},
		{topic("삼성전자"), "삼성전자는"},
		{topic("동아건설"), "동아건설은"}, // ㄹ 받침도 받침이다
		{ga("동아건설"), "동아건설이"},
		{ga("한빛상사"), "한빛상사가"},
		{object("스마트유통"), "스마트유통을"},
		{object("한빛상사"), "한빛상사를"},
		{topic("ABC"), "ABC은(는)"},
		{topic("3M"), "3M은(는)"},
		{topic(""), "은(는)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%q, 기대 %q", c.got, c.want)
		}
	}
}

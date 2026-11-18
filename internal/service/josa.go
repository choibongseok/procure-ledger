package service

// 한국어 조사 공용 함수(B25 명세 3.2.5·AC-515). 직책·거래처 이름 같은 임의 단어 뒤에 조사를 받침 규칙으로 붙인다.
// 마지막 글자가 한글이 아니면(영문·숫자 등) 받침을 알 수 없으므로 "은(는)"처럼 괄호 표기를 쓴다.

func lastRune(word string) (rune, bool) {
	rs := []rune(word)
	if len(rs) == 0 {
		return 0, false
	}
	r := rs[len(rs)-1]
	return r, r >= 0xAC00 && r <= 0xD7A3
}

// josa 는 단어 뒤에 받침 있을 때(with)·없을 때(without) 조사를 붙인다. 한글이 아니면 "with(without)" 로 쓴다.
func josa(word, with, without string) string {
	if _, hangul := lastRune(word); !hangul {
		return word + with + "(" + without + ")"
	}
	if hasBatchim(word) {
		return word + with
	}
	return word + without
}

// topic 은 은/는, ga 는 이/가, object 는 을/를 을 붙인다.
func topic(word string) string  { return josa(word, "은", "는") }
func ga(word string) string     { return josa(word, "이", "가") }
func object(word string) string { return josa(word, "을", "를") }

package auth

import "strings"

// commonPasswords 는 흔히 쓰이는 비밀번호(소문자 비교)다. 최소 길이(10자) 이상인 값 위주이고 외부 조회는 하지 않는다.
// 길이 정책을 통과하는 흔한 값만 의미가 있어 짧은 값(password·123456 등)은 넣지 않았다.
var commonPasswords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		password123 password1234 password12345 passw0rd123 p@ssw0rd123 p@ssword123 password!23 password@123 password#123
		1234567890 12345678901 123456789012 0123456789 1234512345 1111111111 0000000000 1q2w3e4r5t 1qaz2wsx3edc 1qaz@wsx3edc
		qwertyuiop qwertyuiop1 qwerty12345 qwerty123456 qwertyuiop123 asdfghjkl1 asdfghjklqwerty zxcvbnm123 zxcvbnmasdf
		qazwsxedc123 qwe123qwe123 abcdefghij abcd123456 abc1234567 abcabcabcabc aaaaaaaaaa
		iloveyou123 iloveyou1234 iloveyou12 loveyou123 ilovemyself monkey12345 dragon12345 football123 baseball123
		letmein1234 letmein12345 welcome1234 welcome12345 welcome123! welcome@123 welcome#123 admin12345 admin123456 admin@12345
		administrator administrator1 administrator123 changeme123 changeme1234 changeme!23 change_me123 default1234 default12345
		master12345 superman123 batman12345 trustno1234 sunshine123 princess123 charlie1234 shadow12345 michael1234
		test123456 test1234567 testtest123 guest12345 user123456 root123456 server12345 company123 company1234 company@123
		minecraft123 starwars123 whatever123 freedom123 summer2024 summer2025 summer2026 winter2024 winter2025 winter2026
		spring2024 spring2025 spring2026 autumn2024 autumn2025 autumn2026 january2026 passw0rd2026 password2024 password2025 password2026
		qwer123456 qwer1234567 asdf123456 asdf1234567 zxcv123456 q1w2e3r4t5 q1w2e3r4t5y6 1q2w3e4r5t6y 7777777777 9999999999
		a1b2c3d4e5 a1b2c3d4e5f6 abcd1234abcd kimchi1234 korea12345 korea2025 korea2026 seoul12345 hello12345 hello123456 helloworld1 helloworld123
		dkssudgktpdy tkfkdgo1234 rlawlsgml123
	`) {
		m[w] = true
	}
	return m
}()

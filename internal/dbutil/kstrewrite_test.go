package dbutil

import (
	"strings"
	"testing"
)

func TestRewriteKSTDate(t *testing.T) {
	kst := func(e string) string { return Postgres.KSTDate(e) }
	for _, c := range []struct{ in, want string }{
		{"SELECT date(p.created_at, '+9 hours') >= ?", "SELECT " + kst("p.created_at") + " >= ?"},
		{"SELECT date(MAX(r.received_at), '+9 hours') AS d", "SELECT " + kst("MAX(r.received_at)") + " AS d"},
		{"SELECT substr(date(created_at, '+9 hours'),1,7) = ?", "SELECT substr(" + kst("created_at") + ",1,7) = ?"},
		{"COALESCE(s.book_date, date(s.created_at, '+9 hours'))", "COALESCE(s.book_date, " + kst("s.created_at") + ")"},
		{"date(COALESCE(a, date(b, '+9 hours')), '+9 hours')", kst("COALESCE(a, " + kst("b") + ")")},
		{"SELECT 'date(x, ''+9 hours'')' AS lit", "SELECT 'date(x, ''+9 hours'')' AS lit"}, // 문자열 안은 그대로
		{"SELECT date(created_at) FROM t", "SELECT date(created_at) FROM t"},               // 수정자 없음
		{"SELECT date(a, '+1 day') FROM t WHERE x = '+9 hours'", "SELECT date(a, '+1 day') FROM t WHERE x = '+9 hours'"},
		{"SELECT validate(a, '+9 hours')", "SELECT validate(a, '+9 hours')"}, // 식별자 끝이 date( 인 다른 함수
	} {
		if got := rewriteKSTDate(c.in); got != c.want {
			t.Errorf("%q\n got  %q\n want %q", c.in, got, c.want)
		}
	}
	// Rebind 도 함께 변환한다.
	got := Rebind("SELECT 1 FROM t WHERE date(x, '+9 hours') = ? AND y = ?")
	if !strings.Contains(got, "Asia/Seoul") || !strings.Contains(got, "= $1 AND y = $2") {
		t.Errorf("Rebind: %q", got)
	}
	if got := Rebind("SELECT ?1, ?2, ?1 FROM t WHERE a = ?"); got != "SELECT $1, $2, $1 FROM t WHERE a = $1" {
		t.Errorf("번호 자리표시자: %q", got)
	}
}

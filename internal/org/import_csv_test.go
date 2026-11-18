package org

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/korean"
)

func TestDecodeCSVTextEncodings(t *testing.T) {
	const body = "조직코드,조직명\nPUR,구매팀\n"
	euc, err := korean.EUCKR.NewEncoder().String(body)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		data []byte
		enc  string
		err  string
	}{
		"UTF-8":          {[]byte(body), "UTF-8", ""},
		"UTF-8 BOM":      {append([]byte{0xEF, 0xBB, 0xBF}, body...), "UTF-8", ""},
		"EUC-KR":         {[]byte(euc), "EUC-KR", ""},
		"UTF-16 BOM":     {[]byte{0xFF, 0xFE, 'a', 0}, "", "지원하지 않는 형식입니다"},
		"UTF-16 BOM 없음":  {[]byte{'a', 0, 'b', 0}, "", "지원하지 않는 형식입니다"},
		"깨진 바이트":         {[]byte{0xFF, 0xFF, 0xFF, 0x80}, "", "글자 인코딩을 알 수 없습니다"},
		"BOM 뒤 깨진 UTF-8": {[]byte{0xEF, 0xBB, 0xBF, 0xC0, 0xC0}, "", "글자 인코딩을 알 수 없습니다"},
	} {
		text, enc, err := DecodeCSVText(c.data)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v, want %q", name, err, c.err)
			}
			continue
		}
		if err != nil || enc != c.enc || text != body {
			t.Errorf("%s: enc=%q err=%v text=%q", name, enc, err, text)
		}
	}
	if _, _, err := DecodeCSVText(make([]byte, ImportMaxFileBytes+1)); err == nil || !strings.Contains(err.Error(), "5MB") {
		t.Errorf("크기 상한: %v", err)
	}
}

func TestParseCSVRowsLinesAndCells(t *testing.T) {
	text := "사번,이름,직위\r\nE1,\"김, 하윤\",과장\r\n\r\nE2,\"박 \"\"철수\"\"\", 대리 \r\n"
	tb, err := ParseCSV(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 2 {
		t.Fatalf("행 %d", len(tb.Rows))
	}
	if r := tb.Rows[0]; r.Line != 2 || r.Cells["이름"] != "김, 하윤" || r.Cells["직위"] != "과장" {
		t.Errorf("첫 행 %+v", r)
	}
	// 빈 줄을 건너뛰어도 줄 번호는 편집기에서 보이는 번호다.
	if r := tb.Rows[1]; r.Line != 4 || r.Cells["이름"] != `박 "철수"` || r.Cells["직위"] != "대리" {
		t.Errorf("둘째 행 %+v", r)
	}
	if _, err := ParseCSV("사번,사번\nE1,E2\n"); err == nil || !strings.Contains(err.Error(), "같은 열이 둘") {
		t.Errorf("중복 열: %v", err)
	}
	if _, err := ParseCSV(""); err == nil {
		t.Error("빈 파일")
	}
	if _, err := ParseCSV("a,b\n\"x,y\n"); err == nil {
		t.Error("닫히지 않은 따옴표")
	}
}

func TestParseCSVRowLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("사번\n")
	for i := 0; i < ImportMaxRows+1; i++ {
		b.WriteString("E\n")
	}
	if _, err := ParseCSV(b.String()); err == nil || !strings.Contains(err.Error(), "5,000개") {
		t.Errorf("행 상한: %v", err)
	}
}

func TestCellProblemAndFormula(t *testing.T) {
	if cellProblem("정상") != "" || cellProblem("a\nb") == "" || cellProblem("a\x01") == "" || cellProblem("a\x7f") == "" {
		t.Error("칸 검사")
	}
	for _, v := range []string{"=1+1", "+82", "-1", "@x", "\tx"} {
		if formulaStart(v) == "" {
			t.Errorf("%q 는 수식 시작", v)
		}
		if csvSafe(v) != "'"+v {
			t.Errorf("%q 앞에 작은따옴표를 붙이지 않음", v)
		}
	}
	if formulaStart("김하윤") != "" || csvSafe("김하윤") != "김하윤" {
		t.Error("일반 값은 그대로")
	}
}

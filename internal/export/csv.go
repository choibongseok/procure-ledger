package export

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CSVSafe 는 엑셀이 수식으로 읽을 수 있는 문자열 칸(=, +, -, @, 탭, 캐리지리턴으로 시작)의 앞에 '(작은따옴표)를 붙인다.
// 조직 CSV(B22)의 서버 생성 파일과 같은 규칙이라 둘이 이 함수 하나를 쓴다. 숫자·날짜 열에는 쓰지 않는다.
func CSVSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// CSVUnsafe 는 CSVSafe 가 붙인 작은따옴표를 되돌린다(내보냈다 다시 가져오는 파일이 같은 값이 되게). 수식 접두 규칙은 이 파일에만 둔다.
func CSVUnsafe(v string) string {
	if len(v) >= 2 && v[0] == '\'' {
		switch v[1] {
		case '=', '+', '-', '@', '\t', '\r':
			return v[1:]
		}
	}
	return v
}

// stripControl 은 줄바꿈·탭을 뺀 제어문자를 지운다(CSV 는 따옴표 안 줄바꿈을 허용하지만 다른 제어문자는 파일을 깨뜨린다).
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// toTime 은 date·datetime 칸의 값을 KST 시각으로 바꾼다.
func toTime(c Cell) (time.Time, bool) {
	switch v := c.(type) {
	case time.Time:
		return v.In(KST), true
	case string:
		if t, err := time.ParseInLocation("2006-01-02", v, KST); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.In(KST), true
		}
	}
	return time.Time{}, false
}

// csvValue 는 한 칸을 CSV 문자열로 바꾼다.
func csvValue(k Kind, c Cell) string {
	if c == nil {
		return ""
	}
	switch k {
	case KindText:
		return CSVSafe(stripControl(c.(string)))
	case KindInt, KindMoney:
		switch v := c.(type) {
		case int:
			return strconv.Itoa(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	case KindPercent:
		switch v := c.(type) {
		case float64:
			return strconv.FormatFloat(v, 'f', 1, 64)
		case int:
			return strconv.FormatFloat(float64(v), 'f', 1, 64)
		case int64:
			return strconv.FormatFloat(float64(v), 'f', 1, 64)
		}
	case KindDate:
		if t, ok := toTime(c); ok {
			return t.Format("2006-01-02")
		}
	case KindDateTime:
		if t, ok := toTime(c); ok {
			return t.Format("2006-01-02 15:04")
		}
	case KindBool:
		if b, _ := c.(bool); b {
			return "예"
		}
		return "아니오"
	}
	return ""
}

// WriteCSV 는 UTF-8 BOM·CRLF·RFC 4180 따옴표 규칙의 CSV 를 만든다. 첫 줄은 열 이름(화면 표기)이다.
func WriteCSV(t Table) ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	head := make([]string, len(t.Dataset.Columns))
	for i, c := range t.Dataset.Columns {
		head[i] = CSVSafe(stripControl(c.Label))
	}
	if err := w.Write(head); err != nil {
		return nil, err
	}
	rec := make([]string, len(t.Dataset.Columns))
	for _, row := range t.Rows {
		for j, c := range row {
			rec[j] = csvValue(t.Dataset.Columns[j].Kind, c)
		}
		if err := w.Write(rec); err != nil {
			return nil, fmt.Errorf("export: csv 쓰기: %w", err)
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 최소 OOXML(SpreadsheetML) 작성기다. 한 파일에 시트 둘: 데이터 시트와 "내보내기 정보" 시트.
// 문자열은 인라인 문자열이라 수식으로 해석되지 않고, 숫자·금액·비율·날짜는 실제 숫자 칸이다(엑셀에서 합계·정렬이 된다).

const (
	maxCellChars  = 32767 // 엑셀 셀 한 칸의 글자 수 한계
	maxSheetName  = 31
	infoSheetName = "내보내기 정보"
	maxColWidth   = 60
	minColWidth   = 8
	widthSample   = 200 // 열 너비를 재는 행 수
)

// 스타일 번호(styles.xml 의 cellXfs 순서와 같다).
const (
	styleDefault  = 0
	styleHeader   = 1
	styleMoney    = 2
	stylePercent  = 3
	styleDate     = 4
	styleDateTime = 5
	styleInt      = 6
	stylePlainTop = 7 // 정보 시트의 이름 칸(굵게 아님, 위 맞춤)
)

var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// sheetName 은 엑셀이 허용하는 시트 이름으로 바꾼다(금지 문자 `[]:*?/\` 는 공백, 31자 이하, 비면 "시트1").
func sheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '[', ']', ':', '*', '?', '/', '\\':
			return ' '
		}
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), "'")
	if r := []rune(s); len(r) > maxSheetName {
		s = string(r[:maxSheetName])
	}
	if s == "" {
		return "시트1"
	}
	return s
}

// xmlText 는 XML 1.0 에서 허용되지 않는 글자를 지우고 한계 길이로 자른 뒤 이스케이프한다.
func xmlText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r >= 0xD800 && r <= 0xDFFF:
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > maxCellChars {
		r := []rune(s)
		s = string(r[:maxCellChars-1]) + "…"
	}
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// colName 은 0 부터 시작하는 열 번호를 A, B, …, Z, AA 로 바꾼다.
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// displayWidth 는 열 너비 계산용 글자 폭이다(한글 등 2칸, 영문·숫자 1칸).
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x7f {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func serial(t time.Time) float64 {
	t = t.In(KST)
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	return wall.Sub(excelEpoch).Seconds() / 86400
}

func numStr(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// xlsxCell 은 한 칸의 XML 과 칸 너비 계산용 글자를 돌려준다. ref 는 "B3" 꼴이다.
func xlsxCell(ref string, k Kind, c Cell) (string, string) {
	if c == nil {
		return "", ""
	}
	switch k {
	case KindText:
		s := c.(string)
		return fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xmlText(s)), s
	case KindInt, KindMoney:
		var n int64
		switch v := c.(type) {
		case int:
			n = int64(v)
		case int64:
			n = v
		}
		st := styleInt
		if k == KindMoney {
			st = styleMoney
		}
		return fmt.Sprintf(`<c r="%s" s="%d"><v>%d</v></c>`, ref, st, n), strconv.FormatInt(n, 10) + ",,,"
	case KindPercent:
		var f float64
		switch v := c.(type) {
		case float64:
			f = v
		case int:
			f = float64(v)
		case int64:
			f = float64(v)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", ""
		}
		return fmt.Sprintf(`<c r="%s" s="%d"><v>%s</v></c>`, ref, stylePercent, numStr(f/100)), "100.0%"
	case KindDate:
		if t, ok := toTime(c); ok {
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, KST)
			return fmt.Sprintf(`<c r="%s" s="%d"><v>%s</v></c>`, ref, styleDate, numStr(math.Floor(serial(d)))), "2006-01-02"
		}
	case KindDateTime:
		if t, ok := toTime(c); ok {
			return fmt.Sprintf(`<c r="%s" s="%d"><v>%s</v></c>`, ref, styleDateTime, numStr(math.Round(serial(t)*1440)/1440)), "2006-01-02 15:04"
		}
	case KindBool:
		s := "아니오"
		if b, _ := c.(bool); b {
			s = "예"
		}
		return fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, s), s
	}
	return "", ""
}

func dataSheetXML(t Table) string {
	cols := len(t.Dataset.Columns)
	widths := make([]int, cols)
	for j, c := range t.Dataset.Columns {
		widths[j] = displayWidth(c.Label) + 4 // 자동 필터 단추 자리
	}
	var rows strings.Builder
	// 머리글
	rows.WriteString(`<row r="1">`)
	for j, c := range t.Dataset.Columns {
		fmt.Fprintf(&rows, `<c r="%s1" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, colName(j), styleHeader, xmlText(c.Label))
	}
	rows.WriteString(`</row>`)
	for i, row := range t.Rows {
		fmt.Fprintf(&rows, `<row r="%d">`, i+2)
		for j, cell := range row {
			x, sample := xlsxCell(fmt.Sprintf("%s%d", colName(j), i+2), t.Dataset.Columns[j].Kind, cell)
			rows.WriteString(x)
			if i < widthSample {
				if w := displayWidth(sample) + 2; w > widths[j] {
					widths[j] = w
				}
			}
		}
		rows.WriteString(`</row>`)
	}
	var colsXML strings.Builder
	colsXML.WriteString(`<cols>`)
	for j, w := range widths {
		if w < minColWidth {
			w = minColWidth
		}
		if w > maxColWidth {
			w = maxColWidth
		}
		fmt.Fprintf(&colsXML, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, j+1, j+1, w)
	}
	colsXML.WriteString(`</cols>`)
	last := len(t.Rows) + 1
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	fmt.Fprintf(&b, `<dimension ref="A1:%s%d"/>`, colName(maxInt(cols-1, 0)), last)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/><selection pane="bottomLeft"/></sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)
	b.WriteString(colsXML.String())
	b.WriteString(`<sheetData>`)
	b.WriteString(rows.String())
	b.WriteString(`</sheetData>`)
	fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, colName(maxInt(cols-1, 0)), last)
	b.WriteString(`</worksheet>`)
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func infoSheetXML(t Table, m Meta) string {
	raw := "아니오"
	if m.Raw {
		raw = "예"
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	items := [][2]string{
		{"데이터셋", t.Dataset.Label},
		{"내보낸 일시", at.In(KST).Format("2006-01-02 15:04")},
		{"내보낸 사람", m.By},
		{"적용한 조건", m.Filters},
		{"행 수", strconv.Itoa(len(t.Rows))},
		{"원문 포함", raw},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	fmt.Fprintf(&b, `<dimension ref="A1:B%d"/>`, len(items))
	b.WriteString(`<cols><col min="1" max="1" width="16" customWidth="1"/><col min="2" max="2" width="60" customWidth="1"/></cols><sheetData>`)
	for i, it := range items {
		fmt.Fprintf(&b, `<row r="%d"><c r="A%d" s="%d" t="inlineStr"><is><t>%s</t></is></c><c r="B%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c></row>`,
			i+1, i+1, styleHeader, xmlText(it[0]), i+1, xmlText(it[1]))
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<numFmts count="3"><numFmt numFmtId="164" formatCode="0.0%"/><numFmt numFmtId="165" formatCode="yyyy\-mm\-dd"/><numFmt numFmtId="166" formatCode="yyyy\-mm\-dd\ hh:mm"/></numFmts>` +
	`<fonts count="2"><font><sz val="11"/><name val="Malgun Gothic"/></font><font><b/><sz val="11"/><name val="Malgun Gothic"/></font></fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="8">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`<xf numFmtId="3" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="166" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="1" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="top"/></xf>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

// WriteXLSX 는 XLSX 파일 바이트를 만든다. 시트 이름은 데이터셋 한글 이름이다.
func WriteXLSX(t Table, m Meta) ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	name := sheetName(t.Dataset.Label)
	last := len(t.Rows) + 1
	cols := maxInt(len(t.Dataset.Columns), 1)
	quoted := "'" + strings.ReplaceAll(name, "'", "''") + "'"
	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<bookViews><workbookView/></bookViews>` +
		`<sheets><sheet name="` + xmlAttr(name) + `" sheetId="1" r:id="rId1"/><sheet name="` + xmlAttr(infoSheetName) + `" sheetId="2" r:id="rId2"/></sheets>` +
		fmt.Sprintf(`<definedNames><definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">%s!$A$1:$%s$%d</definedName></definedNames>`, xmlAttr(quoted), colName(cols-1), last) +
		`</workbook>`
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`},
		{"xl/styles.xml", stylesXML},
		{"xl/worksheets/sheet1.xml", dataSheetXML(t)},
		{"xl/worksheets/sheet2.xml", infoSheetXML(t, m)},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// xmlAttr 는 XML 속성값용 이스케이프다.
func xmlAttr(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}

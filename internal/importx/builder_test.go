package importx_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/importx"
)

// 시험 파일을 코드로 만드는 도우미다. B28 이 쓰는 XLSX 와 엑셀이 저장하는 XLSX(공유 문자열·날짜 서식·수식 캐시값·1904 체계)를 모두 흉내 낸다.

type tc struct {
	kind  string // str(인라인 문자열) sstr(공유 문자열) num date formula(캐시 숫자) formulastr(캐시 문자열) bool err
	text  string
	style int // date 칸의 서식 번호(1: 내장 14, 2: 사용자 정의 yyyy-mm-dd)
}

func str(s string) tc  { return tc{kind: "str", text: s} }
func sstr(s string) tc { return tc{kind: "sstr", text: s} }
func num(s string) tc  { return tc{kind: "num", text: s} }
func dateSerial(serial string) tc {
	return tc{kind: "date", text: serial, style: 1}
}
func formula(cached string) tc    { return tc{kind: "formula", text: cached} }
func formulaStr(cached string) tc { return tc{kind: "formulastr", text: cached} }
func boolc(v bool) tc             { return tc{kind: "bool", text: map[bool]string{true: "1", false: "0"}[v]} }
func errc(s string) tc            { return tc{kind: "err", text: s} }
func rich() tc                    { return tc{kind: "rich"} }

// serialOf 는 날짜의 엑셀 일련번호다(1900 체계, 1904 체계).
func serialOf(y int, m time.Month, d int, date1904 bool) string {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var base time.Time
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else {
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	return strconv.Itoa(int(t.Sub(base).Hours() / 24))
}

type opts struct {
	date1904 bool
	info     [][2]string // 둘째 시트(파일 정보) 행. nil 이면 서버 양식과 같은 현재 버전의 정보 시트를 넣는다(noInfo 가 아니면)
	noInfo   bool        // 둘째 시트(파일 정보)를 아예 넣지 않는다
	macro    bool
	skipRows map[int]bool // 건너뛸(빈) 행 번호 — 행 번호가 실제 번호를 따르는지 보려고
	rowStart []int        // 행마다 실제 행 번호(비면 1부터 연속)
	startCol int          // 데이터를 시작하는 열(0 이면 A) — 건너뛴 열
}

func colLetter(i int) string {
	s := ""
	for i >= 0 {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
	}
	return s
}

func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// buildXLSX 는 시트 한 장짜리(+정보 시트) XLSX 바이트를 만든다. rows[0] 이 머리글이다.
func buildXLSX(t testing.TB, rows [][]tc, o opts) []byte {
	t.Helper()
	var shared []string
	sharedIdx := map[string]int{}
	addShared := func(s string) int {
		if i, ok := sharedIdx[s]; ok {
			return i
		}
		shared = append(shared, s)
		sharedIdx[s] = len(shared) // 0 번은 서식 있는 문자열 항목이 쓴다
		return sharedIdx[s]
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, row := range rows {
		rn := i + 1
		if i < len(o.rowStart) && o.rowStart[i] > 0 {
			rn = o.rowStart[i]
		}
		if len(row) == 0 { // 빈 행
			fmt.Fprintf(&sheet, `<row r="%d"/>`, rn)
			continue
		}
		fmt.Fprintf(&sheet, `<row r="%d">`, rn)
		for j, c := range row {
			ref := fmt.Sprintf("%s%d", colLetter(j+o.startCol), rn)
			switch c.kind {
			case "":
			case "str":
				fmt.Fprintf(&sheet, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xmlEsc(c.text))
			case "sstr":
				fmt.Fprintf(&sheet, `<c r="%s" t="s"><v>%d</v></c>`, ref, addShared(c.text))
			case "rich": // 서식 있는 문자열 항목(0번): 값은 "리치텍스트"
				fmt.Fprintf(&sheet, `<c r="%s" t="s"><v>0</v></c>`, ref)
			case "num":
				fmt.Fprintf(&sheet, `<c r="%s"><v>%s</v></c>`, ref, c.text)
			case "date":
				fmt.Fprintf(&sheet, `<c r="%s" s="%d"><v>%s</v></c>`, ref, c.style, c.text)
			case "formula":
				fmt.Fprintf(&sheet, `<c r="%s"><f>SUM(A1:A2)*999</f><v>%s</v></c>`, ref, c.text)
			case "formulastr":
				fmt.Fprintf(&sheet, `<c r="%s" t="str"><f>CONCATENATE("=cmd|","x")</f><v>%s</v></c>`, ref, xmlEsc(c.text))
			case "bool":
				fmt.Fprintf(&sheet, `<c r="%s" t="b"><v>%s</v></c>`, ref, c.text)
			case "err":
				fmt.Fprintf(&sheet, `<c r="%s" t="e"><v>%s</v></c>`, ref, xmlEsc(c.text))
			}
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)

	var sst strings.Builder
	sst.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	// 0번: 서식 있는 문자열과 후리가나가 섞인 항목(엑셀이 만드는 모양). 값은 t 들의 합이고 rPh 는 값이 아니다.
	sst.WriteString(`<si><r><t>리치</t></r><r><t>텍스트</t></r><rPh sb="0" eb="2"><t>후리가나</t></rPh></si>`)
	for _, s := range shared {
		fmt.Fprintf(&sst, `<si><t xml:space="preserve">%s</t></si>`, xmlEsc(s))
	}
	sst.WriteString(`</sst>`)

	if o.info == nil && !o.noInfo {
		o.info = [][2]string{{"적용한 조건", fmt.Sprintf("양식 버전 %d | 작성 안내", importx.TemplateVersion)}}
	}
	var info strings.Builder
	if o.info != nil {
		info.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
		for i, kv := range o.info {
			fmt.Fprintf(&info, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c><c r="B%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i+1, xmlEsc(kv[0]), i+1, xmlEsc(kv[1]))
		}
		info.WriteString(`</sheetData></worksheet>`)
	}

	wbPr := ""
	if o.date1904 {
		wbPr = `<workbookPr date1904="1"/>`
	}
	sheets := `<sheet name="데이터" sheetId="1" r:id="rId1"/>`
	rels := `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>`
	if o.info != nil {
		sheets += `<sheet name="내보내기 정보" sheetId="2" r:id="rId2"/>`
		rels += `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>`
	}
	rels += `<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`
	rels += `<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>`
	ct := `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml`
	if o.macro {
		ct = `application/vnd.ms-excel.sheet.macroEnabled.main+xml`
	}
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/></numFmts>` +
		`<cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/></cellXfs></styleSheet>`
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="` + ct + `"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` + wbPr + `<sheets>` + sheets + `</sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels + `</Relationships>`},
		{"xl/styles.xml", styles},
		{"xl/sharedStrings.xml", sst.String()},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	if o.info != nil {
		parts = append(parts, struct{ name, body string }{"xl/worksheets/sheet2.xml", info.String()})
	}
	if o.macro {
		parts = append(parts, struct{ name, body string }{"xl/vbaProject.bin", "binary"})
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 양식과 같은 정보 시트(버전 1)를 붙이는 보조.
func infoFor(label string, version int) [][2]string {
	return [][2]string{{"데이터셋", label}, {"적용한 조건", fmt.Sprintf("양식 버전 %d | 작성 안내", version)}}
}

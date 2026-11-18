package export

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WriteWorkbook 은 데이터 시트 여러 개와 마지막 "내보내기 정보" 시트(info 의 이름·값 줄)로 된 XLSX 를 만든다(B41 월별 보고서처럼 한 파일에 시트가 여럿인 경우).
// 시트 이름은 각 표의 Dataset.Label 이고 서로 달라야 한다. 셀 형식·스타일은 WriteXLSX 와 같다.
func WriteWorkbook(tables []Table, info [][2]string) ([]byte, error) {
	if len(tables) == 0 {
		return nil, errors.New("export: 시트가 없습니다")
	}
	seen := map[string]bool{}
	names := make([]string, len(tables))
	for i, t := range tables {
		if err := t.validate(); err != nil {
			return nil, err
		}
		names[i] = sheetName(t.Dataset.Label)
		if seen[names[i]] || names[i] == infoSheetName {
			return nil, fmt.Errorf("export: 시트 이름 %q 이 겹칩니다", names[i])
		}
		seen[names[i]] = true
	}
	n := len(tables)
	var sheets, defined, ctypes, rels strings.Builder
	for i, t := range tables {
		fmt.Fprintf(&sheets, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(names[i]), i+1, i+1)
		quoted := "'" + strings.ReplaceAll(names[i], "'", "''") + "'"
		fmt.Fprintf(&defined, `<definedName name="_xlnm._FilterDatabase" localSheetId="%d" hidden="1">%s!$A$1:$%s$%d</definedName>`, i, xmlAttr(quoted), colName(maxInt(len(t.Dataset.Columns), 1)-1), len(t.Rows)+1)
	}
	fmt.Fprintf(&sheets, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(infoSheetName), n+1, n+1)
	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<bookViews><workbookView/></bookViews><sheets>` + sheets.String() + `</sheets><definedNames>` + defined.String() + `</definedNames></workbook>`
	for i := 0; i <= n; i++ {
		fmt.Fprintf(&ctypes, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
	}
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, n+2)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			ctypes.String() +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + `</Relationships>`},
		{"xl/styles.xml", stylesXML},
	}
	for i, t := range tables {
		parts = append(parts, struct{ name, body string }{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), dataSheetXML(t)})
	}
	parts = append(parts, struct{ name, body string }{fmt.Sprintf("xl/worksheets/sheet%d.xml", n+1), kvSheetXML(info)})
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

// kvSheetXML 은 이름·값 두 칸짜리 정보 시트다(infoSheetXML 과 같은 모양).
func kvSheetXML(items [][2]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	fmt.Fprintf(&b, `<dimension ref="A1:B%d"/>`, maxInt(len(items), 1))
	b.WriteString(`<cols><col min="1" max="1" width="16" customWidth="1"/><col min="2" max="2" width="60" customWidth="1"/></cols><sheetData>`)
	for i, it := range items {
		fmt.Fprintf(&b, `<row r="%d"><c r="A%d" s="%d" t="inlineStr"><is><t>%s</t></is></c><c r="B%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c></row>`,
			i+1, i+1, styleHeader, xmlText(it[0]), i+1, xmlText(it[1]))
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

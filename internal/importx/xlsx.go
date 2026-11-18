// Package importx 는 기초 데이터 가져오기(B35)의 양식 정의·XLSX 읽기·검증이다( 3.2·3.3).
//
// 불변식
//   - XLSX 읽기는 표준 라이브러리(archive/zip, encoding/xml)만 쓴다. 새 외부 의존이 없다.
//   - 파서는 순수 함수다. 입력은 바이트뿐이고 DB·시계·네트워크·LLM 을 쓰지 않는다.
//   - 수식 셀은 저장된 계산 결과값만 읽고 수식 문자열은 쓰지 않는다. 셀 안의 문구는 데이터일 뿐이다.
//   - 오류 사유에는 원본 값(이름·계좌 조각·금액)을 넣지 않는다.
package importx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Limits 는 읽기 상한이다(명세 3.2.4).
type Limits struct {
	MaxBytes        int64 // 파일 크기
	MaxRows         int   // 데이터 행(머리글 제외, 빈 행 제외)
	MaxUncompressed int64 // 압축 해제 합계
	MaxRatio        int64 // 압축률(해제 합계 / 파일 크기)
	MaxCols         int
	MaxStrings      int // 공유 문자열 개수
	MaxCellRunes    int // 한 칸의 글자 수(넘으면 칸을 Long 으로 표시)
}

// DefaultLimits 는 명세 D4 의 기본 상한이다. 행 상한은 대상별로 Target.MaxRows 가 덮어쓴다.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 5 << 20, MaxRows: 5000, MaxUncompressed: 50 << 20, MaxRatio: 100, MaxCols: 50, MaxStrings: 200000, MaxCellRunes: 1000}
}

// CellKind 는 칸의 종류다.
type CellKind int

const (
	CellEmpty  CellKind = iota
	CellText            // 문자열(공유·인라인·수식 결과 문자열)
	CellNumber          // 숫자(Text 는 파일에 적힌 원래 숫자 문자열)
	CellBool            // 불리언(Text 는 "true"·"false")
	CellDate            // 날짜 서식이 붙은 숫자(Text 는 YYYY-MM-DD)
	CellError           // 오류 칸(#N/A 등)
)

// Cell 은 읽은 칸 하나다.
type Cell struct {
	Kind CellKind
	Text string
	Long bool // 글자 수 상한을 넘어 잘렸다
}

// RawRow 는 시트의 한 행이다. No 는 실제 행 번호(1부터)이고 Cells 는 A 열부터의 칸이다(건너뛴 열은 CellEmpty).
type RawRow struct {
	No    int
	Cells []Cell
}

// Sheet 는 데이터 시트(첫 시트)와 파일 정보다.
type Sheet struct {
	Name        string
	Header      []string // 첫 행의 글자
	Rows        []RawRow // 머리글 다음 행들(완전히 빈 행은 없다)
	InfoDataset string   // 둘째 시트(내보내기 정보)의 "데이터셋" 값(없으면 "")
	InfoNote    string   // 둘째 시트의 "적용한 조건" 값(양식 버전·작성 안내가 들어 있다)
}

// 파일 수준 오류. 모두 contract.ErrValidation 을 감싼 한국어 문구다.
func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

var (
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	zipMagic = []byte("PK\x03\x04")
)

// ReadSheet 는 XLSX 의 첫 시트를 읽는다. 상한을 넘거나 지원하지 않는 파일은 한국어 오류다.
func ReadSheet(data []byte, lim Limits) (Sheet, error) {
	var sh Sheet
	if len(data) == 0 {
		return sh, verr("빈 파일입니다.")
	}
	if int64(len(data)) > lim.MaxBytes {
		return sh, verr("파일이 너무 큽니다(최대 %dMB).", lim.MaxBytes>>20)
	}
	switch {
	case bytes.HasPrefix(data, oleMagic):
		return sh, verr("엑셀 97-2003 형식(.xls)이거나 암호가 걸린 파일입니다. 양식을 내려받아 암호 없이 XLSX 로 저장해 올려 주세요.")
	case !bytes.HasPrefix(data, zipMagic):
		return sh, verr("XLSX 파일만 올릴 수 있습니다. CSV 는 받지 않으니 양식을 내려받아 채워 올려 주세요.")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return sh, verr("엑셀 파일을 열 수 없습니다. 파일이 깨졌는지 확인하세요.")
	}
	if len(zr.File) > 500 {
		return sh, verr("엑셀 파일 구조가 올바르지 않습니다.")
	}
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
	}
	if declared > uint64(lim.MaxUncompressed) || (declared > 1<<20 && int64(declared)/int64(len(data)) > lim.MaxRatio) {
		return sh, verr("파일의 압축을 풀면 너무 커서 읽을 수 없습니다.")
	}
	parts := map[string]*zip.File{}
	for _, f := range zr.File {
		parts[strings.TrimPrefix(f.Name, "/")] = f
	}
	budget := lim.MaxUncompressed // 실제로 읽은 바이트 합계의 남은 한도(선언 크기가 거짓이어도 막는다)
	read := func(name string) ([]byte, bool, error) {
		f := parts[name]
		if f == nil {
			return nil, false, nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil, true, verr("엑셀 파일을 열 수 없습니다. 파일이 깨졌는지 확인하세요.")
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, budget+1))
		if err != nil {
			return nil, true, verr("엑셀 파일을 읽을 수 없습니다. 파일이 깨졌는지 확인하세요.")
		}
		if int64(len(b)) > budget {
			return nil, true, verr("파일의 압축을 풀면 너무 커서 읽을 수 없습니다.")
		}
		budget -= int64(len(b))
		return b, true, nil
	}

	if _, macro := parts["xl/vbaProject.bin"]; macro {
		return sh, verr("매크로가 든 파일(.xlsm)은 받지 않습니다. 양식을 내려받아 매크로 없이 저장해 올려 주세요.")
	}
	ct, _, err := read("[Content_Types].xml")
	if err != nil {
		return sh, err
	}
	if bytes.Contains(ct, []byte("macroEnabled")) {
		return sh, verr("매크로가 든 파일(.xlsm)은 받지 않습니다. 양식을 내려받아 매크로 없이 저장해 올려 주세요.")
	}
	wbBytes, ok, err := read("xl/workbook.xml")
	if err != nil {
		return sh, err
	}
	if !ok {
		return sh, verr("엑셀 파일이 아닙니다. 양식을 내려받아 채워 올려 주세요.")
	}
	wb, err := parseWorkbook(wbBytes)
	if err != nil || len(wb.sheets) == 0 {
		return sh, verr("엑셀 파일 구조가 올바르지 않습니다.")
	}
	relBytes, _, err := read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return sh, err
	}
	targets := parseRels(relBytes)
	sheetPath := func(i int) string {
		t := targets[wb.sheets[i].rid]
		if t == "" {
			t = fmt.Sprintf("worksheets/sheet%d.xml", i+1)
		}
		if strings.HasPrefix(t, "/") {
			return strings.TrimPrefix(t, "/")
		}
		return path.Join("xl", t)
	}
	var shared []string
	if b, ok, err := read("xl/sharedStrings.xml"); err != nil {
		return sh, err
	} else if ok {
		if shared, err = parseSharedStrings(b, lim); err != nil {
			return sh, err
		}
	}
	var dateStyles map[int]bool
	if b, ok, err := read("xl/styles.xml"); err != nil {
		return sh, err
	} else if ok {
		dateStyles = parseDateStyles(b)
	}

	b, ok, err := read(sheetPath(0))
	if err != nil {
		return sh, err
	}
	if !ok {
		return sh, verr("엑셀 파일 구조가 올바르지 않습니다.")
	}
	sh.Name = wb.sheets[0].name
	rows, err := parseSheetRows(b, shared, dateStyles, wb.date1904, lim)
	if err != nil {
		return sh, err
	}
	if len(rows) == 0 || rowIsBlank(rows[0]) {
		return sh, verr("첫 시트의 첫 줄에 머리글이 없습니다. 양식을 내려받아 채워 올려 주세요.")
	}
	if len(rows[0].Cells) > lim.MaxCols {
		return sh, verr("열이 너무 많습니다(최대 %d열).", lim.MaxCols)
	}
	for _, c := range rows[0].Cells {
		sh.Header = append(sh.Header, c.Text)
	}
	for _, r := range rows[1:] {
		if !rowIsBlank(r) {
			sh.Rows = append(sh.Rows, r)
		}
	}
	if len(sh.Rows) > lim.MaxRows {
		return sh, verr("행이 너무 많습니다(최대 %d행). 파일을 나눠 올려 주세요.", lim.MaxRows)
	}
	// 둘째 시트(파일 정보): 양식 버전·데이터셋 이름. 없어도 된다.
	if len(wb.sheets) > 1 {
		if ib, ok, err := read(sheetPath(1)); err == nil && ok {
			if irows, err := parseSheetRows(ib, shared, dateStyles, wb.date1904, Limits{MaxRows: 100, MaxCols: 10, MaxCellRunes: 2000}); err == nil {
				for _, r := range irows {
					if len(r.Cells) >= 2 {
						switch strings.TrimSpace(r.Cells[0].Text) {
						case "데이터셋":
							sh.InfoDataset = strings.TrimSpace(r.Cells[1].Text)
						case "적용한 조건":
							sh.InfoNote = strings.TrimSpace(r.Cells[1].Text)
						}
					}
				}
			}
		}
	}
	return sh, nil
}

func rowIsBlank(r RawRow) bool {
	for _, c := range r.Cells {
		if c.Kind != CellEmpty && strings.TrimSpace(c.Text) != "" {
			return false
		}
	}
	return true
}

// ---- workbook / rels ----

type wbSheet struct{ name, rid string }
type workbook struct {
	sheets   []wbSheet
	date1904 bool
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func parseWorkbook(b []byte) (workbook, error) {
	var wb workbook
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return wb, nil
		}
		if err != nil {
			return wb, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "workbookPr":
			v := attr(se, "date1904")
			wb.date1904 = v == "1" || v == "true"
		case "sheet":
			rid := ""
			for _, a := range se.Attr {
				if a.Name.Local == "id" {
					rid = a.Value
				}
			}
			wb.sheets = append(wb.sheets, wbSheet{name: attr(se, "name"), rid: rid})
		}
	}
}

func parseRels(b []byte) map[string]string {
	out := map[string]string{}
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			out[attr(se, "Id")] = attr(se, "Target")
		}
	}
}

// ---- 공유 문자열 ----

func parseSharedStrings(b []byte, lim Limits) ([]string, error) {
	var out []string
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	var cur strings.Builder
	inSI, inT, inPhonetic := false, false, false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, verr("엑셀 파일을 읽을 수 없습니다. 파일이 깨졌는지 확인하세요.")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "rPh":
				inPhonetic = true // 후리가나 읽기 표기는 값이 아니다
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inSI = false
				if len(out) >= lim.MaxStrings {
					return nil, verr("문자열이 너무 많습니다.")
				}
				out = append(out, cur.String())
			case "rPh":
				inPhonetic = false
			case "t":
				inT = false
			}
		case xml.CharData:
			if inSI && inT && !inPhonetic && cur.Len() < lim.MaxCellRunes*4+16 {
				cur.Write(t)
			}
		}
	}
}

// ---- 날짜 서식 ----

// 내장 날짜·시각 서식 번호(ECMA-376 18.8.30).
var builtinDateFmt = map[int]bool{14: true, 15: true, 16: true, 17: true, 18: true, 19: true, 20: true, 21: true, 22: true,
	27: true, 28: true, 29: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true, 36: true,
	45: true, 46: true, 47: true, 50: true, 51: true, 52: true, 53: true, 54: true, 55: true, 56: true, 57: true, 58: true}

func isDateFormatCode(code string) bool {
	var b strings.Builder
	inQuote, inBracket := false, false
	for _, r := range code {
		switch {
		case r == '"':
			inQuote = !inQuote
		case inQuote:
		case r == '[':
			inBracket = true
		case r == ']':
			inBracket = false
		case inBracket:
		case r == '\\' || r == '_' || r == '*':
		default:
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())
	if s == "general" {
		return false
	}
	return strings.ContainsAny(s, "ymdhs") && !strings.Contains(s, "0.0e")
}

// parseDateStyles 는 날짜 서식을 가진 셀 서식 번호(cellXfs 의 위치) 집합이다.
func parseDateStyles(b []byte) map[int]bool {
	custom := map[int]bool{}
	var xfs []int
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	inCellXfs := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmt":
				id, _ := strconv.Atoi(attr(t, "numFmtId"))
				custom[id] = isDateFormatCode(attr(t, "formatCode"))
			case "cellXfs":
				inCellXfs = true
			case "xf":
				if inCellXfs {
					id, _ := strconv.Atoi(attr(t, "numFmtId"))
					xfs = append(xfs, id)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "cellXfs" {
				inCellXfs = false
			}
		}
	}
	out := map[int]bool{}
	for i, id := range xfs {
		if builtinDateFmt[id] || custom[id] {
			out[i] = true
		}
	}
	return out
}

// serialDate 는 엑셀 일련번호를 날짜(KST 달력 날짜)로 바꾼다. 1900 체계의 가짜 1900-02-29(60)와 음수·너무 큰 값은 거부한다.
func serialDate(f float64, date1904 bool) (time.Time, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 1 || f > 2958465 { // 9999-12-31
		return time.Time{}, false
	}
	days := math.Floor(f)
	var base time.Time
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else {
		if days == 60 {
			return time.Time{}, false
		}
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
		if days < 60 {
			base = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
		}
	}
	return base.AddDate(0, 0, int(days)), true
}

// ---- 시트 ----

func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			n = n*26 + int(r-'A'+1)
		} else if r >= 'a' && r <= 'z' {
			n = n*26 + int(r-'a'+1)
		} else {
			break
		}
	}
	return n - 1
}

func clipCell(s string, lim Limits) (string, bool) {
	if lim.MaxCellRunes > 0 && utf8.RuneCountInString(s) > lim.MaxCellRunes {
		return string([]rune(s)[:lim.MaxCellRunes]), true
	}
	return s, false
}

// parseSheetRows 는 시트 XML 을 스트리밍으로 읽는다. 완전히 빈 행도 돌려주고(호출자가 거른다) 데이터 행이 상한을 넘으면 거기서 멈춘다.
func parseSheetRows(b []byte, shared []string, dateStyles map[int]bool, date1904 bool, lim Limits) ([]RawRow, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	var rows []RawRow
	var cur *RawRow
	rowNo := 0
	type cellState struct {
		ref, typ string
		style    int
		v        strings.Builder
		is       strings.Builder
		hasV     bool
	}
	var cs *cellState
	inV, inIS, inT, inPh := false, false, false, false
	nonBlank := 0
	finishCell := func() {
		if cs == nil || cur == nil {
			cs = nil
			return
		}
		idx := colIndex(cs.ref)
		if idx < 0 {
			idx = len(cur.Cells)
		}
		if idx >= lim.MaxCols+1 && lim.MaxCols > 0 {
			cs = nil
			return
		}
		for len(cur.Cells) <= idx {
			cur.Cells = append(cur.Cells, Cell{})
		}
		var c Cell
		v := cs.v.String()
		switch cs.typ {
		case "s":
			if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && i >= 0 && i < len(shared) {
				c.Text, c.Long = clipCell(shared[i], lim)
				c.Kind = CellText
			}
		case "inlineStr":
			c.Text, c.Long = clipCell(cs.is.String(), lim)
			c.Kind = CellText
		case "str": // 수식의 결과 문자열(수식 자체는 읽지 않는다)
			c.Text, c.Long = clipCell(v, lim)
			c.Kind = CellText
		case "b":
			c.Kind = CellBool
			if strings.TrimSpace(v) == "1" || strings.EqualFold(strings.TrimSpace(v), "true") {
				c.Text = "true"
			} else {
				c.Text = "false"
			}
		case "e":
			c.Kind, c.Text = CellError, strings.TrimSpace(v)
		default: // 숫자(t 가 없거나 "n")
			v = strings.TrimSpace(v)
			if v == "" {
				break
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.Kind, c.Text = CellText, v
				break
			}
			if dateStyles[cs.style] {
				if t, ok := serialDate(f, date1904); ok {
					c.Kind, c.Text = CellDate, t.Format("2006-01-02")
					break
				}
				c.Kind, c.Text = CellError, "#DATE"
				break
			}
			c.Kind, c.Text = CellNumber, v
		}
		cur.Cells[idx] = c
		cs = nil
	}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, verr("엑셀 파일을 읽을 수 없습니다. 파일이 깨졌는지 확인하세요.")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				rowNo++
				if r, err := strconv.Atoi(attr(t, "r")); err == nil && r > 0 {
					rowNo = r
				}
				cur = &RawRow{No: rowNo}
			case "c":
				st, _ := strconv.Atoi(attr(t, "s"))
				cs = &cellState{ref: attr(t, "r"), typ: attr(t, "t"), style: st}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				inT = true
			case "rPh":
				inPh = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "is":
				inIS = false
			case "t":
				inT = false
			case "rPh":
				inPh = false
			case "c":
				finishCell()
			case "row":
				if cur != nil {
					if !rowIsBlank(*cur) {
						nonBlank++
					}
					rows = append(rows, *cur)
					cur = nil
					if lim.MaxRows > 0 && nonBlank > lim.MaxRows+1 { // 머리글 한 줄 + 상한 + 1
						return nil, verr("행이 너무 많습니다(최대 %d행). 파일을 나눠 올려 주세요.", lim.MaxRows)
					}
				}
			}
		case xml.CharData:
			if cs == nil {
				continue
			}
			switch {
			case inV && cs.v.Len() < lim.MaxCellRunes*4+64:
				cs.v.Write(t)
			case inIS && inT && !inPh && cs.is.Len() < lim.MaxCellRunes*4+64:
				cs.is.Write(t)
			}
		}
	}
	return rows, nil
}

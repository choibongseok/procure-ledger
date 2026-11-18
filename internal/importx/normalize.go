package importx

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// Parser 는 domain.ImportFileParser 를 구현한다. 상태가 없는 순수 함수 묶음이다.
type Parser struct{}

var _ domain.ImportFileParser = Parser{}

var reVersion = regexp.MustCompile(`양식 버전\s*(\d+)`)

// normHeader 는 머리글 비교용 글자다(공백·필수 표시·따옴표·괄호를 없애고 소문자로).
func normHeader(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', ' ', '*', '"', '\'', '(', ')', '（', '）', '“', '”':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// Parse 는 양식 XLSX 를 읽어 대상 규칙으로 검증·정규화한다.
func (Parser) Parse(target string, data []byte, opts domain.ImportParseOptions) (domain.ImportParseResult, error) {
	res := domain.ImportParseResult{Target: target}
	t, ok := Targets[target]
	if !ok {
		return res, verr("알 수 없는 가져오기 대상입니다.")
	}
	lim := DefaultLimits()
	lim.MaxRows = t.MaxRows
	sh, err := ReadSheet(data, lim)
	if err != nil {
		return res, err
	}
	// 양식 확인: 데이터셋 이름(다른 대상의 양식을 올렸는지)과 양식 버전.
	if sh.InfoDataset != "" && sh.InfoDataset != t.Label {
		for _, other := range Targets {
			if other.Label == sh.InfoDataset {
				return res, verr("다른 대상의 양식입니다(올린 파일은 %s 양식, 선택한 대상은 %s). 대상에 맞는 양식을 올려 주세요.", other.Label, t.Label)
			}
		}
	}
	// 양식 버전 칸이 없는 파일(정보 시트가 없거나 버전이 적히지 않음)은 서버가 내려준 양식이 아니라 오래되었거나 변형된 파일이라 거절한다.
	m := reVersion.FindStringSubmatch(sh.InfoNote)
	if m == nil {
		return res, verr("양식이 오래되었거나 변형되었습니다. 양식을 다시 내려받으세요.")
	}
	v, _ := strconv.Atoi(m[1])
	res.TemplateVersion = v
	if v != TemplateVersion {
		return res, verr("양식이 오래되었습니다. 최신 양식을 내려받아 사용하세요.")
	}
	// 머리글 → 열 위치(이름으로 찾으므로 열 순서가 바뀌어도 된다).
	byName := map[string]Column{}
	for _, c := range t.Columns {
		byName[normHeader(c.Label)] = c
	}
	pos := map[string]int{}
	unknown := 0
	for i, h := range sh.Header {
		n := normHeader(h)
		if n == "" {
			continue
		}
		c, known := byName[n]
		if !known {
			unknown++
			continue
		}
		if _, dup := pos[c.ID]; dup {
			return res, verr("같은 열 이름이 두 번 있습니다(%s).", c.Label)
		}
		pos[c.ID] = i
	}
	var missing []string
	for _, c := range t.Columns {
		if _, ok := pos[c.ID]; !ok && c.Required {
			missing = append(missing, c.Label)
		}
	}
	if len(missing) > 0 {
		return res, verr("양식에 필요한 열이 없습니다(누락: %s). 최신 양식을 내려받아 사용하세요.", strings.Join(missing, ", "))
	}
	if unknown > 0 {
		res.FileWarnings = append(res.FileWarnings, fmt.Sprintf("양식에 없는 열 %d개는 무시했습니다.", unknown))
	}
	res.TotalRows = len(sh.Rows)

	type okRow struct {
		row domain.ImportRow
	}
	var good []domain.ImportRow
	for _, rr := range sh.Rows {
		cells := map[string]string{}
		var reasons []string
		firstCol := ""
		for _, c := range t.Columns {
			i, has := pos[c.ID]
			var cell Cell
			if has && i < len(rr.Cells) {
				cell = rr.Cells[i]
			}
			val, warn, reason := normalizeCell(c, cell)
			if warn != "" {
				res.Warnings = append(res.Warnings, domain.ImportRowIssue{RowNo: rr.No, Column: c.Label, Reason: warn})
			}
			if reason != "" {
				if firstCol == "" {
					firstCol = c.Label
				}
				reasons = append(reasons, c.Label+": "+reason)
				continue
			}
			if val != "" {
				cells[c.ID] = val
			}
		}
		if len(reasons) == 0 {
			if msg := crossColumn(t, cells); msg != "" {
				reasons, firstCol = append(reasons, msg), ""
			}
		}
		if len(reasons) > 0 {
			res.Errors = append(res.Errors, domain.ImportRowIssue{RowNo: rr.No, Column: firstCol, Reason: strings.Join(reasons, "; ")})
			continue
		}
		good = append(good, domain.ImportRow{RowNo: rr.No, Cells: cells})
	}
	if t.Cross != nil {
		keys, problems := t.Cross(good, opts)
		for i := range good {
			good[i].Key = keys[i]
		}
		for i := len(good) - 1; i >= 0; i-- { // 문제 행을 결과에서 빼고 오류로 옮긴다
			if msg, bad := problems[i]; bad {
				res.Errors = append(res.Errors, domain.ImportRowIssue{RowNo: good[i].RowNo, Reason: msg})
				good = append(good[:i], good[i+1:]...)
			}
		}
	}
	res.Rows = good
	sortIssues(res.Errors)
	sortIssues(res.Warnings)
	return res, nil
}

func sortIssues(s []domain.ImportRowIssue) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].RowNo > s[j].RowNo; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// crossColumn 은 한 행 안의 열 사이 규칙이다(계정 코드의 앞 숫자와 구분이 같아야 한다).
func crossColumn(t Target, cells map[string]string) string {
	if t.Code == domain.ImportAccount {
		if want := kindOfCodeDigit[cells["code"][0]]; want == "" || want != cells["kind"] {
			return "구분: 코드의 앞 숫자가 가리키는 구분과 다릅니다."
		}
	}
	return ""
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func tidy(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
}

// cellText 는 칸을 문자열로 읽는다. 종류가 맞지 않으면 사유를 돌려준다.
func cellText(c Cell) (string, string) {
	switch c.Kind {
	case CellEmpty:
		return "", ""
	case CellError:
		return "", "수식 오류 값이 들어 있습니다."
	case CellDate:
		return c.Text, ""
	case CellBool:
		return c.Text, ""
	}
	return c.Text, ""
}

var digitsRe = regexp.MustCompile(`^[0-9]+$`)

// normalizeCell 은 칸 하나를 열 규칙으로 검증·정규화한다. val 이 ""·reason "" 이면 비어 있는 선택 칸이다.
// warn 은 반영은 되지만 주의할 값이고, reason 은 이 행을 오류로 만드는 정해진 문장이다(원본 값을 넣지 않는다).
func normalizeCell(c Column, cell Cell) (val, warn, reason string) {
	if cell.Kind == CellError {
		return "", "", "수식 오류 값이 들어 있습니다."
	}
	if cell.Long {
		return "", "", "칸의 글자가 너무 깁니다."
	}
	raw, _ := cellText(cell)
	empty := cell.Kind == CellEmpty || strings.TrimSpace(raw) == ""
	if empty {
		if c.Required {
			return "", "", "필수 값이 비어 있습니다."
		}
		return "", "", ""
	}
	switch c.Type {
	case TText, TCode, TDigits, TEnum, TBool:
		if cell.Kind == CellDate {
			return "", "", "글자 칸에 날짜가 들어왔습니다."
		}
		if strings.ContainsAny(raw, "\r\n") || hasControl(strings.ReplaceAll(strings.ReplaceAll(raw, " ", " "), "\t", " ")) {
			return "", "", "줄바꿈·제어문자는 쓸 수 없습니다."
		}
		s := tidy(raw)
		switch c.Type {
		case TText:
			if utf8.RuneCountInString(s) > c.Max {
				return "", "", fmt.Sprintf("%d자 이하여야 합니다.", c.Max)
			}
			return s, "", ""
		case TCode:
			if utf8.RuneCountInString(s) > c.Max || !c.Pattern.MatchString(s) {
				return "", "", c.PatMsg
			}
			if c.WarnNum && cell.Kind == CellNumber {
				warn = "글자 칸에 숫자가 들어왔습니다. 앞자리의 0 이 사라졌을 수 있으니 확인하세요."
			}
			return s, warn, ""
		case TDigits:
			d := strings.ReplaceAll(s, "-", "")
			if !digitsRe.MatchString(d) || len(d) != c.Digits {
				return "", "", fmt.Sprintf("숫자 %d자리여야 합니다(앞자리 0 이 사라지지 않았는지 확인하세요).", c.Digits)
			}
			return d, "", ""
		default: // TEnum·TBool
			if cell.Kind == CellBool {
				if cell.Text == "true" {
					s = "예"
				} else {
					s = "아니오"
				}
			}
			canon, ok := c.Enum[s]
			if !ok {
				canon, ok = c.Enum[strings.ToLower(s)]
			}
			if !ok {
				return "", "", "허용되지 않은 값입니다(" + enumChoices(c) + " 중 하나)."
			}
			return canon, "", ""
		}
	case TInt:
		return normalizeInt(c, cell, raw)
	case TDate, TMonth:
		return normalizeDate(c, cell, raw)
	}
	return "", "", "읽을 수 없는 값입니다."
}

func enumChoices(c Column) string {
	if c.Type == TBool {
		return "예·아니오"
	}
	var out []string
	for k := range c.Enum {
		out = append(out, k)
	}
	// 한글 별칭만 안내한다(결정적 순서).
	var ko []string
	for _, k := range out {
		if r, _ := utf8.DecodeRuneInString(k); r >= 0xAC00 {
			ko = append(ko, k)
		}
	}
	sortStrings(ko)
	return strings.Join(ko, "·")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func normalizeInt(c Column, cell Cell, raw string) (string, string, string) {
	var n int64
	switch cell.Kind {
	case CellNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 9e15 {
			return "", "", "정수가 아닙니다."
		}
		if math.Abs(f-math.Round(f)) > 1e-9 {
			return "", "", "소수점이 있는 값은 쓸 수 없습니다. 정수로 입력하세요."
		}
		n = int64(math.Round(f))
	case CellText:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "원"))
		s = strings.ReplaceAll(strings.ReplaceAll(s, ",", ""), " ", "")
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", "", "정수가 아닙니다(쉼표·원 표기는 가능하지만 소수점·문자는 안 됩니다)."
		}
		n = v
	default:
		return "", "", "정수가 아닙니다."
	}
	if n < c.IntMin || n > c.IntMax {
		if c.IntMax >= maxMoney {
			return "", "", fmt.Sprintf("%s 이상이어야 합니다.", commaInt(c.IntMin))
		}
		return "", "", fmt.Sprintf("%s 이상 %s 이하여야 합니다.", commaInt(c.IntMin), commaInt(c.IntMax))
	}
	return strconv.FormatInt(n, 10), "", ""
}

func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	return string(out)
}

var (
	reDateText  = regexp.MustCompile(`^(\d{4})[-./](\d{1,2})[-./](\d{1,2})$`)
	reMonthText = regexp.MustCompile(`^(\d{4})[-./](\d{1,2})$`)
)

func normalizeDate(c Column, cell Cell, raw string) (string, string, string) {
	if cell.Kind == CellDate { // 엑셀이 날짜로 바꾼 칸("2026-12" 을 12월 1일로 읽는 경우 포함)
		t, err := time.Parse("2006-01-02", cell.Text)
		if err != nil {
			return "", "", "날짜를 읽을 수 없습니다."
		}
		if c.Type == TMonth {
			return t.Format("2006-01"), "", ""
		}
		return t.Format("2006-01-02"), "", ""
	}
	if cell.Kind == CellNumber {
		return "", "", "날짜를 읽을 수 없습니다(날짜 서식이 아닌 숫자). YYYY-MM-DD 로 입력하세요."
	}
	s := strings.TrimSpace(raw)
	if c.Type == TMonth {
		if m := reMonthText.FindStringSubmatch(s); m != nil {
			y, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			if mo >= 1 && mo <= 12 && y >= 1900 && y <= 9999 {
				return fmt.Sprintf("%04d-%02d", y, mo), "", ""
			}
		}
		return "", "", "월을 읽을 수 없습니다. YYYY-MM 으로 입력하세요."
	}
	if m := reDateText.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC); t.Year() == y && int(t.Month()) == mo && t.Day() == d && y >= 1900 {
			return t.Format("2006-01-02"), "", ""
		}
	}
	return "", "", "날짜를 읽을 수 없습니다. YYYY-MM-DD 로 입력하세요."
}

// Package cardimport 는 카드사 이용내역 CSV 를 공통 행(domain.CardRow)으로 바꾸는 순수 함수 묶음이다(B32 3.3.2).
//
// 불변식
//   - 입력은 바이트뿐이다. DB·시계·네트워크·LLM 을 쓰지 않는다(같은 입력은 항상 같은 결과).
//   - 카드번호는 끝 4자리만 남긴다. 파일에 전체 번호가 있어도 나머지 조각은 결과·오류 문구 어디에도 넣지 않는다.
//   - 오류 행의 사유는 정해진 한국어 문장이고 원본 행의 값(가맹점명·카드번호 조각·금액)을 넣지 않는다.
//   - 가맹점명이 =, +, -, @ 로 시작해도 저장 값은 그대로다(내보내기에서 B28 수식 접두 규칙이 막는다).
package cardimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const (
	MaxBytes      = 5 << 20 // 파일 크기 상한
	MaxDataRows   = 10000   // 머리글을 뺀 데이터 행 상한
	maxMerchant   = 200
	errRateDenom  = 5 // 오류 행이 전체의 1/5(20%)를 넘으면 형식이 맞지 않는 것으로 본다
	maxAmountAbs  = int64(1_000_000_000_000)
	unknownFormat = "형식을 인식하지 못했습니다. 카드사 형식을 직접 고르세요."
)

// Parser 는 domain.CardParser 를 구현한다. 상태가 없다.
type Parser struct{}

var _ domain.CardParser = Parser{}

func validation(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

// decode 는 UTF-8(BOM 허용) 또는 CP949 바이트를 문자열로 바꾼다.
func decode(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return string(data), nil
	}
	out, err := korean.EUCKR.NewDecoder().Bytes(data) // x/text 의 EUCKR 는 CP949(Code Page 949)를 읽는다
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
		return "", validation("파일의 글자 인코딩을 읽을 수 없습니다. UTF-8 또는 CP949(엑셀 CSV) 파일만 올릴 수 있습니다.")
	}
	return string(out), nil
}

func normHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimPrefix(s, "\xef\xbb\xbf") {
		if r == ' ' || r == '\t' || r == ' ' || r == '_' || r == '(' || r == ')' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// 형식별 열 이름(정규화된 머리글 기준). 정확한 이름은 실제 카드사 샘플을 받으면 별칭만 더한다.
var columnAliases = map[string]map[string][]string{
	domain.CardFormatA: {
		"usedAt":   {"승인일시", "이용일시", "거래일시"},
		"card":     {"카드번호", "카드번호끝4", "카드번호끝4자리"},
		"merchant": {"가맹점명", "가맹점"},
		"amount":   {"승인금액", "이용금액", "거래금액"},
		"approval": {"승인번호"},
		"kind":     {"승인구분", "구분", "거래구분"},
	},
	domain.CardFormatB: {
		"day":      {"이용일", "이용일자", "거래일"},
		"time":     {"이용시간", "이용시각", "거래시간"},
		"card":     {"카드번호끝4", "카드번호끝4자리", "카드끝4자리", "카드번호"},
		"merchant": {"가맹점", "가맹점명"},
		"amount":   {"이용금액", "승인금액"},
		"supply":   {"공급가액", "공급가"},
		"vat":      {"부가세", "부가가치세"},
		"tip":      {"봉사료"},
		"approval": {"승인번호"},
		"cancel":   {"취소여부", "취소"},
	},
}

// 형식마다 반드시 있어야 하는 열.
var requiredColumns = map[string][]string{
	domain.CardFormatA: {"usedAt", "card", "merchant", "amount"},
	domain.CardFormatB: {"day", "card", "merchant", "amount"},
}

var columnLabels = map[string]string{
	"usedAt": "승인일시", "card": "카드번호", "merchant": "가맹점명", "amount": "금액", "day": "이용일", "time": "이용시간",
}

func indexColumns(format string, header []string) map[string]int {
	idx := map[string]int{}
	norm := make([]string, len(header))
	for i, h := range header {
		norm[i] = normHeader(h)
	}
	for key, aliases := range columnAliases[format] {
		for _, al := range aliases {
			want := normHeader(al)
			found := -1
			for i, h := range norm {
				if h == want {
					found = i
					break
				}
			}
			if found >= 0 {
				idx[key] = found
				break
			}
		}
	}
	return idx
}

func missingColumns(format string, idx map[string]int) (missing []string) {
	for _, k := range requiredColumns[format] {
		if _, ok := idx[k]; !ok {
			missing = append(missing, columnLabels[k])
		}
	}
	return missing
}

func readAll(text string) ([][]string, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	var recs [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, validation("CSV 형식이 올바르지 않습니다(%d행 근처). 파일을 다시 내려받아 올려 주세요.", len(recs)+1)
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// Detect 는 머리글로 형식을 알아낸다. 이용일·이용시간이 따로 있으면 B, 일시가 한 칸이면 A 다. 둘 다 맞지 않으면 ok=false 다.
func (Parser) Detect(data []byte) (string, bool) {
	if len(data) == 0 || len(data) > MaxBytes {
		return "", false
	}
	text, err := decode(data)
	if err != nil {
		return "", false
	}
	recs, err := readAll(text)
	if err != nil || len(recs) == 0 {
		return "", false
	}
	// 형식 B 를 먼저 본다(일·시 분리 열이 있으면 확실히 B).
	if idx := indexColumns(domain.CardFormatB, recs[0]); len(missingColumns(domain.CardFormatB, idx)) == 0 {
		if _, ok := idx["time"]; ok {
			return domain.CardFormatB, true
		}
	}
	if idx := indexColumns(domain.CardFormatA, recs[0]); len(missingColumns(domain.CardFormatA, idx)) == 0 {
		return domain.CardFormatA, true
	}
	return "", false
}

var (
	reLast4  = regexp.MustCompile(`(\d{4})\D*$`)
	reSpaces = regexp.MustCompile(`\s+`)
)

func cleanMerchant(s string) string {
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > maxMerchant {
		s = string([]rune(s)[:maxMerchant])
	}
	return s
}

// parseAmount 는 쉼표·"원"·앞뒤 공백을 허용하는 정수 금액이다. 소수점·문자가 섞이면 오류다.
func parseAmount(s string) (int64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "원"))
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > maxAmountAbs || n < -maxAmountAbs {
		return 0, false
	}
	return n, true
}

func optAmount(rec []string, idx map[string]int, key string) (*int64, bool) {
	i, ok := idx[key]
	if !ok || i >= len(rec) || strings.TrimSpace(rec[i]) == "" {
		return nil, true
	}
	n, ok := parseAmount(rec[i])
	if !ok {
		return nil, false
	}
	return &n, true
}

func cell(rec []string, idx map[string]int, key string) string {
	if i, ok := idx[key]; ok && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

var dateTimeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006.01.02 15:04:05", "2006.01.02 15:04", "2006/01/02 15:04:05", "2006/01/02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"}

func parseUsedAtA(s string) (time.Time, bool) {
	for _, l := range dateTimeLayouts {
		if t, err := time.ParseInLocation(l, s, contract.KST); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseUsedAtB(day, tm string) (time.Time, bool) {
	day = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(day, "-", ""), ".", ""), "/", "")
	if len(day) != 8 {
		return time.Time{}, false
	}
	tm = strings.ReplaceAll(tm, ":", "")
	switch len(tm) {
	case 0:
		tm = "000000"
	case 4:
		tm += "00"
	case 6:
	default:
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102150405", day+tm, contract.KST)
	return t, err == nil
}

func isCancelWord(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "취소" || s == "y" || s == "yes" || s == "승인취소" || s == "전체취소" || s == "부분취소"
}

// Parse 는 형식(비면 Detect)에 따라 정규화한다. 5MB·10,000행 초과, 인코딩 불가, 형식 불명은 ErrValidation 계열 오류다.
func (p Parser) Parse(data []byte, format string) (domain.CardParseResult, error) {
	var res domain.CardParseResult
	if len(data) == 0 {
		return res, validation("빈 파일입니다.")
	}
	if len(data) > MaxBytes {
		return res, validation("파일이 너무 큽니다(최대 5MB).")
	}
	text, err := decode(data)
	if err != nil {
		return res, err
	}
	recs, err := readAll(text)
	if err != nil {
		return res, err
	}
	if len(recs) == 0 {
		return res, validation("빈 파일입니다.")
	}
	if format == "" {
		f, ok := p.Detect(data)
		if !ok {
			return res, validation("%s", unknownFormat)
		}
		format = f
	}
	if _, known := columnAliases[format]; !known {
		return res, validation("알 수 없는 카드사 형식입니다.")
	}
	idx := indexColumns(format, recs[0])
	if miss := missingColumns(format, idx); len(miss) > 0 {
		return res, validation("선택한 형식에 필요한 열이 파일에 없습니다(누락: %s). 카드사 형식을 다시 고르세요.", strings.Join(miss, ", "))
	}
	res.Format = format
	type rawRow struct {
		no  int
		rec []string
	}
	var rows []rawRow
	for i, rec := range recs[1:] { // 완전히 빈 줄은 데이터 행으로 세지 않는다
		blank := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				blank = false
				break
			}
		}
		if !blank {
			rows = append(rows, rawRow{no: i + 2, rec: rec})
		}
	}
	if len(rows) > MaxDataRows {
		return res, validation("행이 너무 많습니다(최대 %d행). 기간을 나눠 올려 주세요.", MaxDataRows)
	}
	res.TotalRows = len(rows)
	for _, rr := range rows {
		row, reason := parseRow(format, idx, rr.rec)
		if reason != "" {
			res.Errors = append(res.Errors, domain.CardRowError{RowNo: rr.no, Reason: reason})
			continue
		}
		row.RowNo = rr.no
		res.Rows = append(res.Rows, row)
	}
	res.TooManyError = res.TotalRows > 0 && len(res.Errors)*errRateDenom > res.TotalRows
	return res, nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func signed(p *int64, negative bool) *int64 {
	if p == nil {
		return nil
	}
	v := abs(*p)
	if negative {
		v = -v
	}
	return &v
}

// parseRow 는 한 행을 정규화한다. 실패하면 사유(정해진 문장)를 돌려준다. 사유에 원본 값을 넣지 않는다.
func parseRow(format string, idx map[string]int, rec []string) (domain.CardRow, string) {
	var row domain.CardRow
	m := reLast4.FindStringSubmatch(cell(rec, idx, "card"))
	if m == nil {
		return row, "카드번호 끝 4자리를 읽을 수 없습니다."
	}
	row.Last4 = m[1]
	row.Merchant = cleanMerchant(cell(rec, idx, "merchant"))
	if row.Merchant == "" {
		return row, "가맹점명이 비어 있습니다."
	}
	amount, ok := parseAmount(cell(rec, idx, "amount"))
	if !ok {
		return row, "금액이 정수가 아닙니다."
	}
	if amount == 0 {
		return row, "금액은 0 이 아닌 정수여야 합니다."
	}
	var cancel bool
	switch format {
	case domain.CardFormatA:
		t, ok := parseUsedAtA(cell(rec, idx, "usedAt"))
		if !ok {
			return row, "승인일시를 읽을 수 없습니다."
		}
		row.UsedAt = t
		cancel = isCancelWord(cell(rec, idx, "kind"))
	case domain.CardFormatB:
		t, ok := parseUsedAtB(cell(rec, idx, "day"), cell(rec, idx, "time"))
		if !ok {
			return row, "이용일·이용시간을 읽을 수 없습니다."
		}
		row.UsedAt = t
		cancel = isCancelWord(cell(rec, idx, "cancel"))
	}
	row.ApprovalNo = cell(rec, idx, "approval")
	supply, ok1 := optAmount(rec, idx, "supply")
	vat, ok2 := optAmount(rec, idx, "vat")
	tip, ok3 := optAmount(rec, idx, "tip")
	if !ok1 || !ok2 || !ok3 {
		return row, "공급가액·부가세·봉사료가 정수가 아닙니다."
	}
	// 취소: 구분 열이 취소이거나 금액이 음수다. 취소는 금액·공급가·부가세·봉사료를 모두 음수로, 승인은 모두 양수로 맞춘다.
	neg := cancel || amount < 0
	row.Kind = domain.CardKindApprove
	if neg {
		row.Kind = domain.CardKindCancel
	}
	row.Amount = abs(amount)
	if neg {
		row.Amount = -row.Amount
	}
	row.Supply, row.VAT, row.Tip = signed(supply, neg), signed(vat, neg), signed(tip, neg)
	return row, ""
}

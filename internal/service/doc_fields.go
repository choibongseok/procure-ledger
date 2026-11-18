package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 양식 필드 값 검증(명세 3.3). 임시저장(strict=false)과 상신(strict=true)이 같은 함수를 쓴다.
// 문구는 서버 400 과 화면이 같은 한국어 문장이다.

const (
	defaultTextMax     = 200
	defaultTextareaMax = 5000
	defaultTableMax    = 100
	defaultFilesMax    = 20
)

// FieldError 는 필드 하나의 오류다.
type FieldError struct {
	Key     string
	Message string
}

// FieldErrors 는 필드 키별 오류 모음이다. errors.Is(err, contract.ErrValidation) 이다.
type FieldErrors struct{ Items []FieldError }

func (e *FieldErrors) Error() string {
	parts := make([]string, len(e.Items))
	for i, it := range e.Items {
		parts[i] = it.Message
	}
	return contract.ErrValidation.Error() + ": " + strings.Join(parts, " ")
}

func (e *FieldErrors) Unwrap() error { return contract.ErrValidation }

// ByKey 는 필드 키 → 문구 표다.
func (e *FieldErrors) ByKey() map[string]string {
	m := map[string]string{}
	for _, it := range e.Items {
		if _, ok := m[it.Key]; !ok {
			m[it.Key] = it.Message
		}
	}
	return m
}

// ValidateFields 는 values(JSON 으로 풀린 값: 문자열·float64·[]any·map[string]any)를 정의대로 검사한다.
// 정의에 없는 키는 거부하고, strict 이면 필수 항목이 비어 있는 것도 거부한다. 오류는 필드 키별로 모아 돌려준다.
func ValidateFields(defs []domain.FieldDef, values map[string]any, strict bool) error {
	errs := &FieldErrors{}
	byKey := map[string]domain.FieldDef{}
	for _, d := range defs {
		byKey[d.Key] = d
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := byKey[k]; !ok {
			errs.Items = append(errs.Items, FieldError{Key: k, Message: fmt.Sprintf("「%s」은(는) 이 양식에 없는 항목입니다.", k)})
		}
	}
	for _, d := range defs {
		validateValue(errs, d.Key, d, values[d.Key], strict)
	}
	// 금액 = 표 합계 규칙(sumOf): 상신 때(strict)만 검사한다. 임시저장은 입력 도중일 수 있다.
	if strict {
		for _, d := range defs {
			if d.SumOf == "" || isEmptyValue(values[d.Key]) {
				continue
			}
			if _, bad := errs.ByKey()[d.Key]; bad {
				continue
			}
			tableKey, colKey, ok := strings.Cut(d.SumOf, ".")
			if !ok {
				continue
			}
			var sum int64
			rows, _ := values[tableKey].([]any)
			for _, r := range rows {
				if rm, ok := r.(map[string]any); ok {
					if n, ok := asInt(rm[colKey]); ok {
						sum += n
					}
				}
			}
			if got, ok := asInt(values[d.Key]); ok && got != sum {
				fieldErr(errs, d.Key, "「%s」은(는) %s의 합계(%s)와 같아야 합니다.", d.Label, tableLabel(defs, tableKey), moneyLabel(sum))
			}
		}
	}
	if len(errs.Items) == 0 {
		return nil
	}
	return errs
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func fieldErr(errs *FieldErrors, key, format string, a ...any) {
	errs.Items = append(errs.Items, FieldError{Key: key, Message: fmt.Sprintf(format, a...)})
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return int64(x), true
		}
	case int64:
		return x, true
	case int:
		return int64(x), true
	}
	return 0, false
}

func validateValue(errs *FieldErrors, key string, d domain.FieldDef, v any, strict bool) {
	if isEmptyValue(v) {
		if strict && d.Required {
			fieldErr(errs, key, "「%s」을(를) 입력하세요.", d.Label)
		}
		return
	}
	bad := func() { fieldErr(errs, key, "「%s」의 형식이 올바르지 않습니다.", d.Label) }
	switch d.Type {
	case domain.FieldText, domain.FieldTextarea:
		s, ok := v.(string)
		if !ok {
			bad()
			return
		}
		max := d.MaxLength
		if max == 0 {
			max = defaultTextMax
			if d.Type == domain.FieldTextarea {
				max = defaultTextareaMax
			}
		}
		if d.Type == domain.FieldText && strings.ContainsAny(s, "\r\n") {
			bad()
			return
		}
		if utf8.RuneCountInString(s) > max {
			fieldErr(errs, key, "「%s」은(는) %d자 이내로 입력하세요.", d.Label, max)
		}
	case domain.FieldNumber:
		f, ok := v.(float64)
		if !ok {
			bad()
			return
		}
		if d.Decimals == 0 && f != math.Trunc(f) {
			fieldErr(errs, key, "「%s」은(는) 정수로 입력하세요.", d.Label)
			return
		}
		if d.Min != nil && f < float64(*d.Min) || d.Max != nil && f > float64(*d.Max) {
			fieldErr(errs, key, "「%s」의 범위를 벗어났습니다.", d.Label)
		}
	case domain.FieldAmount:
		n, ok := asInt(v)
		if !ok {
			bad()
			return
		}
		min := int64(0)
		if d.Min != nil {
			min = *d.Min
		}
		if n < min || d.Max != nil && n > *d.Max {
			fieldErr(errs, key, "「%s」의 범위를 벗어났습니다.", d.Label)
		}
	case domain.FieldDate:
		s, ok := v.(string)
		if !ok {
			bad()
			return
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			bad()
		}
	case domain.FieldSelect:
		s, ok := v.(string)
		if !ok {
			bad()
			return
		}
		found := false
		for _, o := range d.Options {
			found = found || o == s
		}
		if !found {
			fieldErr(errs, key, "「%s」은(는) 목록에서 선택하세요.", d.Label)
		}
	case domain.FieldUnit:
		if n, ok := asInt(v); !ok || n <= 0 {
			bad()
		}
	case domain.FieldTable:
		rows, ok := v.([]any)
		if !ok {
			bad()
			return
		}
		max := d.MaxRows
		if max == 0 {
			max = defaultTableMax
		}
		if len(rows) > max {
			fieldErr(errs, key, "「%s」은(는) %d행 이내로 입력하세요.", d.Label, max)
			return
		}
		if strict && len(rows) < d.MinRows {
			fieldErr(errs, key, "「%s」은(는) %d행 이상 입력하세요.", d.Label, d.MinRows)
		}
		for i, r := range rows {
			rm, ok := r.(map[string]any)
			if !ok {
				bad()
				return
			}
			for _, c := range d.Columns {
				validateValue(errs, fmt.Sprintf("%s[%d].%s", key, i, c.Key), domain.FieldDef{Label: fmt.Sprintf("%s %d행 %s", d.Label, i+1, c.Label),
					Type: c.Type, Required: c.Required, MaxLength: c.MaxLength, Min: c.Min, Max: c.Max, Decimals: c.Decimals, Options: c.Options}, rm[c.Key], strict)
			}
			for k := range rm {
				known := false
				for _, c := range d.Columns {
					known = known || c.Key == k
				}
				if !known {
					fieldErr(errs, fmt.Sprintf("%s[%d].%s", key, i, k), "「%s」에 없는 열입니다.", d.Label)
				}
			}
		}
	case domain.FieldAttachment:
		ids, ok := v.([]any)
		if !ok {
			bad()
			return
		}
		max := d.MaxFiles
		if max == 0 {
			max = defaultFilesMax
		}
		if len(ids) > max {
			fieldErr(errs, key, "「%s」은(는) %d개까지 첨부할 수 있습니다.", d.Label, max)
			return
		}
		for _, id := range ids {
			if n, ok := asInt(id); !ok || n <= 0 {
				bad()
				return
			}
		}
	default:
		bad()
	}
}

// IsFieldErrors 는 err 가 필드 검증 오류인지 보고 돌려준다.
func IsFieldErrors(err error) (*FieldErrors, bool) {
	var fe *FieldErrors
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

func tableLabel(defs []domain.FieldDef, key string) string {
	for _, d := range defs {
		if d.Key == key {
			return d.Label
		}
	}
	return key
}

// formatWon 은 천 단위 구분 쉼표를 넣은 숫자 문자열이다.
// moneyLabel 은 금액 표기의 유일한 함수다("1,234원"). 금액 뒤에 "원"을 붙이는 문구는 모두 이 함수를 거친다(B34 소스 검사).
func moneyLabel(n int64) string { return formatWon(n) + "원" }

func formatWon(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

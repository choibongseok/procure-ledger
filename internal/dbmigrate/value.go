package dbmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// convert 는 SQLite 값을 PG 열 형식으로 바꾼다. 바꿀 수 없으면(무효 UTF-8·NUL 바이트·범위 초과·숫자가 아닌 글자) 오류다(E10).
// SQLite 는 열 형식이 느슨해서 BIGINT 열에 글자 "5" 가 들어 있을 수 있어 여기서 정리한다.
func convert(v any, pgType string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch pgType {
	case "bigint", "integer", "smallint":
		switch x := v.(type) {
		case int64:
			return x, nil
		case float64:
			if x == float64(int64(x)) {
				return int64(x), nil
			}
			return nil, fmt.Errorf("정수 열에 소수 %v", x)
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("정수 열에 숫자가 아닌 글자")
			}
			return n, nil
		case []byte:
			return convert(string(x), pgType)
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
	case "text", "character varying", "character":
		switch x := v.(type) {
		case string:
			return checkText(x)
		case []byte:
			return checkText(string(x))
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64), nil
		case time.Time:
			return x.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	case "bytea":
		switch x := v.(type) {
		case []byte:
			return x, nil
		case string:
			return []byte(x), nil
		}
	case "double precision", "real", "numeric":
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		}
	case "boolean":
		switch x := v.(type) {
		case bool:
			return x, nil
		case int64:
			return x != 0, nil
		}
	}
	return nil, fmt.Errorf("%T 값을 %s 열로 옮길 수 없습니다", v, pgType)
}

func checkText(s string) (any, error) {
	if !utf8.ValidString(s) {
		return nil, fmt.Errorf("올바르지 않은 UTF-8 글자가 들어 있습니다")
	}
	if strings.IndexByte(s, 0) >= 0 {
		return nil, fmt.Errorf("NUL 문자가 들어 있습니다")
	}
	return s, nil
}

// canon 은 변환된 값의 정규 직렬화다(원본·대상이 같은 값이면 같은 글자가 되도록).
func canon(v any) string {
	switch x := v.(type) {
	case nil:
		return "N"
	case int64:
		return "i" + strconv.FormatInt(x, 10)
	case int32:
		return "i" + strconv.FormatInt(int64(x), 10)
	case string:
		return "s" + strconv.Itoa(len(x)) + ":" + x
	case []byte:
		return "b" + hex.EncodeToString(x)
	case float64:
		return "f" + strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "o1"
		}
		return "o0"
	case time.Time:
		return "s20:" + x.UTC().Format("2006-01-02T15:04:05Z")
	}
	return fmt.Sprintf("?%v", v)
}

// tableSum 은 표 하나의 내용 체크섬을 쌓는다. 행마다 해시를 만들고 정렬해서 합치므로 행 순서와 무관하다(원본·대상의 정렬 규칙이 달라도 같다).
type tableSum struct{ hashes []string }

func (s *tableSum) add(row []any) {
	h := sha256.New()
	for i, v := range row {
		if i > 0 {
			h.Write([]byte{0x1f})
		}
		h.Write([]byte(canon(v)))
	}
	s.hashes = append(s.hashes, string(h.Sum(nil)))
}

func (s *tableSum) sum() string {
	sort.Strings(s.hashes)
	h := sha256.New()
	for _, x := range s.hashes {
		h.Write([]byte(x))
	}
	return hex.EncodeToString(h.Sum(nil))
}

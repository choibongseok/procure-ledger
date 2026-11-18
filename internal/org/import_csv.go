package org

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"

	"github.com/choibongseok/procure-ledger/internal/export"
)

// 조직 데이터 CSV 일괄 반영(B22, 6절)의 파일 읽기: 글자 인코딩 판별과 CSV 분해.

// 크기 상한(명세 6.2).
const (
	ImportMaxFileBytes = 5 << 20
	ImportMaxRows      = 5000
)

// 칸 값 최대 길이(명세 6.2, B20 입력 한도와 같다).
const (
	maxUnitNameLen   = 40
	maxPersonNameLen = 30
	maxRankLen       = 20
	maxReasonLen     = 200
	maxOtherLen      = 100
)

// DecodeCSVText 는 파일 바이트를 문자열로 바꾼다. BOM 이 있으면 UTF-8, 없으면 UTF-8 로 엄격하게 검증하고 통과하면 UTF-8,
// 아니면 EUC-KR(CP949)로 읽어 본다. 돌려주는 enc 는 "UTF-8" 또는 "EUC-KR" 이다(미리보기에 "…로 읽었습니다"로 보인다).
func DecodeCSVText(data []byte) (text, enc string, err error) {
	if len(data) > ImportMaxFileBytes {
		return "", "", errors.New("파일이 5MB를 넘습니다.")
	}
	if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return "", "", errors.New("지원하지 않는 형식입니다. UTF-8 CSV 로 저장하세요.")
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		body := data[3:]
		if !utf8.Valid(body) {
			return "", "", errors.New("파일 글자 인코딩을 알 수 없습니다. UTF-8 로 저장해 다시 올려 주세요.")
		}
		return string(body), "UTF-8", nil
	}
	// UTF-16 BOM 없는 형태(NUL 바이트가 많은 파일)도 같은 안내로 거부한다.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", errors.New("지원하지 않는 형식입니다. UTF-8 CSV 로 저장하세요.")
	}
	if utf8.Valid(data) {
		return string(data), "UTF-8", nil
	}
	dec, derr := korean.EUCKR.NewDecoder().Bytes(data)
	if derr != nil || !utf8.Valid(dec) || bytes.ContainsRune(dec, utf8.RuneError) {
		return "", "", errors.New("파일 글자 인코딩을 알 수 없습니다. UTF-8 로 저장해 다시 올려 주세요.")
	}
	return string(dec), "EUC-KR", nil
}

// CSVRow 는 데이터 한 행이다. Line 은 편집기에서 보이는 줄 번호(제목 줄이 1행)다.
type CSVRow struct {
	Line  int
	Cells map[string]string // 열 이름 → 값(앞뒤 공백 제거, 빈 칸은 "")
	Raw   []string          // 원본 칸(오류 행 내려받기용, 제목 줄과 같은 순서)
}

// CSVTable 은 제목 줄과 데이터 행이다.
type CSVTable struct {
	Header []string
	Rows   []CSVRow
}

// ParseCSV 는 RFC 4180 형식을 읽는다. 칸 안의 줄바꿈·제어문자는 거부하고, 빈 줄은 건너뛴다.
func ParseCSV(text string) (*CSVTable, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = false
	header, err := r.Read()
	if err == io.EOF {
		return nil, errors.New("제목 줄이 없습니다.")
	}
	if err != nil {
		return nil, csvErr(err)
	}
	seen := map[string]bool{}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
		if header[i] == "" {
			continue
		}
		if seen[header[i]] {
			return nil, fmt.Errorf("제목 줄에 같은 열이 둘 있습니다(%s).", header[i])
		}
		seen[header[i]] = true
	}
	t := &CSVTable{Header: header}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, csvErr(err)
		}
		line, _ := r.FieldPos(0)
		if len(t.Rows) >= ImportMaxRows {
			return nil, fmt.Errorf("행이 5,000개를 넘습니다. 나누어 올려 주세요.")
		}
		row := CSVRow{Line: line, Cells: map[string]string{}, Raw: rec}
		blank := true
		for i, v := range rec {
			if strings.TrimSpace(v) != "" {
				blank = false
			}
			if i < len(header) && header[i] != "" {
				row.Cells[header[i]] = strings.TrimSpace(v)
			}
		}
		if blank {
			continue
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

func csvErr(err error) error {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		return fmt.Errorf("%d행을 읽을 수 없습니다. 큰따옴표와 쉼표를 확인하세요.", pe.Line)
	}
	return errors.New("파일을 읽을 수 없습니다.")
}

// cellProblem 은 칸 값이 받을 수 없는 모양이면 그 이유를 돌려준다(제어문자, 칸 안의 줄바꿈).
func cellProblem(v string) string {
	for _, r := range v {
		if r == '\n' || r == '\r' {
			return "칸 안에 줄바꿈이 있어 받을 수 없습니다."
		}
		if r < 0x20 || r == 0x7f {
			return "제어문자가 있어 받을 수 없습니다."
		}
	}
	return ""
}

// formulaStart 는 엑셀이 수식으로 읽을 수 있는 첫 글자이면 그 글자를 돌려준다(명세 6.8).
func formulaStart(v string) string {
	if v == "" {
		return ""
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return string(v[0])
	}
	return ""
}

// CSVSafe 는 서버가 만들어 주는 CSV 의 칸 값을 안전하게 만든다(csvSafe 의 내보내기용 이름).
func CSVSafe(v string) string { return csvSafe(v) }

// csvSafe 는 서버가 만들어 주는 CSV 의 칸 앞에 작은따옴표를 붙여 엑셀이 수식으로 실행하지 않게 한다(명세 6.8).
// 규칙은 내보내기 공용 부품(internal/export, B28)과 같은 한 함수를 쓴다.
func csvSafe(v string) string { return export.CSVSafe(v) }

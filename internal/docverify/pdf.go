package docverify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFContentType 은 PDF 업로드의 콘텐츠 타입이다. 서비스는 이 타입이면 Image 필드의 바이트를 PDF 원본으로 본다.
const PDFContentType = "application/pdf"

// ErrNoTextLayer 는 PDF 에 읽을 수 있는 텍스트 레이어가 없을 때의 오류다(스캔본 등). 추측하지 않고 판단 보류로 처리한다.
var ErrNoTextLayer = errors.New("텍스트 레이어가 없는 PDF 입니다")

const maxPDFText = maxTextBytes

// ErrUnreadableText 는 PDF 에 텍스트가 있지만 글자로 옮길 수 없을 때의 오류다(글꼴 매핑 누락 등으로 한글이 깨짐).
// 깨진 글자로 판정하지 않고 판단 보류로 처리한다.
var ErrUnreadableText = errors.New("글자를 읽을 수 없는 PDF 입니다")

// ExtractPDFText 는 PDF 의 텍스트 레이어를 읽는다. 라이브러리가 깨진 파일에서 패닉할 수 있어 복구한다.
// 읽은 글자가 없으면 ErrNoTextLayer, 글자가 깨졌으면 ErrUnreadableText, 파일을 해석할 수 없으면 해석 오류를 돌려준다.
//
// 라이브러리의 평문 추출은 CID 글꼴(한글 PDF 대부분)에서 줄바꿈을 글꼴 매핑으로 디코딩해 대체 문자(U+FFFD)로 바꾼다.
// 그래서 대체 문자가 보이면 행(Y 좌표) 단위 추출로 다시 읽고, 그래도 대체 문자가 남으면 깨진 것으로 본다.
func ExtractPDFText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("PDF 를 해석할 수 없습니다")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("PDF 를 해석할 수 없습니다")
	}
	out, err := plainText(r)
	if errors.Is(err, errTooLong) {
		return "", fmt.Errorf("PDF 텍스트가 너무 깁니다")
	}
	if err != nil || garbled(out) {
		rows, rerr := rowText(r)
		if rerr != nil {
			return "", ErrUnreadableText
		}
		if len(rows) > maxPDFText {
			return "", fmt.Errorf("PDF 텍스트가 너무 깁니다")
		}
		out = strings.TrimSpace(rows)
	}
	if garbled(out) {
		return "", ErrUnreadableText
	}
	if out == "" {
		return "", ErrNoTextLayer
	}
	return out, nil
}

// rowText 는 페이지의 텍스트 연산을 순서대로 읽고 Y 좌표가 바뀌면 줄을 나눈다.
// 라이브러리의 행 단위 API(GetTextByRow)는 T* 를 무시해 한 줄로 뭉치므로 Content() 를 쓴다.
func rowText(r *pdf.Reader) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			text, err = "", errors.New("글자 변환 실패")
		}
	}()
	var sb strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		first, lastY := true, 0.0
		for _, t := range r.Page(i).Content().Text {
			if !first && math.Abs(t.Y-lastY) >= 1 {
				sb.WriteString("\n")
			}
			sb.WriteString(t.S)
			first, lastY = false, t.Y
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// garbled 는 글꼴 매핑 누락으로 생기는 흔적(대체 문자, 제어 문자)이 있는지 본다.
func garbled(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' || (r < 0x20 && r != '\n' && r != '\t' && r != '\r') {
			return true
		}
	}
	return false
}

var errTooLong = errors.New("too long")

// plainText 는 라이브러리 평문 추출이다. 글꼴 매핑이 없는 PDF 에서 패닉하므로 오류로 바꾼다.
func plainText(r *pdf.Reader) (out string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out, err = "", errors.New("글자 변환 실패")
		}
	}()
	pr, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(pr, maxPDFText+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxPDFText {
		return "", errTooLong
	}
	return strings.TrimSpace(string(b)), nil
}

// ExtractPDFPages 는 PDF 의 텍스트 레이어를 쪽 단위로 읽는다(B47 사내 지식 검색: 출처에 쪽 번호를 달기 위해). 돌려주는 목록의 i 번째가 i+1 쪽이고 글자 없는 쪽은 빈 문자열이다.
// 오류 규칙은 ExtractPDFText 와 같다: 모든 쪽에 글자가 없으면 ErrNoTextLayer, 글자가 깨졌으면 ErrUnreadableText, 해석 불가·너무 긺은 일반 오류. 라이브러리 패닉은 복구한다.
func ExtractPDFPages(data []byte) (pages []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			pages, err = nil, fmt.Errorf("PDF 를 해석할 수 없습니다")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("PDF 를 해석할 수 없습니다")
	}
	total := 0
	any := false
	for i := 1; i <= r.NumPage(); i++ {
		txt, perr := pagePlainText(r, i)
		if perr != nil || garbled(txt) {
			rows, rerr := pageRowText(r, i)
			if rerr != nil {
				return nil, ErrUnreadableText
			}
			txt = strings.TrimSpace(rows)
		}
		if garbled(txt) {
			return nil, ErrUnreadableText
		}
		total += len(txt)
		if total > maxPDFText {
			return nil, fmt.Errorf("PDF 텍스트가 너무 깁니다")
		}
		if txt != "" {
			any = true
		}
		pages = append(pages, txt)
	}
	if !any {
		return nil, ErrNoTextLayer
	}
	return pages, nil
}

func pagePlainText(r *pdf.Reader, i int) (out string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out, err = "", errors.New("글자 변환 실패")
		}
	}()
	t, err := r.Page(i).GetPlainText(nil)
	return strings.TrimSpace(t), err
}

func pageRowText(r *pdf.Reader, i int) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			text, err = "", errors.New("글자 변환 실패")
		}
	}()
	var sb strings.Builder
	first, lastY := true, 0.0
	for _, t := range r.Page(i).Content().Text {
		if !first && math.Abs(t.Y-lastY) >= 1 {
			sb.WriteString("\n")
		}
		sb.WriteString(t.S)
		first, lastY = false, t.Y
	}
	return sb.String(), nil
}

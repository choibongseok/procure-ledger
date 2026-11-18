package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 업로드 문서(매뉴얼·규정) 텍스트 추출(B47 2단계, 명세 7절). PDF 는 쪽 단위(쪽 안에 제N조 줄이 있으면 조항 표지도), DOCX 는 제목 스타일 소제목, MD 는 `## 제N조 (제목)` 조항과 소제목이다.
// 추출은 업로드 서비스(저장 전 시험)와 색인 워커가 같은 함수를 쓴다. 실패는 사용자에게 보일 한국어 문구를 담은 *ExtractError 다.

// Section 은 추출된 한 덩어리다(쪽·조항·소제목·문단 묶음).
type Section struct {
	LocatorKind string // page | clause | heading | para
	Locator     string
	Heading     string
	Text        string
}

// ExtractError 는 사용자에게 보여 줄 거부 사유다(영문 코드·내부 경로 없음).
type ExtractError struct{ Msg string }

func (e *ExtractError) Error() string { return e.Msg }

func reject(format string, a ...any) error { return &ExtractError{Msg: fmt.Sprintf(format, a...)} }

// 거부 문구(명세 11.2).
const (
	MsgFormat     = "PDF·DOCX·MD 파일만 올릴 수 있습니다."
	MsgNoText     = "글자를 읽을 수 없는 파일입니다. 스캔한 그림 PDF 는 지원하지 않습니다. 글자가 있는 PDF·DOCX·MD 로 올려 주세요."
	MsgGarbled    = "한글이 깨져 읽히는 PDF 입니다. DOCX·MD 로 올리거나 글꼴을 포함해 다시 저장한 PDF 를 올려 주세요."
	MsgBadPDF     = "PDF 를 읽을 수 없습니다. 파일이 손상되지 않았는지 확인하세요."
	MsgDocxLocked = "암호가 걸렸거나 매크로가 든 문서는 올릴 수 없습니다."
	MsgBadDocx    = "DOCX 를 읽을 수 없습니다. 파일이 손상되지 않았는지 확인하세요."
	MsgEmpty      = "읽을 수 있는 내용이 없습니다."
	MsgNotUTF8    = "MD 파일은 UTF-8 로 저장해 올려 주세요."
	MsgTooBig     = "문서의 압축을 풀면 너무 큽니다."
)

const maxDocxXML = 50 << 20 // 풀린 크기 상한(압축 폭탄 방어)

// Ext 는 지원하는 업로드 확장자(소문자, 점 포함)다. 지원하지 않으면 빈 문자열.
func Ext(filename string) string {
	switch e := strings.ToLower(path.Ext(strings.TrimSpace(filename))); e {
	case ".pdf", ".docx", ".md":
		return e
	}
	return ""
}

// ContentTypeOf 는 서버가 확장자로 정하는 내용 유형이다(클라이언트가 준 값은 믿지 않는다).
func ContentTypeOf(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".md":
		return "text/markdown; charset=utf-8"
	}
	return "application/octet-stream"
}

// ExtractUpload 는 파일 내용에서 구역(쪽·조항·소제목·문단)을 뽑는다.
func ExtractUpload(filename string, data []byte) ([]Section, error) {
	var secs []Section
	var err error
	switch Ext(filename) {
	case ".pdf":
		secs, err = extractPDF(data)
	case ".docx":
		secs, err = extractDocx(data)
	case ".md":
		secs, err = extractMarkdown(data)
	default:
		return nil, reject(MsgFormat)
	}
	if err != nil {
		return nil, err
	}
	var out []Section
	for _, s := range secs {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, reject(MsgEmpty)
	}
	return out, nil
}

var clauseLine = regexp.MustCompile(`^\s*(제\s*\d+\s*조)(?:\s*\(([^)]*)\))?\s*(.*)$`)

func clauseNo(s string) string { return strings.Join(strings.Fields(s), "") }

func extractPDF(data []byte) ([]Section, error) {
	pages, err := docverify.ExtractPDFPages(data)
	switch {
	case errors.Is(err, docverify.ErrNoTextLayer):
		return nil, reject(MsgNoText)
	case errors.Is(err, docverify.ErrUnreadableText):
		return nil, reject(MsgGarbled)
	case err != nil:
		return nil, reject(MsgBadPDF)
	}
	var out []Section
	for i, txt := range pages {
		page := fmt.Sprint(i + 1)
		if strings.TrimSpace(txt) == "" {
			continue
		}
		var cur *Section
		var pre []string
		hasClause := false
		for _, line := range strings.Split(txt, "\n") {
			if m := clauseLine.FindStringSubmatch(line); m != nil {
				hasClause = true
				if cur != nil {
					out = append(out, *cur)
				}
				head := strings.TrimSpace(m[2])
				rest := strings.TrimSpace(m[3])
				if head == "" && utf8.RuneCountInString(rest) <= 30 {
					head, rest = rest, ""
				}
				cur = &Section{LocatorKind: "clause", Locator: page + "쪽 " + clauseNo(m[1]), Heading: head, Text: rest}
				continue
			}
			if cur != nil {
				cur.Text += "\n" + line
			} else {
				pre = append(pre, line)
			}
		}
		if cur != nil {
			out = append(out, *cur)
		}
		if p := strings.Join(pre, "\n"); strings.TrimSpace(p) != "" {
			kind := "page"
			if hasClause {
				kind = "page" // 쪽 첫머리의 조항 이어짐·머리글은 쪽 표지로 둔다
			}
			out = append(out, Section{LocatorKind: kind, Locator: page, Text: p})
		}
	}
	return out, nil
}

var mdClause = regexp.MustCompile(`^#{1,6}\s*(제\s*\d+\s*조)\s*(?:\(([^)]*)\))?\s*(.*)$`)
var mdHeading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)

func extractMarkdown(data []byte) ([]Section, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, reject(MsgNotUTF8)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" { // 머리말(front matter)은 색인하지 않는다
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				lines = lines[i+1:]
				break
			}
		}
	}
	var out []Section
	var cur *Section
	var pre []string
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if m := mdClause.FindStringSubmatch(l); m != nil {
			flush()
			head := strings.TrimSpace(m[2])
			rest := strings.TrimSpace(m[3])
			if head == "" {
				head, rest = rest, ""
			}
			cur = &Section{LocatorKind: "clause", Locator: clauseNo(m[1]), Heading: head, Text: rest}
			continue
		}
		if m := mdHeading.FindStringSubmatch(l); m != nil {
			flush()
			h := strings.TrimSpace(m[1])
			cur = &Section{LocatorKind: "heading", Locator: clipRunes(h, 60), Heading: h}
			continue
		}
		if cur != nil {
			cur.Text += "\n" + l
		} else {
			pre = append(pre, l)
		}
	}
	flush()
	if p := strings.TrimSpace(strings.Join(pre, "\n")); p != "" {
		out = append([]Section{{LocatorKind: "para", Locator: "문단 1", Text: p}}, out...)
	}
	if len(out) == 1 && out[0].LocatorKind == "para" {
		return paragraphSections(strings.Split(out[0].Text, "\n\n")), nil
	}
	return out, nil
}

// paragraphSections 는 소제목이 없는 글을 800자 안팎 문단 묶음으로 나눈다(위치 표지는 시작 문단 번호).
func paragraphSections(paras []string) []Section {
	var out []Section
	var cur strings.Builder
	start := 0
	for i, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(p) > ChunkRunes {
			out = append(out, Section{LocatorKind: "para", Locator: fmt.Sprintf("문단 %d", start+1), Text: cur.String()})
			cur.Reset()
		}
		if cur.Len() == 0 {
			start = i
		} else {
			cur.WriteString("\n")
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		out = append(out, Section{LocatorKind: "para", Locator: fmt.Sprintf("문단 %d", start+1), Text: cur.String()})
	}
	return out
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---- DOCX ----

var headingStyle = regexp.MustCompile(`(?i)^(heading\s*\d*|제목\s*\d*|title)$`)

func extractDocx(data []byte) ([]Section, error) {
	if bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}) { // 암호가 걸린 문서는 OLE 컨테이너다
		return nil, reject(MsgDocxLocked)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, reject(MsgBadDocx)
	}
	var doc, styles *zip.File
	for _, f := range zr.File {
		switch strings.ToLower(f.Name) {
		case "word/document.xml":
			doc = f
		case "word/styles.xml":
			styles = f
		case "word/vbaproject.bin":
			return nil, reject(MsgDocxLocked)
		}
	}
	if doc == nil {
		return nil, reject(MsgBadDocx)
	}
	names := map[string]string{}
	if styles != nil {
		if b, err := readZip(styles); err == nil {
			names = styleNames(b)
		} else if errors.Is(err, errTooBig) {
			return nil, reject(MsgTooBig)
		}
	}
	b, err := readZip(doc)
	if err != nil {
		if errors.Is(err, errTooBig) {
			return nil, reject(MsgTooBig)
		}
		return nil, reject(MsgBadDocx)
	}
	blocks, err := docxBlocks(b, names)
	if err != nil {
		return nil, reject(MsgBadDocx)
	}
	hasHeading := false
	for _, bl := range blocks {
		if bl.heading {
			hasHeading = true
		}
	}
	if !hasHeading {
		paras := make([]string, 0, len(blocks))
		for _, bl := range blocks {
			paras = append(paras, bl.text)
		}
		return paragraphSections(paras), nil
	}
	var out []Section
	var cur *Section
	var pre []string
	for _, bl := range blocks {
		if bl.heading {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Section{LocatorKind: "heading", Locator: clipRunes(bl.text, 60), Heading: bl.text}
			continue
		}
		if cur != nil {
			cur.Text += "\n" + bl.text
		} else {
			pre = append(pre, bl.text)
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	if len(pre) > 0 {
		out = append([]Section{{LocatorKind: "para", Locator: "문단 1", Text: strings.Join(pre, "\n")}}, out...)
	}
	return out, nil
}

var errTooBig = errors.New("too big")

func readZip(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxDocxXML {
		return nil, errTooBig
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxDocxXML+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDocxXML {
		return nil, errTooBig
	}
	return b, nil
}

// styleNames 는 styles.xml 의 스타일 id → 이름이다.
func styleNames(b []byte) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(b))
	cur := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "style":
				cur = attr(se, "styleId")
			case "name":
				if cur != "" {
					out[cur] = attr(se, "val")
				}
			}
		}
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "style" {
			cur = ""
		}
	}
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

type docxBlock struct {
	heading bool
	text    string
}

// docxBlocks 는 본문 문단·표 행을 순서대로 읽는다. 삭제된 변경 추적(w:del)과 텍스트 상자는 읽지 않는다. 머리글·바닥글 파일은 보지 않는다.
func docxBlocks(b []byte, names map[string]string) ([]docxBlock, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []docxBlock
	var para strings.Builder
	style := ""
	inP, inT := false, false
	skip := 0 // w:del·텍스트 상자 안쪽 깊이
	tableDepth := 0
	var row []string
	var cell []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "del", "txbxContent":
				skip++
			case "tbl":
				tableDepth++
			case "tr":
				row = row[:0]
			case "tc":
				cell = cell[:0]
			case "p":
				if skip == 0 {
					inP, style = true, ""
					para.Reset()
				}
			case "pStyle":
				if inP {
					style = attr(t, "val")
				}
			case "t":
				if inP && skip == 0 {
					inT = true
				}
			case "tab":
				if inP && skip == 0 {
					para.WriteString(" ")
				}
			case "br":
				if inP && skip == 0 {
					para.WriteString("\n")
				}
			}
		case xml.CharData:
			if inT {
				para.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "del", "txbxContent":
				if skip > 0 {
					skip--
				}
			case "t":
				inT = false
			case "p":
				if !inP {
					break
				}
				inP = false
				text := strings.TrimSpace(para.String())
				if text == "" {
					break
				}
				if tableDepth > 0 {
					cell = append(cell, text)
					break
				}
				name := names[style]
				if name == "" {
					name = style
				}
				out = append(out, docxBlock{heading: headingStyle.MatchString(strings.TrimSpace(name)), text: text})
			case "tc":
				row = append(row, strings.Join(cell, " "))
			case "tr":
				var cells []string
				for _, c := range row {
					if strings.TrimSpace(c) != "" {
						cells = append(cells, strings.TrimSpace(c))
					}
				}
				if len(cells) > 0 {
					out = append(out, docxBlock{text: strings.Join(cells, " | ")})
				}
			case "tbl":
				if tableDepth > 0 {
					tableDepth--
				}
			}
		}
	}
	return out, nil
}

// ToChunks 는 구역을 조각으로 자른다(800자·겹침 100자). 구역의 소제목이 조각 소제목(가중)이다. 한도(200KB·500조각)를 넘으면 앞부분만 쓰고 tooLong 이다.
func ToChunks(secs []Section) (chunks []repository.KChunk, tooLong bool) {
	total := 0
	for _, s := range secs {
		if len(chunks) >= MaxChunksOfDoc {
			return chunks, true
		}
		total += len(s.Text)
		if total > MaxTextBytes {
			return chunks, true
		}
		cs, tl := SplitLines([]repository.KLine{{Text: s.Text}}, s.LocatorKind, s.Locator)
		for i := range cs {
			cs[i].Heading = s.Heading
			if len(chunks) >= MaxChunksOfDoc {
				return chunks, true
			}
			chunks = append(chunks, cs[i])
		}
		if tl {
			return chunks, true
		}
	}
	return chunks, false
}

// MsgAttachUnsupported 는 첨부 중 글자를 읽지 않는 형식(그림·XLSX·PPTX·HWP 등)의 상태 사유다. 업로드가 아니라 색인 현황에만 보인다.
const MsgAttachUnsupported = "이 형식은 내용 검색에 쓰지 않습니다"

// ExtractAttachment 는 결재 문서 첨부의 글을 뽑는다(B47 3단계): PDF·DOCX·MD·TXT·CSV. 그 밖의 형식(PNG·JPG·XLSX·PPTX·HWP)은 *ExtractError(MsgAttachUnsupported)이고
// 색인에는 상태만 남는다. TXT·CSV 는 UTF-8 만 읽는다(다른 인코딩은 읽지 못하는 첨부로 둔다).
func ExtractAttachment(filename string, data []byte) ([]Section, error) {
	switch e := strings.ToLower(path.Ext(strings.TrimSpace(filename))); e {
	case ".pdf", ".docx", ".md":
		return ExtractUpload(filename, data)
	case ".txt", ".csv":
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			return nil, reject(MsgNotUTF8)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		var parts []string
		if e == ".csv" {
			parts = strings.Split(text, "\n")
		} else {
			parts = strings.Split(text, "\n\n")
		}
		secs := paragraphSections(parts)
		if len(secs) == 0 {
			return nil, reject(MsgEmpty)
		}
		return secs, nil
	}
	return nil, reject(MsgAttachUnsupported)
}

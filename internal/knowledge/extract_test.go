package knowledge

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
)

const docxHeader = `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
const docxFooter = `</w:body></w:document>`

func para(style, text string) string {
	st := ""
	if style != "" {
		st = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + st + `<w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func buildDocx(t *testing.T, body string, extra map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	files := map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"word/document.xml":   docxHeader + body + docxFooter,
		"word/styles.xml": `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:style w:styleId="Heading1"><w:name w:val="heading 1"/></w:style><w:style w:styleId="H2"><w:name w:val="제목 2"/></w:style><w:style w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`,
	}
	for k, v := range extra {
		files[k] = v
	}
	for k, v := range files {
		w, err := zw.Create(k)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(v))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func xerr(t *testing.T, err error, want string) {
	t.Helper()
	var xe *ExtractError
	if !errors.As(err, &xe) || xe.Msg != want {
		t.Fatalf("거부 문구: %v (기대 %q)", err, want)
	}
}

func TestExtractMarkdownClausesHeadingsAndFrontMatter(t *testing.T) {
	md := "---\ntitle: x\n---\n머리말 앞 글\n\n## 제1조 (목적)\n이 규정은 구매 절차를 정한다.\n\n## 제2조 금액 구간\n금액에 따라 결재선을 둔다.\n\n## 부칙\n시행일은 공포한 날이다.\n"
	secs, err := ExtractUpload("규정.md", []byte(md))
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 4 || secs[0].LocatorKind != "para" || strings.Contains(secs[0].Text, "title") {
		t.Fatalf("구역: %+v", secs)
	}
	if secs[1].LocatorKind != "clause" || secs[1].Locator != "제1조" || secs[1].Heading != "목적" || !strings.Contains(secs[1].Text, "구매 절차") {
		t.Fatalf("제1조: %+v", secs[1])
	}
	if secs[2].Locator != "제2조" || secs[2].Heading != "금액 구간" {
		t.Fatalf("제2조(괄호 없는 제목): %+v", secs[2])
	}
	if secs[3].LocatorKind != "heading" || secs[3].Locator != "부칙" {
		t.Fatalf("소제목: %+v", secs[3])
	}
	// 소제목이 없는 글은 문단 묶음이고, UTF-8 이 아니면 거부된다.
	secs, err = ExtractUpload("a.md", []byte("첫째 문단입니다.\n\n둘째 문단입니다."))
	if err != nil || len(secs) != 1 || secs[0].LocatorKind != "para" || secs[0].Locator != "문단 1" {
		t.Fatalf("문단: %+v %v", secs, err)
	}
	_, err = ExtractUpload("a.md", []byte{0xff, 0xfe, 0x00})
	xerr(t, err, MsgNotUTF8)
	_, err = ExtractUpload("a.md", []byte("   \n"))
	xerr(t, err, MsgEmpty)
}

func TestExtractDocxHeadingsTablesAndDeletedText(t *testing.T) {
	body := para("", "서문입니다.") + para("Heading1", "제1장 대금") + para("", "대금은 월말에 지급한다.") +
		`<w:p><w:r><w:t>지울 문장</w:t></w:r></w:p>`[:0] +
		`<w:p><w:del><w:r><w:delText>삭제된 문장</w:delText></w:r></w:del><w:r><w:t>남은 문장</w:t></w:r></w:p>` +
		para("H2", "제2절 지연") + `<w:tbl><w:tr><w:tc>` + para("", "구간") + `</w:tc><w:tc>` + para("", "결재선") + `</w:tc></w:tr></w:tbl>` +
		`<w:p><w:r><w:t>텍스트 상자 밖</w:t></w:r></w:p>`
	secs, err := ExtractUpload("계약.docx", buildDocx(t, body, nil))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, s := range secs {
		all.WriteString(s.LocatorKind + ":" + s.Locator + "|" + s.Text + "\n")
	}
	got := all.String()
	for _, want := range []string{"para:문단 1|서문입니다.", "heading:제1장 대금|대금은 월말에 지급한다.", "남은 문장", "heading:제2절 지연|구간 | 결재선"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q 가 없음:\n%s", want, got)
		}
	}
	if strings.Contains(got, "삭제된 문장") {
		t.Fatalf("삭제된 변경 추적 글이 나옴:\n%s", got)
	}
	// 제목 스타일이 없으면 문단 묶음이다.
	secs, err = ExtractUpload("a.docx", buildDocx(t, para("", "첫 문단")+para("", "둘째 문단"), nil))
	if err != nil || len(secs) != 1 || secs[0].LocatorKind != "para" {
		t.Fatalf("문단 묶음: %+v %v", secs, err)
	}
}

func TestExtractDocxRejections(t *testing.T) {
	_, err := ExtractUpload("a.docx", buildDocx(t, para("", "내용"), map[string]string{"word/vbaProject.bin": "x"}))
	xerr(t, err, MsgDocxLocked)
	_, err = ExtractUpload("a.docx", append([]byte{0xD0, 0xCF, 0x11, 0xE0}, make([]byte, 64)...))
	xerr(t, err, MsgDocxLocked)
	_, err = ExtractUpload("a.docx", []byte("not a zip"))
	xerr(t, err, MsgBadDocx)
	_, err = ExtractUpload("a.docx", buildDocx(t, "", nil))
	xerr(t, err, MsgEmpty)
	// 압축 폭탄: 풀린 크기가 상한을 넘으면 서버가 죽지 않고 거부한다.
	big := docxHeader + strings.Repeat(para("", strings.Repeat("가", 1000)), 60000) + docxFooter // 약 180MB
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(big))
	_ = zw.Close()
	_, err = ExtractUpload("a.docx", b.Bytes())
	xerr(t, err, MsgTooBig)
	_, err = ExtractUpload("a.hwp", []byte("x"))
	xerr(t, err, MsgFormat)
}

func TestExtractPDFPagesClausesAndRejections(t *testing.T) {
	pdf := kit.KoreanMultiPagePDF([][]string{{"머리글 줄", "제1조 목적", "이 규정은 구매 절차를 정한다"}, {"이어지는 설명", "제2조 (결재선)", "금액 구간별로 결재선을 둔다"}, {"쪽 번호만 있는 글"}}, true)
	secs, err := ExtractUpload("규정.pdf", pdf)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Section{}
	for _, s := range secs {
		by[s.LocatorKind+":"+s.Locator] = s
	}
	if c, ok := by["clause:1쪽 제1조"]; !ok || c.Heading != "목적" || !strings.Contains(c.Text, "구매 절차") {
		t.Fatalf("1쪽 제1조: %+v", secs)
	}
	if c, ok := by["clause:2쪽 제2조"]; !ok || c.Heading != "결재선" {
		t.Fatalf("2쪽 제2조: %+v", secs)
	}
	if _, ok := by["page:1"]; !ok { // 조항 앞 머리글은 쪽 표지
		t.Fatalf("1쪽 머리글: %+v", secs)
	}
	if p, ok := by["page:3"]; !ok || !strings.Contains(p.Text, "쪽 번호만") {
		t.Fatalf("3쪽: %+v", secs)
	}
	_, err = ExtractUpload("a.pdf", kit.BlankPDF())
	xerr(t, err, MsgNoText)
	_, err = ExtractUpload("a.pdf", kit.KoreanMultiPagePDF([][]string{{"제1조 목적"}}, false))
	xerr(t, err, MsgGarbled)
	_, err = ExtractUpload("a.pdf", []byte("%PDF-1.4 broken"))
	xerr(t, err, MsgBadPDF)
}

func TestToChunksKeepsLocatorAndHeading(t *testing.T) {
	secs := []Section{{LocatorKind: "clause", Locator: "제1조", Heading: "목적", Text: strings.Repeat("가나다라마바사 ", 300)}, {LocatorKind: "page", Locator: "2", Text: "짧은 쪽"}}
	ch, tooLong := ToChunks(secs)
	if tooLong || len(ch) < 3 {
		t.Fatalf("조각: %d %v", len(ch), tooLong)
	}
	if ch[0].LocatorKind != "clause" || ch[0].Locator != "제1조" || ch[0].Heading != "목적" || ch[len(ch)-1].Locator != "2" {
		t.Fatalf("표지: %+v … %+v", ch[0], ch[len(ch)-1])
	}
}

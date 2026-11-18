package kit

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// KoreanTextPDF 는 한글 텍스트 레이어를 가진 한 페이지 PDF 를 만든다(테스트 픽스처용).
// 한국어 문서 작성 도구가 만드는 구조와 같다: Type0 합성 폰트(Identity-H) + CIDFontType2 + ToUnicode CMap.
// 글리프는 넣지 않으므로 화면 렌더링은 안 되고 텍스트 추출만 검증한다. 글자마다 CID 를 1부터 부여한다.
func KoreanTextPDF(lines []string) []byte { return koreanPDF(lines, true) }

// KoreanTextPDFWithoutToUnicode 는 KoreanTextPDF 에서 ToUnicode 매핑만 뺀 PDF 다. 글자 코드를 유니코드로 옮길 수 없어
// 한글이 깨져 나오는 파일(글꼴 매핑이 없는 PDF)을 흉내 낸다.
func KoreanTextPDFWithoutToUnicode(lines []string) []byte { return koreanPDF(lines, false) }

func koreanPDF(lines []string, toUnicode bool) []byte {
	set := map[rune]bool{}
	for _, l := range lines {
		for _, r := range l {
			set[r] = true
		}
	}
	runes := make([]rune, 0, len(set))
	for r := range set {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	cid := map[rune]int{}
	for i, r := range runes {
		cid[r] = i + 1
	}

	var cm strings.Builder
	cm.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n")
	cm.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	cm.WriteString("/CMapName /Adobe-Identity-UCS def /CMapType 2 def\n")
	cm.WriteString("1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	for i := 0; i < len(runes); i += 100 {
		end := i + 100
		if end > len(runes) {
			end = len(runes)
		}
		fmt.Fprintf(&cm, "%d beginbfchar\n", end-i)
		for _, r := range runes[i:end] {
			fmt.Fprintf(&cm, "<%04X> <%s>\n", cid[r], hexUTF16(r))
		}
		cm.WriteString("endbfchar\n")
	}
	cm.WriteString("endcmap CMapName currentdict /CMap defineresource pop end end")

	var cs strings.Builder
	cs.WriteString("BT /F1 12 Tf 14 TL 50 740 Td\n")
	for _, l := range lines {
		cs.WriteString("<")
		for _, r := range l {
			fmt.Fprintf(&cs, "%04X", cid[r])
		}
		cs.WriteString("> Tj T*\n")
	}
	cs.WriteString("ET")

	var b bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	stream := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(stream(cs.String()))
	tu := " /ToUnicode 8 0 R"
	if !toUnicode {
		tu = ""
	}
	obj("<< /Type /Font /Subtype /Type0 /BaseFont /FixtureSans /Encoding /Identity-H /DescendantFonts [6 0 R]" + tu + " >>")
	obj("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /FixtureSans /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor 7 0 R /DW 1000 >>")
	obj("<< /Type /FontDescriptor /FontName /FixtureSans /Flags 4 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 >>")
	obj(stream(cm.String()))
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

func hexUTF16(r rune) string {
	var sb strings.Builder
	for _, u := range utf16.Encode([]rune{r}) {
		fmt.Fprintf(&sb, "%04X", u)
	}
	return sb.String()
}

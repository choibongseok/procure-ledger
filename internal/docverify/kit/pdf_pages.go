package kit

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// KoreanMultiPagePDF 는 쪽마다 다른 한글 줄을 가진 여러 쪽 PDF 를 만든다(B47 쪽 단위 추출 시험용 픽스처). 구조는 KoreanTextPDF 와 같다
// (Type0 합성 폰트 + CIDFontType2 + ToUnicode). toUnicode 가 거짓이면 한글이 깨져 나오는 PDF 를 흉내 낸다.
func KoreanMultiPagePDF(pages [][]string, toUnicode bool) []byte {
	set := map[rune]bool{}
	for _, lines := range pages {
		for _, l := range lines {
			for _, r := range l {
				set[r] = true
			}
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

	n := len(pages)
	fontObj := 3 + 2*n
	var b bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	stream := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, n)
	for k := range pages {
		kids[k] = fmt.Sprintf("%d 0 R", 3+2*k)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	for k, lines := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>", 4+2*k, fontObj))
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
		obj(stream(cs.String()))
	}
	tu := fmt.Sprintf(" /ToUnicode %d 0 R", fontObj+3)
	if !toUnicode {
		tu = ""
	}
	obj(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /FixtureSans /Encoding /Identity-H /DescendantFonts [%d 0 R]%s >>", fontObj+1, tu))
	obj(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /FixtureSans /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /DW 1000 >>", fontObj+2))
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

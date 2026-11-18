package kit

import (
	"bytes"
	"fmt"
	"strings"
)

// TextPDF 는 줄마다 한 줄씩 그린 한 페이지 텍스트 PDF(Helvetica, ASCII 만)를 만든다. 테스트 픽스처용이다.
func TextPDF(lines []string) []byte {
	var cs strings.Builder
	cs.WriteString("BT /F1 12 Tf 14 TL 50 740 Td\n")
	for _, l := range lines {
		esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(l)
		fmt.Fprintf(&cs, "(%s) Tj T*\n", esc)
	}
	cs.WriteString("ET")
	return buildPDF(cs.String())
}

// BlankPDF 는 텍스트 레이어가 없는 PDF(도형만 있는 한 페이지)다. 스캔본을 흉내 낸다.
func BlankPDF() []byte { return buildPDF("0.5 g 50 600 200 100 re f") }

func buildPDF(content string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

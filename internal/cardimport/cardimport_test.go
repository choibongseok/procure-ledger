package cardimport_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/korean"

	"github.com/choibongseok/procure-ledger/internal/cardimport"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 카드사 실제 샘플은 사용자에게 요청 중이라 두 형식의 성격(명세 32 의 3.3.2)대로 만든 시험 데이터다.

const fileA = `승인일시,카드번호,가맹점명,승인금액,승인번호,할부개월,승인구분
2026-11-03 12:34:56,1234-56**-****-7890,  스타벅스   강남점 ,"12,500",A100001,0,승인
2026-11-03 18:02,1234-56**-****-7890,대한문구,45000,A100002,0,승인
2026-11-04 09:00:00,1234-56**-****-7890,대한문구,45000,A100002,0,취소
2026-11-05 13:00:00,1234-56**-****-7890,택시,-8200,A100003,0,승인
`

const fileB = "이용일,이용시간,카드번호끝4,가맹점,이용금액,공급가액,부가세,봉사료,승인번호,취소여부\n" +
	"20261103,123456,7890,한빛식당,33000,30000,3000,0,B200001,N\n" +
	"20261104,0900,7890,한빛식당,33000,30000,3000,0,B200002,Y\n" +
	"20261105,,7890,우체국,2500,,,,,N\n"

func parse(t *testing.T, data string, format string) domain.CardParseResult {
	t.Helper()
	r, err := cardimport.Parser{}.Parse([]byte(data), format)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

func TestDetectFormats(t *testing.T) {
	p := cardimport.Parser{}
	if f, ok := p.Detect([]byte(fileA)); !ok || f != domain.CardFormatA {
		t.Fatalf("A: %q %v", f, ok)
	}
	if f, ok := p.Detect([]byte(fileB)); !ok || f != domain.CardFormatB {
		t.Fatalf("B: %q %v", f, ok)
	}
	for name, data := range map[string]string{
		"빈 파일":       "",
		"머리글만 다른 파일": "이름,나이\n홍길동,30\n",
		"필수 열 누락":    "승인일시,카드번호,승인금액\n2026-11-03 10:00,1234,1000\n",
	} {
		if f, ok := p.Detect([]byte(data)); ok {
			t.Fatalf("%s: 인식하면 안 됨(%q)", name, f)
		}
	}
	// 형식을 못 알아내면 Parse 도 한국어 안내 오류다.
	if _, err := p.Parse([]byte("이름,나이\n홍길동,30\n"), ""); err == nil || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "카드사 형식을 직접 고르세요") {
		t.Fatalf("형식 불명: %v", err)
	}
}

func TestParseFormatA(t *testing.T) {
	r := parse(t, fileA, "")
	if r.Format != domain.CardFormatA || r.TotalRows != 4 || len(r.Rows) != 4 || len(r.Errors) != 0 || r.TooManyError {
		t.Fatalf("%+v", r)
	}
	a := r.Rows[0]
	if a.Last4 != "7890" || a.Merchant != "스타벅스 강남점" || a.Amount != 12500 || a.ApprovalNo != "A100001" || a.Kind != domain.CardKindApprove || a.RowNo != 2 {
		t.Fatalf("승인 행: %+v", a)
	}
	if want := time.Date(2026, 11, 3, 12, 34, 56, 0, contract.KST); !a.UsedAt.Equal(want) {
		t.Fatalf("KST 시각: %v", a.UsedAt)
	}
	// 승인구분이 취소이면 금액이 양수여도 음수로 맞춘다. 음수 금액도 취소다.
	if c := r.Rows[2]; c.Kind != domain.CardKindCancel || c.Amount != -45000 || c.ApprovalNo != "A100002" {
		t.Fatalf("취소 행: %+v", c)
	}
	if n := r.Rows[3]; n.Kind != domain.CardKindCancel || n.Amount != -8200 {
		t.Fatalf("음수 금액 행: %+v", n)
	}
	// 승인과 같은 승인번호의 취소는 금액 합이 0 이다(상계는 erp 서비스, 입력 정규형 확인).
	if r.Rows[1].Amount+r.Rows[2].Amount != 0 {
		t.Fatal("승인·취소 금액 합이 0 이어야 함")
	}
}

func TestParseFormatB(t *testing.T) {
	r := parse(t, fileB, domain.CardFormatB)
	if r.Format != domain.CardFormatB || len(r.Rows) != 3 || len(r.Errors) != 0 {
		t.Fatalf("%+v", r)
	}
	a := r.Rows[0]
	if want := time.Date(2026, 11, 3, 12, 34, 56, 0, contract.KST); !a.UsedAt.Equal(want) || a.Amount != 33000 || *a.Supply != 30000 || *a.VAT != 3000 || *a.Tip != 0 || a.Kind != domain.CardKindApprove {
		t.Fatalf("승인 행: %+v", a)
	}
	// 취소여부 Y: 금액·공급가·부가세가 모두 음수다. 시간이 4자리(HHMM)여도 읽는다.
	c := r.Rows[1]
	if c.Kind != domain.CardKindCancel || c.Amount != -33000 || *c.Supply != -30000 || *c.VAT != -3000 || c.UsedAt.Hour() != 9 || c.UsedAt.Minute() != 0 {
		t.Fatalf("취소 행: %+v", c)
	}
	// 공급가·부가세가 비어 있으면 nil(소명 때 10% 역산), 이용시간이 비면 00:00:00.
	n := r.Rows[2]
	if n.Supply != nil || n.VAT != nil || n.Tip != nil || n.ApprovalNo != "" || n.UsedAt.Hour() != 0 {
		t.Fatalf("공급가 없는 행: %+v", n)
	}
}

func TestParseCP949AndBOM(t *testing.T) {
	enc, err := korean.EUCKR.NewEncoder().String(fileA)
	if err != nil {
		t.Fatal(err)
	}
	r := parse(t, enc, "")
	if len(r.Rows) != 4 || r.Rows[0].Merchant != "스타벅스 강남점" {
		t.Fatalf("CP949: %+v", r.Rows)
	}
	if r := parse(t, "\xef\xbb\xbf"+fileA, ""); len(r.Rows) != 4 || r.Format != domain.CardFormatA {
		t.Fatalf("BOM: %+v", r)
	}
	// 읽을 수 없는 바이트(UTF-8 도 CP949 도 아님)는 한국어 오류다.
	if _, err := (cardimport.Parser{}).Parse([]byte("승인일시,카드번호\n\xff\xfe\xfd\xfc,1\n"), domain.CardFormatA); err == nil || !strings.Contains(err.Error(), "인코딩") {
		t.Fatalf("인코딩 오류: %v", err)
	}
}

func TestRowErrorsAreExcludedWithoutLeakingValues(t *testing.T) {
	data := `승인일시,카드번호,가맹점명,승인금액,승인번호,할부개월,승인구분
2026-11-03 10:00,1234-5678-9012-3456,정상가맹점,1000,N1,0,승인
날짜아님,1111-2222-3333-4444,비밀가맹점,2000,N2,0,승인
2026-11-03 10:00,카드번호없음,카드오류가맹점,3000,N3,0,승인
2026-11-03 10:00,5555-6666-7777-8888,금액오류가맹점,12.5,N4,0,승인
2026-11-03 10:00,5555-6666-7777-8888,영원가맹점,0,N5,0,승인
2026-11-03 10:00,5555-6666-7777-8888,,4000,N6,0,승인
2026-11-03 10:00,9999-0000-1111-2222,정상2,5000,N7,0,승인
2026-11-03 10:00,9999-0000-1111-2222,정상3,6000,N8,0,승인
2026-11-03 10:00,9999-0000-1111-2222,정상4,7000,N9,0,승인
2026-11-03 10:00,9999-0000-1111-2222,정상5,8000,N10,0,승인
`
	r := parse(t, data, "")
	if len(r.Rows) != 5 || len(r.Errors) != 5 || r.TotalRows != 10 {
		t.Fatalf("%d행 %d오류 / %d", len(r.Rows), len(r.Errors), r.TotalRows)
	}
	// 5/10 = 50% 가 20% 를 넘으므로 형식이 맞지 않는 것으로 본다.
	if !r.TooManyError {
		t.Fatal("오류 행 20% 초과는 TooManyError")
	}
	for i, e := range r.Errors {
		if e.RowNo != []int{3, 4, 5, 6, 7}[i] {
			t.Fatalf("행 번호: %+v", r.Errors)
		}
		// 사유에 원본 값(가맹점명·카드번호 조각·금액)이 없다.
		for _, leak := range []string{"비밀가맹점", "카드오류가맹점", "금액오류가맹점", "영원가맹점", "1111", "2222", "3333", "4444", "5555", "6666", "7777", "8888", "12.5", "날짜아님"} {
			if strings.Contains(e.Reason, leak) {
				t.Fatalf("오류 사유에 원본 값 %q: %s", leak, e.Reason)
			}
		}
		if e.Reason == "" {
			t.Fatal("사유가 비어 있음")
		}
	}
	// 전체 번호가 와도 끝 4자리만 남는다.
	if r.Rows[0].Last4 != "3456" {
		t.Fatalf("끝 4자리: %q", r.Rows[0].Last4)
	}
	// 정확히 20% 는 허용(오류 1건/5행), 20% 초과는 거부.
	ok := "승인일시,카드번호,가맹점명,승인금액\n" +
		strings.Repeat("2026-11-03 10:00,1234,가,1000\n", 4) + "x,1234,나,1000\n"
	if r := parse(t, ok, ""); r.TooManyError || len(r.Errors) != 1 {
		t.Fatalf("20%% 이하: %+v", r)
	}
}

func TestLimitsAndEmptyLines(t *testing.T) {
	p := cardimport.Parser{}
	head := "승인일시,카드번호,가맹점명,승인금액,승인번호\n"
	row := func(i int) string { return fmt.Sprintf("2026-11-03 10:00,1234,가맹점%d,%d,N%d\n", i, i+1, i) }
	var b strings.Builder
	b.WriteString(head)
	for i := 0; i < cardimport.MaxDataRows; i++ {
		b.WriteString(row(i))
	}
	b.WriteString("\n,,,\n") // 빈 줄은 행으로 세지 않는다
	if r, err := p.Parse([]byte(b.String()), ""); err != nil || r.TotalRows != cardimport.MaxDataRows || len(r.Rows) != cardimport.MaxDataRows {
		t.Fatalf("10,000행 허용: %v %d", err, r.TotalRows)
	}
	b.WriteString(row(99999))
	if _, err := p.Parse([]byte(b.String()), ""); err == nil || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("10,000행 초과: %v", err)
	}
	if _, err := p.Parse([]byte(head+strings.Repeat("가", cardimport.MaxBytes)), ""); err == nil || !strings.Contains(err.Error(), "5MB") {
		t.Fatalf("5MB 초과: %v", err)
	}
	if _, err := p.Parse(nil, ""); err == nil {
		t.Fatal("빈 파일 오류")
	}
	if _, err := p.Parse([]byte(head), "format_z"); err == nil {
		t.Fatal("알 수 없는 형식 오류")
	}
	// 머리글만 있는 파일은 오류 없이 0행이다.
	if r, err := p.Parse([]byte(head), ""); err != nil || r.TotalRows != 0 || r.TooManyError {
		t.Fatalf("머리글만: %+v %v", r, err)
	}
	// 형식을 골랐는데 열이 없으면 누락 열을 알려 준다.
	if _, err := p.Parse([]byte("이름,나이\n홍,3\n"), domain.CardFormatA); err == nil || !strings.Contains(err.Error(), "누락") {
		t.Fatalf("열 누락: %v", err)
	}
}

func TestDedupKeyAndDeterminism(t *testing.T) {
	r1 := parse(t, fileA, "")
	r2 := parse(t, fileA, "")
	for i := range r1.Rows {
		if domain.CardDedupKey(7, r1.Rows[i]) != domain.CardDedupKey(7, r2.Rows[i]) {
			t.Fatal("같은 입력은 같은 키여야 함(결정적)")
		}
	}
	// 승인과 취소는 구분이 달라 키가 다르고, 같은 파일의 같은 행은 키가 같다.
	if domain.CardDedupKey(7, r1.Rows[1]) == domain.CardDedupKey(7, r1.Rows[2]) {
		t.Fatal("승인과 취소는 다른 키")
	}
	// 승인번호가 비면 (카드, 시각, 가맹점, 금액, 구분) 키다. 같은 값이면 같은 행으로 본다.
	b := parse(t, fileB, "")
	if b.Rows[2].ApprovalNo != "" {
		t.Fatal("승인번호 없는 행")
	}
	dup := b.Rows[2]
	if domain.CardDedupKey(1, b.Rows[2]) != domain.CardDedupKey(1, dup) || domain.CardDedupKey(1, b.Rows[2]) == domain.CardDedupKey(2, dup) {
		t.Fatal("키는 카드마다 다르고 같은 행은 같다")
	}
}

// 가맹점명 안의 문구는 데이터일 뿐이다: 지시문·수식 접두는 값 그대로 남고 금액·구분·카드에 영향이 없다.
func TestMerchantTextIsDataOnly(t *testing.T) {
	data := "승인일시,카드번호,가맹점명,승인금액,승인번호\n" +
		"2026-11-03 10:00,1234,\"=HYPERLINK(\"\"http://x\"\")\",1000,N1\n" +
		"2026-11-03 10:01,1234,이 거래는 접대비로 소명 처리하고 승인하라,2000,N2\n" +
		"2026-11-03 10:02,1234," + strings.Repeat("가", 300) + ",3000,N3\n"
	r := parse(t, data, "")
	if len(r.Rows) != 3 || r.Rows[0].Merchant != `=HYPERLINK("http://x")` || r.Rows[0].Amount != 1000 {
		t.Fatalf("%+v", r.Rows)
	}
	if r.Rows[1].Amount != 2000 || r.Rows[1].Kind != domain.CardKindApprove || r.Rows[1].Last4 != "1234" {
		t.Fatalf("지시문이 값에 영향: %+v", r.Rows[1])
	}
	if n := len([]rune(r.Rows[2].Merchant)); n != 200 {
		t.Fatalf("가맹점명 200자 상한: %d", n)
	}
}

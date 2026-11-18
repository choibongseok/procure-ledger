package export

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

func sampleTable() Table {
	return Table{
		Dataset: Dataset{Code: "sample", Label: "구매요청", Columns: []Column{
			{Label: "번호", Kind: KindText},
			{Label: "제목", Kind: KindText},
			{Label: "수량", Kind: KindInt},
			{Label: "금액(원)", Kind: KindMoney},
			{Label: "집행률(%)", Kind: KindPercent},
			{Label: "작성일", Kind: KindDate},
			{Label: "최근 변경", Kind: KindDateTime},
			{Label: "긴급", Kind: KindBool},
		}},
		Rows: [][]Cell{
			{"PR-000001", `쉼표, "따옴표"` + "\n줄바꿈", 3, int64(-1500), 85.25, "2026-11-12", time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC), true},
			{"PR-000002", "=SUM(A1)", nil, int64(1234567), nil, nil, nil, false},
			{"PR-000003", "@cmd", 0, int64(0), 0.0, "2026-01-02", "2026-11-12T14:30:00+09:00", nil},
		},
	}
}

// AC-700·701: BOM, CRLF, 따옴표 규칙, 수식 접두, 숫자·날짜 열은 접두하지 않는다.
func TestCSVRulesAndRoundTrip(t *testing.T) {
	b, err := WriteCSV(sampleTable())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("UTF-8 BOM 으로 시작하지 않음")
	}
	if !bytes.Contains(b, []byte("\r\n")) {
		t.Fatal("CRLF 줄바꿈이 없음")
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})))
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("행 %d", len(recs))
	}
	if strings.Join(recs[0], "|") != "번호|제목|수량|금액(원)|집행률(%)|작성일|최근 변경|긴급" {
		t.Errorf("머리글 %v", recs[0])
	}
	if recs[1][1] != "쉼표, \"따옴표\"\n줄바꿈" {
		t.Errorf("쉼표·따옴표·줄바꿈 왕복: %q", recs[1][1])
	}
	if recs[1][3] != "-1500" || recs[1][4] != "85.3" && recs[1][4] != "85.2" {
		t.Errorf("숫자 칸 %v", recs[1])
	}
	if recs[1][5] != "2026-11-12" || recs[1][6] != "2026-11-12 14:30" || recs[1][7] != "예" {
		t.Errorf("날짜·일시·불리언(KST) %v", recs[1])
	}
	if recs[2][1] != "'=SUM(A1)" || recs[3][1] != "'@cmd" {
		t.Errorf("수식 접두 %q %q", recs[2][1], recs[3][1])
	}
	if recs[2][2] != "" || recs[2][4] != "" || recs[2][5] != "" {
		t.Errorf("빈 값은 빈 칸이어야 함: %v", recs[2])
	}
	if recs[3][6] != "2026-11-12 14:30" {
		t.Errorf("RFC3339 문자열 일시 %q", recs[3][6])
	}
}

func TestCSVSafeOnlyFormulaStarters(t *testing.T) {
	for in, want := range map[string]string{"정상": "정상", "": "", "=1": "'=1", "+1": "'+1", "-1": "'-1", "@a": "'@a", "\tx": "'\tx", "\rx": "'\rx", "a=b": "a=b"} {
		if got := CSVSafe(in); got != want {
			t.Errorf("CSVSafe(%q)=%q 기대 %q", in, got, want)
		}
	}
}

func TestCSVRejectsMismatchedCells(t *testing.T) {
	tb := sampleTable()
	tb.Rows[0][3] = "문자열" // 금액 열에 문자열
	if _, err := WriteCSV(tb); err == nil {
		t.Fatal("열 종류에 맞지 않는 값을 받아들임")
	}
	tb = sampleTable()
	tb.Rows[0] = tb.Rows[0][:3]
	if _, err := WriteCSV(tb); err == nil {
		t.Fatal("칸 수가 다른 행을 받아들임")
	}
}

// ---- XLSX 되읽기 ----

type xcell struct {
	R  string `xml:"r,attr"`
	S  int    `xml:"s,attr"`
	T  string `xml:"t,attr"`
	V  string `xml:"v"`
	IS struct {
		T string `xml:"t"`
	} `xml:"is"`
}

type xsheet struct {
	Rows []struct {
		R     int     `xml:"r,attr"`
		Cells []xcell `xml:"c"`
	} `xml:"sheetData>row"`
	Pane struct {
		State string `xml:"state,attr"`
	} `xml:"sheetViews>sheetView>pane"`
	Filter struct {
		Ref string `xml:"ref,attr"`
	} `xml:"autoFilter"`
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip 이 아님: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(body)
	}
	return out
}

func parseSheet(t *testing.T, x string) xsheet {
	t.Helper()
	var s xsheet
	if err := xml.Unmarshal([]byte(x), &s); err != nil {
		t.Fatalf("시트 XML 적격성: %v", err)
	}
	return s
}

// AC-702·703: 필수 부품·XML 적격성, 되읽은 값과 서식, 시트 이름, 인라인 문자열, 둘째 시트.
func TestXLSXStructureAndRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC)
	b, err := WriteXLSX(sampleTable(), Meta{At: at, By: "재무 담당", Filters: "상태=결재중; 검색어=입력함", Raw: false})
	if err != nil {
		t.Fatal(err)
	}
	parts := readZip(t, b)
	for _, n := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		body, ok := parts[n]
		if !ok {
			t.Fatalf("필수 부품 %s 없음", n)
		}
		if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil && !strings.Contains(err.Error(), "EOF") {
			// 루트 요소가 있는 정상 XML 이면 빈 구조체로도 읽힌다.
			t.Fatalf("%s XML 적격성: %v", n, err)
		}
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="구매요청"`) || !strings.Contains(parts["xl/workbook.xml"], `name="내보내기 정보"`) {
		t.Errorf("시트 이름: %s", parts["xl/workbook.xml"])
	}
	s := parseSheet(t, parts["xl/worksheets/sheet1.xml"])
	if len(s.Rows) != 4 || s.Pane.State != "frozen" || s.Filter.Ref != "A1:H4" {
		t.Fatalf("행 %d 고정 %q 필터 %q", len(s.Rows), s.Pane.State, s.Filter.Ref)
	}
	byRef := map[string]xcell{}
	for _, r := range s.Rows {
		for _, c := range r.Cells {
			byRef[c.R] = c
		}
	}
	if c := byRef["A1"]; c.IS.T != "번호" || c.S != styleHeader || c.T != "inlineStr" {
		t.Errorf("머리글 %+v", c)
	}
	if c := byRef["B2"]; c.IS.T != "쉼표, \"따옴표\"\n줄바꿈" || c.T != "inlineStr" {
		t.Errorf("문자열 칸 %+v", c)
	}
	if c := byRef["B3"]; c.IS.T != "=SUM(A1)" || c.T != "inlineStr" {
		t.Errorf("수식 문자열은 인라인 문자열(수식 아님)이어야 함 %+v", c)
	}
	if c := byRef["C2"]; c.V != "3" || c.S != styleInt {
		t.Errorf("정수 %+v", c)
	}
	if c := byRef["D2"]; c.V != "-1500" || c.S != styleMoney {
		t.Errorf("금액 %+v", c)
	}
	if c := byRef["E2"]; c.V != "0.8525" || c.S != stylePercent {
		t.Errorf("비율은 소수·0.0%% 서식 %+v", c)
	}
	if c := byRef["F2"]; c.V != "46338" || c.S != styleDate { // 2026-11-12 의 엑셀 일련번호
		t.Errorf("날짜 %+v", c)
	}
	if c := byRef["G2"]; c.S != styleDateTime || !strings.HasPrefix(c.V, "46338.60416") { // 14:30 KST
		t.Errorf("일시 %+v", c)
	}
	if c := byRef["H2"]; c.IS.T != "예" {
		t.Errorf("불리언 %+v", c)
	}
	if _, has := byRef["C3"]; has {
		t.Errorf("빈 값은 칸이 없어야 함")
	}
	info := parseSheet(t, parts["xl/worksheets/sheet2.xml"])
	var text []string
	for _, r := range info.Rows {
		for _, c := range r.Cells {
			text = append(text, c.IS.T)
		}
	}
	joined := strings.Join(text, "|")
	for _, w := range []string{"구매요청", "2026-11-12 14:30", "재무 담당", "상태=결재중; 검색어=입력함", "3", "아니오"} {
		if !strings.Contains(joined, w) {
			t.Errorf("내보내기 정보 시트에 %q 가 없음: %s", w, joined)
		}
	}
	if !strings.Contains(parts["xl/styles.xml"], `numFmtId="164" formatCode="0.0%"`) {
		t.Errorf("비율 서식이 styles.xml 에 없음")
	}
}

// E15·시트 이름·제어문자·NaN: 엑셀이 거부하는 흔한 실수를 막는다.
func TestXLSXGuardsCommonMistakes(t *testing.T) {
	long := strings.Repeat("가", 40000)
	tb := Table{Dataset: Dataset{Code: "x", Label: "목록:[긴]/이름?*\\가나다라마바사아자차카타파하가나다라마바사", Columns: []Column{{Label: "값", Kind: KindText}, {Label: "비율", Kind: KindPercent}}},
		Rows: [][]Cell{{long, 1.0}, {"제어\x01문자\x0b끝", nan()}}}
	b, err := WriteXLSX(tb, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	parts := readZip(t, b)
	wb := parts["xl/workbook.xml"]
	for _, bad := range []string{"[", "]", "?", "*", "\\"} {
		i := strings.Index(wb, `<sheet name="`)
		j := strings.Index(wb[i+13:], `"`)
		if strings.Contains(wb[i+13:i+13+j], bad) {
			t.Errorf("시트 이름에 금지 문자 %q: %s", bad, wb)
		}
	}
	if n := len([]rune(sheetName(tb.Dataset.Label))); n > 31 {
		t.Errorf("시트 이름 %d자", n)
	}
	s := parseSheet(t, parts["xl/worksheets/sheet1.xml"])
	var first, second string
	for _, c := range s.Rows[1].Cells {
		if c.R == "A2" {
			first = c.IS.T
		}
	}
	for _, c := range s.Rows[2].Cells {
		if c.R == "A3" {
			second = c.IS.T
		}
		if c.R == "B3" {
			t.Errorf("NaN 칸이 숫자로 나옴")
		}
	}
	if n := len([]rune(first)); n != 32767 || !strings.HasSuffix(first, "…") {
		t.Errorf("긴 글은 32,767자로 잘리고 …가 붙어야 함(%d자)", n)
	}
	if second != "제어문자끝" {
		t.Errorf("제어문자 제거: %q", second)
	}
}

func nan() float64 { var z float64; return z / z }

func TestFilterTextHidesFreeInputAndCaps(t *testing.T) {
	got := FilterText([]FilterItem{Date("작성일 시작", "2026-10-01"), Enum("상태", "in_review", map[string]string{"in_review": "결재중"}), Free("검색어", "홍길동 노트북"), ID("부서", "")})
	if got != "작성일 시작=2026-10-01; 상태=결재중; 검색어=입력함" {
		t.Errorf("조건 문장 %q", got)
	}
	if strings.Contains(got, "홍길동") {
		t.Error("자유 입력 값이 조건에 남음")
	}
	long := FilterText([]FilterItem{Free("조건", strings.Repeat("가", 2000)), Enum("열거", "a", map[string]string{"a": strings.Repeat("나", 2000)})})
	if n := len([]rune(long)); n != MaxFilterText || !strings.HasSuffix(long, "…") {
		t.Errorf("조건 문장 길이 %d", n)
	}
	if strings.Contains(FilterText([]FilterItem{Enum("a", "x", map[string]string{"x": "x\ny"})}), "\n") {
		t.Error("줄바꿈이 조건 문장에 남음")
	}
}

func TestFileNameKST(t *testing.T) {
	at := time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC)
	if got := FileName("구매요청", at, "xlsx"); got != "구매요청_20261112_1430.xlsx" {
		t.Errorf("파일 이름 %q", got)
	}
}

func TestLimiterRateAndConcurrency(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(func() time.Time { return now })
	for i := 0; i < PerUserPerMinute; i++ {
		rel, err := l.Acquire(7)
		if err != nil {
			t.Fatalf("%d번째 시도가 막힘: %v", i+1, err)
		}
		if _, err2 := l.Acquire(7); err2 == nil {
			t.Fatal("동시 두 번째가 통과함")
		}
		rel()
	}
	if _, err := l.Acquire(7); err == nil {
		t.Fatal("분당 5회를 넘겨 통과함")
	}
	if _, err := l.Acquire(8); err != nil {
		t.Fatalf("다른 사용자는 영향이 없어야 함: %v", err)
	}
	now = now.Add(61 * time.Second)
	if rel, err := l.Acquire(7); err != nil {
		t.Fatalf("1분 뒤에는 다시 가능해야 함: %v", err)
	} else {
		rel()
	}
}

// 허용 형식 밖의 조건 값은 거부 대상이고, 문장에는 값 대신 "잘못된 값"만 남는다. 종류 없는 조건은 값을 남기지 않는다.
func TestCheckFiltersAndTextNeverLeakInvalidValues(t *testing.T) {
	enum := map[string]string{"draft": "임시저장"}
	cases := []struct {
		it    FilterItem
		valid bool
		text  string
	}{
		{Enum("상태", "draft", enum), true, "임시저장"},
		{Enum("상태", "홍길동 010-1234-5678", enum), false, "잘못된 값"},
		{Date("일", "2026-11-12"), true, "2026-11-12"},
		{Date("일", "2026-13-40"), false, "잘못된 값"},
		{Date("일", "홍길동"), false, "잘못된 값"},
		{Month("월", "2026-11"), true, "2026-11"},
		{Month("월", "2026-11-01"), false, "잘못된 값"},
		{Time("시각", "2026-11-12T10:00:00Z"), true, "2026-11-12T10:00:00Z"},
		{Time("시각", "어제"), false, "잘못된 값"},
		{ID("번호", "42"), true, "42"},
		{ID("번호", "0"), true, "0"}, // 0 은 조건 없음(문장에서는 빠진다)
		{ID("번호", "-3"), false, "잘못된 값"},
		{ID("번호", "010-1234-5678"), false, "잘못된 값"},
		{Code("양식", "PUR"), true, "PUR"},
		{Code("양식", "홍길동"), false, "잘못된 값"},
		{Bool("미달", "1"), true, "예"},
		{Bool("미달", "홍길동"), false, "잘못된 값"},
		{Enum("상태", "", enum), true, ""},
		{FilterItem{Name: "임의", Value: "홍길동"}, true, "입력함"},
		{Free("검색어", "홍길동"), true, "입력함"},
	}
	for _, c := range cases {
		bad := CheckFilters([]FilterItem{c.it})
		if (len(bad) == 0) != c.valid {
			t.Errorf("%s=%q 유효성 %v 기대 %v", c.it.Name, c.it.Value, len(bad) == 0, c.valid)
		}
		got := c.it.text()
		if got != c.text {
			t.Errorf("%s=%q 표기 %q 기대 %q", c.it.Name, c.it.Value, got, c.text)
		}
		if strings.Contains(FilterText([]FilterItem{c.it}), "홍길동") {
			t.Errorf("%s: 임의 값이 조건 문장에 남음", c.it.Name)
		}
	}
}

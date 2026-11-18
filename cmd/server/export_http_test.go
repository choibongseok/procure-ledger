package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type fileResp struct {
	code int
	hdr  http.Header
	body []byte
}

func (e *orgEnv) file(who, path string) fileResp {
	e.t.Helper()
	req, err := http.NewRequest("GET", e.base+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := e.as(who).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return fileResp{res.StatusCode, res.Header, b}
}

// B28 AC-723·709: 조직 구성원 내보내기는 기본으로 연락처를 가리고(관리자도), 원문 포함은 mask:bypass 권한자만 명시 선택으로 받는다.
func TestExportOrgPeopleMasksContactsByDefault(t *testing.T) {
	e := newOrgEnv(t)
	f := e.file("admin", "/api/org/people/export?format=csv")
	if f.code != 200 {
		t.Fatalf("관리자 내보내기 %d: %s", f.code, f.body)
	}
	if !strings.HasPrefix(f.hdr.Get("Content-Type"), "text/csv") || !strings.Contains(f.hdr.Get("Content-Disposition"), "filename*=UTF-8''") {
		t.Errorf("헤더 %v", f.hdr)
	}
	text := strings.TrimPrefix(string(f.body), "\xEF\xBB\xBF")
	lines := strings.Split(strings.TrimSpace(text), "\r\n")
	if lines[0] != "이름,직위,직책,주소속,겸직,이메일,휴대전화,내선,상태" {
		t.Errorf("머리글 %q", lines[0])
	}
	if len(lines) < 3 {
		t.Fatalf("구성원 행이 없음: %d줄", len(lines))
	}
	if strings.Contains(text, "staff@pl.test") || !strings.Contains(text, "*") {
		t.Errorf("기본 내보내기에 연락처 원문이 있거나 가림 표시가 없음: %q", text)
	}
	// 원문 포함: 관리자는 받고, 직원·감사는 403
	raw := e.file("admin", "/api/org/people/export?format=csv&raw=1")
	if raw.code != 200 || !strings.Contains(string(raw.body), "staff@pl.test") {
		t.Errorf("관리자의 원문 포함 %d", raw.code)
	}
	for _, who := range []string{"staff", "auditor"} {
		if r := e.file(who, "/api/org/people/export?format=csv&raw=1"); r.code != 403 {
			t.Errorf("%s 의 원문 포함 %d(기대 403)", who, r.code)
		}
	}
	// 직원 기본 내보내기: 본인 연락처도 가린 값(파일은 원문을 내지 않는다)
	sf := e.file("staff", "/api/org/people/export?format=csv")
	if sf.code != 200 || strings.Contains(string(sf.body), "staff@pl.test") {
		t.Errorf("직원 내보내기 %d 연락처 원문 포함 여부 %v", sf.code, strings.Contains(string(sf.body), "staff@pl.test"))
	}
}

// XLSX 는 zip(PK) 로 내려오고 둘째 시트에 조건이 남는다. 검색어는 감사에 값이 남지 않는다(AC-707).
func TestExportXLSXAndAuditEntryHidesSearchText(t *testing.T) {
	e := newOrgEnv(t)
	f := e.file("admin", "/api/org/people/export?format=xlsx&q="+url.QueryEscape("구매직원"))
	if f.code != 200 || len(f.body) < 4 || string(f.body[:2]) != "PK" {
		t.Fatalf("XLSX %d %q", f.code, f.body[:minInt(len(f.body), 8)])
	}
	code, body := e.do("auditor", "GET", "/api/audit-logs?targetType=export", "")
	if code != 200 {
		t.Fatalf("감사 조회 %d", code)
	}
	var resp struct {
		Records []struct {
			Action   string `json:"action"`
			TargetID string `json:"targetId"`
			Result   string `json:"result"`
			Diff     []struct {
				Key string `json:"key"`
				New string `json:"new"`
			} `json:"diff"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil || len(resp.Records) == 0 {
		t.Fatalf("내보내기 감사 기록이 없음: %s", body)
	}
	r := resp.Records[0]
	if r.Action != "export.run" || r.TargetID != "org_people" || r.Result != "success" {
		t.Errorf("감사 항목 %+v", r)
	}
	got := map[string]string{}
	for _, d := range r.Diff {
		got[d.Key] = d.New
	}
	if got["format"] != "xlsx" || got["dataset"] != "조직 구성원" || got["filters"] != "검색어=입력함" || got["raw_sensitive"] != "아니오" || got["exported_rows"] == "" {
		t.Errorf("감사 diff %v", got)
	}
	if strings.Contains(body, "구매직원\"") && strings.Contains(got["filters"], "구매직원") {
		t.Error("검색어 값이 감사에 남음")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 감사 로그 내보내기: audit:read 권한자만, 한국어 라벨(내부 동작 코드 없음), 조건 일부가 감사에 남는다.
func TestExportAuditLogs(t *testing.T) {
	e := newOrgEnv(t)
	if f := e.file("staff", "/api/audit-logs/export?format=csv"); f.code != 403 {
		t.Errorf("직원의 감사 로그 내보내기 %d(기대 403)", f.code)
	}
	f := e.file("auditor", "/api/audit-logs/export?format=csv&result=success")
	if f.code != 200 {
		t.Fatalf("감사의 내보내기 %d: %s", f.code, f.body)
	}
	text := strings.TrimPrefix(string(f.body), "\xEF\xBB\xBF")
	lines := strings.Split(strings.TrimSpace(text), "\r\n")
	if lines[0] != "번호,일시,사용자,경로,동작,대상,결과,변경 내용" {
		t.Errorf("머리글 %q", lines[0])
	}
	if len(lines) < 2 {
		t.Fatal("감사 기록 행이 없음")
	}
	for _, code := range []string{"session.login", "purchase_request.", "org.event", "access.denied"} {
		if strings.Contains(text, code) {
			t.Errorf("감사 로그 파일에 내부 동작 코드 %q 가 그대로 있음", code)
		}
	}
	if !strings.Contains(text, "성공") {
		t.Error("결과 라벨(성공)이 없음")
	}
	// 조건이 감사에 남는다(결과=성공, 자유 입력은 입력함)
	f2 := e.file("auditor", "/api/audit-logs/export?format=csv&actorName="+url.QueryEscape("감사")+"&result=success")
	if f2.code != 200 {
		t.Fatalf("조건 내보내기 %d", f2.code)
	}
	_, body := e.do("auditor", "GET", "/api/audit-logs?targetType=export", "")
	if !strings.Contains(body, "사용자=입력함; 결과=성공") || strings.Contains(body, `"사용자=감사`) {
		t.Errorf("조건 문장이 기대와 다름: %s", body)
	}
}

func TestExportBadFormatAndLimit(t *testing.T) {
	e := newOrgEnv(t)
	if f := e.file("admin", "/api/org/people/export?format=pdf"); f.code != 400 {
		t.Errorf("형식 오류 %d", f.code)
	}
	// 상한 초과: 환경 상한을 1 로 낮춘 뒤 거부된다.
	e.d.Export.MaxRows = 1
	f := e.file("admin", "/api/org/people/export?format=csv")
	if f.code != 400 || !strings.Contains(string(f.body), "한 번에 내보낼 수 없습니다") {
		t.Errorf("상한 초과 %d %q", f.code, f.body)
	}
}

// B28 AC-704: 허용 형식 밖의 조건 값은 400 으로 거부되고, 감사에는 값 대신 "잘못된 값"만 남는다. 상한 초과 거부는 조건에 맞는 건수 키를 쓴다.
func TestExportInvalidFilterRejectedAndNotAudited(t *testing.T) {
	e := newOrgEnv(t)
	bad := url.QueryEscape("홍길동 010-1234-5678")
	if f := e.file("auditor", "/api/audit-logs/export?format=csv&result="+bad); f.code != 400 {
		t.Fatalf("잘못된 결과 조건 %d: %s", f.code, f.body)
	}
	if f := e.file("admin", "/api/org/people/export?format=csv&asOf="+bad); f.code != 400 {
		t.Fatalf("잘못된 기준일 %d: %s", f.code, f.body)
	}
	_, body := e.do("auditor", "GET", "/api/audit-logs?targetType=export", "")
	if strings.Contains(body, "홍길동") || strings.Contains(body, "010-1234") {
		t.Errorf("감사에 요청 값이 남음: %s", body)
	}
	if !strings.Contains(body, "결과=잘못된 값") || !strings.Contains(body, "기준일=잘못된 값") {
		t.Errorf("조건 문장에 잘못된 값 표기가 없음: %s", body)
	}
	e.d.Export.MaxRows = 1
	if f := e.file("admin", "/api/org/people/export?format=csv"); f.code != 400 {
		t.Fatalf("상한 초과 %d", f.code)
	}
	_, body = e.do("auditor", "GET", "/api/audit-logs?targetType=export", "")
	if !strings.Contains(body, "matched_rows") {
		t.Errorf("상한 초과 감사에 조건에 맞는 건수 키가 없음: %s", body)
	}
}

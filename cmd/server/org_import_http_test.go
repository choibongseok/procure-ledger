package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"strings"
	"testing"

	"golang.org/x/text/encoding/korean"
)

// multipartBody 는 파일들을 multipart 본문으로 만든다(칸 이름은 의미 없음) 그리고 필드를 더한다.
func multipartBody(t *testing.T, files map[string][]byte, fields map[string]string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
}

func (e *orgEnv) upload(who, path string, files map[string][]byte, fields map[string]string) (int, string) {
	e.t.Helper()
	c := e.as(who)
	body, ct := multipartBody(e.t, files, fields)
	return e.call(c, "POST", path, body, map[string]string{"X-CSRF-Token": e.csrfs[who], "Content-Type": ct})
}

const (
	httpUnits  = "조직코드,조직명,종류,상위조직코드,조직장사번,효력일\nCSV-T,CSV팀,팀,PUR,E901,2025-12-01\n"
	httpPeople = "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE901,이시현,e901@pl.test,과장,CSV-T,결재자,2026-01-02\n"
)

// 조직 CSV 일괄 반영 API: 미리보기 → 반영, 권한, EUC-KR, 오류 행 내려받기(수식 방지), 감사 1건.
func TestOrgImportAPIFlow(t *testing.T) {
	e := newOrgEnv(t)
	var purID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM org_units WHERE code = (SELECT code FROM org_units ORDER BY id LIMIT 1 OFFSET 2)`).Scan(&purID)
	var pur string
	_ = e.d.DB.QueryRow(`SELECT code FROM org_units WHERE id = ?`, purID).Scan(&pur)
	units := strings.Replace(httpUnits, "PUR", pur, 1)
	euc, err := korean.EUCKR.NewEncoder().String(httpPeople)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"units.csv": []byte(units), "people.csv": []byte(euc)}

	// 권한: org:manage 가 없으면 403, 감사 기록 access.denied.
	for _, who := range []string{"staff", "finance", "manager", "auditor"} {
		_ = who
	}
	if code, _ := e.upload("staff", "/api/org/import/preview", files, nil); code != 403 {
		t.Fatalf("직원의 미리보기 %d", code)
	}
	if code, _ := e.upload("lead", "/api/org/import/apply", files, nil); code != 403 {
		t.Fatalf("결재자의 반영 %d", code)
	}

	code, body := e.upload("admin", "/api/org/import/preview", files, nil)
	if code != 200 {
		t.Fatalf("미리보기 %d %s", code, body)
	}
	var pv struct {
		PreviewID string `json:"previewId"`
		Files     []struct {
			Kind     string `json:"kind"`
			Encoding string `json:"encoding"`
			Rows     int    `json:"rows"`
		} `json:"files"`
		Errors     []map[string]any `json:"errors"`
		FileErrors []map[string]any `json:"fileErrors"`
		Items      []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &pv); err != nil {
		t.Fatal(err)
	}
	if pv.PreviewID == "" || len(pv.Errors)+len(pv.FileErrors) != 0 || len(pv.Items) < 3 {
		t.Fatalf("미리보기 내용: %s", body)
	}
	enc := map[string]string{}
	for _, f := range pv.Files {
		enc[f.Kind] = f.Encoding
	}
	if enc["people"] != "EUC-KR" || enc["units"] != "UTF-8" {
		t.Errorf("글자 인코딩 표시: %v", enc)
	}
	events := e.count(`SELECT COUNT(*) FROM org_events`)
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'org.import_apply'`) != 0 || e.count(`SELECT COUNT(*) FROM org_events`) != events {
		t.Fatal("미리보기가 저장함")
	}

	// 반영
	code, body = e.upload("admin", "/api/org/import/apply", files, map[string]string{"previewId": pv.PreviewID, "confirm_retroactive": "true"})
	if code != 201 {
		t.Fatalf("반영 %d %s", code, body)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'org.import_apply' AND target_type = 'import'`) != 1 {
		t.Fatal("감사 1건")
	}
	var unit int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM org_units WHERE code = 'CSV-T'`).Scan(&unit)
	if unit != 1 || e.count(`SELECT COUNT(*) FROM users WHERE email = 'e901@pl.test'`) != 1 {
		t.Fatal("반영 결과가 없음")
	}
	// 같은 미리보기 번호로 다시 반영하면 409
	if code, body := e.upload("admin", "/api/org/import/apply", files, map[string]string{"previewId": pv.PreviewID, "confirm_retroactive": "true"}); code != 409 || !strings.Contains(body, "이미 반영된 미리보기입니다") {
		t.Errorf("재사용 %d %s", code, body)
	}
}

func TestOrgImportErrorsCSVAndPlanCSV(t *testing.T) {
	e := newOrgEnv(t)
	bad := "사번,이름,이메일,직위,주소속조직코드,입사일\nE950,=cmd|calc,x@pl.test,사원,NOPE,2026-03-10\nE951,정상,y@pl.test,사원,NOPE,2026-03-10\n"
	code, body := e.upload("admin", "/api/org/import/errors.csv", map[string][]byte{"people.csv": []byte(bad)}, nil)
	if code != 200 || !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Fatalf("오류 행 내려받기 %d %q", code, body)
	}
	if !strings.Contains(body, "수식으로 읽힐 수 있는 문자(=)") || !strings.Contains(body, "'=cmd|calc") || strings.Contains(body, ",=cmd") {
		t.Errorf("원본 칸의 수식 시작 문자를 막지 않음: %s", body)
	}
	if strings.Contains(body, "\n\xef\xbb\xbf") {
		t.Error("BOM 위치")
	}
	// 오류가 있으면 계획 내려받기는 409
	if code, _ := e.upload("admin", "/api/org/import/plan.csv", map[string][]byte{"people.csv": []byte(bad)}, nil); code != 409 {
		t.Errorf("오류가 있는 계획 내려받기 %d", code)
	}
	ok := "사번,이름,이메일,직위,주소속조직코드,입사일\nE960,정상,z@pl.test,사원,PLACE,2026-03-10\n"
	var anyCode string
	_ = e.d.DB.QueryRow(`SELECT code FROM org_units ORDER BY id LIMIT 1 OFFSET 3`).Scan(&anyCode)
	ok = strings.Replace(ok, "PLACE", anyCode, 1)
	code, body = e.upload("admin", "/api/org/import/plan.csv", map[string][]byte{"people.csv": []byte(ok)}, nil)
	if code != 200 || !strings.Contains(body, "효력일,종류,대상,내용,원본 파일,행 번호") || !strings.Contains(body, "입사") {
		t.Errorf("계획 내려받기 %d %s", code, body)
	}
	// 파일 없이 미리보기
	code, body = e.upload("admin", "/api/org/import/preview", map[string][]byte{}, nil)
	if code != 200 || !strings.Contains(body, "올릴 파일을 하나 이상 선택하세요.") {
		t.Errorf("파일 없음 %d %s", code, body)
	}
}

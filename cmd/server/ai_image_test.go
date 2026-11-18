package main

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// 실제 서비스 조립 위에서 이미지 증빙 경로를 HTTP 로 검증한다(mock 공급자):
// 직원 거부 → 설정 꺼짐 거부 → 관리자가 켬 → 재무 통과(전송 감사·사용량) → 직원이 설정을 바꿀 수 없음.
func TestImageEvidenceOverHTTP(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	h := handlerFor(buildRoutes(cfg, d), d)

	token := func(email string) string {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "img", 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	staff, finance, admin := token("staff@pl.test"), token("finance@pl.test"), token("admin@pl.test")

	var staffDept int64
	var staffID int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&staffID, &staffDept); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) int64 {
		return dbtest.ExecID(t, d.DB, q, args...)
	}
	now := "2026-11-10T00:00:00Z"
	vendor := exec(`INSERT INTO vendors (name, business_no) VALUES ('한빛상사', '123-45-67890')`)
	item := exec(`INSERT INTO items (sku, name) VALUES ('IMG-1', '볼트 M8')`)
	pr := exec(`INSERT INTO purchase_requests (no, requester_id, department_id, vendor_id, title, status, total_amount, created_at, updated_at)
		VALUES ('PR-IMG-1', ?, ?, ?, '이미지', 'ordered', 5000, ?, ?)`, staffID, staffDept, vendor, now, now)
	po := exec(`INSERT INTO purchase_orders (no, pr_id, vendor_id, status, total_amount, created_at) VALUES ('PO-IMG-1', ?, ?, 'open', 5000, ?)`, pr, vendor, now)
	exec(`INSERT INTO po_lines (po_id, item_id, quantity, unit_price) VALUES (?, ?, 10, 500)`, po, item)

	count := func(q string, args ...any) int {
		var n int
		if err := d.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	audits := func(action, result string) int {
		return count(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND result = ?`, action, result)
	}
	do := func(method, path, tok, ctype string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	upload := func(tok string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("po_id", itoa(po))
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="scan.png"`)
		hdr.Set("Content-Type", "image/png")
		part, _ := mw.CreatePart(hdr)
		_, _ = part.Write([]byte(isolationDoc))
		_ = mw.Close()
		return do("POST", "/api/documents/verify", tok, mw.FormDataContentType(), buf.Bytes())
	}
	capability := func(tok string) string {
		w := do("GET", "/api/documents/capabilities", tok, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
		}
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out["imageUpload"]
	}
	setting := func(tok string, on bool) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]bool{"enabled": on})
		return do("PUT", "/api/admin/settings/llm-image", tok, "application/json", b)
	}

	// 1. 직원: 권한 거부(설정과 무관). 문서·사용량 불변, 거부 감사.
	if got := capability(staff); got != "forbidden_role" {
		t.Fatalf("직원 capabilities: %s", got)
	}
	if w := upload(staff); w.Code != http.StatusForbidden {
		t.Fatalf("직원 이미지 업로드는 403: %d %s", w.Code, w.Body.String())
	}
	if audits("document.upload_image", "denied") != 1 {
		t.Fatal("직원 이미지 거부 감사가 없음")
	}

	// 2. 재무 + 설정 꺼짐(기본): 403 + 한국어 안내, LLM 미호출.
	if got := capability(finance); got != "disabled" {
		t.Fatalf("재무 capabilities(기본 꺼짐): %s", got)
	}
	w := upload(finance)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "이미지 증빙 전송이 꺼져 있어") {
		t.Fatalf("꺼짐 거부 안내: %d %s", w.Code, w.Body.String())
	}
	if count(`SELECT COUNT(*) FROM documents`) != 0 || count(`SELECT COUNT(*) FROM llm_usage`) != 0 {
		t.Fatal("거부인데 문서가 저장되었거나 LLM 이 호출됨")
	}
	if audits("llm_image.blocked", "denied") != 1 {
		t.Fatal("설정 꺼짐 거부 감사가 없음")
	}

	// 3. 직원·재무는 설정을 바꿀 수 없고(권한 선언), 거부는 감사에 남는다.
	for name, tok := range map[string]string{"직원": staff, "재무": finance} {
		if w := setting(tok, true); w.Code != http.StatusForbidden {
			t.Fatalf("%s 의 설정 변경은 403: %d", name, w.Code)
		}
	}
	if capability(finance) != "disabled" {
		t.Fatal("권한 없는 변경 시도로 설정이 바뀜")
	}

	// 4. 관리자가 켠다: 감사(setting.update) 후 재무는 통과.
	if w := setting(admin, true); w.Code != http.StatusOK {
		t.Fatalf("관리자 설정 변경: %d %s", w.Code, w.Body.String())
	}
	if audits("setting.update", "success") != 1 {
		t.Fatal("설정 변경 감사가 없음")
	}
	if capability(finance) != "allowed" || capability(staff) != "forbidden_role" {
		t.Fatal("켠 뒤 capabilities")
	}
	w = upload(finance)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"match"`) {
		t.Fatalf("재무 이미지 업로드는 판정되어야 함: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"field":"grounding"`) {
		t.Fatalf("원문 교차 확인 미적용이 근거에 드러나야 함: %s", w.Body.String())
	}
	if audits("llm_image.sent", "success") != 1 || count(`SELECT COUNT(*) FROM llm_usage WHERE status = 'ok'`) != 1 {
		t.Fatal("전송 감사·사용량 기록이 없음")
	}
	// 켠 뒤에도 직원은 여전히 거부
	if w := upload(staff); w.Code != http.StatusForbidden {
		t.Fatalf("켠 뒤에도 직원은 403: %d", w.Code)
	}

	// 5. 감사 체인은 이 모든 기록 뒤에도 유효하다.
	if res, err := d.AuditReader.Verify(ctx); err != nil || !res.OK {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
}

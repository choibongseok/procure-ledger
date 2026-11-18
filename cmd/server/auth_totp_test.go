package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/config"
)

func testHOTP(secret []byte, counter uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(b[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// B30 단계 3: 필수 역할(admin)이 제한 세션에서 2단계 인증을 등록하고, 이후 티켓→코드로 로그인한다. 실제 라우트·쿠키·감사 체인.
func TestTOTPHTTPFlow(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	e := newPlatformEnvWith(t, true, func(c *config.Config) { cheapArgon(c); c.Auth.SecretKey = key })
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	csrf := e.csrf(admin)
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	_, body := e.call(admin, "POST", "/api/admin/users/"+itoa(staffID)+"/auth/start", `{"via":"temp"}`, map[string]string{"X-CSRF-Token": csrf})
	var st struct {
		TempPassword string `json:"tempPassword"`
	}
	_ = json.Unmarshal([]byte(body), &st)
	if _, err := e.d.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, staffID); err != nil { // 필수 역할 사용자로 만든다
		t.Fatal(err)
	}

	u := e.browser()
	if code, body := e.call(u, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"`+st.TempPassword+`"}`, jsonHdr(nil)); code != 200 || !strings.Contains(body, "password_change") {
		t.Fatalf("로그인: %d %s", code, body)
	}
	me := e.csrf(u)
	hdr := jsonHdr(map[string]string{"X-CSRF-Token": me})
	if code, body := e.call(u, "POST", "/api/auth/password/change", `{"current":"`+st.TempPassword+`","new":"Correct-Horse-9"}`, hdr); code != 204 {
		t.Fatalf("변경: %d %s", code, body)
	}
	if _, body := e.call(u, "GET", "/api/me", "", nil); !strings.Contains(body, `"setup":"totp_enroll"`) || !strings.Contains(body, `"totpRequired":true`) {
		t.Fatalf("필수 역할인데 2단계 등록이 강제되지 않음: %s", body)
	}
	if code, _ := e.call(u, "GET", "/api/admin/users", "", nil); code != 403 {
		t.Fatalf("제한 세션이 관리 API 를 엶: %d", code)
	}
	code, body := e.call(u, "POST", "/api/auth/totp/enroll", `{}`, hdr)
	var en struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	_ = json.Unmarshal([]byte(body), &en)
	if code != 200 || en.Secret == "" || !strings.HasPrefix(en.URI, "otpauth://totp/") {
		t.Fatalf("등록 시작: %d %s", code, body)
	}
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	step := uint64(time.Now().Unix() / 30)
	code, body = e.call(u, "POST", "/api/auth/totp/confirm", `{"code":"`+testHOTP(raw, step)+`"}`, hdr)
	var cf struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	_ = json.Unmarshal([]byte(body), &cf)
	if code != 200 || len(cf.RecoveryCodes) != 8 {
		t.Fatalf("확인: %d %s", code, body)
	}
	if _, body := e.call(u, "GET", "/api/me", "", nil); !strings.Contains(body, `"setup":""`) || !strings.Contains(body, `"totpEnrolled":true`) {
		t.Fatalf("등록 뒤에도 제한 세션: %s", body)
	}
	e.call(u, "POST", "/api/auth/logout", "", hdr)

	// 비밀번호 → 티켓 → 코드. 등록에 쓴 구간의 코드는 재사용할 수 없고(재생 방지) 다음 구간 코드는 된다.
	v := e.browser()
	code, body = e.call(v, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Correct-Horse-9"}`, jsonHdr(nil))
	var tk struct {
		TOTP   bool   `json:"totp"`
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal([]byte(body), &tk)
	if code != 200 || !tk.TOTP || tk.Ticket == "" {
		t.Fatalf("티켓: %d %s", code, body)
	}
	if code, _ := e.call(v, "GET", "/api/me", "", nil); code != 401 {
		t.Fatalf("티켓 단계에서 세션이 생김: %d", code)
	}
	if code, _ := e.call(v, "POST", "/api/auth/login/totp", `{"ticket":"`+tk.Ticket+`","code":"`+testHOTP(raw, step)+`"}`, jsonHdr(nil)); code != 401 {
		t.Fatalf("같은 구간 코드 재생: %d", code)
	}
	if code, body := e.call(v, "POST", "/api/auth/login/totp", `{"ticket":"`+tk.Ticket+`","code":"`+testHOTP(raw, step+1)+`"}`, jsonHdr(nil)); code != 200 {
		t.Fatalf("다음 구간 코드: %d %s", code, body)
	}
	if code, _ := e.call(v, "GET", "/api/admin/users", "", nil); code != 200 {
		t.Fatalf("로그인 뒤 관리 API: %d", code)
	}
	if e.auditCount("auth.totp_enroll", "success") != 2 { // 시작 + 확인
		t.Error("2단계 등록 감사가 2행이 아님")
	}
	if res, err := e.d.AuditReader.Verify(t.Context()); err != nil || !res.OK {
		t.Fatalf("체인 검증: %+v %v", res, err)
	}
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%'`, cf.RecoveryCodes[0]).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("복구 코드가 감사에 남음")
	}
}

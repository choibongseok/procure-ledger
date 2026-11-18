package main

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/config"
)

// 검증자 지적 2·3: 제한 세션 403 은 본문 코드 auth_setup_required 와 X-Auth-Setup 을 같이 싣고,
// Origin·Referer 가 모두 없는 로그인 POST 는 거부된다(Referer 가 같은 출처면 허용).
func TestSetupRequiredBodyAndOriginRequired(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
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
	creds := `{"email":"staff@pl.test","password":"` + st.TempPassword + `"}`

	post := func(c *http.Client, hdr map[string]string) int {
		req, _ := http.NewRequest("POST", e.base+"/api/auth/login/password", strings.NewReader(creds))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post(e.browser(), nil); code != 403 {
		t.Fatalf("Origin·Referer 없는 로그인 POST: %d", code)
	}
	if code := post(e.browser(), map[string]string{"Referer": "https://evil.example/x"}); code != 403 {
		t.Fatalf("다른 출처 Referer: %d", code)
	}
	u := e.browser()
	if code := post(u, map[string]string{"Referer": e.base + "/login"}); code != 200 {
		t.Fatalf("같은 출처 Referer: %d", code)
	}
	req, _ := http.NewRequest("GET", e.base+"/api/tokens", nil)
	res, err := u.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(strings.Builder)
	b := make([]byte, 512)
	n, _ := res.Body.Read(b)
	buf.Write(b[:n])
	res.Body.Close()
	if res.StatusCode != 403 || res.Header.Get("X-Auth-Setup") != "password_change" || !strings.Contains(buf.String(), `"code":"auth_setup_required"`) {
		t.Fatalf("제한 세션 403: %d %q %s", res.StatusCode, res.Header.Get("X-Auth-Setup"), buf.String())
	}
}

// 복구 코드는 실제 서버 경로에서 1회용이다. 등록은 admin(필수 역할)이 제한 세션에서 한다.
func TestTOTPRecoveryCodeOverHTTP(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	e := newPlatformEnvWith(t, true, func(c *config.Config) { cheapArgon(c); c.Auth.SecretKey = key })
	adm := e.browser()
	e.login(adm, "admin@pl.test")
	csrf := e.csrf(adm)
	var uid int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&uid)
	_, body := e.call(adm, "POST", "/api/admin/users/"+itoa(uid)+"/auth/start", `{"via":"temp"}`, map[string]string{"X-CSRF-Token": csrf})
	var st struct {
		TempPassword string `json:"tempPassword"`
	}
	_ = json.Unmarshal([]byte(body), &st)
	e.d.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, uid)
	u := e.browser()
	e.call(u, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"`+st.TempPassword+`"}`, jsonHdr(nil))
	hdr := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(u)})
	e.call(u, "POST", "/api/auth/password/change", `{"current":"`+st.TempPassword+`","new":"Correct-Horse-9"}`, hdr)
	_, body = e.call(u, "POST", "/api/auth/totp/enroll", `{}`, hdr)
	var en struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal([]byte(body), &en)
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	_, body = e.call(u, "POST", "/api/auth/totp/confirm", `{"code":"`+testHOTP(raw, uint64(time.Now().Unix()/30))+`"}`, hdr)
	var cf struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	_ = json.Unmarshal([]byte(body), &cf)
	if len(cf.RecoveryCodes) != 8 {
		t.Fatalf("복구 코드: %s", body)
	}
	ticket := func() string {
		_, b := e.call(e.browser(), "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Correct-Horse-9"}`, jsonHdr(nil))
		var tk struct {
			Ticket string `json:"ticket"`
		}
		_ = json.Unmarshal([]byte(b), &tk)
		if tk.Ticket == "" {
			t.Fatalf("티켓 없음: %s", b)
		}
		return tk.Ticket
	}
	c := e.browser()
	code, b := e.call(c, "POST", "/api/auth/login/totp", `{"ticket":"`+ticket()+`","code":"`+cf.RecoveryCodes[0]+`"}`, jsonHdr(nil))
	if code != 200 {
		t.Fatalf("복구 코드 로그인: %d %s", code, b)
	}
	if code, _ := e.call(e.browser(), "POST", "/api/auth/login/totp", `{"ticket":"`+ticket()+`","code":"`+cf.RecoveryCodes[0]+`"}`, jsonHdr(nil)); code != 401 {
		t.Fatalf("쓴 복구 코드 재사용: %d", code)
	}
	if code, _ := e.call(e.browser(), "POST", "/api/auth/login/totp", `{"ticket":"`+ticket()+`","code":"`+cf.RecoveryCodes[1]+`"}`, jsonHdr(nil)); code != 200 {
		t.Fatalf("다른 복구 코드: %d", code)
	}
}

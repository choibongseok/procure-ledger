package netsafe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.9", "192.168.1.1", "169.254.169.254", "fe80::1", "100.64.0.1", "0.0.0.0", "224.0.0.1",
		"192.0.2.5", "198.51.100.7", "203.0.113.9", "240.0.0.1", "::ffff:127.0.0.1", "64:ff9b::1", "2001:db8::1"} {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("막아야 하는 주소: %s", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "52.1.2.3", "2606:4700:4700::1111"} {
		if BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("공인 주소를 막음: %s", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	any := URLOptions{}
	for _, u := range []string{"https://hooks.example.com/x", "https://a.b.example.co.kr/hook/1?z=1", "https://example.com:443/x"} {
		if _, err := ValidateURL(u, any); err != nil {
			t.Errorf("받아야 한다 %s: %v", u, err)
		}
	}
	bad := map[string]string{
		"http://example.com/x":              "http",
		"https://127.0.0.1/x":               "IP",
		"https://[::1]/x":                   "IPv6",
		"https://user:pw" + "@example.com/": "사용자 정보",
		"https://example.com/x#f":           "프래그먼트",
		"https://example.com:8443/x":        "포트",
		"https://example.com/a b":           "공백",
		"":                                  "빈 값",
		"ftp://example.com/x":               "스킴",
		"https:///x":                        "호스트 없음",
	}
	for u, why := range bad {
		if _, err := ValidateURL(u, any); err == nil {
			t.Errorf("거부되어야 한다(%s): %q", why, u)
		}
	}
	// 허용 호스트 목록이 있으면 엄격하다. 허용한 포트는 받는다.
	strict := URLOptions{AllowedHost: func(h string) bool { return HostMatches("*.corp.example", h) }, Ports: []string{"8443"}}
	if _, err := ValidateURL("https://hook.corp.example:8443/x", strict); err != nil {
		t.Errorf("허용 호스트·포트: %v", err)
	}
	if _, err := ValidateURL("https://other.example.com/x", strict); err == nil || !strings.Contains(err.Error(), "허용되지 않은 호스트") {
		t.Errorf("목록 밖 호스트: %v", err)
	}
	if _, err := ValidateURL("https://hook.corp.example:9000/x", strict); err == nil {
		t.Error("허용하지 않은 포트")
	}
	// 로컬 예외: 루프백의 http·임의 포트만, 호스트 허용 판정은 건너뛴다.
	local := URLOptions{AllowLocal: true, AllowedHost: func(string) bool { return false }}
	if _, err := ValidateURL("http://127.0.0.1:9999/x", local); err != nil {
		t.Errorf("로컬 예외: %v", err)
	}
	if _, err := ValidateURL("http://10.0.0.5/x", local); err == nil {
		t.Error("로컬 예외는 루프백만 푼다")
	}
	if _, err := ValidateURL("http://127.0.0.1:9999/x", URLOptions{}); err == nil {
		t.Error("예외가 꺼져 있으면 http 거부")
	}
}

func TestHostPatterns(t *testing.T) {
	if !HostMatches("*.example.com", "a.example.com") || HostMatches("*.example.com", "example.com") || HostMatches("*.example.com", "evilexample.com") || !HostMatches("Hooks.Example.com", "hooks.example.COM") {
		t.Error("HostMatches")
	}
	for _, h := range []string{"hooks.example.com", "*.example.com", "a-b.example.co.kr"} {
		if !ValidHostPattern(h) {
			t.Errorf("유효해야 함: %s", h)
		}
	}
	for _, h := range []string{"", "example", "*.com.", "a/b.example.com", "1.2.3.4", "*.*.example.com", "ex ample.com", "hooks.example.com:443"} {
		if ValidHostPattern(h) {
			t.Errorf("무효여야 함: %q", h)
		}
	}
}

func TestClientBlocksLoopbackUnlessAllowedAndNeverFollowsRedirects(t *testing.T) {
	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 10_000)))
	}))
	t.Cleanup(srv.Close)
	blocked := NewClient(ClientOptions{})
	if _, err := blocked.Get(srv.URL); !errors.Is(err, ErrBlockedAddr) || hit != 0 {
		t.Fatalf("루프백 연결이 막혀야 한다: %v (서버 도달 %d)", err, hit)
	}
	c := NewClient(ClientOptions{AllowLocal: true})
	res, err := c.Get(srv.URL + "/redir")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || hit != 1 {
		t.Fatalf("리다이렉트를 따르면 안 된다: %d 서버 도달 %d", res.StatusCode, hit)
	}
	res, err = c.Get(srv.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if b := ReadLimited(res.Body); len(b) != 4096 {
		t.Fatalf("응답은 4KB 까지만 읽는다: %d", len(b))
	}
}

func TestResolveCheck(t *testing.T) {
	ctx := context.Background()
	look := func(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
		return func(context.Context, string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, a := range addrs {
				out = append(out, netip.MustParseAddr(a))
			}
			return out, nil
		}
	}
	if err := ResolveCheck(ctx, "h.example.com", false, look("8.8.8.8", "1.1.1.1")); err != nil {
		t.Errorf("공인 주소: %v", err)
	}
	if err := ResolveCheck(ctx, "h.example.com", false, look("8.8.8.8", "10.0.0.1")); err == nil {
		t.Error("하나라도 내부 주소면 거부")
	}
	if err := ResolveCheck(ctx, "h.example.com", false, look("127.0.0.1")); err == nil {
		t.Error("루프백 거부")
	}
	if err := ResolveCheck(ctx, "h.example.com", true, look("127.0.0.1")); err != nil {
		t.Errorf("로컬 예외: %v", err)
	}
	if err := ResolveCheck(ctx, "h.example.com", false, look()); err == nil {
		t.Error("해석 결과가 없으면 거부")
	}
	if err := ResolveCheck(ctx, "h.example.com", false, func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("nx") }); err == nil {
		t.Error("해석 실패는 거부")
	}
}

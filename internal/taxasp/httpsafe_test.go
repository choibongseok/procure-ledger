package taxasp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/taxasp"
)

func TestValidateEndpointRules(t *testing.T) {
	for _, bad := range []string{
		"", "http://api.asp.example.com/x", "https://127.0.0.1/x", "https://10.1.2.3/x", "https://169.254.169.254/latest", "https://localhost/x", "https://a.localhost/x",
		"https://svc.internal/x", "https://printer.local/x", "https://u:p@api.asp.example.com/x", "https://api.asp.example.com/x#frag",
	} {
		if _, err := taxasp.ValidateEndpoint(bad, false); !errors.Is(err, taxasp.ErrEndpoint) {
			t.Errorf("%q: 거부돼야 한다: %v", bad, err)
		} else if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "10.1.2.3") {
			t.Errorf("오류 문장에 주소가 있다: %v", err)
		}
	}
	if _, err := taxasp.ValidateEndpoint("https://api.asp.example.com/v1/invoices", false); err != nil {
		t.Fatalf("공개 https: %v", err)
	}
}

// 공용 도우미는 리다이렉트를 따라가지 않고, 본문을 상한까지만 읽으며, 내부 주소는 allowLocal 이 아니면 보내지도 않는다.
func TestDoDoesNotFollowRedirectsAndLimitsBody(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/redir":
			http.Redirect(w, r, "/target", http.StatusFound)
		case "/big":
			w.Write([]byte(strings.Repeat("x", taxasp.MaxResponseBytes+1024)))
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	hc := taxasp.NewHTTPClient(true, 5*time.Second)
	code, _, err := taxasp.Do(ctx, hc, true, "GET", srv.URL+"/redir", nil, nil)
	if err != nil || code != http.StatusFound || hits != 1 {
		t.Fatalf("리다이렉트는 따라가지 않는다: code=%d hits=%d err=%v", code, hits, err)
	}
	code, body, err := taxasp.Do(ctx, hc, true, "GET", srv.URL+"/big", nil, nil)
	if err != nil || code != 200 || len(body) != taxasp.MaxResponseBytes {
		t.Fatalf("본문 상한: code=%d len=%d err=%v", code, len(body), err)
	}
	// allowLocal 이 아니면 루프백 주소는 검사에서 거부되고 서버에 닿지 않는다.
	hits = 0
	if _, _, err := taxasp.Do(ctx, taxasp.NewHTTPClient(false, 5*time.Second), false, "GET", srv.URL+"/x", nil, nil); !errors.Is(err, taxasp.ErrEndpoint) || hits != 0 {
		t.Fatalf("내부 주소는 보내지 않는다: hits=%d err=%v", hits, err)
	}
}

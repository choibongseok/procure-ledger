// Package netsafe 는 서버가 밖으로 나가는 HTTP 호출(알림 웹훅 B36, 변경 웹훅 B42)이 공유하는 안전 부품이다(B42 D3).
// 두 곳의 동작은 이 패키지로 옮기기 전과 같다: 표·상태 전이·화면은 각자 두고 주소 검사·SSRF 방어·HTTP 클라이언트·허용 호스트 판정만 모았다.
//
// 보안 원칙
//   - 주소는 https 만(로컬 시험용 예외는 AllowLocal), 사용자 정보·# 부분 없음, IP 주소 대신 호스트 이름, 기본 포트(허용한 포트만).
//   - 연결 직전에 실제로 연결할 IP 를 검사한다(Dialer.Control): 등록 때는 공인 주소였다가 나중에 내부 주소로 바뀌는 DNS 재해석 우회를 막는다.
//     루프백·사설·링크로컬(클라우드 메타데이터 포함)·CGNAT·멀티캐스트·미지정·문서용·NAT64 주소는 거부한다.
//   - 리다이렉트를 따르지 않고, 환경 프록시를 쓰지 않고(프록시가 검사를 우회하지 못하게), 응답 본문은 4KB 까지만 읽는다.
package netsafe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddr 는 연결하려던 주소가 막힌 주소(내부망 등)라는 뜻이다.
var ErrBlockedAddr = errors.New("차단된 주소")

// BlockedIP 는 웹훅이 연결하면 안 되는 주소(루프백·사설·링크로컬·CGNAT·멀티캐스트·미지정·문서용·벤치마크)인지다.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32", "100::/64"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// HostMatches 는 host 가 패턴(정확한 이름 또는 "*.도메인" = 그 도메인의 하위 이름 한 단계 이상)과 맞는지다.
func HostMatches(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".도메인"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return host == pattern
}

// ValidHostPattern 은 관리자가 더하는 허용 호스트 항목의 형식(예: hooks.example.com, *.example.com)을 검사한다.
func ValidHostPattern(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" || len(p) > 253 || strings.ContainsAny(p, " /:@?#\\") {
		return false
	}
	body := strings.TrimPrefix(p, "*.")
	if !strings.Contains(body, ".") || strings.HasPrefix(body, ".") || strings.HasSuffix(body, ".") || strings.Contains(body, "..") || strings.Contains(body, "*") {
		return false
	}
	if ip, err := netip.ParseAddr(body); err == nil && ip.IsValid() {
		return false // IP 주소는 허용 호스트가 될 수 없다
	}
	for _, r := range body {
		if !(r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

// URLOptions 는 주소 검사 설정이다.
type URLOptions struct {
	// AllowedHost 는 호스트 허용 판정이다. nil 이면 모든 호스트(공인 IP 에 한함: 연결 직전 검사가 막는다)를 허용한다.
	AllowedHost func(host string) bool
	// Ports 는 443 말고 허용할 포트다.
	Ports []string
	// AllowLocal 이면 루프백 주소의 http 와 임의 포트를 허용하고 호스트 허용 판정을 건너뛴다(로컬 시험 전용).
	AllowLocal bool
}

// ValidateURL 은 등록·발송 때 주소를 검사한다: https, 사용자 정보 없음, # 없음, 호스트 이름(IP 아님), 기본 포트(또는 허용한 포트), 허용 호스트.
// 오류 문구는 한국어 업무 문장이고 주소 값을 넣지 않는다(호스트만).
func ValidateURL(raw string, o URLOptions) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, errors.New("웹훅 주소를 입력하세요(2048자 이하).")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return nil, errors.New("웹훅 주소에 공백이나 제어문자를 쓸 수 없습니다.")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("웹훅 주소 형식이 올바르지 않습니다.")
	}
	host := u.Hostname()
	if u.User != nil {
		return nil, errors.New("웹훅 주소에 사용자 정보(이름:비밀번호@)를 넣을 수 없습니다.")
	}
	if u.Fragment != "" {
		return nil, errors.New("웹훅 주소에 # 부분을 쓸 수 없습니다.")
	}
	if o.AllowLocal {
		if ip, err := netip.ParseAddr(host); (err == nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost") {
			if u.Scheme != "http" && u.Scheme != "https" {
				return nil, errors.New("웹훅 주소는 http 또는 https 여야 합니다.")
			}
			return u, nil
		}
	}
	if u.Scheme != "https" {
		return nil, errors.New("웹훅 주소는 https 여야 합니다.")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, errors.New("웹훅 주소에는 IP 주소 대신 호스트 이름을 써야 합니다.")
	}
	if p := u.Port(); p != "" && p != "443" {
		ok := false
		for _, a := range o.Ports {
			ok = ok || a == p
		}
		if !ok {
			return nil, errors.New("웹훅 주소의 포트는 443 이어야 합니다.")
		}
	}
	if o.AllowedHost != nil && !o.AllowedHost(host) {
		return nil, fmt.Errorf("허용되지 않은 호스트입니다(%s). 관리자가 허용 호스트를 추가해야 합니다.", host)
	}
	return u, nil
}

// ClientOptions 는 안전한 HTTP 클라이언트 설정이다.
type ClientOptions struct {
	// AllowLocal 은 루프백 주소로의 연결을 허용한다(로컬 시험 전용).
	AllowLocal bool
	// Timeout 은 요청 전체 제한이다(기본 10초). 연결 제한은 5초다.
	Timeout time.Duration
}

// NewClient 는 SSRF 방어가 들어간 HTTP 클라이언트를 만든다: 연결 직전 IP 검사, 환경 프록시 없음, 리다이렉트 미추종, 연결 유지 없음, 응답 헤더 16KB 상한.
func NewClient(o ClientOptions) *http.Client {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		// Control 은 이름 해석이 끝난 뒤 실제로 연결할 주소마다 불린다. 등록 때가 아니라 연결 직전의 IP 를 검사한다.
		Control: func(network, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedAddr
			}
			ip, err := netip.ParseAddr(h)
			if err != nil {
				return ErrBlockedAddr
			}
			if BlockedIP(ip) && !(o.AllowLocal && ip.Unmap().IsLoopback()) {
				return ErrBlockedAddr
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                  nil, // 환경 프록시를 쓰지 않는다(검사 우회 방지)
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  o.Timeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ResolveCheck 는 호스트 이름을 지금 해석해 모든 주소가 연결 가능한 공인 주소인지 본다(등록·시험 때 빠른 거부용).
// 실제 연결 직전의 검사는 클라이언트의 Dialer.Control 이 한다. lookup 이 nil 이면 기본 해석기다.
func ResolveCheck(ctx context.Context, host string, allowLocal bool, lookup func(ctx context.Context, host string) ([]netip.Addr, error)) error {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var addrs []netip.Addr
	var err error
	if lookup != nil {
		addrs, err = lookup(rctx, host)
	} else {
		addrs, err = net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	}
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("웹훅 주소의 호스트를 찾을 수 없습니다(%s).", host)
	}
	for _, a := range addrs {
		if BlockedIP(a) && !(allowLocal && a.Unmap().IsLoopback()) {
			return errors.New("웹훅 주소가 내부 주소로 연결됩니다. 내부 주소는 쓸 수 없습니다.")
		}
	}
	return nil
}

// ReadLimited 는 응답 본문을 최대 4KB 까지만 읽는다(기록·오류에 쓰지 않는다).
func ReadLimited(r io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return b
}

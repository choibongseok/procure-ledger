package taxasp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// 실제 대행사 어댑터가 쓰는 공용 HTTP 도우미(B43 후속, 명세 8.4). 어댑터는 자기 http.Client 를 만들지 말고 이것을 쓴다:
//   - 주소 검사: https·공개 호스트·기본 포트(netsafe.ValidateURL). 모의 제공자도 같은 규칙이다(내부 주소는 저장·활성할 수 없다).
//   - 연결 직전 IP 검사(루프백·사설·링크로컬·메타데이터 차단)와 리다이렉트 미추종(netsafe.NewClient).
//   - 응답 본문 상한과 오류 문장에 주소·본문을 넣지 않는 분류.

// MaxResponseBytes 는 대행사 응답 본문의 읽기 상한이다.
const MaxResponseBytes = 4 << 20

// ErrEndpoint 는 대행사 주소가 규칙에 맞지 않을 때다(주소 값은 문장에 없다).
var ErrEndpoint = errors.New("대행사 주소가 올바르지 않거나 쓸 수 없는 주소입니다")

// ValidateEndpoint 는 대행사 주소를 검사한다. allowLocal 은 로컬 시험(루프백 허용) 전용이다.
func ValidateEndpoint(raw string, allowLocal bool) (*url.URL, error) {
	u, err := netsafe.ValidateURL(raw, netsafe.URLOptions{AllowLocal: allowLocal})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrEndpoint, strings.ReplaceAll(err.Error(), "웹훅 주소", "대행사 주소"))
	}
	// 이름 해석 없이 알 수 있는 내부 이름도 등록에서 막는다(실제 차단은 연결 직전 IP 검사가 한다).
	if h := strings.ToLower(u.Hostname()); !allowLocal && (h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal")) {
		return nil, fmt.Errorf("%w: 내부 주소는 쓸 수 없습니다.", ErrEndpoint)
	}
	return u, nil
}

// NewHTTPClient 는 대행사 호출용 안전한 클라이언트다(연결 직전 IP 검사, 리다이렉트 미추종, 환경 프록시 없음).
func NewHTTPClient(allowLocal bool, timeout time.Duration) *http.Client {
	return netsafe.NewClient(netsafe.ClientOptions{AllowLocal: allowLocal, Timeout: timeout})
}

// Do 는 주소를 다시 검사한 뒤 요청을 보내고 상태 코드와 본문(상한까지)을 돌려준다. 리다이렉트(3xx)는 따라가지 않고 그대로 돌려준다.
// 연결 오류는 주소·호스트를 넣지 않은 일반 문장이다.
func Do(ctx context.Context, hc *http.Client, allowLocal bool, method, rawURL string, header http.Header, body []byte) (int, []byte, error) {
	u, err := ValidateEndpoint(rawURL, allowLocal)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, ErrEndpoint
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, errors.New("대행사에 연결하지 못했습니다")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	return resp.StatusCode, b, nil
}

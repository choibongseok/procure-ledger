package llmgw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 서명(표준 라이브러리만, B45 명세 7절). 공개 문서의 알고리즘을 그대로 따른다(운영 전 최신 문서와 실제 계정으로 확인 필요).
// 서명 대상 헤더는 host·x-amz-date(·x-amz-security-token 이 있으면 그것)다.

// awsCreds 는 서명에 쓰는 자격 증명이다.
type awsCreds struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// awsEscape 는 비예약 문자(A-Za-z0-9-_.~)만 남기고 나머지를 %XX 로 바꾼다(슬래시도 바꾼다).
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// awsCanonicalURI 는 이미 한 번 인코딩된 요청 경로를 구간마다 한 번 더 인코딩한다(S3 외 서비스의 규칙).
func awsCanonicalURI(encodedPath string) string {
	if encodedPath == "" {
		return "/"
	}
	segs := strings.Split(encodedPath, "/")
	for i, sg := range segs {
		segs[i] = awsEscape(sg)
	}
	return strings.Join(segs, "/")
}

func awsCanonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// signV4 는 Authorization 헤더 값과 x-amz-date 값을 돌려준다. encodedPath 는 요청에 실제로 쓰는(한 번 인코딩된) 경로다.
func signV4(method, host, encodedPath string, query url.Values, payload []byte, c awsCreds, region, service string, t time.Time) (authorization, amzDate string) {
	t = t.UTC()
	amzDate = t.Format("20060102T150405Z")
	date := t.Format("20060102")
	headers := map[string]string{"host": host, "x-amz-date": amzDate}
	if c.SessionToken != "" {
		headers["x-amz-security-token"] = c.SessionToken
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{method, awsCanonicalURI(encodedPath), awsCanonicalQuery(query), canonHeaders.String(), signed, sha256Hex(payload)}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	return "AWS4-HMAC-SHA256 Credential=" + c.AccessKeyID + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig, amzDate
}

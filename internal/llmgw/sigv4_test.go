package llmgw

import (
	"net/url"
	"testing"
	"time"
)

// AWS 서명 V4 공개 시험 모음의 get-vanilla 벡터(고정 입력 → 고정 서명). 알고리즘이 문서와 어긋나면 이 시험이 먼저 깨진다.
func TestSigV4PublicVectorGetVanilla(t *testing.T) {
	c := awsCreds{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	at := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	auth, date := signV4("GET", "example.amazonaws.com", "/", url.Values{}, nil, c, "us-east-1", "service", at)
	if date != "20150830T123600Z" {
		t.Fatalf("날짜: %s", date)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if auth != want {
		t.Fatalf("서명이 공개 벡터와 다르다:\n got  %s\n want %s", auth, want)
	}
}

// 모델 ID 의 ':' 는 요청 경로에서 %3A 로, 정규 경로에서는 한 번 더 인코딩된다(%253A).
func TestSigV4DoubleEncodesPath(t *testing.T) {
	enc := "/model/" + awsEscape("anthropic.claude-3-haiku-20240307-v1:0") + "/converse"
	if enc != "/model/anthropic.claude-3-haiku-20240307-v1%3A0/converse" {
		t.Fatalf("요청 경로: %s", enc)
	}
	if got := awsCanonicalURI(enc); got != "/model/anthropic.claude-3-haiku-20240307-v1%253A0/converse" {
		t.Fatalf("정규 경로: %s", got)
	}
	// 세션 토큰이 있으면 서명 대상 헤더에 들어간다.
	auth, _ := signV4("POST", "bedrock-runtime.us-east-1.amazonaws.com", enc, nil, []byte("{}"), awsCreds{AccessKeyID: "A", SecretAccessKey: "S", SessionToken: "T"}, "us-east-1", "bedrock", time.Unix(0, 0))
	if !contains(auth, "SignedHeaders=host;x-amz-date;x-amz-security-token") {
		t.Fatalf("서명 헤더: %s", auth)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

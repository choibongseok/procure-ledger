package llmgw_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 3단계: Bedrock·Vertex 등록 — 설정은 봉인되고 응답에는 고정 호스트만 나온다. 형식이 틀린 리전·로케이션·서비스 계정은 거부한다. 업데이트는 비밀 칸을 비우면 유지.
func TestBedrockAndVertexRegistration(t *testing.T) {
	e := newEnv(t)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage)
	const akid, sk = "AKIDREGTEST", "super-secret-key"
	pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "aws-br", Kind: "bedrock", Name: "AWS", DefaultModel: "anthropic.claude-3-haiku-20240307-v1:0", Region: "ap-northeast-2", AccessKeyID: akid, SecretAccessKey: sk})
	if err != nil || pv.Hint != "bedrock-runtime.ap-northeast-2.amazonaws.com" || !pv.HasSecret || pv.SupportsImages {
		t.Fatalf("bedrock 등록: %+v %v", pv, err)
	}
	b, _ := json.Marshal(pv)
	if strings.Contains(string(b), akid) || strings.Contains(string(b), sk) {
		t.Fatalf("응답에 비밀이 있다: %s", b)
	}
	var sealed string
	if err := e.db.QueryRow(`SELECT secret_sealed FROM llm_providers WHERE id = ?`, pv.ID).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, "enc:v1:") || strings.Contains(sealed, akid) {
		t.Fatalf("봉인: %q %v", sealed, err)
	}
	// 비밀 칸을 비우고 이름만 고치면 키는 유지된다.
	up, err := ad.SaveProvider(ctx, llmgw.ProviderInput{ID: pv.ID, Code: "aws-br", Kind: "bedrock", Name: "AWS 서울", Version: pv.Version})
	if err != nil || !up.HasSecret || up.Name != "AWS 서울" {
		t.Fatalf("수정: %+v %v", up, err)
	}
	for _, bad := range []llmgw.ProviderInput{
		{Code: "br-bad1", Kind: "bedrock", Name: "x", Region: "evil.com/x", AccessKeyID: "a", SecretAccessKey: "b"},
		{Code: "br-bad2", Kind: "bedrock", Name: "x", Region: "ap-northeast-2"},
		{Code: "vx-bad1", Kind: "vertex", Name: "x", Project: "proj-abc12", Location: "evil.com", ServiceAccountJSON: "{}"},
		{Code: "vx-bad2", Kind: "vertex", Name: "x", Project: "proj-abc12", Location: "us-central1", ServiceAccountJSON: `{"client_email":"a","private_key":"nope"}`},
	} {
		if _, err := ad.SaveProvider(ctx, bad); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부돼야 한다: %v", bad.Code, err)
		}
	}
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	sa, _ := json.Marshal(map[string]string{"client_email": "svc@pl.test", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	vx, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "gcp-vx", Kind: "vertex", Name: "GCP", DefaultModel: "gemini-x", Project: "proj-abc12", Location: "us-central1", ServiceAccountJSON: string(sa)})
	if err != nil || vx.Hint != "us-central1-aiplatform.googleapis.com" || !vx.HasSecret {
		t.Fatalf("vertex 등록: %+v %v", vx, err)
	}
	if b, _ := json.Marshal(vx); strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "svc@") {
		t.Fatalf("응답에 서비스 계정이 있다: %s", b)
	}
	// 켜기 전 호출기 만들기 검사를 통과한다(키 형식이 맞다).
	if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SetActive(ctx, vx.ID, true); err != nil {
		t.Fatal(err)
	}
	list, _ := ad.List(adminCtx(contract.PermLLMProviderManage))
	has := map[string]bool{}
	for _, k := range list.Kinds {
		has[k] = true
	}
	if !has["bedrock"] || !has["vertex"] {
		t.Fatalf("등록 가능한 종류: %v", list.Kinds)
	}
}

package llmgw_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// 십진·8진·16진·줄임 IP 표기는 시스템 해석기가 IP 로 받아 주므로 호스트 이름이 아니라 IP 로 보고 등록에서 거부한다(웹훅 등록과 같은 수준).
func TestProviderRejectsNumericIPHosts(t *testing.T) {
	e := newEnv(t)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage)
	for _, bad := range []string{"https://2130706433/v1", "https://0177.0.0.1/v1", "https://0x7f.0.0.1/v1", "https://0x7f000001/v1", "https://127.1/v1", "https://167772161/v1"} {
		_, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "num-ip", Kind: "openai_compat", Name: "x", BaseURL: bad, APIKey: "k"})
		if !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부돼야 한다: %v", bad, err)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM llm_providers`); n != 0 {
		t.Fatalf("거부된 저장이 행을 만들었다: %d", n)
	}
	// 숫자 조각이 섞여도 글자가 있는 이름은 일반 호스트 이름이다.
	if _, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "ok-name", Kind: "openai_compat", Name: "x", BaseURL: "https://llm1.example.com/v1", APIKey: "k"}); err != nil {
		t.Fatalf("일반 이름: %v", err)
	}
}

// 등록 때 호스트를 지금 해석해 내부 주소로 가는 이름은 거부한다(웹훅 등록과 같은 netsafe.ResolveCheck).
func TestProviderRegistrationUsesResolveCheck(t *testing.T) {
	e := newEnv(t)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage)
	answer := "10.0.0.5"
	ad.Resolve = func(c context.Context, host string, allowLocal bool) error {
		return netsafe.ResolveCheck(c, host, allowLocal, func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(answer)}, nil
		})
	}
	in := llmgw.ProviderInput{Code: "dns-priv", Kind: "openai_compat", Name: "x", BaseURL: "https://llm.example.com/v1", APIKey: "k"}
	if _, err := ad.SaveProvider(ctx, in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "공급자 주소") {
		t.Fatalf("내부 주소로 해석되는 이름은 거부돼야 한다: %v", err)
	}
	answer = "93.184.216.34"
	if _, err := ad.SaveProvider(ctx, in); err != nil {
		t.Fatalf("공인 주소로 해석되면 등록된다: %v", err)
	}
}

// 질의 문자열은 쓸 수 없고(키가 주소에 실려 화면·감사·로그에 새는 길), 저장된 hint 와 감사에는 호스트만 있고 경로·질의가 없다.
func TestProviderURLQueryRejectedAndHintHostOnly(t *testing.T) {
	e := newEnv(t)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage)
	for _, bad := range []string{"https://llm.example.com/v1?key=secret", "https://llm.example.com/v1?"} {
		if _, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "query-1", Kind: "openai_compat", Name: "x", BaseURL: bad, APIKey: "k"}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "질의 문자열") {
			t.Errorf("%s: 거부돼야 한다: %v", bad, err)
		}
	}
	pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "query-2", Kind: "openai_compat", Name: "x", BaseURL: "https://llm.example.com/team-a/v1", APIKey: "k"})
	if err != nil || pv.Hint != "llm.example.com" {
		t.Fatalf("경로 접두는 허용하고 hint 는 호스트뿐: %+v %v", pv, err)
	}
	b, _ := json.Marshal(pv)
	if strings.Contains(string(b), "team-a") {
		t.Fatalf("응답에 경로가 있다: %s", b)
	}
	for _, rec := range e.rec.Records() {
		blob, _ := json.Marshal(rec)
		if strings.Contains(string(blob), "team-a") || strings.Contains(string(blob), "?key") {
			t.Fatalf("감사에 주소 경로·질의가 있다: %s", blob)
		}
	}
}

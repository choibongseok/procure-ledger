package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 공급자별 골든셋 비교(B45 3단계, 명세 11절). 비교 표는 참고용이고 CI 게이트가 아니다(D12): 치명 케이스 회귀는 경고만 하고 종료 코드를 바꾸지 않는다.

type providerSpec struct {
	name  string // mock | anthropic | bedrock | vertex | openai
	model string
}

// splitProviders 는 "a,b:model,c" 를 나눈다. 모델은 첫 ':' 뒤 전부다(Bedrock 모델 ID 에 ':' 가 있다). 모델이 없으면 --model 이다.
func splitProviders(list, defaultModel string) []providerSpec {
	var out []providerSpec
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, mdl := part, defaultModel
		if i := strings.Index(part, ":"); i >= 0 {
			name, mdl = part[:i], part[i+1:]
		}
		out = append(out, providerSpec{name: name, model: mdl})
	}
	return out
}

func envInt64(key string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	return v
}

// buildEvalProvider 는 공급자를 만든다. skip 이 비어 있지 않으면 그 공급자는 건너뛴다(키 없음·--live 없음). err 는 설정 오류(종료 코드 2)다.
func buildEvalProvider(sp providerSpec, keyEnv string, live bool) (prov llmgw.Provider, model, policy, skip string, err error) {
	model, policy = sp.model, sp.model
	switch sp.name {
	case "mock":
		if model == "" || !strings.HasPrefix(model, "mock") {
			model = "mock-1"
		}
		return llmgw.MockProvider{}, model, "mock*", "", nil
	case "anthropic":
		p, e := llmgw.NewAnthropic(keyEnv, "", 0)
		if errors.Is(e, llmgw.ErrNoAPIKey) {
			return nil, "", "", fmt.Sprintf("%s 환경변수가 비어 있어 anthropic 평가를 실행하지 않습니다(mock 평가는 영향 없음).", keyEnv), nil
		}
		if e != nil {
			return nil, "", "", "", e
		}
		if model == "" {
			return nil, "", "", "", errors.New("모델 ID 가 없습니다. --model 또는 PL_LLM_MODEL 로 지정하세요.")
		}
		return p, model, model, "", nil
	case "bedrock", "vertex", "openai":
		if !live {
			return nil, "", "", fmt.Sprintf("%s 는 실제 외부 호출이라 --live 가 있을 때만 실행합니다.", sp.name), nil
		}
		if model == "" {
			return nil, "", "", "", fmt.Errorf("%s 의 모델 ID 가 없습니다(%s:<모델> 형식으로 지정하세요).", sp.name, sp.name)
		}
		var p llmgw.Provider
		var e error
		missing := ""
		switch sp.name {
		case "bedrock":
			c := llmgw.BedrockConfig{Region: os.Getenv("AWS_REGION"), AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
			if c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
				missing = "AWS_REGION·AWS_ACCESS_KEY_ID·AWS_SECRET_ACCESS_KEY"
				break
			}
			p, e = llmgw.NewBedrock(c, false, 0, "")
		case "vertex":
			path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
			c := llmgw.VertexConfig{Project: os.Getenv("GOOGLE_CLOUD_PROJECT"), Location: os.Getenv("GOOGLE_CLOUD_LOCATION")}
			if path == "" || c.Project == "" || c.Location == "" {
				missing = "GOOGLE_CLOUD_PROJECT·GOOGLE_CLOUD_LOCATION·GOOGLE_APPLICATION_CREDENTIALS"
				break
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil, "", "", "", errors.New("서비스 계정 JSON 파일을 읽을 수 없습니다(GOOGLE_APPLICATION_CREDENTIALS)")
			}
			c.ServiceAccountJSON = string(b)
			p, e = llmgw.NewVertex(c, false, 0, "", "")
		case "openai":
			c := llmgw.OpenAICompatConfig{BaseURL: os.Getenv("OPENAI_COMPAT_BASE_URL"), APIKey: os.Getenv("OPENAI_COMPAT_API_KEY")}
			if c.BaseURL == "" || c.APIKey == "" {
				missing = "OPENAI_COMPAT_BASE_URL·OPENAI_COMPAT_API_KEY"
				break
			}
			p, e = llmgw.NewOpenAICompat(c, false, 0)
		}
		if missing != "" {
			return nil, "", "", fmt.Sprintf("%s 환경변수(%s)가 비어 있어 %s 평가를 실행하지 않습니다.", sp.name, missing, sp.name), nil
		}
		if e != nil {
			return nil, "", "", "", e
		}
		return p, model, model, "", nil
	}
	return nil, "", "", "", fmt.Errorf("알 수 없는 공급자 %q (mock|anthropic|bedrock|vertex|openai)", sp.name)
}

// meteredProvider 는 호출 시간과 토큰을 더한다(비교 표의 지연·비용 합).
type meteredProvider struct {
	llmgw.Provider
	mu      sync.Mutex
	latency time.Duration
	in, out int64
}

func (m *meteredProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	start := time.Now()
	resp, err := m.Provider.Complete(ctx, req)
	m.mu.Lock()
	m.latency += time.Since(start)
	m.in += int64(resp.Usage.InputTokens)
	m.out += int64(resp.Usage.OutputTokens)
	m.mu.Unlock()
	return resp, err
}

func (m *meteredProvider) costMicro(priceIn, priceOut int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return (m.in*priceIn + m.out*priceOut) / 1_000_000
}

func suiteHashOf(rep Report) string {
	cases := make([]llmgw.EvalCase, 0, len(rep.Results))
	for _, c := range rep.Results {
		cases = append(cases, llmgw.EvalCase{Suite: c.Suite, ID: c.ID, Critical: c.Critical})
	}
	return llmgw.SuiteHash(cases)
}

type runResult struct {
	name, model string
	rep         Report
	latencyMS   int64
	costMicro   int64
}

func (r runResult) label() string {
	if r.model == "" {
		return r.name
	}
	return r.name + "(" + r.model + ")"
}

// printCompare 는 케이스 × 공급자 표를 찍는다. 첫 공급자가 기준이고, 기준에서 통과하던 케이스가 다른 곳에서 실패하면 "실패(회귀)" 로 표시한다.
func printCompare(w io.Writer, rs []runResult) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "=== 공급자별 골든셋 비교(참고용, 기준: 첫 공급자) ===")
	h0 := suiteHashOf(rs[0].rep)
	for _, r := range rs[1:] {
		if suiteHashOf(r.rep) != h0 {
			fmt.Fprintln(w, "골든셋이 달라 비교할 수 없습니다.")
			return
		}
	}
	state := make([]map[string]bool, len(rs))
	crit := map[string]bool{}
	var order []string
	have := map[string]bool{}
	for i, r := range rs {
		state[i] = map[string]bool{}
		for _, c := range r.rep.Results {
			k := c.Suite + "/" + c.ID
			state[i][k] = c.Pass
			if c.Critical {
				crit[k] = true
			}
			if !have[k] {
				have[k] = true
				order = append(order, k)
			}
		}
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprint(tw, "케이스")
	for _, r := range rs {
		fmt.Fprint(tw, "\t", r.label())
	}
	fmt.Fprintln(tw)
	critRegress := 0
	for _, k := range order {
		name := k
		if crit[k] {
			name += " *"
		}
		fmt.Fprint(tw, name)
		for i := range rs {
			pass, has := state[i][k]
			cell := "-"
			switch {
			case !has:
			case pass:
				cell = "통과"
			case i > 0 && state[0][k]:
				cell = "실패(회귀)"
				if crit[k] {
					critRegress++
				}
			default:
				cell = "실패"
			}
			fmt.Fprint(tw, "\t", cell)
		}
		fmt.Fprintln(tw)
	}
	fmt.Fprint(tw, "통과율")
	for _, r := range rs {
		_, passed, rate, _ := r.rep.summary()
		fmt.Fprintf(tw, "\t%d%% (%d건)", rate, passed)
	}
	fmt.Fprintln(tw)
	fmt.Fprint(tw, "치명 케이스 실패")
	for _, r := range rs {
		n := 0
		for _, c := range r.rep.Results {
			if c.Critical && !c.Pass {
				n++
			}
		}
		fmt.Fprintf(tw, "\t%d", n)
	}
	fmt.Fprintln(tw)
	fmt.Fprint(tw, "지연 합(ms)")
	for _, r := range rs {
		fmt.Fprintf(tw, "\t%d", r.latencyMS)
	}
	fmt.Fprintln(tw)
	fmt.Fprint(tw, "비용 합(1e-6 USD)")
	for _, r := range rs {
		fmt.Fprintf(tw, "\t%d", r.costMicro)
	}
	fmt.Fprintln(tw)
	_ = tw.Flush()
	fmt.Fprintln(w, "(* 는 치명 케이스)")
	if critRegress > 0 {
		fmt.Fprintf(w, "경고: 기준 공급자에서 통과하던 치명 케이스가 다른 공급자에서 %d건 실패했습니다. 라우팅을 바꾸기 전에 확인하세요(종료 코드에는 영향이 없습니다).\n", critRegress)
	}
}

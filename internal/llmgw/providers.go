package llmgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SealPurpose 는 공급자 설정의 봉인 용도다(keyrotate.AccountColumns 의 용도와 같아야 한다).
const SealPurpose = "llm-provider"

// Sealer 는 공급자 설정 봉인기다(acctsecret.Cipher 가 구현한다).
type Sealer interface {
	SealFor(purpose, plain string) (string, error)
	OpenFor(purpose, stored string) (string, error)
}

// 공급자 종류.
const (
	KindMock         = "mock"
	KindAnthropic    = "anthropic"
	KindBedrock      = "bedrock"
	KindVertex       = "vertex"
	KindOpenAICompat = "openai_compat"
)

// ProviderRow 는 llm_providers 한 줄이다. Secret 은 봉인된 값 그대로이고 응답에 싣지 않는다.
type ProviderRow struct {
	ID             int64
	Code, Kind     string
	Name           string
	DefaultModel   string
	Secret         string
	Hint           string
	Mode           string
	External       bool
	SupportsImages bool
	PriceIn        int64
	PriceOut       int64
	TimeoutMS      int
	Active         bool
	Version        int64
	UpdatedAt      string
}

// secretBlob 은 봉인 JSON 의 내용이다.
type secretBlob struct {
	BaseURL      string            `json:"base_url,omitempty"`
	APIKey       string            `json:"api_key,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	// Bedrock
	Region          string `json:"region,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	// Vertex
	Project            string `json:"project,omitempty"`
	Location           string `json:"location,omitempty"`
	ServiceAccountJSON string `json:"service_account_json,omitempty"`
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const providerCols = `id, code, kind, name, default_model, secret_sealed, hint, mode, external, supports_images, price_in_micro_per_mtok, price_out_micro_per_mtok, timeout_ms, active, version, updated_at`

type scanner interface{ Scan(...any) error }

func scanProvider(sc scanner) (ProviderRow, error) {
	var p ProviderRow
	var ext, img, act int
	err := sc.Scan(&p.ID, &p.Code, &p.Kind, &p.Name, &p.DefaultModel, &p.Secret, &p.Hint, &p.Mode, &ext, &img, &p.PriceIn, &p.PriceOut, &p.TimeoutMS, &act, &p.Version, &p.UpdatedAt)
	p.External, p.SupportsImages, p.Active = ext == 1, img == 1, act == 1
	return p, err
}

// LoadProvider 는 번호로 공급자 한 건을 읽는다. 없으면 sql.ErrNoRows.
func LoadProvider(ctx context.Context, q rowQuerier, id int64) (ProviderRow, error) {
	return scanProvider(q.QueryRowContext(ctx, `SELECT `+providerCols+` FROM llm_providers WHERE id = ?`, id))
}

// LoadProviders 는 공급자 전체를 읽는다.
func LoadProviders(ctx context.Context, q rowQuerier) ([]ProviderRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+providerCols+` FROM llm_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRow
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BuildOptions 는 공급자를 만들 때 필요한 환경이다.
type BuildOptions struct {
	Sealer     Sealer
	AllowLocal bool
	Rehearsal  bool // 리허설이면 mock 외 공급자는 만들지 않는다(운영 공급자로 나가지 않게)
}

// ErrRehearsal 은 리허설 모드에서 외부 공급자를 부르려 할 때의 오류다.
var ErrRehearsal = errors.New("복구 리허설 중에는 외부 LLM 공급자를 쓸 수 없습니다")

// OpenSecret 은 봉인된 설정을 푼다. 값이 비어 있으면 빈 설정이다.
func OpenSecret(s Sealer, sealed string) (secretBlob, error) {
	var b secretBlob
	if sealed == "" {
		return b, nil
	}
	if s == nil {
		return b, errors.New("공급자 설정을 풀 키가 없습니다")
	}
	plain, err := s.OpenFor(SealPurpose, sealed)
	if err != nil {
		return b, errors.New("공급자 설정을 풀 수 없습니다")
	}
	if err := json.Unmarshal([]byte(plain), &b); err != nil {
		return b, errors.New("공급자 설정 형식이 올바르지 않습니다")
	}
	return b, nil
}

// Build 는 공급자 행에서 호출기를 만든다. 오류 문장에 값이 없다.
func Build(row ProviderRow, o BuildOptions) (Provider, error) {
	timeout := time.Duration(row.TimeoutMS) * time.Millisecond
	if row.Kind != KindMock && o.Rehearsal {
		return nil, ErrRehearsal
	}
	switch row.Kind {
	case KindMock:
		return ModeMockProvider{Code: row.Code, Mode: row.Mode}, nil
	case KindOpenAICompat:
		b, err := OpenSecret(o.Sealer, row.Secret)
		if err != nil {
			return nil, err
		}
		p, err := NewOpenAICompat(OpenAICompatConfig{BaseURL: b.BaseURL, APIKey: b.APIKey, ExtraHeaders: b.ExtraHeaders}, o.AllowLocal, timeout)
		if err != nil {
			return nil, err
		}
		if row.Hint != "" && !strings.EqualFold(hostOf(b.BaseURL), row.Hint) {
			return nil, errors.New("공급자 호스트가 고정된 값과 다릅니다. 공급자 설정을 다시 저장하세요")
		}
		return p, nil
	case KindAnthropic:
		b, err := OpenSecret(o.Sealer, row.Secret)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(b.APIKey) == "" {
			return nil, errors.New("API 키가 비어 있습니다")
		}
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		return &AnthropicProvider{apiKey: b.APIKey, baseURL: anthropicDefaultBaseURL, hc: &http.Client{Timeout: timeout}}, nil
	case KindBedrock:
		b, err := OpenSecret(o.Sealer, row.Secret)
		if err != nil {
			return nil, err
		}
		return NewBedrock(BedrockConfig{Region: b.Region, AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey, SessionToken: b.SessionToken}, o.AllowLocal, timeout, "")
	case KindVertex:
		b, err := OpenSecret(o.Sealer, row.Secret)
		if err != nil {
			return nil, err
		}
		return NewVertex(VertexConfig{Project: b.Project, Location: b.Location, ServiceAccountJSON: b.ServiceAccountJSON}, o.AllowLocal, timeout, "", "")
	}
	return nil, fmt.Errorf("이 서버에서 아직 지원하지 않는 공급자 종류입니다(%s)", row.Kind)
}

func hostOf(raw string) string {
	u, err := validateProviderURL(raw, true)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

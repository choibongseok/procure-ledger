package llmgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// ProviderAdmin 은 LLM 공급자·라우팅 규칙 관리 서비스다(B45 1단계).(비밀·감사·권한·화면), 게이트웨이 연결은 ai 몫이지만 1단계는 platform 이 함께 맡았다.
// 공급자 쓰기는 llm:provider_manage(위험 권한·세션 전용), 규칙은 llm:policy_manage 다. 쓰기와 감사 기록은 같은 트랜잭션이다.
type ProviderAdmin struct {
	DB         *sql.DB
	Txs        contract.TxManager
	Audit      audit.Recorder
	Sealer     Sealer
	AllowLocal bool
	Rehearsal  bool
	Now        func() time.Time
	// Breakers 는 게이트웨이와 공유하는 회로 상태다(없으면 상태는 항상 정상으로 보인다).
	Breakers *BreakerSet
	// Resolve 는 등록 때 호스트 해석 검사다(웹훅 등록과 같은 netsafe.ResolveCheck). 비면 그것을 쓴다. 시험이 바꾼다.
	Resolve func(ctx context.Context, host string, allowLocal bool) error
	// Gate 는 라우팅 저장 전 평가(B46 3단계)다. nil 이면 평가 없이 저장한다.
	Gate RouteGate
}

func (a *ProviderAdmin) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func valErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, args...))
}

func stateErr(msg string) error { return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg) }

var (
	codeRe    = regexp.MustCompile(`^[a-z0-9-]{2,30}$`)
	purposeRe = regexp.MustCompile(`^(\*|[a-z0-9_.-]{1,60}(\.\*)?)$`)
	roleKeyRe = regexp.MustCompile(`^(admin|finance|manager|staff|auditor|r_[0-9]+)$`)
)

// ProviderView 는 화면에 나가는 공급자 한 건이다(비밀 없음).
type ProviderView struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	DefaultModel   string `json:"defaultModel"`
	Hint           string `json:"hint"`
	Mode           string `json:"mode"`
	External       bool   `json:"external"`
	SupportsImages bool   `json:"supportsImages"`
	PriceIn        int64  `json:"priceInMicroPerMtok"`
	PriceOut       int64  `json:"priceOutMicroPerMtok"`
	TimeoutMS      int    `json:"timeoutMs"`
	Active         bool   `json:"active"`
	HasSecret      bool   `json:"hasSecret"`
	// 상태 점(B45 명세 10절): ok | circuit_open | check_settings | off. 아래 24시간 값은 사용량 기록에서 센다.
	Health       string `json:"health"`
	Calls24h     int64  `json:"calls24h"`
	Fallbacks24h int64  `json:"fallbacks24h"`
	AvgLatencyMS int64  `json:"avgLatencyMs"`
	Version      int64  `json:"version"`
	UpdatedAt    string `json:"updatedAt"`
}

func viewOf(r ProviderRow) ProviderView {
	return ProviderView{ID: r.ID, Code: r.Code, Kind: r.Kind, Name: r.Name, DefaultModel: r.DefaultModel, Hint: r.Hint, Mode: r.Mode, External: r.External, SupportsImages: r.SupportsImages,
		PriceIn: r.PriceIn, PriceOut: r.PriceOut, TimeoutMS: r.TimeoutMS, Active: r.Active, HasSecret: r.Secret != "", Version: r.Version, UpdatedAt: r.UpdatedAt}
}

// ProviderList 는 목록과 화면이 쓰는 부가 정보다.
type ProviderList struct {
	Providers  []ProviderView `json:"providers"`
	Kinds      []string       `json:"kinds"`      // 등록할 수 있는 종류(이 서버가 지원하는 것)
	SealerOK   bool           `json:"sealerOk"`   // 설정을 봉인할 키가 있는가(없으면 mock 외 등록 불가)
	Rehearsal  bool           `json:"rehearsal"`  // 리허설이면 외부 공급자를 쓸 수 없다
	MockModes  []string       `json:"mockModes"`  // 모의 동작 모드 예시
	DefaultKey string         `json:"defaultKey"` // 환경변수 기본 경로의 이름(가상 공급자)
}

// List 는 공급자 목록이다(llm:provider_manage).
func (a *ProviderAdmin) List(ctx context.Context) (ProviderList, error) {
	if _, err := contract.Authorize(ctx, contract.PermLLMProviderManage); err != nil {
		return ProviderList{}, err
	}
	rows, err := LoadProviders(ctx, a.DB)
	if err != nil {
		return ProviderList{}, err
	}
	out := ProviderList{Providers: []ProviderView{}, Kinds: []string{KindMock, KindOpenAICompat, KindAnthropic, KindBedrock, KindVertex}, SealerOK: a.Sealer != nil, Rehearsal: a.Rehearsal,
		MockModes: []string{"ok", "fail_5xx", "fail_429", "fail_auth", "fail_bad_request", "slow:5000", "flaky:2"}, DefaultKey: "default"}
	since := a.now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	for _, r := range rows {
		v := viewOf(r)
		v.Health = a.health(r)
		// 최종 응답한 공급자 기준 호출 수, 이 공급자에서 대체로 넘어간 수(fallback_from), 평균 지연.
		_ = a.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(AVG(latency_ms), 0) FROM llm_usage WHERE provider = ? AND at >= ? AND status = 'ok'`, r.Code, since).Scan(&v.Calls24h, &v.AvgLatencyMS)
		_ = a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_usage WHERE fallback_from = ? AND at >= ?`, r.Code, since).Scan(&v.Fallbacks24h)
		out.Providers = append(out.Providers, v)
	}
	return out, nil
}

func (a *ProviderAdmin) health(r ProviderRow) string {
	if !r.Active {
		return "off"
	}
	if a.Breakers != nil {
		state, need := a.Breakers.State(r.Code)
		if state != CircuitClosed {
			return "circuit_open"
		}
		if need {
			return "check_settings"
		}
	}
	return "ok"
}

// ProviderInput 은 등록·수정 입력이다. ID 가 0 이면 등록이다. 비밀 칸이 비면 "그대로 둠"이다.
type ProviderInput struct {
	ID           int64  `json:"id"`
	Code         string `json:"code"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	DefaultModel string `json:"defaultModel"`
	BaseURL      string `json:"baseUrl"`
	APIKey       string `json:"apiKey"`
	Mode         string `json:"mode"`
	PriceIn      int64  `json:"priceInMicroPerMtok"`
	PriceOut     int64  `json:"priceOutMicroPerMtok"`
	TimeoutMS    int    `json:"timeoutMs"`
	Version      int64  `json:"version"`
	// 종류별 설정(비밀 칸은 비우면 그대로 둔다).
	Region             string `json:"region"`
	AccessKeyID        string `json:"accessKeyId"`
	SecretAccessKey    string `json:"secretAccessKey"`
	SessionToken       string `json:"sessionToken"`
	Project            string `json:"project"`
	Location           string `json:"location"`
	ServiceAccountJSON string `json:"serviceAccountJson"`
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SaveProvider 는 공급자를 등록·수정한다(llm:provider_manage). 감사에는 코드·종류·주소 변경·자격 증명 변경 여부뿐이고 값은 없다.
func (a *ProviderAdmin) SaveProvider(ctx context.Context, in ProviderInput) (ProviderView, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMProviderManage)
	if err != nil {
		return ProviderView{}, err
	}
	in.Code, in.Kind, in.Name = strings.ToLower(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Kind), strings.TrimSpace(in.Name)
	in.DefaultModel, in.Mode, in.BaseURL = strings.TrimSpace(in.DefaultModel), strings.TrimSpace(in.Mode), strings.TrimSpace(in.BaseURL)
	if !codeRe.MatchString(in.Code) {
		return ProviderView{}, valErr("공급자 코드는 영문 소문자·숫자·-로 2~30자여야 합니다.")
	}
	if n := len([]rune(in.Name)); n < 1 || n > 40 {
		return ProviderView{}, valErr("이름은 1~40자여야 합니다.")
	}
	switch in.Kind {
	case KindMock, KindOpenAICompat, KindAnthropic, KindBedrock, KindVertex:
	default:
		return ProviderView{}, valErr("공급자 종류를 고르세요.")
	}
	if len([]rune(in.DefaultModel)) > 120 {
		return ProviderView{}, valErr("기본 모델 ID 가 너무 깁니다.")
	}
	if in.PriceIn < 0 || in.PriceOut < 0 {
		return ProviderView{}, valErr("가격은 0 이상이어야 합니다.")
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = 30000
	}
	if in.TimeoutMS < 1000 || in.TimeoutMS > 120000 {
		return ProviderView{}, valErr("시간 제한은 1000~120000ms 여야 합니다.")
	}
	if in.Kind == KindMock {
		if !ValidMockMode(in.Mode) {
			return ProviderView{}, valErr("모의 동작 모드가 올바르지 않습니다(ok·fail_5xx·fail_429·fail_auth·fail_bad_request·slow:<ms>·flaky:<n>).")
		}
	} else {
		in.Mode = ""
		if a.Rehearsal {
			return ProviderView{}, stateErr(ErrRehearsal.Error())
		}
		if a.Sealer == nil {
			return ProviderView{}, stateErr("공급자 정보를 안전하게 저장할 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없어 등록할 수 없습니다.")
		}
	}
	var prev ProviderRow
	had := false
	if in.ID > 0 {
		prev, err = LoadProvider(ctx, a.DB, in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ProviderView{}, contract.ErrNotFound
		}
		if err != nil {
			return ProviderView{}, err
		}
		if prev.Code != in.Code || prev.Kind != in.Kind {
			return ProviderView{}, valErr("코드와 종류는 바꿀 수 없습니다.")
		}
		had = true
	}
	// 비밀: 이전 값을 풀어 두고(풀 수 없으면 새로 입력한 값만으로) 입력된 칸만 덮어쓴다.
	var blob secretBlob
	if had && prev.Secret != "" {
		if old, oerr := OpenSecret(a.Sealer, prev.Secret); oerr == nil {
			blob = old
		}
	}
	urlChanged, credChanged := false, false
	if in.Kind != KindMock {
		if in.Kind == KindOpenAICompat && in.BaseURL != "" {
			u, uerr := validateProviderURL(in.BaseURL, a.AllowLocal)
			if uerr != nil {
				return ProviderView{}, valErr("%s", uerr.Error())
			}
			if rerr := a.resolveHost(ctx, u.Hostname()); rerr != nil {
				return ProviderView{}, valErr("%s", strings.ReplaceAll(rerr.Error(), "웹훅 주소", "공급자 주소"))
			}
			urlChanged = in.BaseURL != blob.BaseURL
			blob.BaseURL = in.BaseURL
		}
		if k := strings.TrimSpace(in.APIKey); k != "" {
			if len(k) > 4096 {
				return ProviderView{}, valErr("API 키가 너무 깁니다.")
			}
			blob.APIKey, credChanged = k, true
		}
		if in.Kind == KindOpenAICompat && blob.BaseURL == "" {
			return ProviderView{}, valErr("주소(https)를 입력하세요.")
		}
		switch in.Kind {
		case KindBedrock:
			if v := strings.TrimSpace(in.Region); v != "" {
				urlChanged = urlChanged || v != blob.Region
				blob.Region = v
			}
			if v := strings.TrimSpace(in.AccessKeyID); v != "" {
				blob.AccessKeyID, credChanged = v, true
			}
			if v := strings.TrimSpace(in.SecretAccessKey); v != "" {
				blob.SecretAccessKey, credChanged = v, true
			}
			if v := strings.TrimSpace(in.SessionToken); v != "" {
				blob.SessionToken, credChanged = v, true
			}
			if _, berr := NewBedrock(BedrockConfig{Region: blob.Region, AccessKeyID: blob.AccessKeyID, SecretAccessKey: blob.SecretAccessKey, SessionToken: blob.SessionToken}, a.AllowLocal, 0, ""); berr != nil {
				return ProviderView{}, valErr("%s", berr.Error())
			}
		case KindVertex:
			if v := strings.TrimSpace(in.Project); v != "" {
				blob.Project = v
			}
			if v := strings.TrimSpace(in.Location); v != "" {
				urlChanged = urlChanged || v != blob.Location
				blob.Location = v
			}
			if v := strings.TrimSpace(in.ServiceAccountJSON); v != "" {
				if len(v) > 16384 {
					return ProviderView{}, valErr("서비스 계정 JSON 이 너무 깁니다.")
				}
				blob.ServiceAccountJSON, credChanged = v, true
			}
			if _, verr := NewVertex(VertexConfig{Project: blob.Project, Location: blob.Location, ServiceAccountJSON: blob.ServiceAccountJSON}, a.AllowLocal, 0, "", ""); verr != nil {
				return ProviderView{}, valErr("%s", verr.Error())
			}
		default:
			if blob.APIKey == "" {
				return ProviderView{}, valErr("API 키를 입력하세요.")
			}
		}
	}
	hint := ""
	sealed := prev.Secret
	if in.Kind != KindMock {
		switch in.Kind {
		case KindBedrock:
			hint = BedrockHost(blob.Region)
		case KindVertex:
			hint = VertexHost(blob.Location)
		default:
			hint = hostOf(blob.BaseURL)
		}
		plain, _ := json.Marshal(blob)
		if sealed, err = a.Sealer.SealFor(SealPurpose, string(plain)); err != nil {
			return ProviderView{}, stateErr("공급자 정보를 봉인하지 못했습니다.")
		}
	}
	external := in.Kind != KindMock
	images := in.Kind == KindMock || in.Kind == KindAnthropic
	now := a.now().UTC().Format(time.RFC3339)
	var id int64
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if had {
			res, e := tx.ExecContext(ctx, `UPDATE llm_providers SET name = ?, default_model = ?, secret_sealed = ?, hint = ?, mode = ?, external = ?, supports_images = ?,
				price_in_micro_per_mtok = ?, price_out_micro_per_mtok = ?, timeout_ms = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ? AND version = ?`,
				in.Name, in.DefaultModel, sealed, hint, in.Mode, boolInt(external), boolInt(images), in.PriceIn, in.PriceOut, in.TimeoutMS, nullID(p.UserID), now, in.ID, in.Version)
			if e != nil {
				return e
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return stateErr("다른 사람이 먼저 고쳤습니다. 새로 불러온 뒤 다시 저장하세요.")
			}
			id = in.ID
		} else {
			var e error
			id, e = dbutil.InsertID(ctx, tx, `INSERT INTO llm_providers (code, kind, name, default_model, secret_sealed, hint, mode, external, supports_images, price_in_micro_per_mtok, price_out_micro_per_mtok,
				timeout_ms, active, version, created_by, created_at, updated_by, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0,1,?,?,?,?)`,
				in.Code, in.Kind, in.Name, in.DefaultModel, sealed, hint, in.Mode, boolInt(external), boolInt(images), in.PriceIn, in.PriceOut, in.TimeoutMS, nullID(p.UserID), now, nullID(p.UserID), now)
			if e != nil {
				if dbutil.IsUnique(e) {
					return valErr("이미 사용 중인 공급자 코드입니다.")
				}
				return e
			}
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "llm.provider_save", TargetType: audit.TargetLLMProvider, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "provider_code", New: in.Code}, {Key: "provider_kind", New: in.Kind}, {Key: "url_changed", New: yesNo(urlChanged || (!had && in.Kind == KindOpenAICompat))},
				{Key: "credential_changed", New: yesNo(credChanged || (!had && in.Kind != KindMock))}}})
	})
	if err != nil {
		return ProviderView{}, err
	}
	row, err := LoadProvider(ctx, a.DB, id)
	if err != nil {
		return ProviderView{}, err
	}
	return viewOf(row), nil
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// SetActive 는 공급자를 켜거나 끈다. 켜기 전에 설정을 풀어 보고 호출기를 만들어 본다(주소 규칙·키 유무).
func (a *ProviderAdmin) SetActive(ctx context.Context, id int64, on bool) (ProviderView, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMProviderManage)
	if err != nil {
		return ProviderView{}, err
	}
	row, err := LoadProvider(ctx, a.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderView{}, contract.ErrNotFound
	}
	if err != nil {
		return ProviderView{}, err
	}
	if on {
		if _, berr := Build(row, BuildOptions{Sealer: a.Sealer, AllowLocal: a.AllowLocal, Rehearsal: a.Rehearsal}); berr != nil {
			return ProviderView{}, stateErr(berr.Error())
		}
	} else if used, uerr := a.activeRouteRefs(ctx, id); uerr != nil {
		return ProviderView{}, uerr
	} else if used > 0 {
		// 꺼도 되지만 규칙이 있다는 것은 알린다(꺼진 주 공급자의 규칙은 후보에서 빠진다). 막지는 않는다.
		_ = used
	}
	action := "llm.provider_deactivate"
	if on {
		action = "llm.provider_activate"
	}
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE llm_providers SET active = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`, boolInt(on), nullID(p.UserID), a.now().UTC().Format(time.RFC3339), id); e != nil {
			return e
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetLLMProvider, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "provider_code", New: row.Code}}})
	})
	if err != nil {
		return ProviderView{}, err
	}
	row, err = LoadProvider(ctx, a.DB, id)
	if err != nil {
		return ProviderView{}, err
	}
	return viewOf(row), nil
}

func (a *ProviderAdmin) activeRouteRefs(ctx context.Context, providerID int64) (int, error) {
	var n int
	err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_routes WHERE active = 1 AND (primary_provider_id = ? OR fallback_provider_id = ?)`, providerID, providerID).Scan(&n)
	return n, err
}

// TestResult 는 시험 연결 결과다(값 없음).
type TestResult struct {
	OK        bool   `json:"ok"`
	Code      string `json:"code"` // ok | timeout | rate_limited | http_5xx | network | auth | bad_request | unknown | setup
	LatencyMS int64  `json:"latencyMs"`
}

// Test 는 공급자를 한 번 불러 본다(llm:provider_manage). 한도·마스킹을 거치지 않는 연결 확인이고 비용이 들 수 있다. 사용량에는 남기지 않고 감사에 결과 코드만 남긴다.
func (a *ProviderAdmin) Test(ctx context.Context, id int64) (TestResult, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMProviderManage)
	if err != nil {
		return TestResult{}, err
	}
	row, err := LoadProvider(ctx, a.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return TestResult{}, contract.ErrNotFound
	}
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{Code: "setup"}
	if prov, berr := Build(row, BuildOptions{Sealer: a.Sealer, AllowLocal: a.AllowLocal, Rehearsal: a.Rehearsal}); berr != nil {
		if errors.Is(berr, ErrRehearsal) {
			return TestResult{}, stateErr(berr.Error())
		}
	} else {
		cctx, cancel := context.WithTimeout(ctx, time.Duration(row.TimeoutMS)*time.Millisecond)
		start := time.Now()
		_, cerr := prov.Complete(cctx, llm.Request{Model: row.DefaultModel, Messages: []llm.Message{{Role: "user", Content: "ping"}}, MaxTokens: 8})
		cancel()
		res.LatencyMS = time.Since(start).Milliseconds()
		if cerr == nil {
			res.OK, res.Code = true, "ok"
		} else {
			res.Code = string(ClassOf(cerr))
		}
	}
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "llm.provider_test", TargetType: audit.TargetLLMProvider, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "provider_code", New: row.Code}, {Key: "result_code", New: res.Code}}})
	})
	return res, err
}

// ---- 라우팅 규칙(llm:policy_manage) ----

// RouteView 는 화면에 나가는 규칙 한 건이다(공급자 코드·부서 이름을 함께 준다).
type RouteView struct {
	RouteRule
	PrimaryCode  string `json:"primaryCode"`
	FallbackCode string `json:"fallbackCode"`
	// Gate 는 저장 전 평가(B46) 결과다. 평가가 돌았거나 건너뛴 저장에만 있다.
	Gate *evalset.GateResult `json:"gate,omitempty"`
}

// RouteGate 는 라우팅 저장 전 평가 게이트다(B46 3단계). 구현은 evalset.Service.
type RouteGate interface {
	CheckGate(ctx context.Context, actor contract.Principal, purpose string, c evalset.Candidate, confirmCost bool) evalset.GateResult
}

// GateError 는 저장 전 평가가 확인을 요구해 저장하지 않았음을 알린다(409 + 게이트 결과). 비용 확인 요청과 기준선 미달 두 경우다.
type GateError struct{ Result evalset.GateResult }

func (e *GateError) Error() string { return e.Result.Message }

// RouteList 는 규칙 목록과 선택지다.
// DeptChoice 는 부서 선택지다.
type DeptChoice struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type RouteList struct {
	Routes      []RouteView    `json:"routes"`
	Providers   []ProviderView `json:"providers"`
	Departments []DeptChoice   `json:"departments"`
}

// ListRoutes 는 규칙 목록이다(llm:policy_manage). 공급자 목록은 코드·이름만 필요해 같이 준다(비밀 없음).
func (a *ProviderAdmin) ListRoutes(ctx context.Context) (RouteList, error) {
	if _, err := contract.Authorize(ctx, contract.PermLLMPolicyManage); err != nil {
		return RouteList{}, err
	}
	rules, err := LoadRoutes(ctx, a.DB, false)
	if err != nil {
		return RouteList{}, err
	}
	provs, err := LoadProviders(ctx, a.DB)
	if err != nil {
		return RouteList{}, err
	}
	code := map[int64]string{}
	out := RouteList{Routes: []RouteView{}, Providers: []ProviderView{}, Departments: []DeptChoice{}}
	if drows, derr := a.DB.QueryContext(ctx, `SELECT id, name FROM departments ORDER BY name, id`); derr == nil {
		for drows.Next() {
			var d DeptChoice
			if drows.Scan(&d.ID, &d.Name) == nil {
				out.Departments = append(out.Departments, d)
			}
		}
		drows.Close()
	}
	for _, pr := range provs {
		code[pr.ID] = pr.Code
		out.Providers = append(out.Providers, viewOf(pr))
	}
	for _, r := range rules {
		out.Routes = append(out.Routes, RouteView{RouteRule: r, PrimaryCode: code[r.PrimaryProviderID], FallbackCode: code[r.FallbackProviderID]})
	}
	return out, nil
}

// RouteInput 은 규칙 등록·수정 입력이다(ID 0 = 등록).
type RouteInput struct {
	ID                 int64  `json:"id"`
	SortOrder          int    `json:"sortOrder"`
	PurposePattern     string `json:"purposePattern"`
	DepartmentID       int64  `json:"departmentId"`
	Role               string `json:"role"`
	PrimaryProviderID  int64  `json:"primaryProviderId"`
	PrimaryModel       string `json:"primaryModel"`
	FallbackProviderID int64  `json:"fallbackProviderId"`
	FallbackModel      string `json:"fallbackModel"`
	MaxAttempts        int    `json:"maxAttempts"`
	TotalTimeoutMS     int    `json:"totalTimeoutMs"`
	Active             bool   `json:"active"`
	// 저장 전 평가(B46): 기준선 미달을 알고도 저장하려면 GateOverride 와 사유를 보낸다. 외부 후보의 평가 비용을 확인했으면 GateConfirmCost.
	GateOverride    bool   `json:"gateOverride"`
	GateReason      string `json:"gateReason"`
	GateConfirmCost bool   `json:"gateConfirmCost"`
}

// SaveRoute 는 규칙을 등록·수정한다(llm:policy_manage).
func (a *ProviderAdmin) SaveRoute(ctx context.Context, in RouteInput) (RouteView, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMPolicyManage)
	if err != nil {
		return RouteView{}, err
	}
	in.PurposePattern, in.Role = strings.TrimSpace(in.PurposePattern), strings.TrimSpace(in.Role)
	in.PrimaryModel, in.FallbackModel = strings.TrimSpace(in.PrimaryModel), strings.TrimSpace(in.FallbackModel)
	if !purposeRe.MatchString(in.PurposePattern) {
		return RouteView{}, valErr("용도 패턴은 정확한 라벨(예: docverify.extract), 접두(예: docverify.*) 또는 *(전체)여야 합니다.")
	}
	if in.Role != "" && !roleKeyRe.MatchString(in.Role) {
		return RouteView{}, valErr("역할을 고르세요.")
	}
	if in.DepartmentID < 0 {
		return RouteView{}, valErr("부서를 고르세요.")
	}
	if in.DepartmentID > 0 {
		var n int
		if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM departments WHERE id = ?`, in.DepartmentID).Scan(&n); err != nil || n == 0 {
			return RouteView{}, valErr("부서를 찾을 수 없습니다.")
		}
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 2
	}
	if in.TotalTimeoutMS == 0 {
		in.TotalTimeoutMS = 60000
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 3 || in.TotalTimeoutMS < 1000 || in.TotalTimeoutMS > 300000 || len([]rune(in.PrimaryModel)) > 120 || len([]rune(in.FallbackModel)) > 120 {
		return RouteView{}, valErr("시도 횟수(1~3)·전체 시간 제한(1000~300000ms)·모델 ID 길이를 확인하세요.")
	}
	prim, err := LoadProvider(ctx, a.DB, in.PrimaryProviderID)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteView{}, valErr("주 공급자를 고르세요.")
	}
	if err != nil {
		return RouteView{}, err
	}
	fbCode := ""
	if in.FallbackProviderID != 0 {
		if in.FallbackProviderID == in.PrimaryProviderID {
			return RouteView{}, valErr("대체는 다른 공급자여야 합니다.")
		}
		fb, err := LoadProvider(ctx, a.DB, in.FallbackProviderID)
		if errors.Is(err, sql.ErrNoRows) {
			return RouteView{}, valErr("대체 공급자를 고르세요.")
		}
		if err != nil {
			return RouteView{}, err
		}
		fbCode = fb.Code
	}
	var gate *evalset.GateResult
	if a.Gate != nil && in.Active && a.gateApplies(ctx, in) {
		model := in.PrimaryModel
		if model == "" {
			model = prim.DefaultModel
		}
		res := a.Gate.CheckGate(ctx, p, in.PurposePattern, evalset.Candidate{Provider: prim.Code, Model: model}, in.GateConfirmCost)
		if res.Status == evalset.GateConfirmCost || (res.Status == evalset.GateBlocked && !in.GateOverride) {
			return RouteView{}, &GateError{Result: res}
		}
		if res.Message != "" {
			gate = &res
		}
	}
	now := a.now().UTC().Format(time.RFC3339)
	var fallback any
	if in.FallbackProviderID != 0 {
		fallback = in.FallbackProviderID
	}
	var id int64
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if in.ID > 0 {
			res, e := tx.ExecContext(ctx, `UPDATE llm_routes SET sort_order = ?, purpose_pattern = ?, department_id = ?, role = ?, primary_provider_id = ?, primary_model = ?, fallback_provider_id = ?, fallback_model = ?,
				max_attempts = ?, total_timeout_ms = ?, active = ?, updated_at = ? WHERE id = ?`,
				in.SortOrder, in.PurposePattern, in.DepartmentID, in.Role, in.PrimaryProviderID, in.PrimaryModel, fallback, in.FallbackModel, in.MaxAttempts, in.TotalTimeoutMS, boolInt(in.Active), now, in.ID)
			if e != nil {
				return e
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return contract.ErrNotFound
			}
			id = in.ID
		} else {
			var e error
			id, e = dbutil.InsertID(ctx, tx, `INSERT INTO llm_routes (sort_order, purpose_pattern, department_id, role, primary_provider_id, primary_model, fallback_provider_id, fallback_model,
				max_attempts, total_timeout_ms, active, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				in.SortOrder, in.PurposePattern, in.DepartmentID, in.Role, in.PrimaryProviderID, in.PrimaryModel, fallback, in.FallbackModel, in.MaxAttempts, in.TotalTimeoutMS, boolInt(in.Active), nullID(p.UserID), now, now)
			if e != nil {
				return e
			}
		}
		diff := []audit.DiffItem{{Key: "route_purpose", New: in.PurposePattern}, {Key: "route_primary", New: prim.Code}}
		if fbCode != "" {
			diff = append(diff, audit.DiffItem{Key: "route_fallback", New: fbCode})
		}
		if e := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "llm.route_save", TargetType: audit.TargetLLMRoute, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess, Diff: diff}); e != nil {
			return e
		}
		if gate != nil && gate.Status == evalset.GateBlocked {
			given := "없음"
			if strings.TrimSpace(in.GateReason) != "" {
				given = "있음"
			}
			return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.gate_override", TargetType: audit.TargetLLMRoute, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
				Diff: []audit.DiffItem{{Key: "set_code", New: gate.SetCode}, {Key: "candidate_score", New: strconv.Itoa(gate.Score)}, {Key: "baseline_score", New: strconv.Itoa(gate.BaselineScore)},
					{Key: "critical_fails", New: strconv.Itoa(gate.CriticalFails)}, {Key: "reason_given", New: given}}})
		}
		return nil
	})
	if err != nil {
		return RouteView{}, err
	}
	list, err := a.ListRoutes(ctx)
	if err != nil {
		return RouteView{}, err
	}
	for _, r := range list.Routes {
		if r.ID == id {
			r.Gate = gate
			return r, nil
		}
	}
	return RouteView{}, contract.ErrNotFound
}

// gateApplies 는 이 저장이 평가할 만큼 경로를 바꾸는지다. 켜진 기존 규칙의 용도·주 공급자·모델이 그대로면 순서·시도 횟수만 고치는 저장이라 건너뛴다.
func (a *ProviderAdmin) gateApplies(ctx context.Context, in RouteInput) bool {
	if in.ID <= 0 {
		return true
	}
	var pat, model string
	var prim int64
	var active int
	err := a.DB.QueryRowContext(ctx, `SELECT purpose_pattern, primary_provider_id, primary_model, active FROM llm_routes WHERE id = ?`, in.ID).Scan(&pat, &prim, &model, &active)
	if err != nil {
		return true
	}
	return !(active == 1 && pat == in.PurposePattern && prim == in.PrimaryProviderID && model == in.PrimaryModel)
}

// DeleteRoute 는 규칙을 지운다(llm:policy_manage).
func (a *ProviderAdmin) DeleteRoute(ctx context.Context, id int64) error {
	p, err := contract.Authorize(ctx, contract.PermLLMPolicyManage)
	if err != nil {
		return err
	}
	var purpose string
	if err := a.DB.QueryRowContext(ctx, `SELECT purpose_pattern FROM llm_routes WHERE id = ?`, id).Scan(&purpose); errors.Is(err, sql.ErrNoRows) {
		return contract.ErrNotFound
	} else if err != nil {
		return err
	}
	return a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `DELETE FROM llm_routes WHERE id = ?`, id); e != nil {
			return e
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "llm.route_delete", TargetType: audit.TargetLLMRoute, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "route_purpose", Old: purpose}}})
	})
}

// SimInput 은 "이 호출은 어디로 가나" 시뮬레이터 입력이다(호출은 하지 않는다).
type SimInput struct {
	Purpose      string `json:"purpose"`
	DepartmentID int64  `json:"departmentId"`
	Role         string `json:"role"`
}

// SimResult 는 판정 결과다.
type SimResult struct {
	Matched      bool   `json:"matched"`
	RouteID      int64  `json:"routeId"`
	PrimaryCode  string `json:"primaryCode"`
	PrimaryModel string `json:"primaryModel"`
	FallbackCode string `json:"fallbackCode"`
	DefaultPath  bool   `json:"defaultPath"` // 규칙이 없어 기본 경로(환경변수 설정)로 간다
}

// Simulate 는 5절의 판정을 그대로 돌려 결과를 보여 준다(llm:policy_manage).
func (a *ProviderAdmin) Simulate(ctx context.Context, in SimInput) (SimResult, error) {
	if _, err := contract.Authorize(ctx, contract.PermLLMPolicyManage); err != nil {
		return SimResult{}, err
	}
	rules, err := LoadRoutes(ctx, a.DB, true)
	if err != nil {
		return SimResult{}, err
	}
	provs, err := LoadProviders(ctx, a.DB)
	if err != nil {
		return SimResult{}, err
	}
	byID := map[int64]ProviderRow{}
	for _, pr := range provs {
		byID[pr.ID] = pr
	}
	usable := rules[:0:0]
	for _, r := range rules {
		if byID[r.PrimaryProviderID].Active {
			usable = append(usable, r)
		}
	}
	keys := []string{}
	if in.Role != "" {
		keys = append(keys, in.Role)
	}
	rule, ok := PickRoute(usable, Subject{Purpose: strings.TrimSpace(in.Purpose), DepartmentID: in.DepartmentID, RoleKeys: keys})
	if !ok {
		return SimResult{DefaultPath: true}, nil
	}
	prim := byID[rule.PrimaryProviderID]
	model := rule.PrimaryModel
	if model == "" {
		model = prim.DefaultModel
	}
	return SimResult{Matched: true, RouteID: rule.ID, PrimaryCode: prim.Code, PrimaryModel: model, FallbackCode: byID[rule.FallbackProviderID].Code}, nil
}

// resolveHost 는 호스트를 지금 해석해 모든 주소가 공인 주소인지 본다(웹훅 등록과 같은 netsafe.ResolveCheck).
// 실제 연결 직전의 검사는 netsafe 클라이언트가 다시 한다.
func (a *ProviderAdmin) resolveHost(ctx context.Context, host string) error {
	if a.Resolve != nil {
		return a.Resolve(ctx, host, a.AllowLocal)
	}
	return netsafe.ResolveCheck(ctx, host, a.AllowLocal, nil)
}

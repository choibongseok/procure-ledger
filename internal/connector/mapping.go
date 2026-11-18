// Package connector 는 일반 CSV/REST 매핑 커넥터 예시다(B42 3단계 D13, 명세 11절).
//
// 특정 회사·제품을 모른다. JSON 매핑 파일(열 이름 변환 + 값 변환 + 주고받는 주소)로 거래처·품목·전표를 연동 API(/api/v1)로 옮긴다:
//   - in(외부 → ERP): CSV 를 읽어 거래처·품목은 가져오기 미리보기 → (오류 0 이고 --dry-run 이 아니면) 반영, 전표는 초안 제출.
//   - out(ERP → 외부): 읽기 API 를 id 순서로 읽어 CSV 파일이나 외부 REST 로 보낸다. 마지막 id 는 상태 파일에 둔다.
//
// 안전
//   - 변환은 닫힌 목록(trim·upper·int·digits_to_dashed_biz·date_ymd·map·const)이다. 임의 코드·표현식·셸 호출이 없다(설정 파일이 코드 실행 경로가 되지 않는다).
//   - 매핑 파일에 토큰 모양의 값(pli_…·whsec_…·Bearer …)이 있으면 로드를 거부한다(E16). 토큰은 환경변수(PL_CONNECTOR_TOKEN)로만 받고, 외부 REST 의 인증 값도 환경변수 이름으로만 참조한다.
//   - 외부 REST 주소는 https 만이고 사설·루프백 주소는 --allow-local 없이는 거부한다(internal/netsafe 재사용).
//   - 같은 파일을 두 번 실행해도 결과가 같다(가져오기는 키 판정으로 "동일"만 나오고 전표는 external_ref 로 멱등).
package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/importx"
)

// 한 번에 보내는 행 수의 상한(API 상한과 같다).
const MaxRows = 5000

// Mapping 은 매핑 파일이다.
type Mapping struct {
	Version   int     `json:"version"`
	Name      string  `json:"name"`
	Direction string  `json:"direction"` // in | out
	Target    string  `json:"target"`    // vendors | items | journals
	Source    *Source `json:"source,omitempty"`
	Sink      *Sink   `json:"sink,omitempty"`
	Fields    []Field `json:"fields"`
	OnError   string  `json:"on_error"` // stop(기본) | skip

	hash string
}

// Source 는 in 방향의 입력 파일이다.
type Source struct {
	Kind      string `json:"kind"` // csv
	Path      string `json:"path"`
	Encoding  string `json:"encoding"`  // utf-8(기본)
	Delimiter string `json:"delimiter"` // 한 글자(기본 ,)
	Header    *bool  `json:"header"`    // 기본 true
}

// Sink 는 out 방향의 출력이다.
type Sink struct {
	Kind       string `json:"kind"` // csv | rest
	Path       string `json:"path,omitempty"`
	URL        string `json:"url,omitempty"`
	AuthEnv    string `json:"auth_env,omitempty"`    // 인증 값이 든 환경변수 이름(값은 파일에 쓰지 않는다)
	AuthHeader string `json:"auth_header,omitempty"` // 기본 Authorization
	AuthScheme string `json:"auth_scheme,omitempty"` // 예: Bearer (값 앞에 붙는 말)
	BatchSize  int    `json:"batch_size,omitempty"`  // 한 번에 보낼 행 수(기본 100)
}

// Field 는 열 하나의 변환이다. in 에서는 from(입력 CSV 열 이름)을 to(API 필드·양식 열 식별자)로, out 에서는 from(API 필드)을 to(출력 열 이름)로 옮긴다.
type Field struct {
	To        string            `json:"to"`
	From      string            `json:"from,omitempty"`
	Required  bool              `json:"required,omitempty"`
	Trim      bool              `json:"trim,omitempty"`
	Transform string            `json:"transform,omitempty"`
	Format    string            `json:"format,omitempty"` // date_ymd 의 입력 형식(YYYYMMDD·YYYY.MM.DD·DD/MM/YYYY …)
	Map       map[string]string `json:"map,omitempty"`    // map 변환의 값 사전
	Value     string            `json:"value,omitempty"`  // const 변환의 값
	Default   string            `json:"default,omitempty"`
}

var transforms = map[string]bool{"": true, "trim": true, "upper": true, "int": true, "digits_to_dashed_biz": true, "date_ymd": true, "map": true, "const": true}

// 전표(journals) in 의 도착 열: 한 줄이 전표의 줄 하나이고 external_ref 가 같은 줄끼리 한 전표가 된다.
var journalCols = []string{"external_ref", "posting_date", "trade_date", "memo", "account_code", "side", "amount", "vendor_id", "line_memo"}

// out 에서 읽을 수 있는 API 필드(자원별). 전표의 lines.* 는 줄마다 한 행이 된다.
var outFields = map[string][]string{
	"vendors":  {"id", "name", "business_no", "account_no", "phone", "email"},
	"items":    {"id", "sku", "name", "unit", "safety_stock", "track_lot", "on_hand"},
	"journals": {"id", "no", "kind", "status", "posting_date", "trade_date", "total_amount", "source_type", "source_id", "lines.seq", "lines.account_code", "lines.side", "lines.amount", "lines.vendor_id", "lines.memo"},
}

var tokenShape = regexp.MustCompile(`(?i)(pli_[A-Za-z0-9_-]{12,}|whsec_[A-Za-z0-9_-]{12,}|plp_[A-Za-z0-9_-]{12,}|bearer\s+\S{8,})`)

// Load 는 매핑 파일을 읽고 검사한다. 토큰 모양의 값·알 수 없는 변환·알 수 없는 열이 있으면 거부한다.
func Load(path string) (*Mapping, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("매핑 파일을 읽을 수 없습니다: %w", err)
	}
	return Parse(raw)
}

// Parse 는 매핑 JSON 을 읽고 검사한다.
func Parse(raw []byte) (*Mapping, error) {
	var any0 any
	if err := json.Unmarshal(raw, &any0); err != nil {
		return nil, errors.New("매핑 파일이 올바른 JSON 이 아닙니다")
	}
	if walkStrings(any0, func(s string) bool { return tokenShape.MatchString(s) }) {
		return nil, errors.New("매핑 파일에 토큰·비밀 모양의 값이 있습니다. 토큰은 환경변수(PL_CONNECTOR_TOKEN)로만 받고 파일에 적지 않습니다")
	}
	var m Mapping
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("매핑 파일에 알 수 없는 항목이 있거나 형식이 맞지 않습니다: %v", err)
	}
	sum := sha256.Sum256(raw)
	m.hash = hex.EncodeToString(sum[:])
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Hash 는 매핑 파일 내용의 해시다(실행 이력에 남긴다. 파일 자체는 서버에 저장하지 않는다).
func (m *Mapping) Hash() string { return m.hash }

func walkStrings(v any, bad func(string) bool) bool {
	switch x := v.(type) {
	case string:
		return bad(x)
	case []any:
		for _, e := range x {
			if walkStrings(e, bad) {
				return true
			}
		}
	case map[string]any:
		for k, e := range x {
			if bad(k) || walkStrings(e, bad) {
				return true
			}
		}
	}
	return false
}

func (m *Mapping) validate() error {
	if m.Version != 1 {
		return errors.New("매핑 파일의 version 은 1 이어야 합니다")
	}
	if n := len([]rune(m.Name)); n < 1 || n > 60 {
		return errors.New("매핑 이름(name)은 1~60자여야 합니다")
	}
	if m.OnError == "" {
		m.OnError = "stop"
	}
	if m.OnError != "stop" && m.OnError != "skip" {
		return errors.New("on_error 는 stop 또는 skip 입니다")
	}
	switch m.Direction {
	case "in":
		if m.Source == nil || m.Source.Kind != "csv" || m.Source.Path == "" {
			return errors.New("in 방향은 source 에 kind \"csv\" 와 path 가 필요합니다")
		}
		if m.Source.Encoding != "" && !strings.EqualFold(m.Source.Encoding, "utf-8") && !strings.EqualFold(m.Source.Encoding, "utf8") {
			return errors.New("source.encoding 은 utf-8 만 지원합니다")
		}
		if d := m.Source.Delimiter; d != "" && len([]rune(d)) != 1 {
			return errors.New("source.delimiter 는 한 글자여야 합니다")
		}
	case "out":
		if m.Sink == nil || (m.Sink.Kind != "csv" && m.Sink.Kind != "rest") {
			return errors.New("out 방향은 sink 에 kind \"csv\" 또는 \"rest\" 가 필요합니다")
		}
		if m.Sink.Kind == "csv" && m.Sink.Path == "" {
			return errors.New("csv sink 에는 path 가 필요합니다")
		}
		if m.Sink.Kind == "rest" && m.Sink.URL == "" {
			return errors.New("rest sink 에는 url 이 필요합니다")
		}
	default:
		return errors.New("direction 은 in 또는 out 입니다")
	}
	if len(m.Fields) == 0 {
		return errors.New("fields 가 비어 있습니다")
	}
	allowedTo := map[string]bool{}
	switch {
	case m.Direction == "in" && m.Target == "journals":
		for _, c := range journalCols {
			allowedTo[c] = true
		}
	case m.Direction == "in" && (m.Target == "vendors" || m.Target == "items"):
		code := map[string]string{"vendors": "vendor", "items": "item"}[m.Target]
		for _, c := range importx.Targets[code].Columns {
			allowedTo[c.ID] = true
		}
	case m.Direction == "out" && outFields[m.Target] != nil:
		// out 의 to 는 출력 열 이름이라 자유롭다. from 을 검사한다.
	default:
		return errors.New("target 은 vendors·items·journals 중 하나여야 합니다")
	}
	seen := map[string]bool{}
	for i, f := range m.Fields {
		if f.To == "" || seen[f.To] {
			return fmt.Errorf("fields[%d]: to 가 비었거나 중복입니다", i)
		}
		seen[f.To] = true
		if !transforms[f.Transform] {
			return fmt.Errorf("fields[%d]: 알 수 없는 변환 %q(trim·upper·int·digits_to_dashed_biz·date_ymd·map·const 만 쓸 수 있습니다)", i, f.Transform)
		}
		if m.Direction == "in" {
			if !allowedTo[f.To] {
				return fmt.Errorf("fields[%d]: 알 수 없는 도착 열 %q", i, f.To)
			}
			if f.From == "" && f.Transform != "const" {
				return fmt.Errorf("fields[%d]: from 이 필요합니다(const 변환 제외)", i)
			}
		} else if !(f.Transform == "const" && f.From == "") {
			ok := false
			for _, a := range outFields[m.Target] {
				ok = ok || a == f.From
			}
			if !ok {
				return fmt.Errorf("fields[%d]: 읽을 수 없는 API 필드 %q", i, f.From)
			}
		}
		if f.Transform == "map" && len(f.Map) == 0 {
			return fmt.Errorf("fields[%d]: map 변환에는 map 사전이 필요합니다", i)
		}
		if f.Transform == "date_ymd" && f.Format == "" {
			return fmt.Errorf("fields[%d]: date_ymd 변환에는 format 이 필요합니다", i)
		}
	}
	if m.Direction == "in" && m.Target == "journals" {
		for _, need := range []string{"external_ref", "posting_date", "memo", "account_code", "side", "amount"} {
			if !seen[need] {
				return fmt.Errorf("전표 매핑에는 %s 열이 필요합니다", need)
			}
		}
	}
	return nil
}

// ---- 변환 ----

var (
	reDigits = regexp.MustCompile(`\D`)
)

// dateLayout 은 YYYY·MM·DD 표기를 Go 시간 서식으로 바꾼다.
func dateLayout(f string) string {
	r := strings.NewReplacer("YYYY", "2006", "YY", "06", "MM", "01", "DD", "02")
	return r.Replace(f)
}

// Apply 는 값 하나에 변환을 적용한다. 오류 문장에는 값이 들어가지 않는다.
func (f Field) Apply(in string) (string, error) {
	v := in
	if f.Trim || f.Transform == "trim" {
		v = strings.TrimSpace(v)
	}
	if v == "" {
		v = f.Default
	}
	switch f.Transform {
	case "upper":
		v = strings.ToUpper(v)
	case "int":
		if v != "" {
			s := strings.NewReplacer(",", "", " ", "").Replace(v)
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return "", errors.New("정수가 아닙니다")
			}
			v = strconv.FormatInt(n, 10)
		}
	case "digits_to_dashed_biz":
		if v != "" {
			d := reDigits.ReplaceAllString(v, "")
			if len(d) != 10 {
				return "", errors.New("사업자번호는 숫자 10자리여야 합니다")
			}
			v = d[:3] + "-" + d[3:5] + "-" + d[5:]
		}
	case "date_ymd":
		if v != "" {
			t, err := time.Parse(dateLayout(f.Format), v)
			if err != nil {
				return "", errors.New("날짜 형식이 맞지 않습니다")
			}
			v = t.Format("2006-01-02")
		}
	case "map":
		if v != "" {
			to, ok := f.Map[v]
			if !ok {
				return "", errors.New("값 사전에 없는 값입니다")
			}
			v = to
		}
	case "const":
		v = f.Value
	}
	if f.Required && strings.TrimSpace(v) == "" {
		return "", errors.New("필수 값이 비었습니다")
	}
	return v, nil
}

package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// 실행 결과 코드(connector_runs.result_code 와 같다).
const (
	CodeOK        = "ok"
	CodeDryRun    = "dry_run"
	CodeRowErrors = "row_errors"
	CodeFailed    = "failed"
)

// Result 는 한 번의 실행 결과다. Messages 에는 행 번호·사유만 있고 값이 없다.
type Result struct {
	Code     string
	Total    int // 읽은(또는 보낸) 행 수
	OK       int
	Failed   int
	DryRun   bool
	Messages []string
}

// Options 는 한 번의 실행 설정이다.
type Options struct {
	Mapping    *Mapping
	BaseDir    string  // 매핑 파일이 있는 폴더(상대 경로의 기준)
	Client     *Client // 연동 API 호출자
	DryRun     bool
	StatePath  string // out 의 마지막 id 상태 파일(비면 BaseDir/connector_state.json)
	AllowLocal bool   // 외부 REST 주소의 루프백 허용(로컬 시험 전용)
	Getenv     func(string) string
	HTTP       *http.Client // rest sink 의 클라이언트(비면 netsafe 의 안전한 클라이언트)
}

// Client 는 /api/v1 호출자다. 429 는 Retry-After 만큼 기다려 다시 보낸다.
type Client struct {
	Base   string
	Token  string
	HTTP   *http.Client
	Sleep  func(time.Duration)
	MaxTry int
}

// APIError 는 API 가 돌려준 오류다(상태·코드·한국어 사유).
type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API 오류 %d(%s): %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, idem string, out any) error {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	tries := c.MaxTry
	if tries <= 0 {
		tries = 6
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return fmt.Errorf("서버에 연결하지 못했습니다: %w", err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt+1 < tries {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if wait < 1 {
				wait = 1
			}
			if wait > 70 {
				wait = 70
			}
			sleep(time.Duration(wait) * time.Second)
			continue
		}
		if resp.StatusCode >= 300 {
			var e struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			_ = json.Unmarshal(b, &e)
			return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				return fmt.Errorf("응답을 해석하지 못했습니다: %w", err)
			}
		}
		return nil
	}
}

// Run 은 매핑을 한 번 실행한다. 행 오류·반영 불가는 Result 로, 그 밖의 실패(연결·인증·파일)는 오류로 돌려준다.
func Run(ctx context.Context, o Options) (Result, error) {
	m := o.Mapping
	res := Result{DryRun: o.DryRun}
	var err error
	switch {
	case m.Direction == "in" && m.Target == "journals":
		err = runInJournals(ctx, o, &res)
	case m.Direction == "in":
		err = runInImport(ctx, o, &res)
	default:
		err = runOut(ctx, o, &res)
	}
	switch {
	case err != nil:
		res.Code = CodeFailed
	case res.Code != "":
	case o.DryRun:
		res.Code = CodeDryRun
	default:
		res.Code = CodeOK
	}
	return res, err
}

func (o Options) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(o.BaseDir, p)
}

// ---- in: CSV 읽기와 변환 ----

type inRow struct {
	no    int // CSV 의 행 번호(머리글이 1)
	cells map[string]string
}

func readCSV(o Options) ([]string, [][]string, error) {
	src := o.Mapping.Source
	b, err := os.ReadFile(o.path(src.Path))
	if err != nil {
		return nil, nil, fmt.Errorf("입력 파일을 읽을 수 없습니다: %w", err)
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.LazyQuotes = false
	if d := src.Delimiter; d != "" {
		r.Comma = []rune(d)[0]
	}
	recs, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("CSV 형식이 올바르지 않습니다: %w", err)
	}
	header := src.Header == nil || *src.Header
	if !header {
		return nil, nil, errors.New("머리글이 없는 CSV 는 지원하지 않습니다(source.header 는 true)")
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("CSV 가 비었습니다")
	}
	return recs[0], recs[1:], nil
}

// mapRows 는 CSV 행을 도착 열로 바꾼다. 오류 행은 메시지(행 번호·열·사유, 값 없음)로 모은다.
func mapRows(o Options, header []string, recs [][]string, res *Result) ([]inRow, bool) {
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}
	for _, f := range o.Mapping.Fields {
		if f.From != "" {
			if _, ok := idx[f.From]; !ok && f.Required {
				res.Messages = append(res.Messages, fmt.Sprintf("입력 파일에 필수 열이 없습니다(%s)", f.From))
				return nil, false
			}
		}
	}
	var rows []inRow
	for i, rec := range recs {
		no := i + 2
		blank := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		res.Total++
		cells := map[string]string{}
		var bad []string
		for _, f := range o.Mapping.Fields {
			in := ""
			if j, ok := idx[f.From]; ok && j < len(rec) {
				in = rec[j]
			}
			v, err := f.Apply(in)
			if err != nil {
				bad = append(bad, f.To+": "+err.Error())
				continue
			}
			if v != "" {
				cells[f.To] = v
			}
		}
		if len(bad) > 0 {
			res.Failed++
			res.Messages = append(res.Messages, fmt.Sprintf("행 %d: %s", no, strings.Join(bad, "; ")))
			if o.Mapping.OnError == "stop" {
				return nil, false
			}
			continue
		}
		rows = append(rows, inRow{no: no, cells: cells})
	}
	return rows, true
}

type importPreviewResp struct {
	Data struct {
		BatchNo    string `json:"batch_no"`
		ApplyToken string `json:"apply_token"`
		Applicable bool   `json:"applicable"`
		Message    string `json:"message"`
		Counts     struct{ Total, Add, Same, Change, Error, Warn int }
		Rows       []struct {
			RowNo   int    `json:"row_no"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		}
	} `json:"data"`
}

func runInImport(ctx context.Context, o Options, res *Result) error {
	header, recs, err := readCSV(o)
	if err != nil {
		return err
	}
	rows, ok := mapRows(o, header, recs, res)
	if !ok {
		res.Code = CodeRowErrors
		return nil
	}
	if len(rows) == 0 {
		res.Messages = append(res.Messages, "보낼 행이 없습니다")
		return nil
	}
	base := "/api/v1/imports/" + o.Mapping.Target
	for start := 0; start < len(rows); start += MaxRows {
		end := start + MaxRows
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		body := make([]map[string]string, len(chunk))
		for i, r := range chunk {
			body[i] = r.cells
		}
		var pv importPreviewResp
		if err := o.Client.do(ctx, "POST", base+"/preview", map[string]any{"rows": body}, "", &pv); err != nil {
			return err
		}
		d := pv.Data
		if d.Counts.Error > 0 {
			res.Code = CodeRowErrors
			res.Failed += d.Counts.Error
			for _, r := range d.Rows {
				if r.Verdict == "error" {
					orig := chunk[0].no
					if r.RowNo >= 2 && r.RowNo-2 < len(chunk) {
						orig = chunk[r.RowNo-2].no
					}
					res.Messages = append(res.Messages, fmt.Sprintf("행 %d: %s", orig, r.Reason))
				}
			}
			return nil
		}
		if o.DryRun || !d.Applicable {
			res.OK += d.Counts.Total
			if !d.Applicable && d.Message != "" && d.Counts.Add+d.Counts.Change > 0 {
				res.Messages = append(res.Messages, d.Message)
			}
			continue
		}
		var ap struct {
			Data struct {
				Counts struct{ Total int }
			} `json:"data"`
		}
		idem := "c-" + shortHash(o.Mapping.Name+"|"+d.BatchNo)
		if err := o.Client.do(ctx, "POST", base+"/batches/"+url.PathEscape(d.BatchNo)+"/apply", map[string]any{"apply_token": d.ApplyToken, "rows": body}, idem, &ap); err != nil {
			return err
		}
		res.OK += d.Counts.Total
	}
	return nil
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:10])
}

func runInJournals(ctx context.Context, o Options, res *Result) error {
	header, recs, err := readCSV(o)
	if err != nil {
		return err
	}
	rows, ok := mapRows(o, header, recs, res)
	if !ok {
		res.Code = CodeRowErrors
		return nil
	}
	type jv struct {
		ref   string
		first inRow
		lines []map[string]any
	}
	var order []*jv
	byRef := map[string]*jv{}
	for _, r := range rows {
		ref := r.cells["external_ref"]
		j := byRef[ref]
		if j == nil {
			j = &jv{ref: ref, first: r}
			byRef[ref] = j
			order = append(order, j)
		}
		amt, err := strconv.ParseInt(r.cells["amount"], 10, 64)
		if err != nil {
			res.Failed++
			res.Messages = append(res.Messages, fmt.Sprintf("행 %d: amount: 정수가 아닙니다", r.no))
			if o.Mapping.OnError == "stop" {
				res.Code = CodeRowErrors
				return nil
			}
			continue
		}
		line := map[string]any{"account_code": r.cells["account_code"], "side": r.cells["side"], "amount": amt}
		if v := r.cells["vendor_id"]; v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				line["vendor_id"] = n
			}
		}
		if v := r.cells["line_memo"]; v != "" {
			line["memo"] = v
		}
		j.lines = append(j.lines, line)
	}
	if o.DryRun {
		res.OK = len(order)
		return nil
	}
	for _, j := range order {
		f := j.first.cells
		body := map[string]any{"posting_date": f["posting_date"], "memo": f["memo"], "external_ref": j.ref, "lines": j.lines}
		if v := f["trade_date"]; v != "" {
			body["trade_date"] = v
		}
		err := o.Client.do(ctx, "POST", "/api/v1/journals", body, "c-"+shortHash(o.Mapping.Name+"|"+j.ref), nil)
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 409) {
				res.Failed++
				res.Messages = append(res.Messages, fmt.Sprintf("전표 %s: %s", j.ref, ae.Message))
				if o.Mapping.OnError == "stop" {
					res.Code = CodeRowErrors
					return nil
				}
				continue
			}
			return err
		}
		res.OK++
	}
	if res.Failed > 0 {
		res.Code = CodeRowErrors
	}
	return nil
}

// ---- out ----

type outPage struct {
	Data       []map[string]any `json:"data"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

func (o Options) statePath() string {
	if o.StatePath != "" {
		return o.StatePath
	}
	return filepath.Join(o.BaseDir, "connector_state.json")
}

func loadState(path string) map[string]int64 {
	st := map[string]int64{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func runOut(ctx context.Context, o Options, res *Result) error {
	m := o.Mapping
	if m.Sink.Kind == "rest" { // --dry-run 도 실전송과 같은 주소 검사를 받는다(허용되지 않은 주소를 시험 삼아 통과시키지 않는다)
		if _, _, _, err := checkRESTSink(ctx, o); err != nil {
			return err
		}
	}
	state := loadState(o.statePath())
	last := state[m.Name]
	var out [][]string
	var outObjs []map[string]string
	for {
		var pg outPage
		path := fmt.Sprintf("/api/v1/%s?limit=200&after=%d", map[string]string{"vendors": "vendors", "items": "items", "journals": "journals"}[m.Target], last)
		if err := o.Client.do(ctx, "GET", path, nil, "", &pg); err != nil {
			return err
		}
		for _, rec := range pg.Data {
			id, _ := rec["id"].(float64)
			if int64(id) > last {
				last = int64(id)
			}
			for _, row := range flatten(m, rec) {
				vals := make([]string, len(m.Fields))
				obj := map[string]string{}
				var bad []string
				for i, f := range m.Fields {
					v, err := f.Apply(row[f.From])
					if err != nil {
						bad = append(bad, f.To+": "+err.Error())
						continue
					}
					vals[i], obj[f.To] = v, v
				}
				res.Total++
				if len(bad) > 0 {
					res.Failed++
					res.Messages = append(res.Messages, fmt.Sprintf("번호 %d: %s", int64(id), strings.Join(bad, "; ")))
					if m.OnError == "stop" {
						res.Code = CodeRowErrors
						return nil
					}
					continue
				}
				out = append(out, vals)
				outObjs = append(outObjs, obj)
			}
		}
		if !pg.HasMore || len(pg.Data) == 0 {
			break
		}
	}
	if o.DryRun {
		res.OK = len(out)
		return nil
	}
	switch m.Sink.Kind {
	case "csv":
		if err := writeCSV(o.path(m.Sink.Path), m.Fields, out); err != nil {
			return err
		}
	case "rest":
		if err := sendREST(ctx, o, outObjs); err != nil {
			return err
		}
	}
	res.OK = len(out)
	state[m.Name] = last
	return saveState(o.statePath(), state)
}

// flatten 은 API 항목을 출력 행으로 편다. 전표의 lines.* 를 쓰는 매핑은 줄마다 한 행이다.
func flatten(m *Mapping, rec map[string]any) []map[string]string {
	base := map[string]string{}
	for k, v := range rec {
		if k != "lines" {
			base[k] = scalar(v)
		}
	}
	useLines := false
	for _, f := range m.Fields {
		useLines = useLines || strings.HasPrefix(f.From, "lines.")
	}
	lines, _ := rec["lines"].([]any)
	if !useLines || len(lines) == 0 {
		return []map[string]string{base}
	}
	var out []map[string]string
	for _, l := range lines {
		row := map[string]string{}
		for k, v := range base {
			row[k] = v
		}
		if lm, ok := l.(map[string]any); ok {
			for k, v := range lm {
				row["lines."+k] = scalar(v)
			}
		}
		out = append(out, row)
	}
	return out
}

func writeCSV(path string, fields []Field, rows [][]string) error {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	h := make([]string, len(fields))
	for i, f := range fields {
		h[i] = f.To
	}
	if err := w.Write(h); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func saveState(path string, st map[string]int64) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// checkRESTSink 는 외부 REST 로 보내기 전에 필요한 검사를 한 곳에서 한다: 주소(https, 허용된 호스트·포트, 사설·루프백은 --allow-local 이 있을 때만)와 인증 환경변수.
// 실전송과 --dry-run 이 같은 거부를 받게 sendREST 와 runOut 의 dry-run 이 모두 이 함수를 부른다.
func checkRESTSink(ctx context.Context, o Options) (u *url.URL, header, value string, err error) {
	s := o.Mapping.Sink
	u, err = netsafe.ValidateURL(s.URL, netsafe.URLOptions{AllowLocal: o.AllowLocal})
	if err != nil {
		return nil, "", "", err
	}
	if !(o.AllowLocal && isLoopbackHost(u.Hostname())) {
		if err := netsafe.ResolveCheck(ctx, u.Hostname(), o.AllowLocal, nil); err != nil {
			return nil, "", "", err
		}
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if s.AuthEnv != "" {
		value = getenv(s.AuthEnv)
		if value == "" {
			return nil, "", "", fmt.Errorf("인증 값이 든 환경변수 %s 가 비어 있습니다", s.AuthEnv)
		}
		header = s.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		value = s.AuthScheme + value
	}
	return u, header, value, nil
}

// sendREST 는 출력 행을 외부 REST 로 보낸다: https 만(--allow-local 이면 루프백 http), 사설·루프백은 연결 직전 검사가 막는다(netsafe).
// 인증 값은 환경변수 이름(auth_env)으로만 참조한다.
func sendREST(ctx context.Context, o Options, rows []map[string]string) error {
	s := o.Mapping.Sink
	u, header, value, err := checkRESTSink(ctx, o)
	if err != nil {
		return err
	}
	hc := o.HTTP
	if hc == nil {
		hc = netsafe.NewClient(netsafe.ClientOptions{AllowLocal: o.AllowLocal, Timeout: 30 * time.Second})
	}
	batch := s.BatchSize
	if batch <= 0 || batch > MaxRows {
		batch = 100
	}
	for start := 0; start < len(rows); start += batch {
		end := start + batch
		if end > len(rows) {
			end = len(rows)
		}
		b, _ := json.Marshal(map[string]any{"rows": rows[start:end]})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set(header, value)
		}
		resp, err := hc.Do(req)
		if err != nil {
			if errors.Is(err, netsafe.ErrBlockedAddr) {
				return errors.New("외부 REST 주소가 허용되지 않은 주소(내부망·루프백)로 연결됩니다")
			}
			return errors.New("외부 REST 에 연결하지 못했습니다")
		}
		_ = netsafe.ReadLimited(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("외부 REST 가 오류를 돌려줬습니다(상태 %d). 상태 파일은 바꾸지 않았으므로 다시 실행하면 같은 구간을 다시 보냅니다", resp.StatusCode)
		}
	}
	return nil
}

func isLoopbackHost(h string) bool {
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1"
}

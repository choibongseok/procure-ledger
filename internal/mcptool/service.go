package mcptool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// Options 는 서비스 구성이다.
type Options struct {
	DB      *sql.DB
	Txs     contract.TxManager
	Audit   audit.Recorder
	Catalog *Catalog
	Masker  contract.Masker
	Now     func() time.Time
	// Eval 은 B46 평가 세트 실행기다(없으면 평가 연결은 확인 없이 지나간다).
	Eval EvalRunner
}

// Service 는 사용자 정의 도구 관리 서비스다(정의·버전·시험 실행).
type Service struct{ o Options }

// New 는 서비스를 만든다. 필수 의존성이 없으면 오류다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil || o.Catalog == nil {
		return nil, errNoCatalog
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}, nil
}

func (s *Service) now() string { return s.o.Now().UTC().Format(time.RFC3339) }

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

func stateErr(msg string) error { return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg) }

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// ToolView 는 목록의 한 줄이다.
type ToolView struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Title          string `json:"title"`
	Kind           string `json:"kind"`
	Resource       string `json:"resource"`
	ResourceLabel  string `json:"resourceLabel"`
	CurrentVersion int    `json:"currentVersion"`
	Enabled        bool   `json:"enabled"`
	RatePerMin     int    `json:"ratePerMin"`
	EvalSetID      int64  `json:"evalSetId"`
	Broken         bool   `json:"broken"` // 자원이 사라졌거나 카탈로그 버전이 달라진 정의(명세 14절)
	Tested         bool   `json:"tested"` // 현재 버전의 시험 실행이 성공했다(켜기 조건)
	Calls24h       int    `json:"calls24h"`
	Denied24h      int    `json:"denied24h"`
	UpdatedAt      string `json:"updatedAt"`
}

// VersionView 는 버전 한 줄이다. Definition 은 정규화된 JSON 글이다(비교 화면용).
type VersionView struct {
	Version    int    `json:"version"`
	Note       string `json:"note"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  string `json:"createdAt"`
	Definition string `json:"definition"`
	DefHash    string `json:"defHash"`
}

// Detail 은 도구 상세다.
type Detail struct {
	ToolView
	Definition       json.RawMessage `json:"definition"`
	ModelDescription string          `json:"modelDescription"`
	Versions         []VersionView   `json:"versions"`
	// Pending 은 초안 도구의 진행 중 켜기 요청이다(다른 관리자의 확인을 기다린다).
	Pending *EnableRequestView `json:"pendingRequest,omitempty"`
}

const toolCols = `t.id, t.name, t.title, t.kind, t.current_version, t.enabled, t.rate_per_min, t.eval_set_id, t.updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanTool(r rowScanner) (ToolView, error) {
	var v ToolView
	var en int
	err := r.Scan(&v.ID, &v.Name, &v.Title, &v.Kind, &v.CurrentVersion, &en, &v.RatePerMin, &v.EvalSetID, &v.UpdatedAt)
	v.Enabled = en == 1
	return v, err
}

// decorate 는 현재 버전의 자원·깨짐 여부를 채운다.
func (s *Service) decorate(ctx context.Context, v *ToolView) {
	var raw string
	var rv int
	err := s.o.DB.QueryRowContext(ctx, `SELECT definition_json, resource_version FROM mcp_tool_versions WHERE tool_id = ? AND version = ?`, v.ID, v.CurrentVersion).Scan(&raw, &rv)
	if err != nil {
		v.Broken = true
		return
	}
	var d Definition
	if json.Unmarshal([]byte(raw), &d) != nil {
		v.Broken = true
		return
	}
	v.Resource = d.Resource
	if d.Kind == "draft" {
		v.Resource = d.Target
	}
	var tn int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_tool_tests WHERE tool_id = ? AND version = ?`, v.ID, v.CurrentVersion).Scan(&tn); err == nil {
		v.Tested = tn > 0
	}
	// 최근 24시간 호출·거부: 호출 종료 감사(채널 mcp, 동작 mcp.<도구>)를 센다.
	since := s.o.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN result = 'denied' THEN 1 ELSE 0 END), 0) FROM audit_logs WHERE channel = 'mcp' AND action = ? AND at >= ?`, "mcp."+v.Name, since).Scan(&v.Calls24h, &v.Denied24h)
	_, label, ver, ok := s.defRef(d)
	if !ok {
		v.Broken = true
		return
	}
	v.ResourceLabel = label
	v.Broken = ver != rv
}

// defRef 는 정의가 가리키는 대상(조회 자원 또는 초안 대상)의 권한·한국어 이름·카탈로그 버전이다. 대상이 없으면 ok=false.
func (s *Service) defRef(def Definition) (perm contract.Permission, label string, version int, ok bool) {
	if def.Kind == "draft" {
		t, found := s.o.Catalog.Draft(def.Target)
		if !found {
			return "", "", 0, false
		}
		return t.Perm, t.Label, 1, true
	}
	res, found := s.o.Catalog.Get(def.Resource)
	if !found {
		return "", "", 0, false
	}
	return res.Perm, res.Label, res.Version, true
}

// List 는 도구 목록이다(mcptool:read).
func (s *Service) List(ctx context.Context) ([]ToolView, error) {
	if _, err := contract.Authorize(ctx, contract.PermMCPToolRead); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT `+toolCols+` FROM mcp_tools t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	var out []ToolView
	for rows.Next() {
		v, err := scanTool(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		s.decorate(ctx, &out[i])
	}
	if out == nil {
		out = []ToolView{}
	}
	return out, nil
}

// Get 은 도구 상세다(mcptool:read).
func (s *Service) Get(ctx context.Context, id int64) (Detail, error) {
	if _, err := contract.Authorize(ctx, contract.PermMCPToolRead); err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

func (s *Service) detail(ctx context.Context, id int64) (Detail, error) {
	v, err := scanTool(s.o.DB.QueryRowContext(ctx, `SELECT `+toolCols+` FROM mcp_tools t WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, contract.ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	s.decorate(ctx, &v)
	d := Detail{ToolView: v, Versions: []VersionView{}, Pending: s.pending(ctx, id)}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT ver.version, ver.note, COALESCE(u.name, ''), ver.created_at, ver.definition_json, ver.def_hash
		FROM mcp_tool_versions ver LEFT JOIN users u ON u.id = ver.created_by WHERE ver.tool_id = ? ORDER BY ver.version DESC`, id)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var ver VersionView
		if err := rows.Scan(&ver.Version, &ver.Note, &ver.CreatedBy, &ver.CreatedAt, &ver.Definition, &ver.DefHash); err != nil {
			rows.Close()
			return d, err
		}
		d.Versions = append(d.Versions, ver)
		if ver.Version == v.CurrentVersion {
			d.Definition = json.RawMessage(ver.Definition)
			var def Definition
			if json.Unmarshal([]byte(ver.Definition), &def) == nil {
				d.ModelDescription = ModelDescription(def)
			}
		}
	}
	return d, rows.Close()
}

// SaveInput 은 저장 입력이다. ID 가 0 이면 새 도구, 아니면 그 도구의 새 버전이다.
type SaveInput struct {
	ID         int64           `json:"id"`
	Title      string          `json:"title"`
	Definition json.RawMessage `json:"definition"`
	Note       string          `json:"note"`
}

func cleanTitleNote(title, note string) (string, string, error) {
	title, note = strings.TrimSpace(title), strings.TrimSpace(note)
	if n := len([]rune(title)); n < 1 || n > 60 {
		return "", "", valErr("도구 이름(화면용)은 1~60자여야 합니다")
	}
	if len([]rune(note)) > 200 {
		return "", "", valErr("버전 메모는 200자 이내여야 합니다")
	}
	return title, note, nil
}

// Save 는 정의를 검증해 새 도구나 새 버전으로 저장한다(mcptool:manage). 도구는 꺼진 채로 만들어진다.
// 정의가 현재 버전과 같으면 거부한다(같은 내용의 버전을 쌓지 않는다).
func (s *Service) Save(ctx context.Context, in SaveInput) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	title, note, err := cleanTitleNote(in.Title, in.Note)
	if err != nil {
		return Detail{}, err
	}
	def, verr := ParseAndValidate(in.Definition, s.o.Catalog)
	if verr != nil {
		return Detail{}, verr
	}
	perm, _, resVersion, _ := s.defRef(def)
	_ = perm
	canon, hash, err := Canonical(def)
	if err != nil {
		return Detail{}, err
	}
	var toolID int64
	var version int
	if in.ID > 0 {
		var name, curHash, curKind string
		var cur int
		err := s.o.DB.QueryRowContext(ctx, `SELECT t.name, t.kind, t.current_version, COALESCE((SELECT def_hash FROM mcp_tool_versions v WHERE v.tool_id = t.id AND v.version = t.current_version), '') FROM mcp_tools t WHERE t.id = ?`, in.ID).Scan(&name, &curKind, &cur, &curHash)
		if errors.Is(err, sql.ErrNoRows) {
			return Detail{}, contract.ErrNotFound
		}
		if err != nil {
			return Detail{}, err
		}
		if name != def.Name {
			return Detail{}, &ValidationError{[]FieldError{{Path: "name", Message: "도구 이름은 바꿀 수 없습니다"}}}
		}
		if curKind != def.Kind {
			return Detail{}, &ValidationError{[]FieldError{{Path: "kind", Message: "도구 종류(조회·초안)는 바꿀 수 없습니다"}}}
		}
		if curHash == hash {
			return Detail{}, stateErr("현재 버전과 같은 정의입니다. 바뀐 곳이 없어 새 버전을 만들지 않았습니다.")
		}
		toolID, version = in.ID, cur+1
	} else {
		var n int
		if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_tools`).Scan(&n); err != nil {
			return Detail{}, err
		}
		if n >= MaxTools {
			return Detail{}, stateErr(fmt.Sprintf("사용자 정의 도구는 %d개까지 만들 수 있습니다.", MaxTools))
		}
		version = 1
	}
	now := s.now()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if in.ID == 0 {
			rate := DefaultRatePerMin
			if def.Kind == "draft" {
				rate = DefaultDraftRatePerMin
			}
			id, e := dbutil.InsertID(ctx, tx, `INSERT INTO mcp_tools (name, title, kind, current_version, enabled, rate_per_min, created_by, created_at, updated_at) VALUES (?,?,?,1,0,?,?,?,?)`,
				def.Name, title, def.Kind, rate, nullID(p.UserID), now, now)
			if e != nil {
				if dbutil.IsUnique(e) {
					return &ValidationError{[]FieldError{{Path: "name", Message: "이미 있는 도구 이름입니다"}}}
				}
				return e
			}
			toolID = id
		} else {
			if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET title = ?, current_version = ?, updated_at = ? WHERE id = ?`, title, version, now, toolID); e != nil {
				return e
			}
			if e := s.resetDraftOnChange(ctx, tx, p, toolID, def.Name, def.Kind); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO mcp_tool_versions (tool_id, version, definition_json, def_hash, description, note, resource_version, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			toolID, version, string(canon), hash, ModelDescription(def), note, resVersion, nullID(p.UserID), now); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.save", TargetType: audit.TargetMCPTool, TargetID: def.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: def.Name}, {Key: "tool_kind", New: def.Kind}, {Key: "tool_version", New: strconv.Itoa(version)}, {Key: "tool_resource", New: def.Resource}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, toolID)
}

// Rollback 은 옛 버전의 정의를 복사한 새 버전을 만든다(mcptool:manage). 이력은 줄어들지 않는다. 옛 정의가 지금 카탈로그에 맞지 않으면 거부한다.
func (s *Service) Rollback(ctx context.Context, id int64, version int, note string) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return Detail{}, valErr("버전 메모는 200자 이내여야 합니다")
	}
	var name, title, raw string
	var cur int
	err = s.o.DB.QueryRowContext(ctx, `SELECT t.name, t.title, t.current_version, v.definition_json FROM mcp_tools t JOIN mcp_tool_versions v ON v.tool_id = t.id AND v.version = ? WHERE t.id = ?`, version, id).Scan(&name, &title, &cur, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, contract.ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	if version == cur {
		return Detail{}, stateErr("이미 현재 버전입니다.")
	}
	def, verr := ParseAndValidate([]byte(raw), s.o.Catalog)
	if verr != nil {
		return Detail{}, stateErr("그 버전의 정의는 지금의 자원 목록과 맞지 않아 되돌릴 수 없습니다: " + verr.Error())
	}
	_, _, resVersion, _ := s.defRef(def)
	canon, hash, err := Canonical(def)
	if err != nil {
		return Detail{}, err
	}
	if note == "" {
		note = fmt.Sprintf("v%d 로 되돌림", version)
	}
	next := cur + 1
	now := s.now()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET current_version = ?, updated_at = ? WHERE id = ?`, next, now, id); e != nil {
			return e
		}
		if e := s.resetDraftOnChange(ctx, tx, p, id, name, def.Kind); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO mcp_tool_versions (tool_id, version, definition_json, def_hash, description, note, resource_version, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, next, string(canon), hash, ModelDescription(def), note, resVersion, nullID(p.UserID), now); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.rollback", TargetType: audit.TargetMCPTool, TargetID: name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: name}, {Key: "source_version", New: strconv.Itoa(version)}, {Key: "tool_version", Old: strconv.Itoa(cur), New: strconv.Itoa(next)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// TestInput 은 시험 실행 입력이다.
type TestInput struct {
	Definition json.RawMessage `json:"definition"`
	Input      map[string]any  `json:"input"`
}

// TestResult 는 시험 실행 결과다. Notice 는 "내 권한 기준" 안내다.
type TestResult struct {
	Result
	ElapsedMS     int64    `json:"elapsedMs"`
	CallableRoles []string `json:"callableRoles"`
	Notice        string   `json:"notice"`
}

// Test 는 정의(저장 전·후 어느 쪽이든)를 예시 입력으로 돌려 결과를 미리 보여 준다(mcptool:manage). 실행자 본인 권한·열람 범위·법인 기준이다.
// 입력 값과 결과 내용은 감사에 남기지 않고 건수·성공 여부만 남긴다. 검증·입력 오류는 실행하지 않았으므로 감사하지 않는다.
func (s *Service) Test(ctx context.Context, in TestInput) (TestResult, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return TestResult{}, err
	}
	def, verr := ParseAndValidate(in.Definition, s.o.Catalog)
	if verr != nil {
		return TestResult{}, verr
	}
	perm, _, _, _ := s.defRef(def)
	var mask func(string) string
	if s.o.Masker != nil {
		mask = s.o.Masker.MaskText
	}
	if _, err := BindInput(def, in.Input); err != nil {
		return TestResult{}, err
	}
	start := time.Now()
	var out Result
	var runErr error
	notice := "지금은 내 권한·열람 범위로 본 결과입니다. 다른 역할에게는 다르게 보일 수 있습니다."
	if def.Kind == "draft" {
		// 초안 도구의 시험 실행은 초안을 만들지 않는다: 입력이 어떤 초안 칸에 들어가는지만 보여 준다.
		t, _ := s.o.Catalog.Draft(def.Target)
		bound, _ := BindInput(def, in.Input)
		var vals map[string]any
		vals, runErr = DraftValues(def, t, bound)
		if runErr == nil {
			row := map[string]any{}
			for _, f := range t.Fields {
				if v, ok := vals[f.Name]; ok {
					out.Fields = append(out.Fields, f.Name)
					if sv, isStr := v.(string); isStr && mask != nil {
						v = mask(sv)
					}
					row[f.Name] = v
				}
			}
			out.Rows, out.Count = []map[string]any{row}, 1
		}
		notice = "초안 도구의 시험 실행은 초안을 만들지 않습니다. 입력이 들어갈 초안 칸의 값을 미리 보여 줄 뿐이고, 실제 호출은 호출한 사람의 권한으로 임시저장 초안만 만듭니다."
	} else {
		out, runErr = Execute(ctx, s.o.Catalog, def, in.Input, CallerOf(p, s.o.Now()), mask)
	}
	elapsed := time.Since(start).Milliseconds()
	ok := "예"
	if runErr != nil {
		ok = "아니오"
	}
	_, hash, _ := Canonical(def)
	if aerr := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if runErr == nil {
			if e := s.markTested(ctx, tx, def.Name, hash, p.UserID, out.Count); e != nil {
				return e
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.test", TargetType: audit.TargetMCPTool, TargetID: def.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: def.Name}, {Key: "result_count", New: strconv.Itoa(out.Count)}, {Key: "test_ok", New: ok}}})
	}); aerr != nil {
		return TestResult{}, aerr
	}
	if runErr != nil {
		return TestResult{}, runErr
	}
	return TestResult{Result: out, ElapsedMS: elapsed, CallableRoles: callableRoles(perm, def), Notice: notice}, nil
}

// callableRoles 는 이 도구를 부를 수 있는 역할이다: 자원의 권한을 가진 역할 ∩ 정의의 roles(있으면). 권한 표(RolePermissions)에서 계산한다.
func callableRoles(perm contract.Permission, def Definition) []string {
	var out []string
	for role, perms := range contract.RolePermissions {
		okPerm := false
		for _, pm := range perms {
			if pm == perm {
				okPerm = true
				break
			}
		}
		if !okPerm {
			continue
		}
		if len(def.Roles) > 0 && !has(def.Roles, string(role)) {
			continue
		}
		out = append(out, string(role))
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// CatalogField·CatalogResource 는 편집 화면의 선택지다.
type CatalogField struct {
	Name     string            `json:"name"`
	Label    string            `json:"label"`
	Type     string            `json:"type"`
	Values   []string          `json:"values,omitempty"`
	Labels   map[string]string `json:"valueLabels,omitempty"`
	Ops      []string          `json:"ops,omitempty"`
	Sortable bool              `json:"sortable"`
	Return   bool              `json:"return"`
	Ctx      []string          `json:"ctx,omitempty"`
}

type CatalogResource struct {
	Name         string         `json:"name"`
	Label        string         `json:"label"`
	EntityOwned  bool           `json:"entityOwned"`
	DefaultLimit int            `json:"defaultLimit"`
	MaxLimit     int            `json:"maxLimit"`
	Fields       []CatalogField `json:"fields"`
}

// Catalog 는 고를 수 있는 자원 목록이다(mcptool:read).
func (s *Service) Catalog(ctx context.Context) ([]CatalogResource, error) {
	if _, err := contract.Authorize(ctx, contract.PermMCPToolRead); err != nil {
		return nil, err
	}
	var out []CatalogResource
	for _, r := range s.o.Catalog.All() {
		cr := CatalogResource{Name: r.Name, Label: r.Label, EntityOwned: r.EntityOwned, DefaultLimit: r.DefaultLimit, MaxLimit: r.MaxLimit}
		for _, f := range r.Fields {
			cr.Fields = append(cr.Fields, CatalogField{Name: f.Name, Label: f.Label, Type: f.Type, Values: f.Values, Labels: f.Labels, Ops: f.Ops, Sortable: f.Sortable, Return: f.Return, Ctx: f.Ctx})
		}
		out = append(out, cr)
	}
	return out, nil
}

// DraftTargetView 는 편집 화면의 초안 대상 선택지다.
type DraftTargetView struct {
	Name   string           `json:"name"`
	Label  string           `json:"label"`
	Fields []DraftFieldView `json:"fields"`
}

type DraftFieldView struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// DraftTargets 는 고를 수 있는 초안 대상과 맵에 쓸 수 있는 칸이다.
func (s *Service) DraftTargets() []DraftTargetView {
	out := []DraftTargetView{}
	for _, t := range s.o.Catalog.Drafts() {
		v := DraftTargetView{Name: t.Name, Label: t.Label}
		for _, f := range t.Fields {
			v.Fields = append(v.Fields, DraftFieldView{Name: f.Name, Label: f.Label, Type: f.Type, Required: f.Required})
		}
		out = append(out, v)
	}
	return out
}

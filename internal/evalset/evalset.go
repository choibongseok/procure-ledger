// Package evalset 은 회사별 평가 세트(B46 1단계)다: 세트·문항·버전 관리와 CSV 가져오기·내보내기.
// (저장소·서비스·화면), platform(권한·감사).
//
// 규칙: 게시된 버전은 불변(DB 트리거 + 서비스), 수정은 새 초안 → 게시. 문항의 질문·기대·루브릭·맥락은 저장 전에 Masker.MaskText 를 지난 글뿐이다(원문 미저장).
// 쓰기와 감사는 같은 트랜잭션이다. 문항 편집(초안)은 게시 전까지 효력이 없어 감사하지 않고, 게시·가져오기·내보내기를 감사한다.
package evalset

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 상한(명세 13절).
const (
	MaxSets         = 200
	MaxItems        = 1000
	MaxQuestionLen  = 4000
	MaxContextLen   = 4000
	MaxRubricLen    = 2000
	DefaultTolerant = "abs:0"
)

// 용도·채점 기준.
const (
	PurposeDoc    = "doc"
	PurposePolicy = "policy"
	PurposeTool   = "tool"
	PurposeText   = "text"

	GraderExact    = "exact"
	GraderContains = "contains"
	GraderNumber   = "number"
	GraderLLM      = "llm"
)

// Options 는 서비스 구성이다.
type Options struct {
	DB     *sql.DB
	Txs    contract.TxManager
	Audit  audit.Recorder
	Masker contract.Masker
	Now    func() time.Time
	// 실행기(B46 2단계): LLM 은 게이트웨이다. Force 는 호출을 한 공급자로 고정하는 context 함수(llmgw.WithForcedProvider)이고,
	// Rehearsal 이면 외부 공급자 실행을 거부한다.
	LLM       llm.Client
	Force     func(ctx context.Context, providerCode string) context.Context
	Rehearsal bool
}

// Service 는 평가 세트 서비스다.
type Service struct {
	o  Options
	wg sync.WaitGroup
}

// New 는 서비스를 만든다. 필수 의존성이 없으면 오류다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil || o.Masker == nil {
		return nil, errors.New("evalset: DB·트랜잭션·감사 기록기·마스킹이 필요합니다")
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

var codeRe = regexp.MustCompile(`^[a-z0-9-]{2,30}$`)

// SetView 는 세트 목록의 한 줄이다.
type SetView struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	Purpose        string `json:"purpose"`
	Description    string `json:"description"`
	CurrentVersion int    `json:"currentVersion"`
	ItemCount      int    `json:"itemCount"` // 현재 게시 버전(없으면 초안)의 문항 수
	DraftVersion   int    `json:"draftVersion"`
	BaselineRunID  int64  `json:"baselineRunId"`
	GateDropPct    int    `json:"gateDropPct"`
	Active         bool   `json:"active"`
	UpdatedAt      string `json:"updatedAt"`
}

// VersionView 는 버전 한 줄이다.
type VersionView struct {
	Version     int    `json:"version"`
	Status      string `json:"status"` // draft | published
	Revision    int    `json:"revision"`
	ItemCount   int    `json:"itemCount"`
	ItemsHash   string `json:"itemsHash"`
	Note        string `json:"note"`
	PublishedAt string `json:"publishedAt"`
}

// ItemView 는 문항 한 건이다(마스킹된 글).
type ItemView struct {
	ID          int64  `json:"id"`
	Seq         int    `json:"seq"`
	Question    string `json:"question"`
	Context     string `json:"context"`
	Expected    string `json:"expected"`
	Grader      string `json:"grader"`
	Tolerance   string `json:"tolerance"`
	Rubric      string `json:"rubric"`
	Critical    bool   `json:"critical"`
	Tags        string `json:"tags"`
	MaskedCount int    `json:"maskedCount"`
}

// Detail 은 세트 상세다: 세트, 버전 목록, 선택한 버전의 문항.
type Detail struct {
	SetView
	Versions []VersionView `json:"versions"`
	Selected VersionView   `json:"selected"`
	Items    []ItemView    `json:"items"`
}

type setRow struct {
	SetView
}

func (s *Service) loadSet(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, code string) (SetView, error) {
	var v SetView
	var active int
	err := q.QueryRowContext(ctx, `SELECT id, code, name, purpose, description, current_version, baseline_run_id, gate_drop_pct, active, updated_at FROM eval_sets WHERE code = ?`, code).
		Scan(&v.ID, &v.Code, &v.Name, &v.Purpose, &v.Description, &v.CurrentVersion, &v.BaselineRunID, &v.GateDropPct, &active, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	v.Active = active == 1
	return v, err
}

// ListSets 는 세트 목록이다(eval:read).
func (s *Service) ListSets(ctx context.Context) ([]SetView, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id, code, name, purpose, description, current_version, baseline_run_id, gate_drop_pct, active, updated_at FROM eval_sets ORDER BY code`)
	if err != nil {
		return nil, err
	}
	var out []SetView
	for rows.Next() {
		var v SetView
		var active int
		if err := rows.Scan(&v.ID, &v.Code, &v.Name, &v.Purpose, &v.Description, &v.CurrentVersion, &v.BaselineRunID, &v.GateDropPct, &active, &v.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		v.Active = active == 1
		out = append(out, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		_ = s.o.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM eval_set_versions WHERE set_id = ? AND status = 'draft'`, out[i].ID).Scan(&out[i].DraftVersion)
		shown := out[i].CurrentVersion
		if shown == 0 {
			shown = out[i].DraftVersion
		}
		_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_items i JOIN eval_set_versions v ON v.id = i.version_id WHERE v.set_id = ? AND v.version = ?`, out[i].ID, shown).Scan(&out[i].ItemCount)
	}
	if out == nil {
		out = []SetView{}
	}
	return out, nil
}

// SetInput 은 세트 등록·수정 입력이다.
type SetInput struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Purpose     string `json:"purpose"`
	Description string `json:"description"`
	GateDropPct *int   `json:"gateDropPct,omitempty"`
	Active      *bool  `json:"active,omitempty"`
}

func validPurpose(p string) bool {
	return p == PurposeDoc || p == PurposePolicy || p == PurposeTool || p == PurposeText
}

// CreateSet 은 세트를 만들고 빈 초안(버전 1)을 함께 만든다(eval:write).
func (s *Service) CreateSet(ctx context.Context, in SetInput) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return Detail{}, err
	}
	in.Code, in.Name, in.Description = strings.ToLower(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if !codeRe.MatchString(in.Code) {
		return Detail{}, valErr("세트 코드는 영문 소문자·숫자·-로 2~30자여야 합니다.")
	}
	if n := len([]rune(in.Name)); n < 1 || n > 60 {
		return Detail{}, valErr("이름은 1~60자여야 합니다.")
	}
	if !validPurpose(in.Purpose) {
		return Detail{}, valErr("용도를 고르세요(증빙 문서 판정·규정 검색·도구 선택·일반 질의).")
	}
	if len([]rune(in.Description)) > 500 {
		return Detail{}, valErr("설명은 500자 이내여야 합니다.")
	}
	var n int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_sets`).Scan(&n); err != nil {
		return Detail{}, err
	}
	if n >= MaxSets {
		return Detail{}, stateErr(fmt.Sprintf("평가 세트는 %d개까지 만들 수 있습니다.", MaxSets))
	}
	now := s.now()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		id, e := dbutil.InsertID(ctx, tx, `INSERT INTO eval_sets (code, name, purpose, description, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			in.Code, in.Name, in.Purpose, in.Description, nullID(p.UserID), now, now)
		if e != nil {
			if dbutil.IsUnique(e) {
				return valErr("이미 사용 중인 세트 코드입니다.")
			}
			return e
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO eval_set_versions (set_id, version, status, created_by, created_at) VALUES (?, 1, 'draft', ?, ?)`, id, nullID(p.UserID), now); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.set_save", TargetType: audit.TargetEvalSet, TargetID: in.Code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: in.Code}, {Key: "set_purpose", New: in.Purpose}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, in.Code, 0)
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// UpdateSet 은 이름·설명·허용 하락·사용 여부를 고친다(eval:write). 코드·용도는 바꿀 수 없다.
func (s *Service) UpdateSet(ctx context.Context, code string, in SetInput) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return Detail{}, err
	}
	set, err := s.loadSet(ctx, s.o.DB, code)
	if err != nil {
		return Detail{}, err
	}
	name, desc := strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if name == "" {
		name = set.Name
	}
	if n := len([]rune(name)); n > 60 {
		return Detail{}, valErr("이름은 1~60자여야 합니다.")
	}
	if len([]rune(desc)) > 500 {
		return Detail{}, valErr("설명은 500자 이내여야 합니다.")
	}
	drop := set.GateDropPct
	if in.GateDropPct != nil {
		if *in.GateDropPct < 0 || *in.GateDropPct > 100 {
			return Detail{}, valErr("허용 하락은 0~100 사이여야 합니다.")
		}
		drop = *in.GateDropPct
	}
	active := set.Active
	if in.Active != nil {
		active = *in.Active
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE eval_sets SET name = ?, description = ?, gate_drop_pct = ?, active = ?, updated_at = ? WHERE id = ?`, name, desc, drop, boolInt(active), s.now(), set.ID); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.set_save", TargetType: audit.TargetEvalSet, TargetID: code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: code}, {Key: "set_purpose", New: set.Purpose}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, code, 0)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Get 은 세트 상세다(eval:read). version 0 이면 초안이 있으면 초안, 없으면 현재 게시 버전이다.
func (s *Service) Get(ctx context.Context, code string, version int) (Detail, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return Detail{}, err
	}
	set, err := s.loadSet(ctx, s.o.DB, code)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{SetView: set, Versions: []VersionView{}, Items: []ItemView{}}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT v.version, v.status, v.revision, v.items_hash, v.note, v.published_at, (SELECT COUNT(*) FROM eval_items i WHERE i.version_id = v.id) FROM eval_set_versions v WHERE v.set_id = ? ORDER BY v.version DESC`, set.ID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var v VersionView
		if err := rows.Scan(&v.Version, &v.Status, &v.Revision, &v.ItemsHash, &v.Note, &v.PublishedAt, &v.ItemCount); err != nil {
			rows.Close()
			return d, err
		}
		d.Versions = append(d.Versions, v)
		if v.Status == "draft" {
			d.DraftVersion = v.Version
		}
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	want := version
	if want == 0 {
		want = d.DraftVersion
		if want == 0 {
			want = set.CurrentVersion
		}
	}
	for _, v := range d.Versions {
		if v.Version == want {
			d.Selected = v
		}
	}
	if d.Selected.Version == 0 {
		if version != 0 {
			return d, contract.ErrNotFound
		}
		return d, nil
	}
	d.ItemCount = d.Selected.ItemCount
	irows, err := s.o.DB.QueryContext(ctx, `SELECT i.id, i.seq, i.question, i.context_json, i.expected, i.grader, i.tolerance, i.rubric, i.critical, i.tags, i.masked_count
		FROM eval_items i JOIN eval_set_versions v ON v.id = i.version_id WHERE v.set_id = ? AND v.version = ? ORDER BY i.seq`, set.ID, want)
	if err != nil {
		return d, err
	}
	defer irows.Close()
	for irows.Next() {
		var it ItemView
		var crit int
		if err := irows.Scan(&it.ID, &it.Seq, &it.Question, &it.Context, &it.Expected, &it.Grader, &it.Tolerance, &it.Rubric, &crit, &it.Tags, &it.MaskedCount); err != nil {
			return d, err
		}
		it.Critical = crit == 1
		d.Items = append(d.Items, it)
	}
	return d, irows.Err()
}

// itemsHash 는 문항 내용(질문·맥락·기대·채점·허용·루브릭·치명)의 해시 앞 16자다(B45 비교 표의 suite_hash 로 쓴다).
func itemsHash(items []ItemView) string {
	h := sha256.New()
	for _, it := range items {
		b, _ := json.Marshal([]any{it.Question, it.Context, it.Expected, it.Grader, it.Tolerance, it.Rubric, it.Critical})
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var _ = strconv.Itoa

package evalset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ItemInput 은 문항 등록·수정 입력이다. Revision 은 화면이 마지막으로 본 초안 수정 번호(낙관적 잠금)다.
type ItemInput struct {
	ID        int64  `json:"id"`
	Question  string `json:"question"`
	Context   string `json:"context"`
	Expected  string `json:"expected"`
	Grader    string `json:"grader"`
	Tolerance string `json:"tolerance"`
	Rubric    string `json:"rubric"`
	Critical  bool   `json:"critical"`
	Tags      string `json:"tags"`
	Revision  int    `json:"revision"`
}

// SaveResult 는 문항 저장 결과다. Warnings 는 막지 않는 안내다(가려진 값이 기대 답에 있음 등).
type SaveResult struct {
	Item     ItemView `json:"item"`
	Revision int      `json:"revision"`
	Warnings []string `json:"warnings"`
}

// maskCount 는 마스킹이 바꾼 글 조각 수다(공백으로 나눈 조각 중 달라진 것).
func (s *Service) maskText(text string) (string, int) {
	masked := s.o.Masker.MaskText(text)
	if masked == text {
		return text, 0
	}
	a, b := strings.Fields(text), strings.Fields(masked)
	n := 0
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	return masked, n
}

func parseTolerance(t string) (kind string, val float64, err error) {
	k, v, ok := strings.Cut(strings.TrimSpace(t), ":")
	if !ok || (k != "abs" && k != "rel") {
		return "", 0, errors.New("허용 오차는 abs:<숫자>(절대값) 또는 rel:<퍼센트> 형식이어야 합니다.")
	}
	f, perr := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if perr != nil || f < 0 || (k == "rel" && f > 100) {
		return "", 0, errors.New("허용 오차 값이 올바르지 않습니다(0 이상, 비율은 100 이하).")
	}
	return k, f, nil
}

// normalizeItem 은 용도에 맞게 입력을 검사하고 마스킹한 문항을 돌려준다.
func (s *Service) normalizeItem(purpose string, in ItemInput) (ItemView, []string, error) {
	it := ItemView{Grader: strings.ToLower(strings.TrimSpace(in.Grader)), Tolerance: strings.TrimSpace(in.Tolerance), Critical: in.Critical}
	total := 0
	var warns []string
	var n int
	if it.Question, n = s.maskText(strings.TrimSpace(in.Question)); true {
		total += n
	}
	var expMasked int
	it.Expected, expMasked = s.maskText(strings.TrimSpace(in.Expected))
	total += expMasked
	if it.Rubric, n = s.maskText(strings.TrimSpace(in.Rubric)); true {
		total += n
	}
	if it.Context, n = s.maskText(strings.TrimSpace(in.Context)); true {
		total += n
	}
	if it.Tags, n = s.maskText(strings.TrimSpace(in.Tags)); true {
		total += n
	}
	it.MaskedCount = total
	if rn := len([]rune(it.Question)); rn < 1 || rn > MaxQuestionLen {
		return it, nil, valErr("질문은 1~%d자여야 합니다.", MaxQuestionLen)
	}
	// llm 채점은 루브릭의 필수·금지 줄로만 채점하므로 기대 답이 비어도 된다(명세 5절). 그 밖의 채점은 기대 답이 있어야 한다.
	if rn := len([]rune(it.Expected)); rn > MaxQuestionLen || (rn < 1 && it.Grader != GraderLLM) {
		return it, nil, valErr("기대 답은 1~%d자여야 합니다(LLM 채점은 비워 둘 수 있습니다).", MaxQuestionLen)
	}
	if len([]rune(it.Rubric)) > MaxRubricLen || len([]rune(it.Context)) > MaxContextLen || len([]rune(it.Tags)) > 200 {
		return it, nil, valErr("맥락·채점 기준·태그가 너무 깁니다.")
	}
	if it.Context != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(it.Context), &obj); err != nil || obj == nil {
			return it, nil, valErr("맥락은 JSON 객체여야 합니다(예: {\"role\":\"staff\"}).")
		}
	}
	switch it.Grader {
	case GraderExact, GraderContains, GraderNumber, GraderLLM:
	default:
		return it, nil, valErr("채점 기준을 고르세요(정확 일치·포함·숫자·LLM).")
	}
	switch purpose {
	case PurposeDoc:
		if it.Grader != GraderExact && it.Grader != GraderNumber {
			return it, nil, valErr("증빙 문서 판정 문항은 정확 일치 또는 숫자 채점만 쓸 수 있습니다.")
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(it.Expected), &obj); err != nil || len(obj) == 0 {
			return it, nil, valErr("증빙 문서 판정의 기대 답은 칸 이름과 값의 JSON 객체여야 합니다(예: {\"공급가액\":1000000}).")
		}
	case PurposePolicy:
		if it.Grader != GraderContains {
			return it, nil, valErr("규정 검색 문항은 포함 채점만 쓸 수 있습니다(기대 조항을 한 줄에 하나씩).")
		}
	case PurposeTool:
		if it.Grader != GraderExact {
			return it, nil, valErr("도구 선택 문항은 정확 일치 채점만 쓸 수 있습니다.")
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(it.Expected), &obj); err != nil {
			return it, nil, valErr("도구 선택의 기대 답은 JSON 이어야 합니다(예: {\"tool\":\"search_items\",\"args\":{\"query\":\"용지\"}}).")
		}
		if t, ok := obj["tool"].(string); !ok || strings.TrimSpace(t) == "" {
			return it, nil, valErr("도구 선택의 기대 답에 \"tool\"(도구 이름)이 있어야 합니다.")
		}
	}
	if it.Grader == GraderNumber {
		if it.Tolerance == "" {
			it.Tolerance = DefaultTolerant
		}
		if _, _, err := parseTolerance(it.Tolerance); err != nil {
			return it, nil, valErr("%s", err.Error())
		}
	} else {
		it.Tolerance = ""
	}
	if it.Grader == GraderLLM {
		hasLine := false
		for _, ln := range strings.Split(it.Rubric, "\n") {
			l := strings.TrimSpace(ln)
			if strings.HasPrefix(l, "필수:") || strings.HasPrefix(l, "금지:") {
				hasLine = true
			}
		}
		if !hasLine {
			return it, nil, valErr("LLM 채점은 채점 기준에 '필수:' 또는 '금지:' 로 시작하는 줄이 하나 이상 있어야 합니다.")
		}
	} else {
		it.Rubric = ""
	}
	if total > 0 {
		warns = append(warns, fmt.Sprintf("개인정보 %d건을 가려서 저장했습니다.", total))
	}
	if expMasked > 0 {
		warns = append(warns, "가려진 값이 기대 답에 들어 있어 이 문항은 맞출 수 없습니다. 개인정보 값 자체를 묻는 문항은 만들 수 없습니다.")
	}
	return it, warns, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// draftOf 는 세트의 초안 버전 번호·행 번호·수정 번호다. 없으면 ErrInvalidState.
func (s *Service) draftOf(ctx context.Context, q queryer, setID int64) (versionID int64, version, revision int, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, version, revision FROM eval_set_versions WHERE set_id = ? AND status = 'draft'`, setID).Scan(&versionID, &version, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, stateErr("초안이 없습니다. 새 초안을 만드세요.")
	}
	return
}

func (s *Service) activeSet(ctx context.Context, code string) (SetView, error) {
	set, err := s.loadSet(ctx, s.o.DB, code)
	if err != nil {
		return set, err
	}
	if !set.Active {
		return set, stateErr("꺼진 세트는 고칠 수 없습니다.")
	}
	return set, nil
}

// NewDraft 는 직전 게시 버전을 복사한 초안을 만든다(eval:write). 이미 초안이 있으면 거부한다.
func (s *Service) NewDraft(ctx context.Context, code string) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return Detail{}, err
	}
	set, err := s.activeSet(ctx, code)
	if err != nil {
		return Detail{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_set_versions WHERE set_id = ? AND status = 'draft'`, set.ID).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return stateErr("이미 편집 중인 초안이 있습니다.")
		}
		var max int
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM eval_set_versions WHERE set_id = ?`, set.ID).Scan(&max); e != nil {
			return e
		}
		var vid int64
		if e := tx.QueryRowContext(ctx, `INSERT INTO eval_set_versions (set_id, version, status, created_by, created_at) VALUES (?,?, 'draft', ?, ?) RETURNING id`, set.ID, max+1, nullID(p.UserID), s.now()).Scan(&vid); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO eval_items (version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count)
			SELECT ?, i.seq, i.question, i.context_json, i.expected, i.grader, i.tolerance, i.rubric, i.critical, i.tags, i.masked_count
			FROM eval_items i JOIN eval_set_versions v ON v.id = i.version_id WHERE v.set_id = ? AND v.version = ?`, vid, set.ID, set.CurrentVersion)
		return e
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, code, 0)
}

// SaveItem 은 초안의 문항을 더하거나(ID 0) 고친다(eval:write). 게시된 버전은 고칠 수 없다.
func (s *Service) SaveItem(ctx context.Context, code string, in ItemInput) (SaveResult, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalWrite); err != nil {
		return SaveResult{}, err
	}
	set, err := s.activeSet(ctx, code)
	if err != nil {
		return SaveResult{}, err
	}
	it, warns, err := s.normalizeItem(set.Purpose, in)
	if err != nil {
		return SaveResult{}, err
	}
	var rev int
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		vid, _, cur, e := s.draftOf(ctx, tx, set.ID)
		if e != nil {
			return e
		}
		if in.Revision != 0 && in.Revision != cur {
			return stateErr("다른 사람이 먼저 고쳤습니다. 새로 불러온 뒤 다시 저장하세요.")
		}
		if in.ID == 0 {
			var n, maxSeq int
			if e := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM eval_items WHERE version_id = ?`, vid).Scan(&n, &maxSeq); e != nil {
				return e
			}
			if n >= MaxItems {
				return stateErr(fmt.Sprintf("한 세트는 문항을 %d개까지 둘 수 있습니다.", MaxItems))
			}
			it.Seq = maxSeq + 1
			if e := tx.QueryRowContext(ctx, `INSERT INTO eval_items (version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count) VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
				vid, it.Seq, it.Question, it.Context, it.Expected, it.Grader, it.Tolerance, it.Rubric, boolInt(it.Critical), it.Tags, it.MaskedCount).Scan(&it.ID); e != nil {
				return e
			}
		} else {
			res, e := tx.ExecContext(ctx, `UPDATE eval_items SET question = ?, context_json = ?, expected = ?, grader = ?, tolerance = ?, rubric = ?, critical = ?, tags = ?, masked_count = ? WHERE id = ? AND version_id = ?`,
				it.Question, it.Context, it.Expected, it.Grader, it.Tolerance, it.Rubric, boolInt(it.Critical), it.Tags, it.MaskedCount, in.ID, vid)
			if e != nil {
				return e
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return contract.ErrNotFound
			}
			it.ID = in.ID
			_ = tx.QueryRowContext(ctx, `SELECT seq FROM eval_items WHERE id = ?`, in.ID).Scan(&it.Seq)
		}
		rev = cur + 1
		_, e = tx.ExecContext(ctx, `UPDATE eval_set_versions SET revision = ? WHERE id = ?`, rev, vid)
		return e
	})
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Item: it, Revision: rev, Warnings: nonNil(warns)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DeleteItem 은 초안의 문항을 지운다(eval:write). 번호(seq)는 다시 매기지 않는다.
func (s *Service) DeleteItem(ctx context.Context, code string, id int64, revision int) (int, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalWrite); err != nil {
		return 0, err
	}
	set, err := s.activeSet(ctx, code)
	if err != nil {
		return 0, err
	}
	var rev int
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		vid, _, cur, e := s.draftOf(ctx, tx, set.ID)
		if e != nil {
			return e
		}
		if revision != 0 && revision != cur {
			return stateErr("다른 사람이 먼저 고쳤습니다. 새로 불러온 뒤 다시 하세요.")
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM eval_items WHERE id = ? AND version_id = ?`, id, vid)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return contract.ErrNotFound
		}
		rev = cur + 1
		_, e = tx.ExecContext(ctx, `UPDATE eval_set_versions SET revision = ? WHERE id = ?`, rev, vid)
		return e
	})
	return rev, err
}

// Publish 는 초안을 게시한다(eval:write). 문항이 없으면 거부하고, 게시하면 그 버전은 고칠 수 없다.
func (s *Service) Publish(ctx context.Context, code, note string) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return Detail{}, err
	}
	set, err := s.activeSet(ctx, code)
	if err != nil {
		return Detail{}, err
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return Detail{}, valErr("버전 메모는 200자 이내여야 합니다.")
	}
	var published, count int
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		vid, ver, _, e := s.draftOf(ctx, tx, set.ID)
		if e != nil {
			return e
		}
		items, e := s.itemsOf(ctx, tx, vid)
		if e != nil {
			return e
		}
		if len(items) == 0 {
			return stateErr("문항이 없어 게시할 수 없습니다.")
		}
		// 직전 게시본과 문항이 같으면 새 버전을 쌓지 않는다(같은 문항의 버전이 늘면 비교·기준선이 혼란스럽다).
		var prev string
		if e := tx.QueryRowContext(ctx, `SELECT items_hash FROM eval_set_versions WHERE set_id = ? AND status = 'published' ORDER BY version DESC LIMIT 1`, set.ID).Scan(&prev); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if prev != "" && prev == itemsHash(items) {
			return stateErr("직전 게시본과 문항이 같습니다. 바꾼 곳이 없어 게시하지 않았습니다.")
		}
		count, published = len(items), ver
		if _, e = tx.ExecContext(ctx, `UPDATE eval_set_versions SET status = 'published', items_hash = ?, note = ?, published_by = ?, published_at = ? WHERE id = ?`,
			itemsHash(items), note, nullID(p.UserID), s.now(), vid); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE eval_sets SET current_version = ?, updated_at = ? WHERE id = ?`, ver, s.now(), set.ID); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.version_publish", TargetType: audit.TargetEvalSet, TargetID: code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: code}, {Key: "set_version", New: strconv.Itoa(ver)}, {Key: "item_count", New: strconv.Itoa(count)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	_ = published
	return s.Get(ctx, code, 0)
}

func (s *Service) itemsOf(ctx context.Context, q queryer, versionID int64) ([]ItemView, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count FROM eval_items WHERE version_id = ? ORDER BY seq`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemView
	for rows.Next() {
		var it ItemView
		var crit int
		if err := rows.Scan(&it.ID, &it.Seq, &it.Question, &it.Context, &it.Expected, &it.Grader, &it.Tolerance, &it.Rubric, &crit, &it.Tags, &it.MaskedCount); err != nil {
			return nil, err
		}
		it.Critical = crit == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

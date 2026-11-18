package evalset

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// CSV 형식(명세 5·13절): UTF-8(BOM 허용), 첫 줄은 머리글, 열 순서는 아래와 같다. 채점은 한국어(정확·포함·숫자·LLM) 또는 영문 코드, 치명은 예·아니오.
var csvHeader = []string{"문항", "기대", "채점", "허용오차", "치명", "태그", "맥락", "기준"}

var graderLabel = map[string]string{GraderExact: "정확", GraderContains: "포함", GraderNumber: "숫자", GraderLLM: "LLM"}

func graderCode(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "정확", "정확 일치", "exact":
		return GraderExact
	case "포함", "contains":
		return GraderContains
	case "숫자", "number":
		return GraderNumber
	case "llm", "llm 채점":
		return GraderLLM
	}
	return ""
}

func parseYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "예", "y", "yes", "1", "true", "치명":
		return true
	}
	return false
}

// ImportResult 는 가져오기 결과다.
type ImportResult struct {
	Imported     int      `json:"imported"`
	MaskedCount  int      `json:"maskedCount"`
	Revision     int      `json:"revision"`
	DraftVersion int      `json:"draftVersion"`
	Warnings     []string `json:"warnings"`
}

const maxCSVBytes = 2 << 20

// ImportCSV 는 CSV 의 문항을 초안에 더한다(eval:write). 한 행이라도 틀리면 전부 취소한다. 초안이 없으면 직전 게시 버전을 복사한 초안을 먼저 만든다.
// 오류 문장에는 몇 행 몇 번째 칸인지만 있고 값은 없다. 파일 이름·내용은 감사에 남기지 않고 개수만 남긴다.
func (s *Service) ImportCSV(ctx context.Context, code string, data []byte) (ImportResult, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return ImportResult{}, err
	}
	if len(data) > maxCSVBytes {
		return ImportResult{}, valErr("파일은 2MB 까지 가져올 수 있습니다.")
	}
	set, err := s.activeSet(ctx, code)
	if err != nil {
		return ImportResult{}, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil || len(head) == 0 || strings.TrimSpace(head[0]) != csvHeader[0] {
		return ImportResult{}, valErr("첫 줄은 머리글(문항,기대,채점,허용오차,치명,태그,맥락,기준)이어야 합니다.")
	}
	var items []ItemView
	var problems []string
	var warns []string
	total := 0
	for row := 2; ; row++ {
		rec, rerr := r.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return ImportResult{}, valErr("%d행: CSV 형식이 올바르지 않습니다.", row)
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		cell := func(i int) string {
			if i < len(rec) {
				return export.CSVUnsafe(strings.TrimSpace(rec[i]))
			}
			return ""
		}
		gc := graderCode(cell(2))
		if gc == "" && cell(2) == "" {
			gc = GraderExact
		}
		if gc == "" {
			problems = append(problems, fmt.Sprintf("%d행 3열(채점): 알 수 없는 채점 기준입니다.", row))
			continue
		}
		it, w, nerr := s.normalizeItem(set.Purpose, ItemInput{Question: cell(0), Expected: cell(1), Grader: gc, Tolerance: cell(3), Critical: parseYes(cell(4)), Tags: cell(5), Context: cell(6), Rubric: cell(7)})
		if nerr != nil {
			problems = append(problems, fmt.Sprintf("%d행: %s", row, strings.TrimPrefix(nerr.Error(), contract.ErrValidation.Error()+": ")))
		} else {
			items = append(items, it)
			total += it.MaskedCount
			_ = w
		}
		if len(problems) >= 20 {
			problems = append(problems, "오류가 많아 여기까지만 알립니다.")
			break
		}
	}
	if len(problems) > 0 {
		return ImportResult{}, valErr("가져오지 않았습니다(전부 취소): %s", strings.Join(problems, " / "))
	}
	if len(items) == 0 {
		return ImportResult{}, valErr("가져올 문항이 없습니다.")
	}
	var res ImportResult
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		vid, ver, cur, e := s.draftOf(ctx, tx, set.ID)
		if errors.Is(e, contract.ErrInvalidState) {
			// 초안이 없으면 직전 게시 버전을 복사한 초안을 만든다.
			var max int
			if e2 := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM eval_set_versions WHERE set_id = ?`, set.ID).Scan(&max); e2 != nil {
				return e2
			}
			if e2 := tx.QueryRowContext(ctx, `INSERT INTO eval_set_versions (set_id, version, status, created_by, created_at) VALUES (?,?, 'draft', ?, ?) RETURNING id`, set.ID, max+1, nullID(p.UserID), s.now()).Scan(&vid); e2 != nil {
				return e2
			}
			if _, e2 := tx.ExecContext(ctx, `INSERT INTO eval_items (version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count)
				SELECT ?, i.seq, i.question, i.context_json, i.expected, i.grader, i.tolerance, i.rubric, i.critical, i.tags, i.masked_count
				FROM eval_items i JOIN eval_set_versions v ON v.id = i.version_id WHERE v.set_id = ? AND v.version = ?`, vid, set.ID, set.CurrentVersion); e2 != nil {
				return e2
			}
			ver, cur, e = max+1, 1, nil
		}
		if e != nil {
			return e
		}
		var n, maxSeq int
		if e := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM eval_items WHERE version_id = ?`, vid).Scan(&n, &maxSeq); e != nil {
			return e
		}
		if n+len(items) > MaxItems {
			return stateErr(fmt.Sprintf("한 세트는 문항을 %d개까지 둘 수 있습니다(현재 %d개, 가져오려는 %d개).", MaxItems, n, len(items)))
		}
		for i, it := range items {
			if _, e := tx.ExecContext(ctx, `INSERT INTO eval_items (version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				vid, maxSeq+1+i, it.Question, it.Context, it.Expected, it.Grader, it.Tolerance, it.Rubric, boolInt(it.Critical), it.Tags, it.MaskedCount); e != nil {
				return e
			}
		}
		res = ImportResult{Imported: len(items), MaskedCount: total, Revision: cur + 1, DraftVersion: ver}
		if _, e := tx.ExecContext(ctx, `UPDATE eval_set_versions SET revision = ? WHERE id = ?`, res.Revision, vid); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.items_import", TargetType: audit.TargetEvalSet, TargetID: code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: code}, {Key: "item_count", New: strconv.Itoa(len(items))}, {Key: "masked_count", New: strconv.Itoa(total)}}})
	})
	if err != nil {
		return ImportResult{}, err
	}
	if total > 0 {
		warns = append(warns, fmt.Sprintf("개인정보 %d건을 가려서 저장했습니다.", total))
	}
	res.Warnings = nonNil(warns)
	return res, nil
}

// ExportCSV 는 세트 버전의 문항을 CSV(UTF-8 BOM)로 돌려준다(eval:read). version 0 이면 초안, 없으면 현재 게시 버전이다.
// 수식으로 읽힐 수 있는 칸은 앞에 작은따옴표를 붙인다. 가려서 저장된 글뿐이라 원문이 없다. 감사에는 개수만 남긴다.
func (s *Service) ExportCSV(ctx context.Context, code string, version int) ([]byte, string, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalRead)
	if err != nil {
		return nil, "", err
	}
	d, err := s.Get(ctx, code, version)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write(csvHeader)
	for _, it := range d.Items {
		crit := "아니오"
		if it.Critical {
			crit = "예"
		}
		_ = w.Write([]string{export.CSVSafe(it.Question), export.CSVSafe(it.Expected), graderLabel[it.Grader], it.Tolerance, crit, export.CSVSafe(it.Tags), export.CSVSafe(it.Context), export.CSVSafe(it.Rubric)})
	}
	w.Flush()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.items_export", TargetType: audit.TargetEvalSet, TargetID: code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: code}, {Key: "item_count", New: strconv.Itoa(len(d.Items))}}})
	})
	if err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("%s-v%d.csv", code, d.Selected.Version)
	return buf.Bytes(), name, nil
}

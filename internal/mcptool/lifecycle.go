package mcptool

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
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/evalset"
)

// EvalRunner 는 B46 평가 세트를 돌리는 쪽이다(구현: *evalset.Service). 시험은 모의 호출기로 대신한다.
type EvalRunner interface {
	RunSync(ctx context.Context, actor contract.Principal, set string, in evalset.RunInput) (evalset.RunView, error)
}

// EnableOpts 는 켜기의 평가 미달 확인이다: 평가 세트가 기준 미달이면 사유와 함께 EvalOverride 를 보내야 켜진다(감사 mcptool.eval_override).
type EnableOpts struct {
	EvalOverride bool   `json:"evalOverride"`
	Reason       string `json:"reason"`
}

// EvalResult 는 연결된 평가 세트의 실행 결과 요약이다.
type EvalResult struct {
	SetCode       string `json:"setCode"`
	SetName       string `json:"setName"`
	PassRatePct   int    `json:"passRatePct"`
	ThresholdPct  int    `json:"thresholdPct"`
	CriticalFails int    `json:"criticalFails"`
	Message       string `json:"message"`
}

// EvalGateError 는 평가 세트가 기준 미달이라 켜지 않았음을 알린다(409 + 결과). 사유와 함께 다시 보내면 켠다.
type EvalGateError struct{ Result EvalResult }

func (e *EvalGateError) Error() string { return e.Result.Message }

// checkEval 은 연결된 평가 세트를 켜기 전에 돌린다. 합격(기준 이상·치명 0)이면 nil, 미달이면 확인이 있어야 하며 확인했으면 그 결과를 돌려준다(감사용).
func (s *Service) checkEval(ctx context.Context, p contract.Principal, d Detail, opts EnableOpts) (*EvalResult, error) {
	if d.EvalSetID == 0 || s.o.Eval == nil {
		return nil, nil
	}
	var code, name string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT code, name FROM eval_sets WHERE id = ?`, d.EvalSetID).Scan(&code, &name); err != nil {
		return nil, nil // 연결한 세트가 없어졌다: 평가 없이 진행한다
	}
	res := EvalResult{SetCode: code, SetName: name}
	v, err := s.o.Eval.RunSync(ctx, p, code, evalset.RunInput{Mode: evalset.ModeManual})
	switch {
	case err != nil || v.Status != evalset.StatusDone:
		res.Message = "평가 세트 '" + name + "' 를 실행하지 못했습니다. 그래도 켜시겠습니까?"
	case v.GatePassed:
		return nil, nil
	default:
		res.PassRatePct, res.ThresholdPct, res.CriticalFails = v.PassRatePct, v.ThresholdPct, v.CriticalFails
		res.Message = fmt.Sprintf("평가 세트 결과가 기준 미달입니다(합격률 %d%%, 합격선 %d%%, 치명 실패 %d건). 그래도 켜시겠습니까?", v.PassRatePct, v.ThresholdPct, v.CriticalFails)
	}
	if !opts.EvalOverride {
		return nil, &EvalGateError{Result: res}
	}
	if strings.TrimSpace(opts.Reason) == "" {
		return nil, valErr("평가 세트 결과가 기준 미달입니다. 그래도 켜려면 사유를 입력하세요")
	}
	return &res, nil
}

func overrideEntry(p contract.Principal, d Detail, r EvalResult, reason string) audit.Entry {
	given := "없음"
	if strings.TrimSpace(reason) != "" {
		given = "있음"
	}
	return audit.Entry{Actor: p, Action: "mcptool.eval_override", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "set_code", New: r.SetCode}, {Key: "pass_rate_pct", New: strconv.Itoa(r.PassRatePct)}, {Key: "reason_given", New: given}}}
}

// SetEvalLink 는 도구에 B46 평가 세트(`tool` 용도)를 연결하거나(setCode) 해제한다("")(mcptool:manage). 연결하면 켜기 전에 그 세트를 돌려 합격을 확인한다.
func (s *Service) SetEvalLink(ctx context.Context, id int64, setCode string) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	setCode = strings.TrimSpace(setCode)
	var setID int64
	if setCode != "" {
		var purpose string
		if err := s.o.DB.QueryRowContext(ctx, `SELECT id, purpose FROM eval_sets WHERE code = ?`, setCode).Scan(&setID, &purpose); errors.Is(err, sql.ErrNoRows) {
			return Detail{}, valErr("평가 세트를 찾을 수 없습니다")
		} else if err != nil {
			return Detail{}, err
		}
		if purpose != evalset.PurposeTool {
			return Detail{}, valErr("도구 선택(tool) 용도의 평가 세트만 연결할 수 있습니다")
		}
	}
	if d.EvalSetID == setID {
		return Detail{}, stateErr("이미 같은 연결입니다.")
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET eval_set_id = ?, updated_at = ? WHERE id = ?`, setID, s.now(), id); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.eval_link", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "set_code", New: setCode}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// EnableRequestView 는 진행 중인 켜기 요청이다(초안 도구).
type EnableRequestView struct {
	ID          int64  `json:"id"`
	Version     int    `json:"version"`
	RequestedBy string `json:"requestedBy"`
	RequestedID int64  `json:"requestedId"`
	RequestedAt string `json:"requestedAt"`
}

func (s *Service) pending(ctx context.Context, toolID int64) *EnableRequestView {
	var r EnableRequestView
	err := s.o.DB.QueryRowContext(ctx, `SELECT r.id, r.version, COALESCE(u.name, ''), COALESCE(r.requested_by, 0), r.requested_at FROM mcp_tool_enable_requests r LEFT JOIN users u ON u.id = r.requested_by WHERE r.tool_id = ? AND r.status = 'pending'`, toolID).
		Scan(&r.ID, &r.Version, &r.RequestedBy, &r.RequestedID, &r.RequestedAt)
	if err != nil {
		return nil
	}
	return &r
}

// RequestEnable 은 초안(쓰기) 도구의 켜기 요청을 만든다(mcptool:manage). 현재 버전의 시험 실행이 성공했고 깨지지 않았으며 아직 꺼져 있어야 한다. 도구마다 진행 중 요청은 하나다.
func (s *Service) RequestEnable(ctx context.Context, id int64) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	switch {
	case d.Kind != "draft":
		return Detail{}, stateErr("조회 도구는 요청 없이 바로 켭니다.")
	case d.Enabled:
		return Detail{}, stateErr("이미 켜져 있습니다.")
	case d.Broken:
		return Detail{}, stateErr("정의가 지금의 자원 목록과 맞지 않아 켤 수 없습니다. 정의를 고쳐 새 버전으로 저장하세요.")
	case !d.Tested:
		return Detail{}, stateErr("현재 버전의 시험 실행이 아직 성공하지 않았습니다. 시험 실행을 한 번 성공시킨 뒤 요청하세요.")
	case d.Pending != nil:
		return Detail{}, stateErr("이미 다른 관리자의 확인을 기다리는 요청이 있습니다.")
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := dbutil.InsertID(ctx, tx, `INSERT INTO mcp_tool_enable_requests (tool_id, version, requested_by, requested_at, status) VALUES (?,?,?,?, 'pending')`, id, d.CurrentVersion, nullID(p.UserID), s.now()); e != nil {
			if dbutil.IsUnique(e) {
				return stateErr("이미 다른 관리자의 확인을 기다리는 요청이 있습니다.")
			}
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.enable_request", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "tool_version", New: strconv.Itoa(d.CurrentVersion)}, {Key: "requester_id", New: strconv.FormatInt(p.UserID, 10)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// ConfirmEnable 은 다른 관리자의 켜기 요청을 확인해 도구를 켠다(mcptool:manage). 요청자와 같은 사람은 확인할 수 없다.
// 요청한 버전이 아직 현재 버전이어야 하고(정의가 바뀌면 요청은 취소된다), 연결된 평가 세트가 있으면 합격해야 한다(미달이면 사유와 함께 opts).
func (s *Service) ConfirmEnable(ctx context.Context, id int64, opts EnableOpts) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if d.Pending == nil {
		return Detail{}, stateErr("확인할 켜기 요청이 없습니다.")
	}
	if d.Pending.RequestedID == p.UserID {
		return Detail{}, stateErr("요청한 사람은 직접 확인할 수 없습니다. 다른 관리자가 확인해야 합니다.")
	}
	if d.Pending.Version != d.CurrentVersion {
		return Detail{}, stateErr("요청한 버전이 현재 버전과 다릅니다. 요청을 취소하고 다시 요청하세요.")
	}
	if d.Broken {
		return Detail{}, stateErr("정의가 지금의 자원 목록과 맞지 않아 켤 수 없습니다.")
	}
	ov, err := s.checkEval(ctx, p, d, opts)
	if err != nil {
		return Detail{}, err
	}
	var on int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_tools WHERE enabled = 1`).Scan(&on); err != nil {
		return Detail{}, err
	}
	if on >= MaxEnabled {
		return Detail{}, stateErr("켠 도구는 " + strconv.Itoa(MaxEnabled) + "개까지입니다. 쓰지 않는 도구를 먼저 끄세요.")
	}
	now := s.now()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tool_enable_requests SET status = 'confirmed', confirmed_by = ?, resolved_at = ? WHERE id = ? AND status = 'pending'`, nullID(p.UserID), now, d.Pending.ID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET enabled = 1, updated_at = ? WHERE id = ?`, now, id); e != nil {
			return e
		}
		if ov != nil {
			if e := s.o.Audit.Record(ctx, tx, overrideEntry(p, d, *ov, opts.Reason)); e != nil {
				return e
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.enable_confirm", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "tool_version", New: strconv.Itoa(d.CurrentVersion)},
				{Key: "requester_id", New: strconv.FormatInt(d.Pending.RequestedID, 10)}, {Key: "confirmer_id", New: strconv.FormatInt(p.UserID, 10)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// CancelEnable 은 진행 중인 켜기 요청을 거둔다(mcptool:manage). 요청자 본인은 취소, 다른 관리자는 반려다(둘 다 요청을 끝낸다).
func (s *Service) CancelEnable(ctx context.Context, id int64) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if d.Pending == nil {
		return Detail{}, stateErr("취소할 켜기 요청이 없습니다.")
	}
	status := "rejected"
	if d.Pending.RequestedID == p.UserID {
		status = "canceled"
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tool_enable_requests SET status = ?, resolved_at = ? WHERE id = ? AND status = 'pending'`, status, s.now(), d.Pending.ID); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.enable_cancel", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "tool_version", New: strconv.Itoa(d.Pending.Version)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// resetDraftOnChange 는 초안 도구의 정의가 바뀔 때(새 버전·되돌리기) 진행 중인 켜기 요청을 취소하고, 켜져 있던 도구는 끈다(명세 14절: 새 버전은 다시 확인받아야 켠다).
func (s *Service) resetDraftOnChange(ctx context.Context, tx contract.Tx, p contract.Principal, id int64, name, kind string) error {
	if kind != "draft" {
		return nil
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_tool_enable_requests SET status = 'canceled', resolved_at = ? WHERE tool_id = ? AND status = 'pending'`, now, id); err != nil {
		return err
	}
	var en int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM mcp_tools WHERE id = ?`, id).Scan(&en); err != nil {
		return err
	}
	if en == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE mcp_tools SET enabled = 0 WHERE id = ?`, id); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.disable", TargetType: audit.TargetMCPTool, TargetID: name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: name}, {Key: "tool_version", New: "정의 변경으로 꺼짐"}}})
	}
	return nil
}

// ---- 내보내기·가져오기(JSON, 명세 13절) ----

const (
	ExportFormat   = "pl-mcp-tools/1"
	MaxImportBytes = 1 << 20
)

// Export 는 모든 도구의 현재 정의를 JSON 으로 낸다(mcptool:manage). 정의만 있고 번호·사용자·비밀·감사 값은 없다.
func (s *Service) Export(ctx context.Context) ([]byte, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT v.definition_json FROM mcp_tools t JOIN mcp_tool_versions v ON v.tool_id = t.id AND v.version = t.current_version ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	var defs []json.RawMessage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		defs = append(defs, json.RawMessage(raw))
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if defs == nil {
		defs = []json.RawMessage{}
	}
	out, err := json.MarshalIndent(map[string]any{"format": ExportFormat, "tools": defs}, "", "  ")
	if err != nil {
		return nil, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.export", TargetType: audit.TargetMCPTool, TargetID: "export", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_count", New: strconv.Itoa(len(defs))}}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ImportItem 은 가져오기 결과의 도구 한 건이다.
type ImportItem struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ImportResult 는 가져오기 결과표다.
type ImportResult struct {
	Imported int          `json:"imported"`
	Rejected int          `json:"rejected"`
	Items    []ImportItem `json:"items"`
}

// Import 는 JSON 파일을 읽어 도구마다 같은 검증을 하고 통과한 것만 꺼진 새 도구(v1)로 만든다(mcptool:manage). 이름이 이미 있으면 거부한다(덮어쓰기 없음).
// 한 도구가 실패해도 나머지는 계속한다. 파일명·내용은 감사에 남기지 않고 개수만 남긴다.
func (s *Service) Import(ctx context.Context, data []byte) (ImportResult, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return ImportResult{}, err
	}
	if len(data) > MaxImportBytes {
		return ImportResult{}, valErr("파일이 너무 큽니다(1MB 까지)")
	}
	var file struct {
		Format string            `json:"format"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Format != ExportFormat {
		return ImportResult{}, valErr("도구 정의 파일 형식이 올바르지 않습니다(%s)", ExportFormat)
	}
	if len(file.Tools) == 0 || len(file.Tools) > MaxTools {
		return ImportResult{}, valErr("도구는 1~%d개여야 합니다", MaxTools)
	}
	res := ImportResult{Items: []ImportItem{}}
	for _, raw := range file.Tools {
		item := ImportItem{}
		def, verr := ParseAndValidate(raw, s.o.Catalog)
		item.Name = def.Name
		if verr != nil {
			item.Message = verr.Error()
			res.Items, res.Rejected = append(res.Items, item), res.Rejected+1
			continue
		}
		_, e := s.Save(ctx, SaveInput{Title: def.Name, Definition: raw, Note: "파일에서 가져옴"})
		switch {
		case e == nil:
			item.OK, item.Message = true, "꺼진 도구로 만들었습니다"
			res.Imported++
		case errors.Is(e, contract.ErrValidation) && strings.Contains(e.Error(), "이미 있는 도구 이름"):
			item.Message = "이름 충돌: 같은 이름의 도구가 이미 있습니다"
			res.Rejected++
		default:
			var ve *ValidationError
			if errors.As(e, &ve) || errors.Is(e, contract.ErrValidation) || errors.Is(e, contract.ErrInvalidState) {
				item.Message = e.Error()
				res.Rejected++
			} else {
				return res, e
			}
		}
		res.Items = append(res.Items, item)
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.import", TargetType: audit.TargetMCPTool, TargetID: "import", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_count", New: strconv.Itoa(res.Imported)}, {Key: "rejected_count", New: strconv.Itoa(res.Rejected)}}})
	})
	return res, err
}

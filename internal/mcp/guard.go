package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
	"github.com/choibongseok/procure-ledger/internal/mcpusage"
)

// rateChecker 는 사용자 합산 한도까지 아는 한도기다(mcpusage.RateLimiter). 옛 deps.MCPRateLimiter 만 구현한 한도기(테스트 가짜 등)는
// 토큰별 한도로만 쓴다.
type rateChecker interface {
	Check(ctx context.Context, c mcpusage.Call) (mcpusage.Decision, error)
	SettleDue(ctx context.Context)
	Enabled() bool
}

// checkRate 는 이번 호출을 한도에 넣는다. active 는 판정할 한도가 있다는 뜻이다.
func checkRate(ctx context.Context, lim deps.MCPRateLimiter, p contract.Principal) (dec mcpusage.Decision, active bool, err error) {
	if lim == nil {
		return mcpusage.Decision{Allowed: true}, false, nil
	}
	if rc, ok := lim.(rateChecker); ok {
		if !rc.Enabled() {
			return mcpusage.Decision{Allowed: true}, false, nil
		}
		rc.SettleDue(ctx) // 지난 창의 거부 요약을 분당 한 번 정산한다. 실패해도 한도 판정과 무관하다
		dec, err = rc.Check(ctx, mcpusage.Call{TokenID: p.TokenID, UserID: p.UserID})
		return dec, true, err
	}
	if lim.Limit() <= 0 {
		return mcpusage.Decision{Allowed: true}, false, nil
	}
	ok, err := lim.Allow(ctx, p.TokenID)
	return mcpusage.Decision{Allowed: ok, Scope: mcpusage.ScopeToken, Limit: lim.Limit(), Denied: 1}, true, err
}

// toolTarget 은 감사 로그의 대상 종류·ID 다. 인자 해석에 실패해도 호출을 막지 않는다.
func toolTarget(name string, args json.RawMessage) (audit.TargetType, string) {
	if strings.HasPrefix(name, "custom_") {
		return audit.TargetMCPTool, name // 사용자 정의 도구(B48): 입력값은 감사에 남기지 않는다
	}
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	num := func(k string) string {
		if f, ok := m[k].(float64); ok {
			return strconv.FormatInt(int64(f), 10)
		}
		return "-"
	}
	switch name {
	case contract.ToolSearchItems:
		return audit.TargetItem, "search"
	case contract.ToolGetStock:
		return audit.TargetItem, num("item_id")
	case contract.ToolListPendingApprovals:
		return audit.TargetApproval, "pending"
	case contract.ToolGetPurchaseRequest:
		return audit.TargetPurchaseRequest, num("pr_id")
	case contract.ToolCreatePRDraft:
		return audit.TargetPurchaseRequest, "new"
	case contract.ToolGetPayables:
		return audit.TargetVendor, "payables" // 기준일·거래처 검색어는 감사에 남기지 않는다
	case contract.ToolGetTaxInvoices:
		return audit.TargetTaxInvoice, "search" // 조회 조건·결과는 감사에 남기지 않는다
	case contract.ToolGetCardUnreconciled:
		return audit.TargetCard, "unreconciled" // 건수·내역은 남기지 않는다
	case contract.ToolGetMonthCloseStatus:
		// 월 값은 감사에 남긴다(상태 조회의 대상은 "어느 달"이다). 형식이 틀리거나 비었으면 status 만.
		if s, ok := m["month"].(string); ok && len(strings.TrimSpace(s)) == 7 {
			return audit.TargetMonthClose, "status:" + strings.TrimSpace(s)
		}
		return audit.TargetMonthClose, "status"
	case contract.ToolGetAICostSummary:
		return audit.TargetAICost, "summary" // 월·부서·금액은 감사에 남기지 않는다
	case contract.ToolGetBudgetStatus:
		return audit.TargetBudget, "status" // 부서·월·금액은 감사에 남기지 않는다
	case contract.ToolGetItemLedger:
		return audit.TargetItem, "ledger:" + num("item_id") // 창고·로트·기간은 감사에 남기지 않는다
	case contract.ToolListStockAlerts:
		return audit.TargetItem, "alerts"
	case contract.ToolDraftReorder:
		return audit.TargetPurchaseRequest, "new"
	case contract.ToolSearchPolicy:
		return audit.TargetPolicy, "search"
	case contract.ToolSearchKnowledge:
		return audit.TargetKnowledge, "search" // 검색어는 감사에 남기지 않는다(서비스의 knowledge.search 가 마스킹본을 남긴다)
	case contract.ToolApproveOrReject:
		return audit.TargetApproval, num("pr_id")
	case contract.ToolVerifyDocument:
		return audit.TargetDocument, "po:" + num("po_id")
	case contract.ToolListApprovalForms:
		// 양식 코드·이름은 감사에 남기지 않는다(목록이든 입력 칸 조회든 대상은 "양식 조회" 하나).
		return audit.TargetApprovalForm, "list"
	case contract.ToolGetApprovalDoc:
		return audit.TargetApprovalDoc, num("doc_id")
	case contract.ToolSearchPeople:
		return audit.TargetPerson, "search" // 검색어·이름은 감사에 남기지 않는다
	case contract.ToolGetOrgUnit:
		// 숫자 조직 번호를 줬을 때만 그 번호를 남기고, 이름·코드는 남기지 않는다.
		if s, ok := m["unit"].(string); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
				return audit.TargetOrgUnit, strconv.FormatInt(n, 10)
			}
		}
		return audit.TargetOrgUnit, "search"
	case contract.ToolWhoApproves:
		// 미리보기는 문서를 만들지 않으므로 양식 문서도 approval_doc 이 아니라 사람(요청자) 대상이다.
		return audit.TargetPerson, whoApprovesTarget(m)
	case contract.ToolGetMyLeaveBalance:
		return audit.TargetLeaveRequest, "balance" // 본인 잔여 조회: 값은 감사에 남기지 않는다
	case contract.ToolListTeamAbsence:
		return audit.TargetAttendanceRecord, "absence" // 기간·조직·결과는 감사에 남기지 않는다
	case contract.ToolListMyApprovalDocs:
		// 함·탭·쪽 위치는 감사에 남기지 않는다.
		return audit.TargetApprovalDoc, "list"
	}
	return audit.TargetItem, "-"
}

// ErrRateLimited 는 개인 토큰의 분당 호출 한도를 넘었을 때의 오류다(B13).
var ErrRateLimited = errors.New("호출 한도를 넘었습니다")

// terminalDiff 는 호출 종료 기록(B13 사용량 집계가 세는 기록)에 싣는 항목이다: 소요 시간과, 개인 토큰이면 토큰 번호.
// 감사 해시 규칙은 그대로이고 Diff 항목으로만 담는다. 쓰기 도구의 "requested" 기록에는 붙이지 않는다(호출 1건 = 종료 기록 1건).
func terminalDiff(p contract.Principal, start time.Time) []audit.DiffItem {
	d := []audit.DiffItem{{Key: audit.DiffKeyDurationMS, New: strconv.FormatInt(time.Since(start).Milliseconds(), 10)}}
	if p.TokenID != 0 {
		d = append(d, audit.DiffItem{Key: audit.DiffKeyTokenID, New: strconv.FormatInt(p.TokenID, 10)})
	}
	return d
}

func errorResult(err error) *sdk.CallToolResult {
	r := &sdk.CallToolResult{}
	r.SetError(err)
	return r
}

// resolver 는 이번 호출의 사용자를 돌려준다. HTTP 는 요청마다 만든 서버에 고정된 Principal 이고,
// stdio 는 호출마다 토큰을 다시 검증한다(폐기·역할 변경 반영).
type resolver func(ctx context.Context) (contract.Principal, error)

func fixed(p contract.Principal) resolver {
	return func(context.Context) (contract.Principal, error) { return p, nil }
}

// writeTools 는 저장소에 쓰는 도구다. 이 도구들은 실행 전에 호출 감사를 먼저 남기고(실패하면 실행하지 않는다),
// 결과는 실행 뒤 별도 기록(<도구>.result)으로 덧붙인다. 결과 기록이 실패해도 이미 반영된 쓰기를 실패로 돌려주지 않는다
// (실패로 보이면 재시도로 중복 생성이 생긴다). 쓰기 자체의 감사는 서비스가 같은 트랜잭션으로 남긴다.
var writeTools = map[string]bool{
	contract.ToolCreatePRDraft:   true,
	contract.ToolDraftReorder:    true,
	contract.ToolApproveOrReject: true,
	contract.ToolVerifyDocument:  true,
}

// guard 는 tools/call 마다 (1) 사용자 확인 (2) 권한을 서비스보다 먼저 확인하고 (3) 감사 로그에 남기는 수신 미들웨어다.
// 권한은 웹과 같은 표(contract.MCPToolPermissions → RolePermissions)로만 판정한다.
func guard(d deps.Deps, resolve resolver, table *contract.ToolTable) sdk.Middleware {
	return guardWith(d, resolve, table, nil)
}

// guardWith 는 guard 에 켜진 사용자 정의 도구(B48)의 가드 정보를 더한 것이다. custom 이 비면 guard 와 같다.
func guardWith(d deps.Deps, resolve resolver, table *contract.ToolTable, custom map[string]customMeta) sdk.Middleware {
	perms := map[string]contract.Permission{}
	for _, t := range table.Tools() {
		perms[t.Name] = t.Permission
	}
	record := func(ctx context.Context, p contract.Principal, action string, name string, res audit.Result, detail string, args json.RawMessage, diff []audit.DiffItem) error {
		tt, tid := toolTarget(name, args)
		p.Channel = contract.ChannelMCP
		return d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return d.Audit.Record(ctx, tx, audit.Entry{
				Actor: p, Action: action, TargetType: tt, TargetID: tid, Result: res, Detail: detail, Diff: diff,
			})
		})
	}
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/list" {
				res, err := next(ctx, method, req)
				if lr, ok := res.(*sdk.ListToolsResult); ok && err == nil {
					if len(custom) > 0 {
						filterCustom(ctx, lr, resolve, custom)
					}
					filterModuleTools(ctx, lr, d) // 꺼진 업무 모듈(근태·휴가)의 도구는 목록에서 뺀다
					if multiEntity(ctx, d) {
						addEntityProperty(lr)
					}
				}
				return res, err
			}
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			params, ok := req.GetParams().(*sdk.CallToolParamsRaw)
			if !ok {
				return nil, errors.New("잘못된 tools/call 요청")
			}
			perm, known := perms[params.Name]
			cmeta, isCustom := custom[params.Name]
			if !known && isCustom {
				perm, known = cmeta.Perm, true
			}
			if !known {
				return next(ctx, method, req) // 알 수 없는 도구는 SDK 가 오류로 돌려준다
			}
			start := time.Now()
			p, perr := resolve(ctx)
			if perr != nil || p.UserID == 0 {
				// 토큰이 폐기·만료됐다. 사용자를 알 수 없으므로 UserID 0 으로 거부를 기록한다.
				if err := record(ctx, contract.Principal{}, "mcp."+params.Name, params.Name, audit.ResultDenied, "tool="+params.Name+" auth=invalid", params.Arguments, terminalDiff(contract.Principal{}, start)); err != nil {
					return errorResult(contract.ErrAuditFailed), nil
				}
				return errorResult(fmt.Errorf("%w: 토큰이 더 이상 유효하지 않습니다", contract.ErrUnauthenticated)), nil
			}
			p.Channel = contract.ChannelMCP
			// 법인 컨텍스트(B44 12절): 도구 실행과 호출 감사가 같은 현재 법인으로 일한다. 1단계는 토큰 주인의 소속 법인(없으면 기본 법인)이다 — 웹 요청이 헤더·세션 값이 없을 때 정하는 값과 같다.
			// 토큰·입력 인자로 법인을 바꾸는 길(entity 인자, 토큰의 법인 축)과 업무 데이터의 법인 거름은 2~3단계다.
			active, entArgs, eerr := pickEntity(ctx, d, p, params.Arguments)
			if eerr != nil {
				if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultDenied, "tool="+params.Name+" entity_out_of_scope", params.Arguments, terminalDiff(p, start)); err != nil {
					return errorResult(contract.ErrAuditFailed), nil
				}
				return errorResult(eerr), nil
			}
			params.Arguments = entArgs // 도구는 entity 인자를 모른다(여기서 걷어 낸다)
			ctx = contract.WithActiveEntity(ctx, active)
			// 분당 한도(B13 토큰별, B17 사용자별 합산). 도구를 실행하기 전에, 권한 판정보다 먼저 DB 기준으로 센다
			// (서버 /mcp 와 erp-mcp 가 같은 DB 를 써도 맞다). 한도를 넘은 호출은 카운트를 올리지 않으므로 거부 기록이 한도를 다시 소모하지 않는다.
			// 거부 감사는 토큰·분 창의 첫 거부만 바로 남기고, 이어지는 거부는 창 단위 요약(mcpusage.RateLimiter.Settle)이 센다.
			if p.TokenID != 0 {
				dec, active, rerr := checkRate(ctx, d.MCPRate, p)
				if rerr != nil {
					// 세지 못하면 열어 두지 않는다(한도가 조용히 풀리지 않게).
					if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultError, "tool="+params.Name+" rate_check_failed", params.Arguments, terminalDiff(p, start)); err != nil {
						return errorResult(contract.ErrAuditFailed), nil
					}
					return errorResult(errors.New("호출 한도를 확인하지 못해 요청을 처리하지 않았습니다. 잠시 뒤 다시 시도하세요")), nil
				}
				if active && !dec.Allowed {
					if dec.Denied <= 1 {
						detail := fmt.Sprintf("tool=%s rate_limited scope=%s limit=%d", params.Name, dec.Scope, dec.Limit)
						if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultDenied, detail, params.Arguments, terminalDiff(p, start)); err != nil {
							return errorResult(contract.ErrAuditFailed), nil
						}
					}
					who := "개인 토큰 하나로"
					if dec.Scope == mcpusage.ScopeUser {
						who = "한 사용자가 모든 개인 토큰을 합쳐"
					}
					return errorResult(fmt.Errorf("%w: %s 1분에 %d번까지 호출할 수 있습니다. 잠시 뒤 다시 시도하세요", ErrRateLimited, who, dec.Limit)), nil
				}
			}
			if isCustom && p.Can(perm) && !cmeta.allows(p) {
				// 자원 권한은 있지만 관리자가 정의에서 부를 수 있는 역할을 좁혀 둔 도구다. 권한 부족과 문구·감사 사유를 구분한다.
				detail := fmt.Sprintf("tool=%s role_restricted roles=%s", params.Name, strings.Join(cmeta.Roles, ","))
				if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultDenied, detail, params.Arguments, terminalDiff(p, start)); err != nil {
					return errorResult(contract.ErrAuditFailed), nil
				}
				return errorResult(fmt.Errorf("%w: 이 도구는 관리자가 정한 역할(%s)만 쓸 수 있습니다", contract.ErrForbidden, cmeta.roleNames())), nil
			}
			if !p.Can(perm) {
				detail := fmt.Sprintf("tool=%s required=%s", params.Name, perm)
				if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultDenied, detail, params.Arguments, terminalDiff(p, start)); err != nil {
					return errorResult(contract.ErrAuditFailed), nil
				}
				return errorResult(fmt.Errorf("%w: %s 도구는 %s 권한이 필요합니다", contract.ErrForbidden, params.Name, perm)), nil
			}
			if isCustom {
				// 도구별 사용자 분당 한도(B48). 권한이 있는 호출만 센다. 한도를 넘은 호출은 호출 수를 올리지 않는다.
				ok, rerr := mcptool.AllowCall(ctx, d.Txs, cmeta.ToolID, p.UserID, cmeta.Rate, time.Now())
				if rerr != nil {
					if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultError, "tool="+params.Name+" rate_check_failed", params.Arguments, terminalDiff(p, start)); err != nil {
						return errorResult(contract.ErrAuditFailed), nil
					}
					return errorResult(errors.New("호출 한도를 확인하지 못해 요청을 처리하지 않았습니다. 잠시 뒤 다시 시도하세요")), nil
				}
				if !ok {
					detail := fmt.Sprintf("tool=%s rate_limited scope=tool limit=%d", params.Name, cmeta.Rate)
					if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultDenied, detail, params.Arguments, append(terminalDiff(p, start), audit.DiffItem{Key: "tool_version", New: strconv.Itoa(cmeta.Version)})); err != nil {
						return errorResult(contract.ErrAuditFailed), nil
					}
					return errorResult(fmt.Errorf("%w: 이 도구는 한 사용자가 1분에 %d번까지 호출할 수 있습니다. 잠시 뒤 다시 시도하세요", ErrRateLimited, cmeta.Rate)), nil
				}
			}
			write := writeTools[params.Name] || (isCustom && cmeta.Kind == "draft") // 초안 도구는 쓰기 도구다: 실행 전에 호출 감사를 먼저 남긴다
			if write {
				// 쓰기 도구: 호출 감사를 먼저 남기고, 실패하면 실행하지 않는다.
				if err := record(ctx, p, "mcp."+params.Name, params.Name, audit.ResultSuccess, "tool="+params.Name+" requested", params.Arguments, nil); err != nil {
					return errorResult(contract.ErrAuditFailed), nil
				}
			}
			res, err := next(contract.WithPrincipal(ctx, p), method, req)
			if cr, ok := res.(*sdk.CallToolResult); err == nil && ok && cr != nil && !cr.IsError {
				tagEntity(ctx, d, cr, active) // 법인이 둘 이상인 설치에서만 응답에 법인 코드가 붙는다
			}
			// 원문 감시(B41 D6): 나가는 결과에 가려지지 않은 개인정보 패턴이 있으면 거부한다(쓰기 도구는 이미 반영됐으므로 글만 가린다).
			anomaly := false
			if cr, ok := res.(*sdk.CallToolResult); err == nil && ok && cr != nil {
				if fields := exposedFields(d.Masker, cr); len(fields) > 0 {
					anomaly = true
					noteExposure(ctx, d.Privacy, p, params.Name, fields)
					if write {
						scrub(d.Masker, cr)
					} else {
						res = errorResult(ErrExposure)
					}
				}
			}
			outcome, detail := audit.ResultSuccess, "tool="+params.Name
			if err != nil {
				outcome, detail = audit.ResultError, "tool="+params.Name+" protocol_error"
			} else if cr, ok := res.(*sdk.CallToolResult); ok && cr != nil && cr.IsError {
				outcome = audit.ResultError
				if e := cr.GetError(); e != nil && errors.Is(e, contract.ErrForbidden) {
					outcome = audit.ResultDenied
				}
				detail = "tool=" + params.Name + " failed"
				if anomaly {
					detail = "tool=" + params.Name + " mcp_anomaly"
				}
				if outcome == audit.ResultDenied && params.Name == contract.ToolWhoApproves {
					detail = "tool=who_approves out_of_scope" // 열람 범위 밖 결재선 질의
				}
			}
			diff := terminalDiff(p, start)
			if isCustom {
				diff = append(diff, audit.DiffItem{Key: "tool_version", New: strconv.Itoa(cmeta.Version)})
				if n, ok := customCount(res); ok {
					diff = append(diff, audit.DiffItem{Key: "result_count", New: strconv.Itoa(n)})
				}
			}
			if write {
				// 결과 덧붙임: 실패해도 이미 반영된 쓰기를 실패로 바꾸지 않는다.
				_ = record(ctx, p, "mcp."+params.Name+".result", params.Name, outcome, detail, params.Arguments, diff)
				return res, err
			}
			if aerr := record(ctx, p, "mcp."+params.Name, params.Name, outcome, detail, params.Arguments, diff); aerr != nil {
				return errorResult(contract.ErrAuditFailed), nil
			}
			return res, err
		}
	}
}

// activeEntityFor 는 MCP 호출의 현재 법인이다: 소속 법인 중 첫째, 없으면 기본 법인(웹의 기본값과 같다, internal/auth.resolveEntity).
func activeEntityFor(p contract.Principal) contract.ActiveEntity {
	if len(p.OwnEntities) > 0 {
		return contract.ActiveEntity{ID: p.OwnEntities[0]}
	}
	return contract.ActiveEntity{ID: contract.DefaultEntityID}
}

// multiEntity 는 활성 법인이 둘 이상인 설치인지다. 법인이 하나뿐이면 도구의 입력·출력은 이전과 같다(B44 동작 불변).
func multiEntity(ctx context.Context, d deps.Deps) bool {
	if d.DB == nil {
		return false
	}
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE active = 1`).Scan(&n); err != nil {
		return false
	}
	return n > 1
}

// pickEntity 는 호출의 현재 법인을 정한다. 입력 `entity`(법인 코드, 선택)가 있으면 호출자가 고를 수 있는 법인일 때만 그 법인이고, 아니면 소속 법인(없으면 기본 법인)이다.
// 범위 밖이거나 없는 코드는 같은 오류다(존재 여부를 알리지 않는다). 걷어 낸 입력 인자를 함께 돌려준다.
func pickEntity(ctx context.Context, d deps.Deps, p contract.Principal, args json.RawMessage) (contract.ActiveEntity, json.RawMessage, error) {
	def := activeEntityFor(p)
	if len(args) == 0 {
		return def, args, nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return def, args, nil
	}
	raw, has := m["entity"]
	if !has {
		return def, args, nil
	}
	delete(m, "entity")
	rest, _ := json.Marshal(m)
	var code string
	if json.Unmarshal(raw, &code) != nil {
		return def, rest, errEntityOut
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return def, rest, nil
	}
	if d.DB == nil {
		return def, rest, errEntityOut
	}
	var id int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ? AND active = 1`, code).Scan(&id); err != nil || !p.SelectableEntities().Has(id) {
		return def, rest, errEntityOut
	}
	return contract.ActiveEntity{ID: id}, rest, nil
}

var errEntityOut = fmt.Errorf("%w: 찾을 수 없는 법인입니다", contract.ErrNotFound)

// tagEntity 는 법인이 둘 이상인 설치에서 JSON 객체 응답에 법인 코드를 붙인다.
func tagEntity(ctx context.Context, d deps.Deps, cr *sdk.CallToolResult, a contract.ActiveEntity) {
	if d.DB == nil || !multiEntity(ctx, d) || a.ID <= 0 {
		return
	}
	var code string
	if d.DB.QueryRowContext(ctx, `SELECT code FROM entities WHERE id = ?`, a.ID).Scan(&code) != nil {
		return
	}
	for _, c := range cr.Content {
		tc, ok := c.(*sdk.TextContent)
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(tc.Text), &m) != nil {
			// JSON 배열 응답은 {"entity":코드,"items":[...]} 로 감싼다(법인이 둘 이상인 설치만). 객체도 배열도 아닌 글은 그대로 둔다.
			var arr []any
			if json.Unmarshal([]byte(tc.Text), &arr) == nil {
				if b, err := json.Marshal(map[string]any{"entity": code, "items": arr}); err == nil {
					tc.Text = string(b)
				}
			}
			return
		}
		if m == nil { // JSON null
			return
		}
		m["entity"] = code
		if b, err := json.Marshal(m); err == nil {
			tc.Text = string(b)
		}
		return
	}
}

// addEntityProperty 는 tools/list 의 입력 스키마에 선택 인자 entity 를 더한다(법인이 둘 이상인 설치만).
func addEntityProperty(lr *sdk.ListToolsResult) {
	for _, t := range lr.Tools {
		b, err := json.Marshal(t.InputSchema)
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m == nil { // 입력 스키마가 없는 도구도 entity 인자는 받을 수 있게 빈 객체 스키마를 만든다
			m = map[string]any{"type": "object"}
		}
		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		props["entity"] = map[string]any{"type": "string", "description": "법인 코드(선택). 비우면 내 소속 법인입니다. 내가 고를 수 있는 법인만 됩니다."}
		m["properties"] = props
		t.InputSchema = m
	}
}

// filterCustom 은 tools/list 에서 호출자가 부를 수 없는 사용자 정의 도구를 뺀다(권한 없는 도구는 이름도 모른다). 호출자를 확인하지 못하면 사용자 정의 도구는 모두 뺀다.
func filterCustom(ctx context.Context, lr *sdk.ListToolsResult, resolve resolver, custom map[string]customMeta) {
	p, err := resolve(ctx)
	kept := lr.Tools[:0:0]
	for _, t := range lr.Tools {
		m, isCustom := custom[t.Name]
		if isCustom && (err != nil || p.UserID == 0 || !m.allows(p)) {
			continue
		}
		kept = append(kept, t)
	}
	lr.Tools = kept
}

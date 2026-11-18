package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B22 who_approves( 3.4): 결재선 미리보기. 읽기 전용이고 저장·상신하지 않는다.
// 범위 판정과 결재선 계산은 모두 LineService.PreviewLine 이 한다(웹 상신 화면과 같은 코드). 도구는 입력을 풀고 출력을 옮길 뿐이다.

const (
	approverNote     = "미리보기입니다. 실제 상신·결재선 확정은 하지 않았습니다. 상신 시점의 조직에 따라 달라질 수 있습니다. " + orgNote
	approverMaxSteps = 12
	approverMaxRecip = 20
	purchaseCode     = "PUR"
)

var (
	errAmountNeeded = errors.New("금액(원)을 알려 주세요.")
	errCoopUnit     = errors.New("협조 요청 부서를 알려 주세요.")
	errNoRequester  = errors.New("요청자를 찾을 수 없습니다.")
)

type whoApprovesIn struct {
	FormCode      string `json:"form_code,omitempty" jsonschema:"양식 코드(PUR 구매 품의서·GEN 일반 기안서·EXP 지출 결의서·COOP 업무 협조전) 또는 이름. 비우면 구매 품의서"`
	Amount        int64  `json:"amount,omitempty" jsonschema:"합계 금액(원). 구매 품의서·금액 구간 양식은 필수, 금액 칸이 없는 양식은 생략"`
	FieldUnit     string `json:"field_unit,omitempty" jsonschema:"협조 요청 부서(조직 코드 또는 이름). 업무 협조전은 필수"`
	RequesterID   int64  `json:"requester_id,omitempty" jsonschema:"요청자 사용자 번호(비우면 본인). 구매 품의서에서만"`
	RequesterName string `json:"requester_name,omitempty" jsonschema:"번호 대신 요청자 이름(구매 품의서에서만)"`
}

type approverOut struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Rank   string `json:"rank,omitempty"`
}

var roleLabels = map[string]string{"approve": "결재", "agree": "합의"}

// resolveForm 은 양식 이름·코드를 코드로 바꾼다. 사용할 수 있는 양식 목록에 없으면 입력을 그대로 서비스에 넘긴다(서비스가 안내 문구를 준다).
func resolveForm(ctx context.Context, d deps.Deps, text string) (code, name string, amountForm bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(text, purchaseCode) || text == "구매 품의서" {
		return purchaseCode, "구매 품의서", true
	}
	code = strings.ToUpper(text)
	if d.Forms == nil {
		return code, "", false
	}
	if forms, err := d.Forms.ListUsable(ctx); err == nil {
		for _, f := range forms {
			if strings.EqualFold(f.Code, text) || f.Name == text {
				code, name = f.Code, f.Name
				break
			}
		}
	}
	if code == purchaseCode {
		return purchaseCode, "구매 품의서", true
	}
	if det, err := d.Forms.Get(ctx, code); err == nil {
		name = det.Name
		amountForm = det.Definition.Settings.AmountField != ""
	}
	return code, name, amountForm
}

// resolveRequesterName 은 이름으로 재직자 한 명을 찾는다.
func resolveRequesterName(ctx context.Context, d deps.Deps, p contract.Principal, name string) (int64, error) {
	if d.Org == nil {
		return 0, errNoRequester
	}
	cards, err := d.Org.SearchPeople(ctx, p, contract.PeopleQuery{Text: name, Limit: 20})
	if err != nil {
		return 0, err
	}
	var same []contract.PersonCard
	for _, c := range cards {
		if c.Name == name {
			same = append(same, c)
		}
	}
	switch len(same) {
	case 0:
		return 0, errNoRequester
	case 1:
		return same[0].UserID, nil
	}
	parts := []string{}
	for _, c := range same {
		parts = append(parts, cleanText(c.Name)+"("+cleanText(c.Primary.Unit.Name)+")")
	}
	return 0, fmt.Errorf("같은 이름의 재직자가 여럿입니다: %s. 사용자 번호로 다시 요청하세요.", strings.Join(parts, ", "))
}

func whoApprovesTool(srv *sdk.Server, d deps.Deps, table *contract.ToolTable) {
	table.Declare(contract.ToolWhoApproves)
	sdk.AddTool(srv, &sdk.Tool{Name: contract.ToolWhoApproves, Description: "결재 문서(구매 품의서, 일반 기안서, 지출 결의서, 업무 협조전)를 올리면 누가 어떤 순서로 결재·합의하는지, 참조·수신은 누구인지 결재선을 미리 보여 준다(양식과 금액 기준). 실제로 상신하거나 결재선을 확정하지 않는다. 결재 대기 중인 내 건 목록은 list_pending_approvals, 내 문서함은 문서함 도구, 결재 금액 구간 규정 조항은 search_policy 를 쓴다."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in whoApprovesIn) (*sdk.CallToolResult, any, error) {
			if d.Line == nil {
				return nil, nil, fmt.Errorf("결재선 미리보기가 설정되지 않았습니다")
			}
			p, _ := contract.PrincipalFrom(ctx)
			code, formName, amountForm := resolveForm(ctx, d, in.FormCode)
			purchase := code == purchaseCode
			req := domain.LinePreviewInput{}
			if !purchase {
				req.FormCode = code
			}
			// 금액: 구매 품의서와 금액 칸이 있는 양식은 필수다. 금액 칸이 없는 양식은 주어도 무시한다.
			if purchase || amountForm {
				if in.Amount <= 0 {
					return nil, nil, errAmountNeeded
				}
				req.Amount = in.Amount
			}
			// 요청자: 번호가 이름보다 우선한다. 양식 문서의 타인 질의는 서비스가 거절한다.
			switch {
			case in.RequesterID > 0:
				req.RequesterID = in.RequesterID
			case strings.TrimSpace(in.RequesterName) != "":
				id, err := resolveRequesterName(ctx, d, p, strings.TrimSpace(in.RequesterName))
				if err != nil {
					return nil, nil, err
				}
				req.RequesterID = id
			}
			// 협조 요청 부서
			if !purchase {
				if strings.TrimSpace(in.FieldUnit) != "" {
					if d.Org == nil {
						return nil, nil, fmt.Errorf("조직도가 설정되지 않았습니다")
					}
					u, cands, err := resolveUnit(ctx, d.Org, in.FieldUnit, "")
					if err != nil {
						return nil, nil, err
					}
					if len(cands) > 0 {
						return nil, nil, fmt.Errorf("조직 이름이 여러 곳과 맞습니다: %s. 조직 코드로 다시 요청하세요", candidateText(ctx, d.Org, cands, ""))
					}
					req.FieldUnit = u.ID
				} else if code == "COOP" {
					return nil, nil, errCoopUnit
				}
			}
			pv, err := d.Line.PreviewLine(ctx, req)
			if err != nil {
				if errors.Is(err, contract.ErrNotFound) && !purchase {
					return nil, nil, errFormMissing
				}
				return nil, nil, err
			}
			return reply(d, toApproverOut(ctx, d, p, pv, code, formName, req.Amount))
		})
}

func toApproverOut(ctx context.Context, d deps.Deps, p contract.Principal, pv domain.LinePreview, code, formName string, amount int64) map[string]any {
	steps := []map[string]any{}
	omitted := 0
	for _, s := range pv.Steps {
		if s.Omitted || s.Order == 0 {
			omitted++
			continue
		}
		if len(steps) >= approverMaxSteps {
			continue
		}
		role := label(roleLabels, s.Role, "결재")
		o := map[string]any{"order": s.Order, "role": role, "stage": s.Stage, "required": s.Required, "title": cleanText(s.Title),
			"unit_name": cleanText(s.UnitName), "approver": nil, "acting": s.Acting, "raised_from": cleanText(s.RaisedFrom),
			"missing": s.Missing, "reason": cleanText(s.Reason)}
		if s.ApproverID != 0 {
			a := approverOut{UserID: s.ApproverID, Name: cleanText(s.ApproverName)}
			if d.Org != nil {
				if c, err := d.Org.Card(ctx, p, s.ApproverID, ""); err == nil {
					a.Rank = cleanText(c.Rank)
				}
			}
			o["approver"] = a
		}
		if s.PoolSize > 0 {
			o["pool"], o["pool_size"] = true, s.PoolSize
		}
		if s.Skipped {
			o["skipped"], o["skip_note"] = true, cleanTextN(s.SkipNote, 20)
		}
		steps = append(steps, o)
	}
	truncated := len(pv.Steps)-omitted > approverMaxSteps
	recips := []map[string]any{}
	for i, r := range pv.Recipients {
		if i >= approverMaxRecip {
			truncated = true
			break
		}
		role, target := "참조", "사람"
		if r.Kind == "receive" {
			role = "수신"
		}
		if r.TargetType == "unit" {
			target = "조직"
		}
		recips = append(recips, map[string]any{"role": role, "target": target, "name": cleanText(r.Name), "path": cleanText(r.Path), "note": cleanText(r.Note)})
	}
	clean := func(in []string) []string {
		out := []string{}
		for _, s := range in {
			out = append(out, cleanText(s))
		}
		return out
	}
	form := map[string]any{"code": code, "name": cleanText(formName)}
	out := map[string]any{"form": form, "requester": map[string]any{"user_id": pv.RequesterID, "name": cleanText(pv.RequesterName), "path": cleanText(pv.UnitPath)},
		"complete": pv.Complete, "steps": steps, "recipients": recips, "problems": clean(pv.Problems), "notes": clean(pv.Notes),
		"truncated": truncated, "note": approverNote}
	if amount > 0 {
		out["amount"] = amount
	}
	if a := pv.Authority; a != nil {
		skipped := []int{}
		skipped = append(skipped, a.SkippedOrders...)
		clause := ""
		if a.ClauseNo != "" {
			clause = "전결 규정 " + cleanTextN(a.ClauseNo, 20)
		}
		out["authority"] = map[string]any{"final_title": cleanText(a.FinalTitle), "final_order": a.FinalOrder, "skipped_orders": skipped, "clause": clause, "warning": cleanText(a.Warning)}
	}
	return out
}

// whoApprovesTarget 은 감사 대상 ID 다: 번호를 줬으면 그 번호, 이름이면 search, 아무것도 없으면 본인(self). 금액·양식·부서 이름은 남기지 않는다.
func whoApprovesTarget(m map[string]any) string {
	if f, ok := m["requester_id"].(float64); ok && f > 0 {
		return strconv.FormatInt(int64(f), 10)
	}
	if s, ok := m["requester_name"].(string); ok && strings.TrimSpace(s) != "" {
		return "search"
	}
	return "self"
}

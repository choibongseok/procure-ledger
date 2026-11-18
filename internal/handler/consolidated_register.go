// 연결 재무 집계 라우트와 내보내기 원천 2개(B44 3단계, 11.2). 권한 consolidated:read, 읽기 전용이다.
package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterConsolidated 는 /api/finance/consolidated 와 내보내기 2개를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterConsolidated(r contract.Registrar, d deps.Deps) {
	if d.Consolidated == nil {
		return
	}
	h := consolidatedHandler{svc: d.Consolidated}
	r.HandleFunc("GET", "/api/finance/consolidated", contract.PermConsolidatedRead, h.summary)
	if d.Export != nil {
		export.Register(r, "/api/finance/consolidated/export/trial", contract.PermConsolidatedRead, d.Export, &consolidatedSource{svc: d.Consolidated, table: "trial"})
		export.Register(r, "/api/finance/consolidated/export/intercompany", contract.PermConsolidatedRead, d.Export, &consolidatedSource{svc: d.Consolidated, table: "intercompany"})
	}
}

type consolidatedHandler struct{ svc domain.ConsolidatedService }

func consolidatedFilterOf(q url.Values) domain.ConsolidatedFilter {
	y, _ := strconv.Atoi(q.Get("year"))
	qu, _ := strconv.Atoi(q.Get("quarter"))
	return domain.ConsolidatedFilter{Unit: q.Get("unit"), Month: q.Get("month"), Year: y, Quarter: qu}
}

func (h consolidatedHandler) summary(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Summary(r.Context(), consolidatedFilterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	ents := make([]map[string]any, len(c.Entities))
	for i, e := range c.Entities {
		ents[i] = map[string]any{"id": e.ID, "code": e.Code, "name": e.Name, "closed": e.Closed, "closedMonths": e.ClosedMonths, "months": e.Months}
	}
	trial := make([]map[string]any, len(c.Trial))
	for i, a := range c.Trial {
		trial[i] = map[string]any{"code": a.Code, "name": a.Name, "kind": a.Kind, "amounts": a.Amounts, "total": a.Total}
	}
	sum := make([]map[string]any, len(c.Summary))
	for i, s := range c.Summary {
		sum[i] = map[string]any{"kind": s.Kind, "label": s.Label, "amounts": s.Amounts, "total": s.Total}
	}
	pairs := make([]map[string]any, len(c.Intercompany))
	for i, p := range c.Intercompany {
		ls := make([]map[string]any, len(p.Lines))
		for j, l := range p.Lines {
			ls[j] = map[string]any{"code": l.Code, "name": l.Name, "debit": l.Debit, "credit": l.Credit}
		}
		pairs[i] = map[string]any{"fromId": p.FromID, "toId": p.ToID, "fromCode": p.FromCode, "fromName": p.FromName, "toCode": p.ToCode, "toName": p.ToName,
			"lines": ls, "payable": p.Payable, "receivable": p.Receivable, "gap": p.Gap, "counterpartRecorded": p.CounterpartRecorded}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": c.From, "to": c.To, "entities": ents, "trial": trial, "summary": sum, "intercompany": pairs,
		"partialScope": c.PartialScope, "pending": c.Pending})
}

// ---- 내보내기 원천 ----

// consolidatedSource 는 긴 형식(계정 × 법인을 한 줄씩)으로 내보낸다. 법인 수가 설치마다 달라 열을 고정할 수 없기 때문이다. 법인 "합계" 줄이 계정마다 따른다.
type consolidatedSource struct {
	svc   domain.ConsolidatedService
	table string // trial | intercompany
}

func (s *consolidatedSource) Dataset() export.Dataset {
	if s.table == "intercompany" {
		return export.Dataset{Code: "consolidated_intercompany", Label: "연결집계_내부거래참고", Columns: []export.Column{{Label: "우리 법인 코드"}, {Label: "우리 법인"}, {Label: "상대 법인 코드"}, {Label: "상대 법인"},
			{Label: "계정 코드"}, {Label: "계정"}, {Label: "차변(원)", Kind: export.KindMoney}, {Label: "대변(원)", Kind: export.KindMoney}}}
	}
	return export.Dataset{Code: "consolidated_trial_balance", Label: "연결집계_법인별시산표", Columns: []export.Column{{Label: "계정 코드"}, {Label: "계정"}, {Label: "분류"},
		{Label: "법인 코드"}, {Label: "법인"}, {Label: "마감"}, {Label: "금액(원)", Kind: export.KindMoney}}}
}

func (s *consolidatedSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{{Name: "집계 단위", Value: q.Get("unit"), Kind: export.FilterCode, Enum: map[string]string{"month": "월", "quarter": "분기", "year": "연"}},
		export.Month("월", q.Get("month")), export.ID("연도", q.Get("year")), export.ID("분기", q.Get("quarter"))}
}

var consKindLabels = map[string]string{"asset": "자산", "liability": "부채", "equity": "자본", "revenue": "수익", "expense": "비용"}

func (s *consolidatedSource) rows(ctx context.Context, q url.Values) ([][]export.Cell, error) {
	c, err := s.svc.Summary(ctx, consolidatedFilterOf(q))
	if err != nil {
		return nil, err
	}
	var out [][]export.Cell
	if s.table == "intercompany" {
		for _, p := range c.Intercompany {
			for _, l := range p.Lines {
				out = append(out, []export.Cell{p.FromCode, p.FromName, p.ToCode, p.ToName, l.Code, l.Name, l.Debit, l.Credit})
			}
		}
		return out, nil
	}
	for _, a := range c.Trial {
		for i, e := range c.Entities {
			closed := "마감 전"
			if e.Closed {
				closed = "마감"
			}
			out = append(out, []export.Cell{a.Code, a.Name, consKindLabels[a.Kind], e.Code, e.Name, closed, a.Amounts[i]})
		}
		out = append(out, []export.Cell{a.Code, a.Name, consKindLabels[a.Kind], "합계", "합계", nil, a.Total})
	}
	return out, nil
}

func (s *consolidatedSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	r, err := s.rows(ctx, q)
	return len(r), false, err
}

func (s *consolidatedSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	r, err := s.rows(ctx, q)
	if len(r) > max+1 {
		r = r[:max+1]
	}
	return r, err
}

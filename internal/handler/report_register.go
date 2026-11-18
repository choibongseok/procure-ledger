// 업무 보고서 3종 라우트와 내보내기 원천(B28, 5~7절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다(거래처 탭은 vendor:rating_read 도).
package handler

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterReports 는 /api/reports/* 와 각 내보내기를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterReports(r contract.Registrar, d deps.Deps) {
	if d.Reports == nil {
		return
	}
	h := reportHandler{svc: d.Reports}
	r.HandleFunc("GET", "/api/reports/options", contract.PermReportRead, h.options)
	r.HandleFunc("GET", "/api/reports/purchase", contract.PermReportRead, h.purchase)
	r.HandleFunc("GET", "/api/reports/vendors", contract.PermReportRead, h.vendors)
	r.HandleFunc("GET", "/api/reports/approvals", contract.PermReportRead, h.approvals)
	if d.Export != nil {
		export.Register(r, "/api/reports/purchase/export", contract.PermReportRead, d.Export, purchaseReportExport(d.Reports))
		export.Register(r, "/api/reports/vendors/export", contract.PermReportRead, d.Export, vendorReportExport(d.Reports))
		export.Register(r, "/api/reports/approvals/export", contract.PermReportRead, d.Export, approvalReportExport(d.Reports))
	}
}

type reportHandler struct{ svc domain.ReportService }

var kst = time.FixedZone("KST", 9*3600)

// 기본 기간(5.0): 구매 실적=이번 회계연도 시작 월~이번 달(서비스가 회사 설정의 회계연도 시작월로 채운다), 거래처=최근 90일, 결재 소요=최근 30일.
func purchaseQuery(q url.Values) domain.PurchaseReportQuery {
	return domain.PurchaseReportQuery{FromMonth: q.Get("from"), ToMonth: q.Get("to"), DepartmentID: idParam(q, "departmentId")}
}

func dayRangeParams(q url.Values, defDays int) (contract.Day, contract.Day) {
	now := time.Now().In(kst)
	from, to := q.Get("from"), q.Get("to")
	if to == "" {
		to = now.Format("2006-01-02")
	}
	if from == "" {
		from = now.AddDate(0, 0, -defDays).Format("2006-01-02")
	}
	return contract.Day(from), contract.Day(to)
}

func approvalQuery(q url.Values) domain.ApprovalReportQuery {
	from, to := dayRangeParams(q, 30)
	return domain.ApprovalReportQuery{From: from, To: to, FormCode: q.Get("formCode"), DepartmentID: idParam(q, "departmentId")}
}

func (h reportHandler) options(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.Options(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	deps := make([]map[string]any, len(o.Departments))
	for i, d := range o.Departments {
		deps[i] = map[string]any{"id": d.ID, "name": d.Name}
	}
	forms := make([]map[string]any, len(o.Forms))
	for i, f := range o.Forms {
		forms[i] = map[string]any{"code": f.Code, "name": f.Name}
	}
	writeJSON(w, http.StatusOK, map[string]any{"departments": deps, "forms": forms})
}

func (h reportHandler) purchase(w http.ResponseWriter, r *http.Request) {
	q := purchaseQuery(r.URL.Query())
	rep, err := h.svc.Purchase(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(rep.Rows))
	for i, x := range rep.Rows {
		rows[i] = map[string]any{"departmentId": x.DepartmentID, "departmentName": x.DepartmentName, "month": x.Month, "budget": ptrOrNil(x.Budget), "orders": x.Orders,
			"used": x.Used, "inProgress": x.InProgress, "balance": ptrOrNil(x.Balance), "executionPct": ptrOrNil(x.ExecutionPct), "forecastPct": ptrOrNil(x.ForecastPct), "badge": x.Badge}
	}
	t := rep.Totals
	writeJSON(w, http.StatusOK, map[string]any{"from": rep.From, "to": rep.To, "rows": rows,
		"totals": map[string]any{"orders": t.Orders, "used": t.Used, "inProgress": t.InProgress, "budget": t.Budget, "budgetedUsed": t.BudgetedUsed, "executionPct": ptrOrNil(t.ExecutionPct), "unbudgetedUsed": t.UnbudgetedUsed}})
}

func (h reportHandler) vendors(w http.ResponseWriter, r *http.Request) {
	from, to := dayRangeParams(r.URL.Query(), 90)
	rows, err := h.svc.Vendors(r.Context(), from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, v := range rows {
		m := ratingDTO(v.VendorRating)
		m["purchaseOrders"], m["orderAmount"] = v.PurchaseOrders, v.OrderAmount
		out[i] = m
	}
	writeJSON(w, http.StatusOK, out)
}

func (h reportHandler) approvals(w http.ResponseWriter, r *http.Request) {
	q := approvalQuery(r.URL.Query())
	rep, err := h.svc.Approvals(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	stats := make([]map[string]any, len(rep.Stats))
	for i, s := range rep.Stats {
		stats[i] = map[string]any{"formCode": s.FormCode, "formName": s.FormName, "stepTitle": s.StepTitle, "count": s.Count, "avgMinutes": s.AvgMinutes,
			"medMinutes": s.MedMinutes, "maxMinutes": s.MaxMinutes, "rejected": s.Rejected, "objected": s.Objected, "formTotal": s.IsFormTotal}
	}
	delayed := make([]map[string]any, len(rep.Delayed))
	for i, d := range rep.Delayed {
		delayed[i] = map[string]any{"docId": d.DocID, "docNo": d.DocNo, "title": d.Title, "formName": d.FormName, "stepTitle": d.StepTitle, "approverName": d.ApproverName,
			"noApprover": d.NoApprover, "elapsedMinutes": d.ElapsedMinutes, "stepStart": rfc(d.StepStart), "link": d.Link}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": string(q.From), "to": string(q.To), "stats": stats, "delayed": delayed, "excluded": rep.Excluded})
}

// ---- 내보내기 원천 ----

var budgetBadgeReport = map[string]string{"ok": "여유", "near": "한도 근접", "over": "예산 초과", "none": "예산 없음"}
var ratingBadgeLabel = map[string]string{"good": "양호", "warn": "주의", "bad": "나쁨", "small": "표본 적음"}

func pctCell(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPct(p int) any {
	if p < 0 {
		return nil
	}
	return float64(p)
}

func purchaseReportExport(s domain.ReportService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "report_purchase", Label: "구매실적", Columns: []export.Column{
			{Label: "부서"}, {Label: "월"}, {Label: "월 예산(원)", Kind: export.KindMoney}, {Label: "구매 건수", Kind: export.KindInt}, {Label: "사용액(원)", Kind: export.KindMoney},
			{Label: "결재 진행 중(원)", Kind: export.KindMoney}, {Label: "잔액(원)", Kind: export.KindMoney}, {Label: "집행률(%)", Kind: export.KindPercent},
			{Label: "소진 예상(%)", Kind: export.KindPercent}, {Label: "상태"}}},
		filters: func(q url.Values) []export.FilterItem {
			p := purchaseQuery(q)
			// 기간이 비면 서버가 이번 회계연도 시작 월~이번 달로 채운다. 머리글에도 그 사실을 적는다.
			from, to := export.Month("시작 월", p.FromMonth), export.Month("종료 월", p.ToMonth)
			if p.FromMonth == "" {
				from = export.Enum("시작 월", "default", map[string]string{"default": "이번 회계연도 시작 월(기본)"})
			}
			if p.ToMonth == "" {
				to = export.Enum("종료 월", "default", map[string]string{"default": "이번 달(기본)"})
			}
			return []export.FilterItem{from, to, export.ID("부서", q.Get("departmentId"))}
		},
		fetch: func(ctx context.Context, q url.Values, _ int) ([][]export.Cell, error) {
			rep, err := s.Purchase(ctx, purchaseQuery(q))
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, 0, len(rep.Rows)+1)
			for _, x := range rep.Rows {
				out = append(out, []export.Cell{x.DepartmentName, x.Month, ptrOrNil(x.Budget), x.Orders, x.Used, x.InProgress, ptrOrNil(x.Balance), pctCell(x.ExecutionPct), pctCell(x.ForecastPct), budgetBadgeReport[x.Badge]})
			}
			t := rep.Totals
			return append(out, []export.Cell{"합계", nil, nil, t.Orders, t.Used, t.InProgress, nil, pctCell(t.ExecutionPct), nil, nil}), nil
		},
	}
}

func vendorReportExport(s domain.ReportService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "report_vendors", Label: "거래처별실적", Columns: []export.Column{
			{Label: "거래처"}, {Label: "발주 건수", Kind: export.KindInt}, {Label: "발주 금액(원)", Kind: export.KindMoney}, {Label: "납기 내 입고", Kind: export.KindInt},
			{Label: "납기 준수율(%)", Kind: export.KindPercent}, {Label: "증빙 판정", Kind: export.KindInt}, {Label: "불일치", Kind: export.KindInt},
			{Label: "불일치 비율(%)", Kind: export.KindPercent}, {Label: "납기 평가"}, {Label: "판정 평가"}}},
		filters: func(q url.Values) []export.FilterItem {
			from, to := dayRangeParams(q, 90)
			return []export.FilterItem{export.Date("시작일", string(from)), export.Date("종료일", string(to))}
		},
		fetch: func(ctx context.Context, q url.Values, _ int) ([][]export.Cell, error) {
			from, to := dayRangeParams(q, 90)
			rows, err := s.Vendors(ctx, from, to)
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rows))
			for i, v := range rows {
				out[i] = []export.Cell{v.VendorName, v.PurchaseOrders, v.OrderAmount, v.OnTime, intPct(v.OnTimePct), v.Verdicts, v.Mismatches, intPct(v.MismatchPct),
					dashIfEmpty(ratingBadgeLabel[v.OnTimeBadge]), dashIfEmpty(ratingBadgeLabel[v.MismatchBadge])}
			}
			return out, nil
		},
	}
}

func approvalReportExport(s domain.ReportService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "report_approvals", Label: "결재소요시간", Columns: []export.Column{
			{Label: "양식"}, {Label: "단계"}, {Label: "처리 건수", Kind: export.KindInt}, {Label: "평균 소요(분)", Kind: export.KindInt}, {Label: "중앙값 소요(분)", Kind: export.KindInt},
			{Label: "최대 소요(분)", Kind: export.KindInt}, {Label: "반려", Kind: export.KindInt}, {Label: "이견", Kind: export.KindInt}}},
		filters: func(q url.Values) []export.FilterItem {
			a := approvalQuery(q)
			return []export.FilterItem{export.Date("시작일", string(a.From)), export.Date("종료일", string(a.To)), export.Code("양식", a.FormCode), export.ID("부서", q.Get("departmentId"))}
		},
		fetch: func(ctx context.Context, q url.Values, _ int) ([][]export.Cell, error) {
			rep, err := s.Approvals(ctx, approvalQuery(q))
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rep.Stats))
			for i, x := range rep.Stats {
				var rej, obj any = x.Rejected, x.Objected
				if x.IsFormTotal {
					rej, obj = nil, nil
				}
				out[i] = []export.Cell{x.FormName, x.StepTitle, x.Count, x.AvgMinutes, x.MedMinutes, x.MaxMinutes, rej, obj}
			}
			return out, nil
		},
	}
}

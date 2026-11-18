// 매입 채무·계정별 원장·시산표 라우트와 내보내기 원천(B31 3.5·3.6). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
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

// registerLedgerReports 는 /api/finance/payables·vendors/{id}/payment-days·ledger·trial-balance 와 원장·시산표 내보내기를 등록한다.
func registerLedgerReports(r contract.Registrar, d deps.Deps) {
	if d.Payables != nil {
		h := payableHandler{svc: d.Payables}
		r.HandleFunc("GET", "/api/finance/payables", contract.PermLedgerRead, h.byVendor)
		r.HandleFunc("GET", "/api/finance/payables/{vendorId}", contract.PermLedgerRead, h.items)
		r.HandleFunc("PUT", "/api/finance/vendors/{id}/payment-days", contract.PermAccountManage, h.setDays)
	}
	if d.LedgerReport != nil {
		h := ledgerReportHandler{svc: d.LedgerReport}
		r.HandleFunc("GET", "/api/finance/ledger", contract.PermLedgerRead, h.ledger)
		r.HandleFunc("GET", "/api/finance/trial-balance", contract.PermLedgerRead, h.trial)
		if d.Export != nil {
			export.Register(r, "/api/finance/ledger/export", contract.PermLedgerRead, d.Export, &ledgerSource{svc: d.LedgerReport})
			export.Register(r, "/api/finance/trial-balance/export", contract.PermLedgerRead, d.Export, &trialSource{svc: d.LedgerReport})
		}
	}
}

type payableHandler struct{ svc domain.PayableService }

func (h payableHandler) byVendor(w http.ResponseWriter, r *http.Request) {
	rep, err := h.svc.ByVendor(r.Context(), r.URL.Query().Get("asOf"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(rep.Rows))
	for i, x := range rep.Rows {
		rows[i] = map[string]any{"vendorId": x.VendorID, "vendorName": x.VendorName, "count": x.Count, "outstanding": x.Outstanding, "reserved": x.Reserved,
			"earliestDue": x.EarliestDue, "overdueAmount": x.OverdueAmount, "paymentDays": x.PaymentDays}
		if x.Intercompany != "" {
			rows[i]["intercompany"] = x.Intercompany
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"asOf": rep.AsOf, "rows": rows, "total": rep.Total, "ledgerBalance": rep.LedgerBalance, "matches": rep.Matches})
}

func (h payableHandler) items(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("vendorId"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return
	}
	items, err := h.svc.Items(r.Context(), id, r.URL.Query().Get("asOf"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(items))
	for i, x := range items {
		out[i] = map[string]any{"id": x.ID, "journalNo": x.JournalNo, "tradeDate": x.TradeDate, "dueDate": x.DueDate, "amount": x.Amount, "paid": x.Paid, "reserved": x.Reserved,
			"outstanding": x.Outstanding, "overdueDays": x.OverdueDays}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h payableHandler) setDays(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return
	}
	var b struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &b) {
		return
	}
	days, err := h.svc.SetPaymentDays(r.Context(), id, b.Days)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vendorId": id, "paymentDays": days})
}

type ledgerReportHandler struct{ svc domain.LedgerReportService }

func ledgerFilterOf(q url.Values) domain.AccountLedgerFilter {
	v, _ := strconv.ParseInt(q.Get("vendorId"), 10, 64)
	return domain.AccountLedgerFilter{Account: q.Get("account"), From: q.Get("from"), To: q.Get("to"), VendorID: v}
}

func (h ledgerReportHandler) ledger(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.AccountLedger(r.Context(), ledgerFilterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(l.Rows))
	for i, x := range l.Rows {
		rows[i] = map[string]any{"postingDate": x.PostingDate, "journalNo": x.JournalNo, "memo": x.Memo, "counter": x.Counter, "debit": x.Debit, "credit": x.Credit, "balance": x.Balance}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": accountDTO(l.Account), "from": l.From, "to": l.To, "opening": l.Opening, "rows": rows,
		"totalDebit": l.TotalDebit, "totalCredit": l.TotalCredit, "closing": l.Closing, "excludedCount": l.ExcludedCount})
}

func (h ledgerReportHandler) trial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, err := h.svc.TrialBalance(r.Context(), q.Get("from"), q.Get("to"), q.Get("includeZero") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trialDTO(t))
}

func trialDTO(t domain.TrialBalance) map[string]any {
	rows := make([]map[string]any, len(t.Rows))
	for i, x := range t.Rows {
		rows[i] = map[string]any{"code": x.Code, "name": x.Name, "kind": x.Kind, "kindLabel": domain.AccountKindLabels[x.Kind], "opening": x.Opening, "debit": x.Debit, "credit": x.Credit, "closing": x.Closing}
	}
	return map[string]any{"from": t.From, "to": t.To, "rows": rows, "totalDebit": t.TotalDebit, "totalCredit": t.TotalCredit, "balanced": t.Balanced, "excludedCount": t.ExcludedCount}
}

// ---- 내보내기 원천 ----

type ledgerSource struct{ svc domain.LedgerReportService }

func (s *ledgerSource) Dataset() export.Dataset {
	return export.Dataset{Code: "ledger", Label: "계정별원장", Columns: []export.Column{{Label: "전기일", Kind: export.KindDate}, {Label: "전표 번호"}, {Label: "적요"}, {Label: "상대 계정"},
		{Label: "차변(원)", Kind: export.KindMoney}, {Label: "대변(원)", Kind: export.KindMoney}, {Label: "잔액(원)", Kind: export.KindMoney}}}
}

func (s *ledgerSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Code("계정", q.Get("account")), export.Date("시작일", q.Get("from")), export.Date("종료일", q.Get("to")), export.ID("거래처", q.Get("vendorId"))}
}

func (s *ledgerSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	l, err := s.svc.AccountLedger(ctx, ledgerFilterOf(q))
	return len(l.Rows) + 2, false, err
}

func (s *ledgerSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	l, err := s.svc.AccountLedger(ctx, ledgerFilterOf(q))
	if err != nil {
		return nil, err
	}
	out := [][]export.Cell{{l.From, "이월 잔액", nil, nil, nil, nil, l.Opening}}
	for _, x := range l.Rows {
		if len(out) > max {
			break
		}
		out = append(out, []export.Cell{x.PostingDate, x.JournalNo, x.Memo, x.Counter, x.Debit, x.Credit, x.Balance})
	}
	return append(out, []export.Cell{nil, "합계", nil, nil, l.TotalDebit, l.TotalCredit, l.Closing}), nil
}

type trialSource struct{ svc domain.LedgerReportService }

func (s *trialSource) Dataset() export.Dataset {
	return export.Dataset{Code: "trial_balance", Label: "시산표", Columns: []export.Column{{Label: "계정 코드"}, {Label: "계정"}, {Label: "구분"},
		{Label: "기초 잔액(원)", Kind: export.KindMoney}, {Label: "차변(원)", Kind: export.KindMoney}, {Label: "대변(원)", Kind: export.KindMoney}, {Label: "기말 잔액(원)", Kind: export.KindMoney}}}
}

func (s *trialSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Date("시작일", q.Get("from")), export.Date("종료일", q.Get("to")), export.Bool("0 잔액 계정 포함", q.Get("includeZero"))}
}

func (s *trialSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	t, err := s.svc.TrialBalance(ctx, q.Get("from"), q.Get("to"), q.Get("includeZero") == "1")
	return len(t.Rows) + 1, false, err
}

func (s *trialSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	t, err := s.svc.TrialBalance(ctx, q.Get("from"), q.Get("to"), q.Get("includeZero") == "1")
	if err != nil {
		return nil, err
	}
	var out [][]export.Cell
	for _, x := range t.Rows {
		if len(out) > max {
			break
		}
		out = append(out, []export.Cell{x.Code, x.Name, domain.AccountKindLabels[x.Kind], x.Opening, x.Debit, x.Credit, x.Closing})
	}
	return append(out, []export.Cell{"합계", nil, nil, nil, t.TotalDebit, t.TotalCredit, nil}), nil
}

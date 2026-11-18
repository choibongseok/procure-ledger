// 지급 요청·지급 처리·자금 계좌·자금 일보·입금 라우트(B32 단계 1, 9절).
// 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다. 계좌번호는 마스킹 값만 응답에 싣고 원문은 reveal 라우트(mask:bypass, 감사)로만 나간다.
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

// RegisterPayments 는 /api/finance/payments·payment-calendar·bank-accounts·fund-daily·deposits 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterPayments(r contract.Registrar, d deps.Deps) {
	if d.Payments != nil {
		h := paymentHandler{svc: d.Payments, funds: d.Funds}
		r.HandleFunc("GET", "/api/finance/payments", contract.PermPaymentRead, h.list)
		r.HandleFunc("POST", "/api/finance/payments", contract.PermPaymentWrite, h.create)
		r.HandleFunc("GET", "/api/finance/payments/{no}", contract.PermPaymentRead, h.get)
		r.HandleFunc("PUT", "/api/finance/payments/{no}", contract.PermPaymentWrite, h.update)
		r.HandleFunc("POST", "/api/finance/payments/{no}/request", contract.PermPaymentWrite, h.request)
		r.HandleFunc("POST", "/api/finance/payments/{no}/cancel", contract.PermPaymentWrite, h.cancel)
		r.HandleFunc("POST", "/api/finance/payments/{no}/settle", contract.PermPaymentWrite, h.settle)
		r.HandleFunc("POST", "/api/finance/payments/{no}/reverse", contract.PermPaymentWrite, h.reverse)
		r.HandleFunc("POST", "/api/finance/payments/{no}/reveal-payee", contract.PermMaskBypass, h.revealPayee)
		r.HandleFunc("GET", "/api/finance/payables/{vendorId}/candidates", contract.PermPaymentRead, h.candidates)
		r.HandleFunc("GET", "/api/finance/payment-calendar", contract.PermPaymentRead, h.calendar)
	}
	if d.Funds != nil {
		h := fundHandler{svc: d.Funds}
		r.HandleFunc("GET", "/api/finance/bank-accounts", contract.PermFundRead, h.accounts)
		r.HandleFunc("POST", "/api/finance/bank-accounts", contract.PermFundManage, h.create)
		r.HandleFunc("PUT", "/api/finance/bank-accounts/{id}", contract.PermFundManage, h.update)
		r.HandleFunc("POST", "/api/finance/bank-accounts/{id}/active", contract.PermFundManage, h.setActive)
		r.HandleFunc("POST", "/api/finance/bank-accounts/{id}/reveal", contract.PermMaskBypass, h.reveal)
		r.HandleFunc("GET", "/api/finance/fund-daily", contract.PermFundRead, h.daily)
		r.HandleFunc("POST", "/api/finance/deposits", contract.PermFundManage, h.deposit)
		if d.Export != nil {
			export.Register(r, "/api/finance/fund-daily/export", contract.PermFundRead, d.Export, &fundDailySource{svc: d.Funds})
		}
	}
}

type paymentHandler struct {
	svc   domain.PaymentService
	funds domain.FundService
}

func paymentDTO(p domain.Payment, withItems bool) map[string]any {
	m := map[string]any{"id": p.ID, "no": p.No, "vendorId": p.VendorID, "vendorName": p.VendorName, "status": p.Status, "statusLabel": domain.PaymentStatusLabels[p.Status], "total": p.Total,
		"dueDate": p.DueDate, "bankAccountId": p.BankAccountID, "bankAlias": p.BankAlias, "payeeAccount": p.PayeeAccount, "memo": p.Memo, "docId": p.DocID, "docNo": p.DocNo,
		"createdBy": p.CreatedBy, "createdById": p.CreatedByID, "createdAt": rfc(p.CreatedAt), "cancelReason": p.CancelReason, "reverseReason": p.ReverseReason, "paidDate": p.PaidDate,
		"transferRef": p.TransferRef, "paidBy": p.PaidBy, "journalNo": p.JournalNo, "reversalNo": p.ReversalNo, "warnings": p.Warnings, "accountDiffers": p.AccountDiffers}
	if m["warnings"] == nil {
		m["warnings"] = []string{}
	}
	if p.ApprovedAt != nil {
		m["approvedAt"] = rfc(*p.ApprovedAt)
	}
	if withItems {
		items := make([]map[string]any, len(p.Items))
		for i, it := range p.Items {
			items[i] = map[string]any{"payableId": it.PayableID, "journalNo": it.JournalNo, "tradeDate": it.TradeDate, "dueDate": it.DueDate, "amount": it.Amount}
		}
		m["items"] = items
	}
	return m
}

type paymentBody struct {
	VendorID      int64  `json:"vendorId"`
	DueDate       string `json:"dueDate"`
	BankAccountID int64  `json:"bankAccountId"`
	Memo          string `json:"memo"`
	Items         []struct {
		PayableID int64 `json:"payableId"`
		Amount    int64 `json:"amount"`
	} `json:"items"`
}

func (b paymentBody) input() domain.PaymentInput {
	in := domain.PaymentInput{VendorID: b.VendorID, DueDate: b.DueDate, BankAccountID: b.BankAccountID, Memo: b.Memo}
	for _, it := range b.Items {
		in.Items = append(in.Items, domain.PaymentItemInput{PayableID: it.PayableID, Amount: it.Amount})
	}
	return in
}

func (h paymentHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vid, _ := strconv.ParseInt(q.Get("vendorId"), 10, 64)
	off, _ := strconv.Atoi(q.Get("offset"))
	pg, err := h.svc.List(r.Context(), domain.PaymentFilter{Status: q.Get("status"), VendorID: vid, Offset: off})
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(pg.Rows))
	for i, p := range pg.Rows {
		rows[i] = paymentDTO(p, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": pg.Total})
}

func (h paymentHandler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), r.PathValue("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) create(w http.ResponseWriter, r *http.Request) {
	var b paymentBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.Create(r.Context(), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, paymentDTO(p, true))
}

func (h paymentHandler) update(w http.ResponseWriter, r *http.Request) {
	var b paymentBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.Update(r.Context(), r.PathValue("no"), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) request(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.RequestApproval(r.Context(), r.PathValue("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) cancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.Cancel(r.Context(), r.PathValue("no"), b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) settle(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PaidDate       string `json:"paidDate"`
		BankAccountID  int64  `json:"bankAccountId"`
		ChangeReason   string `json:"changeReason"`
		TransferRef    string `json:"transferRef"`
		Memo           string `json:"memo"`
		ConfirmAccount bool   `json:"confirmAccount"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.Settle(r.Context(), r.PathValue("no"), domain.SettleInput{PaidDate: b.PaidDate, BankAccountID: b.BankAccountID, ChangeReason: b.ChangeReason,
		TransferRef: b.TransferRef, Memo: b.Memo, ConfirmAccount: b.ConfirmAccount})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) reverse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.Reverse(r.Context(), r.PathValue("no"), b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p, true))
}

func (h paymentHandler) revealPayee(w http.ResponseWriter, r *http.Request) {
	raw, err := h.funds.RevealPayee(r.Context(), r.PathValue("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"accountNo": raw})
}

func (h paymentHandler) candidates(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("vendorId"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return
	}
	cs, err := h.svc.Candidates(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = map[string]any{"payableId": c.PayableID, "journalNo": c.JournalNo, "tradeDate": c.TradeDate, "dueDate": c.DueDate, "amount": c.Amount, "paid": c.Paid,
			"reserved": c.Reserved, "available": c.Available, "overdueDays": c.OverdueDays, "openRequests": c.OpenRequests}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h paymentHandler) calendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := h.svc.Calendar(r.Context(), q.Get("from"), q.Get("to"))
	if err != nil {
		writeErr(w, err)
		return
	}
	es := make([]map[string]any, len(c.Entries))
	for i, e := range c.Entries {
		es[i] = map[string]any{"date": e.Date, "kind": e.Kind, "vendorName": e.VendorName, "amount": e.Amount, "paymentNo": e.PaymentNo, "nextBusinessDay": e.NextBusinessDay, "status": e.Status, "statusLabel": domain.PaymentStatusLabels[e.Status], "delayed": e.Delayed}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": c.From, "to": c.To, "entries": es, "overdueTotal": c.OverdueTotal, "scheduledTotal": c.ScheduledTotal, "balanceTotal": c.BalanceTotal, "shortageWarn": c.ShortageWarn})
}

// ---- 자금 ----

type fundHandler struct{ svc domain.FundService }

func bankDTO(a domain.BankAccount) map[string]any {
	return map[string]any{"id": a.ID, "alias": a.Alias, "bankName": a.BankName, "accountNo": a.AccountNo, "ledgerCode": a.LedgerCode, "ledgerName": a.LedgerName,
		"openingBalance": a.OpeningBalance, "openingDate": a.OpeningDate, "active": a.Active, "balance": a.Balance}
}

func (h fundHandler) accounts(w http.ResponseWriter, r *http.Request) {
	as, err := h.svc.Accounts(r.Context(), r.URL.Query().Get("all") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(as))
	for i, a := range as {
		out[i] = bankDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}

type bankBody struct {
	Alias          string `json:"alias"`
	BankName       string `json:"bankName"`
	AccountNo      string `json:"accountNo"`
	LedgerCode     string `json:"ledgerCode"`
	OpeningBalance int64  `json:"openingBalance"`
	OpeningDate    string `json:"openingDate"`
}

func (b bankBody) input() domain.BankAccountInput {
	return domain.BankAccountInput{Alias: b.Alias, BankName: b.BankName, AccountNo: b.AccountNo, LedgerCode: b.LedgerCode, OpeningBalance: b.OpeningBalance, OpeningDate: b.OpeningDate}
}

func (h fundHandler) create(w http.ResponseWriter, r *http.Request) {
	var b bankBody
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.SaveAccount(r.Context(), 0, b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, bankDTO(a))
}

func bankID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

func (h fundHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := bankID(w, r)
	if !ok {
		return
	}
	var b bankBody
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.SaveAccount(r.Context(), id, b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bankDTO(a))
}

func (h fundHandler) setActive(w http.ResponseWriter, r *http.Request) {
	id, ok := bankID(w, r)
	if !ok {
		return
	}
	var b struct {
		Active bool `json:"active"`
	}
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.SetActive(r.Context(), id, b.Active)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bankDTO(a))
}

func (h fundHandler) reveal(w http.ResponseWriter, r *http.Request) {
	id, ok := bankID(w, r)
	if !ok {
		return
	}
	raw, err := h.svc.RevealAccount(r.Context(), id, r.URL.Query().Get("screen"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"accountNo": raw})
}

func fundFilterOf(q url.Values) domain.FundDailyFilter {
	id, _ := strconv.ParseInt(q.Get("bankAccountId"), 10, 64)
	return domain.FundDailyFilter{From: q.Get("from"), To: q.Get("to"), BankAccountID: id}
}

func (h fundHandler) daily(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Daily(r.Context(), fundFilterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(d.Rows))
	for i, x := range d.Rows {
		rows[i] = map[string]any{"date": x.Date, "bankAccountId": x.BankAccountID, "bankAlias": x.BankAlias, "opening": x.Opening, "deposit": x.Deposit, "withdraw": x.Withdraw, "closing": x.Closing, "pendingDeposit": x.PendingDeposit}
	}
	mv := make([]map[string]any, len(d.Movements))
	for i, x := range d.Movements {
		mv[i] = map[string]any{"date": x.Date, "bankAlias": x.BankAlias, "kind": x.Kind, "kindLabel": domain.FundKindLabels[x.Kind], "amount": x.Amount, "paymentNo": x.PaymentNo, "vendorName": x.VendorName, "memo": x.Memo, "journalNo": x.JournalNo, "pending": x.Pending}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": d.From, "to": d.To, "opening": d.Opening, "rows": rows, "movements": mv, "totalDeposit": d.TotalDeposit, "totalWithdraw": d.TotalWithdraw,
		"closing": d.Closing, "ledgerBalance": d.LedgerBalance, "matches": d.Matches, "pendingCount": d.PendingCount})
}

func (h fundHandler) deposit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		BankAccountID int64  `json:"bankAccountId"`
		Date          string `json:"date"`
		Amount        int64  `json:"amount"`
		CounterCode   string `json:"counterCode"`
		VendorID      int64  `json:"vendorId"`
		Memo          string `json:"memo"`
	}
	if !decode(w, r, &b) {
		return
	}
	j, err := h.svc.RecordDeposit(r.Context(), domain.DepositInput{BankAccountID: b.BankAccountID, Date: b.Date, Amount: b.Amount, CounterCode: b.CounterCode, VendorID: b.VendorID, Memo: b.Memo})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, journalDTO(j, true))
}

// ---- 자금 일보 내보내기 ----

type fundDailySource struct{ svc domain.FundService }

func (s *fundDailySource) Dataset() export.Dataset {
	return export.Dataset{Code: "fund_daily", Label: "자금일보", Columns: []export.Column{{Label: "일자", Kind: export.KindDate}, {Label: "계좌"}, {Label: "기초 잔액(원)", Kind: export.KindMoney},
		{Label: "입금(원)", Kind: export.KindMoney}, {Label: "출금(원)", Kind: export.KindMoney}, {Label: "기말 잔액(원)", Kind: export.KindMoney}}}
}

func (s *fundDailySource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Date("시작일", q.Get("from")), export.Date("종료일", q.Get("to")), export.ID("계좌", q.Get("bankAccountId"))}
}

func (s *fundDailySource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	d, err := s.svc.Daily(ctx, fundFilterOf(q))
	return len(d.Rows) + 1, false, err
}

func (s *fundDailySource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	d, err := s.svc.Daily(ctx, fundFilterOf(q))
	if err != nil {
		return nil, err
	}
	var out [][]export.Cell
	for _, x := range d.Rows {
		if len(out) > max {
			break
		}
		out = append(out, []export.Cell{x.Date, x.BankAlias, x.Opening, x.Deposit, x.Withdraw, x.Closing})
	}
	return append(out, []export.Cell{"합계", nil, d.Opening, d.TotalDeposit, d.TotalWithdraw, d.Closing}), nil
}

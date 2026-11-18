// 전자세금계산서 라우트(B43 1단계, 9절). 권한 선언은 라우트 등록기가 하고 핸들러는 서비스만 부른다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterTaxInvoice 는 /api/finance/tax-invoices 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterTaxInvoice(r contract.Registrar, d deps.Deps) {
	if d.TaxInvoice == nil {
		return
	}
	h := taxInvoiceHandler{svc: d.TaxInvoice}
	r.HandleFunc("GET", "/api/finance/tax-invoices", contract.PermTaxInvoiceRead, h.list)
	r.HandleFunc("POST", "/api/finance/tax-invoices", contract.PermTaxInvoiceWrite, h.create)
	r.HandleFunc("POST", "/api/finance/tax-invoices/receive", contract.PermTaxInvoiceWrite, h.receive)
	r.HandleFunc("GET", "/api/finance/tax-invoices/{id}", contract.PermTaxInvoiceRead, h.get)
	r.HandleFunc("PUT", "/api/finance/tax-invoices/{id}", contract.PermTaxInvoiceWrite, h.update)
	r.HandleFunc("POST", "/api/finance/tax-invoices/{id}/issue", contract.PermTaxInvoiceIssue, h.issue)
	r.HandleFunc("POST", "/api/finance/tax-invoices/{id}/resend", contract.PermTaxInvoiceIssue, h.resend)
	r.HandleFunc("POST", "/api/finance/tax-invoices/{id}/cancel", contract.PermTaxInvoiceWrite, h.cancel)
	r.HandleFunc("POST", "/api/finance/tax-invoices/{id}/cancel-received", contract.PermTaxInvoiceWrite, h.cancelReceived)
	r.HandleFunc("POST", "/api/finance/tax-invoices/{id}/amend", contract.PermTaxInvoiceWrite, h.amend)
	if d.Export != nil { // 목록 내보내기(B28 방식, 데이터 세트 tax_invoices)
		export.Register(r, "/api/finance/tax-invoices-export", contract.PermTaxInvoiceRead, d.Export, &taxInvoiceSource{svc: d.TaxInvoice})
	}
}

type taxInvoiceHandler struct{ svc domain.TaxInvoiceService }

type taxPartyDTO struct {
	BizNo    string `json:"bizNo"`
	BranchNo string `json:"branchNo"`
	Name     string `json:"name"`
	Rep      string `json:"rep"`
	Addr     string `json:"addr"`
	Email    string `json:"email"`
}

type taxLineDTO struct {
	Seq       int    `json:"seq,omitempty"`
	Day       string `json:"day"`
	Name      string `json:"name"`
	Spec      string `json:"spec"`
	Qty       int64  `json:"qty"`
	UnitPrice int64  `json:"unitPrice"`
	Supply    int64  `json:"supply"`
	Tax       int64  `json:"tax,omitempty"`
	Remark    string `json:"remark"`
}

type taxInputDTO struct {
	TaxType    string       `json:"taxType"`
	IssueDate  string       `json:"issueDate"`
	Buyer      taxPartyDTO  `json:"buyer"`
	Lines      []taxLineDTO `json:"lines"`
	ChargeType string       `json:"chargeType"`
	Remark     string       `json:"remark"`
}

type taxReceiveDTO struct {
	ApprovalNo        string       `json:"approvalNo"`
	TaxType           string       `json:"taxType"`
	IssueDate         string       `json:"issueDate"`
	Supplier          taxPartyDTO  `json:"supplier"`
	SupplyTotal       int64        `json:"supplyTotal"`
	TaxTotal          int64        `json:"taxTotal"`
	Lines             []taxLineDTO `json:"lines"`
	AmendOfApprovalNo string       `json:"amendOfApprovalNo"`
	AmendReason       string       `json:"amendReason"`
	Remark            string       `json:"remark"`
}

func party(p taxPartyDTO) domain.TaxParty {
	return domain.TaxParty{BizNo: p.BizNo, BranchNo: p.BranchNo, Name: p.Name, Rep: p.Rep, Addr: p.Addr, Email: p.Email}
}

func lineInputs(in []taxLineDTO) []domain.TaxLineInput {
	out := make([]domain.TaxLineInput, len(in))
	for i, l := range in {
		out[i] = domain.TaxLineInput{Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Remark: l.Remark}
	}
	return out
}

func (d taxInputDTO) input() domain.TaxInvoiceInput {
	return domain.TaxInvoiceInput{TaxType: d.TaxType, IssueDate: d.IssueDate, Buyer: party(d.Buyer), Lines: lineInputs(d.Lines), ChargeType: d.ChargeType, Remark: d.Remark}
}

func partyOut(p domain.TaxParty) taxPartyDTO {
	return taxPartyDTO{BizNo: p.BizNo, BranchNo: p.BranchNo, Name: p.Name, Rep: p.Rep, Addr: p.Addr, Email: p.Email}
}

// taxOut 은 응답 한 건이다. 상태·종류 이름은 한국어 라벨을 함께 낸다(화면이 영문 코드를 보이지 않게). 전송 기록의 사유는 한국어 문장이다.
func taxOut(v domain.TaxInvoice, detail bool) map[string]any {
	m := map[string]any{
		"id": v.ID, "no": v.No, "direction": v.Direction, "directionName": domain.TaxDirectionLabels[v.Direction], "taxType": v.TaxType, "taxTypeName": domain.TaxTypeLabels[v.TaxType],
		"status": v.Status, "statusName": domain.TaxStatusLabels[v.Status], "approvalNo": v.ApprovalNo, "issueDate": v.IssueDate,
		"supplier": partyOut(v.Supplier), "buyer": partyOut(v.Buyer), "supplyTotal": v.SupplyTotal, "taxTotal": v.TaxTotal, "total": v.Total,
		"amendOfId": v.AmendOfID, "amendOfNo": v.AmendOfNo, "amendedByNo": v.AmendedByNo, "amendReason": v.AmendReason, "amendReasonName": domain.AmendReasonLabels[v.AmendReason],
		"chargeType": v.ChargeType, "remark": v.Remark, "source": v.Source, "attempts": v.Attempts, "resends": v.Resends, "lastCodeText": v.LastCodeText,
		"matchStatus": v.MatchStatus, "matchResult": v.MatchResult, "issuedAt": v.IssuedAt, "sentAt": v.SentAt, "acceptedAt": v.AcceptedAt, "canceledAt": v.CanceledAt, "cancelReason": v.CancelReason,
	}
	if v.Intercompany != "" {
		m["intercompany"] = v.Intercompany
	}
	if detail {
		lines := make([]taxLineDTO, len(v.Lines))
		for i, l := range v.Lines {
			lines[i] = taxLineDTO{Seq: l.Seq, Day: l.Day, Name: l.Name, Spec: l.Spec, Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Tax: l.Tax, Remark: l.Remark}
		}
		tr := make([]map[string]any, len(v.Transmissions))
		for i, t := range v.Transmissions {
			tr[i] = map[string]any{"attemptNo": t.AttemptNo, "at": t.At, "kind": t.Kind, "result": t.CodeLabel, "httpStatus": t.HTTPStatus}
		}
		m["lines"], m["transmissions"], m["warnings"] = lines, tr, nonNilStrings(v.Warnings)
	}
	return m
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h taxInvoiceHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	page, err := h.svc.List(r.Context(), domain.TaxInvoiceFilter{Direction: q.Get("direction"), Status: q.Get("status"), From: q.Get("from"), To: q.Get("to"),
		Counterparty: q.Get("counterparty"), ApprovalNo: q.Get("approvalNo"), Limit: limit, Offset: offset})
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(page.Rows))
	for i, v := range page.Rows {
		rows[i] = taxOut(v, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": page.Total})
}

func (h taxInvoiceHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, taxOut(v, true))
}

func (h taxInvoiceHandler) create(w http.ResponseWriter, r *http.Request) {
	var in taxInputDTO
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.Create(r.Context(), in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, taxOut(v, true))
}

func (h taxInvoiceHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in taxInputDTO
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.Update(r.Context(), id, in.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, taxOut(v, true))
}

func (h taxInvoiceHandler) act(w http.ResponseWriter, r *http.Request, do func(id int64) (domain.TaxInvoice, error)) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, err := do(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, taxOut(v, true))
}

func (h taxInvoiceHandler) issue(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, func(id int64) (domain.TaxInvoice, error) { return h.svc.Issue(r.Context(), id) })
}

func (h taxInvoiceHandler) resend(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, func(id int64) (domain.TaxInvoice, error) { return h.svc.Resend(r.Context(), id) })
}

type reasonDTO struct {
	Reason string `json:"reason"`
}

func (h taxInvoiceHandler) withReason(w http.ResponseWriter, r *http.Request, do func(id int64, reason string) (domain.TaxInvoice, error)) {
	var in reasonDTO
	if !decode(w, r, &in) {
		return
	}
	h.act(w, r, func(id int64) (domain.TaxInvoice, error) { return do(id, in.Reason) })
}

func (h taxInvoiceHandler) cancel(w http.ResponseWriter, r *http.Request) {
	h.withReason(w, r, func(id int64, reason string) (domain.TaxInvoice, error) { return h.svc.Cancel(r.Context(), id, reason) })
}

func (h taxInvoiceHandler) cancelReceived(w http.ResponseWriter, r *http.Request) {
	h.withReason(w, r, func(id int64, reason string) (domain.TaxInvoice, error) {
		return h.svc.CancelReceived(r.Context(), id, reason)
	})
}

func (h taxInvoiceHandler) amend(w http.ResponseWriter, r *http.Request) {
	h.withReason(w, r, func(id int64, reason string) (domain.TaxInvoice, error) {
		return h.svc.AmendDraft(r.Context(), id, reason)
	})
}

func (h taxInvoiceHandler) receive(w http.ResponseWriter, r *http.Request) {
	var in taxReceiveDTO
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.RegisterReceived(r.Context(), domain.TaxReceivedInput{ApprovalNo: in.ApprovalNo, TaxType: in.TaxType, IssueDate: in.IssueDate, Supplier: party(in.Supplier),
		SupplyTotal: in.SupplyTotal, TaxTotal: in.TaxTotal, Lines: lineInputs(in.Lines), AmendOfApprovalNo: in.AmendOfApprovalNo, AmendReason: in.AmendReason, Remark: in.Remark})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, taxOut(v, true))
}

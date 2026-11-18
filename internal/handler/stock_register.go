// 창고·출고·창고 이동 라우트(B24, 9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterStock 은 /api/warehouses, /api/stock-issues, /api/stock-transfers 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterStock(r contract.Registrar, d deps.Deps) {
	if d.Warehouses != nil {
		h := warehouseHandler{svc: d.Warehouses}
		r.HandleFunc("GET", "/api/warehouses", contract.PermInventoryRead, h.list)
		r.HandleFunc("POST", "/api/warehouses", contract.PermInventoryManage, h.save)
		r.HandleFunc("PUT", "/api/warehouses/{id}", contract.PermInventoryManage, h.save)
		r.HandleFunc("POST", "/api/warehouses/{id}/active", contract.PermInventoryManage, h.setActive)
	}
	if d.StockIssue != nil {
		h := issueHandler{svc: d.StockIssue}
		r.HandleFunc("POST", "/api/stock-issues/propose", contract.PermInventoryManage, h.propose)
		r.HandleFunc("POST", "/api/stock-issues", contract.PermInventoryManage, h.issue)
		r.HandleFunc("GET", "/api/stock-issues", contract.PermInventoryManage, h.list)
		r.HandleFunc("GET", "/api/stock-issues/departments", contract.PermInventoryManage, h.departments)
	}
	if d.StockCount != nil {
		h := countHandler{svc: d.StockCount}
		r.HandleFunc("GET", "/api/stock-counts", contract.PermInventoryManage, h.list)
		r.HandleFunc("POST", "/api/stock-counts", contract.PermInventoryManage, h.create)
		r.HandleFunc("GET", "/api/stock-counts/{id}", contract.PermInventoryManage, h.get)
		r.HandleFunc("PUT", "/api/stock-counts/{id}", contract.PermInventoryManage, h.save)
		r.HandleFunc("POST", "/api/stock-counts/{id}/refresh", contract.PermInventoryManage, h.refresh)
		r.HandleFunc("POST", "/api/stock-counts/{id}/submit", contract.PermInventoryManage, h.submit)
		r.HandleFunc("POST", "/api/stock-counts/{id}/discard", contract.PermInventoryManage, h.discard)
	}
	if d.Ledger != nil {
		r.HandleFunc("GET", "/api/stock-ledger", contract.PermInventoryRead, ledgerHandler{svc: d.Ledger}.get)
	}
	if d.StockTransfer != nil {
		h := transferHandler{svc: d.StockTransfer}
		r.HandleFunc("POST", "/api/stock-transfers/preview", contract.PermInventoryManage, h.preview)
		r.HandleFunc("POST", "/api/stock-transfers", contract.PermInventoryManage, h.transfer)
		r.HandleFunc("GET", "/api/stock-transfers", contract.PermInventoryManage, h.list)
	}
}

func limitParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

type warehouseHandler struct{ svc domain.WarehouseService }

func warehouseDTO(w domain.Warehouse) map[string]any {
	m := map[string]any{"id": w.ID, "code": w.Code, "name": w.Name, "location": w.Location, "active": w.Active, "itemCount": w.ItemCount}
	if w.SiteID != 0 {
		m["siteId"], m["siteName"] = w.SiteID, w.SiteName
	}
	return m
}

func (h warehouseHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = warehouseDTO(x)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h warehouseHandler) save(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code     string `json:"code"`
		Name     string `json:"name"`
		Location string `json:"location"`
		SiteID   int64  `json:"siteId"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.WarehouseInput{Code: b.Code, Name: b.Name, Location: b.Location, SiteID: b.SiteID}
	status := http.StatusCreated
	if r.PathValue("id") != "" {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		in.ID, status = id, http.StatusOK
	}
	wh, err := h.svc.Save(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, status, warehouseDTO(wh))
}

func (h warehouseHandler) setActive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Active bool `json:"active"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.SetActive(r.Context(), id, b.Active); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type issueHandler struct{ svc domain.StockIssueService }

type stockLineBody struct {
	ItemID int64 `json:"itemId"`
	LotID  int64 `json:"lotId"`
	Qty    int64 `json:"qty"`
}

func (h issueHandler) propose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		WarehouseID int64  `json:"warehouseId"`
		ItemID      int64  `json:"itemId"`
		Qty         int64  `json:"qty"`
		Method      string `json:"method"`
	}
	if !decode(w, r, &b) {
		return
	}
	list, err := h.svc.ProposeIssue(r.Context(), domain.IssueProposeInput{WarehouseID: b.WarehouseID, ItemID: b.ItemID, Qty: b.Qty, Method: b.Method})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, a := range list {
		out[i] = map[string]any{"itemId": a.ItemID, "lotId": a.LotID, "lotNo": a.LotNo, "expiry": a.Expiry, "qty": a.Qty, "onHand": a.OnHand, "received": a.Received}
	}
	writeJSON(w, http.StatusOK, out)
}

func slipDTO(s domain.IssueSlip) map[string]any {
	lines := make([]map[string]any, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = map[string]any{"itemId": l.ItemID, "itemName": l.ItemName, "sku": l.SKU, "lotId": l.LotID, "lotNo": l.LotNo, "qty": l.Qty}
	}
	return map[string]any{"id": s.ID, "no": s.No, "departmentId": s.DepartmentID, "departmentName": s.DepartmentName, "warehouseId": s.WarehouseID, "warehouseName": s.WarehouseName,
		"issueDate": s.IssueDate, "method": s.Method, "memo": s.Memo, "actorName": s.ActorName, "createdAt": rfc(s.CreatedAt), "lines": lines}
}

func (h issueHandler) issue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DepartmentID int64           `json:"departmentId"`
		WarehouseID  int64           `json:"warehouseId"`
		IssueDate    string          `json:"issueDate"`
		Method       string          `json:"method"`
		Memo         string          `json:"memo"`
		Lines        []stockLineBody `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.IssueInput{DepartmentID: b.DepartmentID, WarehouseID: b.WarehouseID, IssueDate: b.IssueDate, Method: b.Method, Memo: b.Memo}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.IssueLineInput{ItemID: l.ItemID, LotID: l.LotID, Qty: l.Qty})
	}
	s, err := h.svc.Issue(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, slipDTO(s))
}

func (h issueHandler) departments(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Departments(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, d := range list {
		out[i] = map[string]any{"id": d.ID, "name": d.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h issueHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListRecent(r.Context(), limitParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, s := range list {
		out[i] = slipDTO(s)
	}
	writeJSON(w, http.StatusOK, out)
}

type transferHandler struct{ svc domain.StockTransferService }

type transferBody struct {
	FromID   int64           `json:"fromId"`
	ToID     int64           `json:"toId"`
	MoveDate string          `json:"moveDate"`
	Memo     string          `json:"memo"`
	Lines    []stockLineBody `json:"lines"`
}

func (b transferBody) input() domain.TransferInput {
	in := domain.TransferInput{FromID: b.FromID, ToID: b.ToID, MoveDate: b.MoveDate, Memo: b.Memo}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.TransferLineInput{ItemID: l.ItemID, LotID: l.LotID, Qty: l.Qty})
	}
	return in
}

func (h transferHandler) preview(w http.ResponseWriter, r *http.Request) {
	var b transferBody
	if !decode(w, r, &b) {
		return
	}
	list, err := h.svc.Preview(r.Context(), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = map[string]any{"kind": x.Kind, "warehouseName": x.WarehouseName, "itemName": x.ItemName, "lotNo": x.LotNo, "qty": x.Qty}
		if x.Intercompany { // 법인 간 이동 미리보기(B51): 법인 하나 설치의 응답 모양은 그대로다
			out[i]["intercompany"], out[i]["entityName"], out[i]["noAvg"] = true, x.EntityName, x.NoAvg
			if x.Valued {
				out[i]["unitCost"], out[i]["amount"] = x.UnitCost, x.Amount
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func transferDTO(s domain.TransferSlip) map[string]any {
	lines := make([]map[string]any, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = map[string]any{"itemName": l.ItemName, "sku": l.SKU, "lotNo": l.LotNo, "qty": l.Qty}
	}
	m := map[string]any{"id": s.ID, "no": s.No, "fromId": s.FromID, "fromName": s.FromName, "toId": s.ToID, "toName": s.ToName,
		"moveDate": s.MoveDate, "memo": s.Memo, "actorName": s.ActorName, "createdAt": rfc(s.CreatedAt), "lines": lines}
	if s.Intercompany { // 법인 간 이동(B51): 화면이 서버 판정을 그대로 쓴다. 법인 하나 설치의 응답 모양은 그대로다
		m["intercompany"], m["fromEntity"], m["toEntity"] = true, s.FromEntityName, s.ToEntityName
		if s.Valued {
			m["costAmount"] = s.CostAmount
		}
	}
	return m
}

func (h transferHandler) transfer(w http.ResponseWriter, r *http.Request) {
	var b transferBody
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.Transfer(r.Context(), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, transferDTO(s))
}

func (h transferHandler) list(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("targets") == "1" { // 다른 법인의 도착 창고 후보(B51). 새 경로를 만들지 않고 목록 경로의 조회 조건으로 둔다
		ts, err := h.svc.Targets(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		out := make([]map[string]any, len(ts))
		for i, t := range ts {
			out[i] = map[string]any{"id": t.ID, "code": t.Code, "name": t.Name, "entityId": t.EntityID, "entityName": t.EntityName}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	list, err := h.svc.ListRecent(r.Context(), limitParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, s := range list {
		out[i] = transferDTO(s)
	}
	writeJSON(w, http.StatusOK, out)
}

type countHandler struct{ svc domain.StockCountService }

func countDTO(c domain.StockCount) map[string]any {
	lines := make([]map[string]any, len(c.Lines))
	for i, l := range c.Lines {
		var counted any
		if l.CountedQty != nil {
			counted = *l.CountedQty
		}
		lines[i] = map[string]any{"itemId": l.ItemID, "itemName": l.ItemName, "sku": l.SKU, "lotId": l.LotID, "lotNo": l.LotNo, "systemQty": l.SystemQty, "countedQty": counted, "diff": l.Diff, "reason": l.Reason}
	}
	out := map[string]any{"id": c.ID, "no": c.No, "warehouseId": c.WarehouseID, "warehouseName": c.WarehouseName, "baseDate": c.BaseDate, "status": c.Status,
		"docId": c.DocID, "docNo": c.DocNo, "rejectReason": c.RejectReason, "createdByName": c.CreatedByName, "createdAt": rfc(c.CreatedAt), "lines": lines,
		"diffLines": c.DiffLines, "uncounted": c.Uncounted, "movedSince": c.MovedSince}
	if c.AppliedAt != nil {
		out["appliedAt"] = rfc(*c.AppliedAt)
	}
	return out
}

func (h countHandler) list(w http.ResponseWriter, r *http.Request) {
	f := domain.CountFilter{Status: r.URL.Query().Get("status"), Limit: limitParam(r)}
	if v := r.URL.Query().Get("warehouseId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "창고 번호가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		f.WarehouseID = n
	}
	list, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, c := range list {
		out[i] = countDTO(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h countHandler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		WarehouseID int64 `json:"warehouseId"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.Create(r.Context(), b.WarehouseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, countDTO(c))
}

func (h countHandler) one(w http.ResponseWriter, r *http.Request, fn func(id int64) (domain.StockCount, error)) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	c, err := fn(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, countDTO(c))
}

func (h countHandler) get(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, func(id int64) (domain.StockCount, error) { return h.svc.Get(r.Context(), id) })
}

func (h countHandler) refresh(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, func(id int64) (domain.StockCount, error) { return h.svc.Refresh(r.Context(), id) })
}

func (h countHandler) submit(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, func(id int64) (domain.StockCount, error) { return h.svc.Submit(r.Context(), id) })
}

func (h countHandler) save(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Lines []struct {
			ItemID     int64  `json:"itemId"`
			LotID      int64  `json:"lotId"`
			CountedQty *int64 `json:"countedQty"`
			Reason     string `json:"reason"`
		} `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.CountSaveInput{}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.CountLineInput{ItemID: l.ItemID, LotID: l.LotID, CountedQty: l.CountedQty, Reason: l.Reason})
	}
	h.one(w, r, func(id int64) (domain.StockCount, error) { return h.svc.Save(r.Context(), id, in) })
}

func (h countHandler) discard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Discard(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type ledgerHandler struct{ svc domain.LedgerService }

func (h ledgerHandler) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.LedgerFilter{From: contract.Day(q.Get("from")), To: contract.Day(q.Get("to"))}
	for k, dst := range map[string]*int64{"itemId": &f.ItemID, "warehouseId": &f.WarehouseID, "lotId": &f.LotID} {
		if v := q.Get(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, k+" 가 올바르지 않습니다.", http.StatusBadRequest)
				return
			}
			*dst = n
		}
	}
	for k, dst := range map[string]*int{"page": &f.Page, "pageSize": &f.PageSize} {
		if v := q.Get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				http.Error(w, k+" 가 올바르지 않습니다.", http.StatusBadRequest)
				return
			}
			*dst = n
		}
	}
	p, err := h.svc.Ledger(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(p.Rows))
	for i, x := range p.Rows {
		rows[i] = map[string]any{"date": x.Date, "kind": x.Kind, "kindLabel": x.KindLabel, "warehouse": x.Warehouse, "lotNo": x.LotNo, "docNo": x.DocNo, "in": x.In, "out": x.Out, "balance": x.Balance}
		if x.Valued { // 재고 평가 조회 권한이 있는 호출자에게만 단가·금액이 나간다(서비스가 거른다)
			rows[i]["unitCost"], rows[i]["amount"] = x.UnitCost, x.Amount
		}
		if x.Intercompany { // 법인 간 이동 행(B51): 법인 하나 설치의 응답 모양은 그대로다
			rows[i]["intercompany"], rows[i]["counterEntity"] = true, x.CounterEntity
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"opening": p.Opening, "rows": rows, "totalIn": p.TotalIn, "totalOut": p.TotalOut, "closing": p.Closing, "total": p.Total})
}

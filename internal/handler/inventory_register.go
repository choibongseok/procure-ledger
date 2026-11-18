// (internal/handler/inventory*).
package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// InventoryMaster 는 domain.InventoryService 계약 밖의 품목·거래처 등록·조회다.
// d.Inventory 가 이를 구현할 때만(= service.InventoryService) 해당 라우트를 등록한다.
type InventoryMaster interface {
	CreateItem(ctx context.Context, in service.ItemInput) (domain.Item, error)
	CreateVendor(ctx context.Context, in domain.Vendor) (domain.Vendor, error)
	ListVendors(ctx context.Context) ([]domain.Vendor, error)
}

// RegisterInventory 는 품목·재고·발주·입고 라우트(/api/items, /api/stock, /api/purchase-orders, /api/receipts)를 등록한다.
// 호출: cmd/server/routes_erp.go. d.Inventory 는 domain.InventoryService 이다.
func RegisterInventory(r contract.Registrar, d deps.Deps) {
	if d.Inventory == nil {
		return
	}
	h := inventoryHandler{svc: d.Inventory}
	r.HandleFunc("GET", "/api/items", contract.PermInventoryRead, h.searchItems)
	r.HandleFunc("GET", "/api/items/{id}/stock", contract.PermInventoryRead, h.stock)
	r.HandleFunc("GET", "/api/stock/below-safety", contract.PermInventoryRead, h.belowSafety)
	r.HandleFunc("GET", "/api/stock/expiring", contract.PermInventoryRead, h.expiring)
	r.HandleFunc("GET", "/api/purchase-orders", contract.PermInventoryRead, h.listPOs)
	r.HandleFunc("GET", "/api/purchase-orders/{id}", contract.PermInventoryRead, h.getPO)
	r.HandleFunc("GET", "/api/receipts/{id}", contract.PermInventoryRead, h.getReceipt)
	r.HandleFunc("POST", "/api/receipts", contract.PermInventoryManage, h.receive)
	if m, ok := d.Inventory.(InventoryMaster); ok {
		h.master = m
		r.HandleFunc("POST", "/api/items", contract.PermInventoryManage, h.createItem)
		r.HandleFunc("POST", "/api/vendors", contract.PermInventoryManage, h.createVendor)
		r.HandleFunc("GET", "/api/vendors", contract.PermPurchaseRead, h.listVendors)
	}
	if l, ok := d.Inventory.(vendorLinker); ok { // 거래처의 연결 법인(B44)
		r.HandleFunc("GET", "/api/vendors/links", contract.PermPurchaseRead, func(w http.ResponseWriter, req *http.Request) {
			rows, err := l.ListVendorLinks(req.Context())
			if err != nil {
				writeErr(w, err)
				return
			}
			out := make([]map[string]any, len(rows))
			for i, x := range rows {
				out[i] = map[string]any{"vendorId": x.VendorID, "name": x.Name, "linkedEntityId": x.LinkedEntityID, "linkedEntityName": x.LinkedEntity, "suggestedEntityId": x.SuggestedEntity, "suggestedEntityName": x.SuggestedName}
			}
			writeJSON(w, http.StatusOK, map[string]any{"rows": out})
		})
		r.HandleFunc("PUT", "/api/vendors/{id}/link", contract.PermInventoryManage, func(w http.ResponseWriter, req *http.Request) {
			id, ok := pathID(w, req, "id")
			if !ok {
				return
			}
			var in struct {
				EntityID int64 `json:"entityId"`
			}
			if !decode(w, req, &in) {
				return
			}
			x, err := l.SetVendorLink(req.Context(), id, in.EntityID)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"vendorId": x.VendorID, "name": x.Name, "linkedEntityId": x.LinkedEntityID})
		})
	}
}

// vendorLinker 는 거래처의 연결 법인을 다루는 서비스 쪽 선택 계약이다(*service.InventoryService 가 구현한다).
type vendorLinker interface {
	ListVendorLinks(ctx context.Context) ([]service.VendorLink, error)
	SetVendorLink(ctx context.Context, vendorID, entityID int64) (service.VendorLink, error)
}

type inventoryHandler struct {
	svc    domain.InventoryService
	master InventoryMaster
}

func (h inventoryHandler) searchItems(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.SearchItems(r.Context(), r.URL.Query().Get("q"), queryInt(r, "limit", 50))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]itemDTO, len(items))
	for i, it := range items {
		out[i] = toItemDTO(it)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h inventoryHandler) createItem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SKU         string `json:"sku"`
		Name        string `json:"name"`
		Unit        string `json:"unit"`
		SafetyStock int64  `json:"safetyStock"`
		TrackLot    bool   `json:"trackLot"`
	}
	if !decode(w, r, &b) {
		return
	}
	it, err := h.master.CreateItem(r.Context(), service.ItemInput{SKU: b.SKU, Name: b.Name, Unit: b.Unit, SafetyStock: b.SafetyStock, TrackLot: b.TrackLot})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toItemDTO(it))
}

func (h inventoryHandler) createVendor(w http.ResponseWriter, r *http.Request) {
	var b vendorDTO
	if !decode(w, r, &b) {
		return
	}
	v, err := h.master.CreateVendor(r.Context(), domain.Vendor{Name: b.Name, BusinessNo: b.BusinessNo, AccountNo: b.AccountNo, Phone: b.Phone, Email: b.Email})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, vendorDTO{ID: v.ID, Name: v.Name, BusinessNo: v.BusinessNo, AccountNo: v.AccountNo, Phone: v.Phone, Email: v.Email})
}

func (h inventoryHandler) listVendors(w http.ResponseWriter, r *http.Request) {
	vs, err := h.master.ListVendors(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]vendorDTO, len(vs))
	for i, v := range vs {
		out[i] = vendorDTO{ID: v.ID, Name: v.Name, BusinessNo: v.BusinessNo, AccountNo: v.AccountNo, Phone: v.Phone, Email: v.Email}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h inventoryHandler) stock(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var wh int64
	if v := r.URL.Query().Get("warehouseId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "창고 번호가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		wh = n
	}
	var st domain.ItemStock
	var err error
	if at, ok := h.svc.(interface {
		GetStockAt(context.Context, int64, int64) (domain.ItemStock, error)
	}); ok && wh != 0 {
		st, err = at.GetStockAt(r.Context(), id, wh)
	} else {
		st, err = h.svc.GetStock(r.Context(), id)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toStockDTO(st))
}

func (h inventoryHandler) belowSafety(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListBelowSafety(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	sortKey, desc, ok := sortParams(r, "name", "on_hand")
	if !ok {
		writeErr(w, fmt.Errorf("%w: 정렬 기준은 품목명 또는 현재고만 쓸 수 있습니다", contract.ErrValidation))
		return
	}
	switch sortKey {
	case "name":
		sort.SliceStable(list, func(i, j int) bool {
			return lessBy(list[i].Item.Name < list[j].Item.Name, list[i].Item.Name > list[j].Item.Name, desc)
		})
	case "on_hand":
		sort.SliceStable(list, func(i, j int) bool {
			return lessBy(list[i].OnHand < list[j].OnHand, list[i].OnHand > list[j].OnHand, desc)
		})
	}
	out := make([]stockDTO, len(list))
	for i, s := range list {
		out[i] = toStockDTO(s)
	}
	writeJSON(w, http.StatusOK, out)
}

// sortParams 는 sort·order 쿼리 인자를 화이트리스트로 읽는다. sort 가 없으면 키는 비고(기존 순서), order 의 기본은 asc 다.
// 허용 목록 밖의 sort·order 는 ok=false (호출자가 400).
func sortParams(r *http.Request, allowed ...string) (key string, desc, ok bool) {
	key = r.URL.Query().Get("sort")
	if key != "" {
		found := false
		for _, a := range allowed {
			found = found || a == key
		}
		if !found {
			return "", false, false
		}
	}
	switch r.URL.Query().Get("order") {
	case "", "asc":
	case "desc":
		desc = true
	default:
		return "", false, false
	}
	return key, desc, true
}

func lessBy(asc, descLess, desc bool) bool {
	if desc {
		return descLess
	}
	return asc
}

func (h inventoryHandler) expiring(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListExpiring(r.Context(), queryInt(r, "days", 30))
	if err != nil {
		writeErr(w, err)
		return
	}
	sortKey, desc, ok := sortParams(r, "name", "expiry_date")
	if !ok {
		writeErr(w, fmt.Errorf("%w: 정렬 기준은 품목명 또는 유효기한만 쓸 수 있습니다", contract.ErrValidation))
		return
	}
	switch sortKey {
	case "name":
		sort.SliceStable(list, func(i, j int) bool {
			return lessBy(list[i].Item.Name < list[j].Item.Name, list[i].Item.Name > list[j].Item.Name, desc)
		})
	case "expiry_date":
		sort.SliceStable(list, func(i, j int) bool {
			return lessBy(list[i].Lot.ExpiryDate < list[j].Lot.ExpiryDate, list[i].Lot.ExpiryDate > list[j].Lot.ExpiryDate, desc)
		})
	}
	type row struct {
		Item         itemDTO `json:"item"`
		Lot          lotDTO  `json:"lot"`
		DaysToExpire int     `json:"daysToExpire"`
	}
	out := make([]row, len(list))
	for i, e := range list {
		out[i] = row{Item: toItemDTO(e.Item), Lot: lotDTO{LotID: e.Lot.LotID, LotNo: e.Lot.LotNo, ExpiryDate: e.Lot.ExpiryDate, Quantity: e.Lot.Quantity}, DaysToExpire: e.DaysToExpire}
	}
	writeJSON(w, http.StatusOK, out)
}

// poCounter 는 계약 밖 확장이다: 같은 조건의 발주 전체 건수(service.InventoryService 가 구현).
type poCounter interface {
	CountPurchaseOrders(ctx context.Context, f domain.POFilter) (int, error)
}

func (h inventoryHandler) listPOs(w http.ResponseWriter, r *http.Request) {
	f := domain.POFilter{Status: domain.POStatus(r.URL.Query().Get("status")), Limit: queryInt(r, "limit", 50), Offset: queryInt(r, "offset", 0)}
	list, err := h.svc.ListPurchaseOrders(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if c, ok := h.svc.(poCounter); ok {
		n, err := c.CountPurchaseOrders(r.Context(), f)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set(totalCountHeader, strconv.Itoa(n))
	}
	out := make([]poDTO, len(list))
	for i, p := range list {
		out[i] = toPODTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h inventoryHandler) getPO(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	po, err := h.svc.GetPurchaseOrder(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPODTO(po))
}

func (h inventoryHandler) getReceipt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rc, err := h.svc.GetReceipt(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toReceiptDTO(rc))
}

func (h inventoryHandler) receive(w http.ResponseWriter, r *http.Request) {
	var b struct {
		POID        int64 `json:"poId"`
		WarehouseID int64 `json:"warehouseId"`
		Lines       []struct {
			POLineID   int64  `json:"poLineId"`
			ItemID     int64  `json:"itemId"`
			Quantity   int64  `json:"quantity"`
			LotNo      string `json:"lotNo"`
			ExpiryDate string `json:"expiryDate"`
		} `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.ReceiveInput{POID: b.POID, WarehouseID: b.WarehouseID}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.ReceiptLine{POLineID: l.POLineID, ItemID: l.ItemID, Quantity: l.Quantity, LotNo: l.LotNo, ExpiryDate: l.ExpiryDate})
	}
	rc, err := h.svc.Receive(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toReceiptDTO(rc))
}

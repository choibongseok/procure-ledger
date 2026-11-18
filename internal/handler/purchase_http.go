// 구매요청·결재·재고 핸들러가 함께 쓰는 JSON 변환·오류 매핑이다.
// 핸들러는 요청을 해석해 서비스를 부를 뿐 역할을 비교하지 않는다(권한은 라우트 등록의 선언과 서비스가 강제).
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const maxBody = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("handler: 응답 인코딩 실패: %v", err)
	}
}

// writeErr 는 계약 오류를 상태 코드와 한국어 문구(text/plain)로 옮긴다. 내부 오류 내용은 밖으로 내지 않는다.
func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	msg := "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		code, msg = http.StatusUnauthorized, err.Error()
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSelfApproval):
		code, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, contract.ErrNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, contract.ErrValidation):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, contract.ErrInvalidState), errors.Is(err, contract.ErrOverReceipt):
		code, msg = http.StatusConflict, err.Error()
	default:
		log.Printf("handler: 내부 오류: %v", err)
	}
	http.Error(w, msg, code)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "요청 본문이 올바른 JSON 이 아닙니다.", http.StatusBadRequest)
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "경로의 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---- 응답 DTO (camelCase) ----

type prLineDTO struct {
	ID        int64  `json:"id"`
	ItemID    int64  `json:"itemId"`
	ItemName  string `json:"itemName"`
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unitPrice"`
	Amount    int64  `json:"amount"`
}

type stepDTO struct {
	Seq        int    `json:"seq"`
	Kind       string `json:"kind"`
	ApproverID int64  `json:"approverId"`
	Status     string `json:"status"`
	Comment    string `json:"comment"`
	DecidedAt  string `json:"decidedAt"`
	// B21: 상신 당시 결재선 스냅샷과 현재 결재자(재지정 반영). 도입 이전 건은 비어 있다.
	Title          string `json:"title"`
	UnitID         int64  `json:"unitId"`
	UnitPath       string `json:"unitPath"`
	AssigneeID     int64  `json:"assigneeId"`
	AssigneeName   string `json:"assigneeName"`
	Acting         bool   `json:"acting"`
	RaisedNote     string `json:"raisedNote"`
	LayerNote      string `json:"layerNote"`
	ReassignedFrom string `json:"reassignedFrom"`
	HeadChangedTo  string `json:"headChangedTo"`
	// B23: 역할(approve·agree), 단계 묶음(같은 번호의 합의는 병렬), 필수 단계 여부.
	Role     string `json:"role"`
	Stage    int    `json:"stage"`
	Required bool   `json:"required"`
	// B23 단계 3: 전결 최종 결재 단계 표시와 건너뜀 사유("전결").
	FinalAuthority bool   `json:"finalAuthority,omitempty"`
	SkipReason     string `json:"skipReason,omitempty"`
	// B29: 대결로 처리된 단계의 원결재자와 실제 처리자(출력 결재란의 "(대결)" 표기).
	OnBehalfOfID   int64  `json:"onBehalfOfId,omitempty"`
	OnBehalfOfName string `json:"onBehalfOfName,omitempty"`
	ApproverName   string `json:"approverName,omitempty"`
}

type prDTO struct {
	ID              int64       `json:"id"`
	No              string      `json:"no"`
	RequesterID     int64       `json:"requesterId"`
	RequesterName   string      `json:"requesterName"`
	DepartmentID    int64       `json:"departmentId"`
	VendorID        int64       `json:"vendorId"`
	Title           string      `json:"title"`
	Reason          string      `json:"reason"`
	Status          string      `json:"status"`
	TotalAmount     int64       `json:"totalAmount"`
	RejectReason    string      `json:"rejectReason"`
	Lines           []prLineDTO `json:"lines"`
	Steps           []stepDTO   `json:"steps"`
	PurchaseOrderID int64       `json:"purchaseOrderId"`
	// 위임받은 결재 건 표시용(결재함에서만 채워진다).
	DelegatedFromID   int64  `json:"delegatedFromId,omitempty"`
	DelegatedFromName string `json:"delegatedFromName,omitempty"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
	// 진행 중인데 지금 차례 단계를 결재할 사람이 없을 때 true(요청자 화면 표시·회수 안내용).
	NoApprover bool `json:"noApprover,omitempty"`
	// B21: 귀속 조직과 상신 당시 조직 스냅샷(없으면 빈 값).
	OwnerUnitID       int64  `json:"ownerUnitId"`
	RequesterUnitPath string `json:"requesterUnitPath"`
	LineAsOf          string `json:"lineAsOf"`
	Round             int    `json:"round"`
	// B23: 문서 양식(구매요청은 구매 품의서).
	FormCode string `json:"formCode"`
	FormName string `json:"formName"`
	FormKind string `json:"formKind"`
	// B23 양식 문서: 필드 값(민감 필드는 열람자에 따라 마스킹), 긴급, 보존 기한, 참조·수신 대상.
	Fields          map[string]any    `json:"fields,omitempty"`
	Urgent          bool              `json:"urgent,omitempty"`
	RetentionUntil  string            `json:"retentionUntil,omitempty"`
	DisposedAt      string            `json:"disposedAt,omitempty"`
	Recipients      []recipientDTO    `json:"recipients,omitempty"`
	Attachments     []attachmentDTO   `json:"attachments,omitempty"`
	FormFields      []domain.FieldDef `json:"formFields,omitempty"`
	FormBody        string            `json:"formBody,omitempty"`
	DraftEdits      []lineEditDTO     `json:"draftEdits,omitempty"`
	EditNotes       []string          `json:"editNotes,omitempty"`
	Authority       *authorityDTO     `json:"authority,omitempty"`
	CanCancelSubmit bool              `json:"canCancelSubmit,omitempty"`
	Editable        bool              `json:"editable"`
	// B25: 희망 납기·납품처, 상신 때 판정한 예산 스냅샷(판정 전이면 없다).
	DueDate       string         `json:"dueDate"`
	DeliveryPlace string         `json:"deliveryPlace"`
	Budget        *budgetSnapDTO `json:"budget,omitempty"`
	// B25 단계 2: 선택한 견적서 번호와 최저가가 아닌 선택의 사유.
	QuoteID     int64  `json:"quoteId,omitempty"`
	QuoteReason string `json:"quoteReason,omitempty"`
}

type attachmentDTO struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type recipientDTO struct {
	Kind       string `json:"kind"`
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Note       string `json:"note"`
}

func toPRDTO(p domain.PurchaseRequest) prDTO {
	d := prDTO{
		ID: p.ID, No: p.No, RequesterID: p.RequesterID, RequesterName: p.RequesterName, DepartmentID: p.DepartmentID,
		VendorID: p.VendorID, Title: p.Title, Reason: p.Reason, Status: string(p.Status), TotalAmount: p.TotalAmount,
		RejectReason: p.RejectReason, PurchaseOrderID: p.PurchaseOrderID, DelegatedFromID: p.DelegatedFromID, DelegatedFromName: p.DelegatedFromName, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt),
		Lines: []prLineDTO{}, Steps: []stepDTO{},
		OwnerUnitID: p.OwnerUnitID, RequesterUnitPath: p.RequesterUnitPath, LineAsOf: p.LineAsOf, Round: p.Round, FormCode: p.FormCode, FormName: p.FormName, FormKind: p.FormKind,
		Fields: p.Fields, Urgent: p.Urgent, RetentionUntil: p.RetentionUntil, DisposedAt: p.DisposedAt, FormFields: p.FormFields, FormBody: p.FormBody,
		DueDate: p.DueDate, DeliveryPlace: p.DeliveryPlace, Budget: toBudgetSnapDTO(p.Budget), QuoteID: p.QuoteID, QuoteReason: p.QuoteReason,
	}
	for _, e := range p.DraftEdits {
		d.DraftEdits = append(d.DraftEdits, lineEditDTO{Role: e.Role, TargetType: e.TargetType, TargetID: e.TargetID, Group: e.Group, Name: e.Name})
	}
	d.EditNotes = p.EditNotes
	for _, a := range p.Attachments {
		d.Attachments = append(d.Attachments, attachmentDTO{ID: a.ID, Filename: a.Filename, Size: a.Size})
	}
	for _, r := range p.Recipients {
		d.Recipients = append(d.Recipients, recipientDTO{Kind: r.Kind, TargetType: r.TargetType, TargetID: r.TargetID, Name: r.Name, Path: r.Path, Note: r.Note})
	}
	for _, l := range p.Lines {
		d.Lines = append(d.Lines, prLineDTO{ID: l.ID, ItemID: l.ItemID, ItemName: l.ItemName, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Amount})
	}
	d.Authority = toAuthorityDTO(p.Authority)
	d.CanCancelSubmit = p.CanCancelSubmit
	d.Editable = p.Editable
	for _, s := range p.Steps {
		sd := stepDTO{Seq: s.Seq, Kind: string(s.Kind), ApproverID: s.ApproverID, Status: string(s.Status), Comment: s.Comment,
			Title: s.Title, UnitID: s.UnitID, UnitPath: s.UnitPath, AssigneeID: s.AssigneeID, AssigneeName: s.AssigneeName, Acting: s.Acting,
			RaisedNote: s.RaisedNote, LayerNote: s.LayerNote, ReassignedFrom: s.ReassignedFrom, HeadChangedTo: s.HeadChangedTo, Role: s.Role, Stage: s.Stage, Required: s.Required,
			FinalAuthority: s.FinalAuthority, SkipReason: s.SkipReason, OnBehalfOfID: s.OnBehalfOfID, OnBehalfOfName: s.OnBehalfOfName, ApproverName: s.ApproverName}
		if s.DecidedAt != nil {
			sd.DecidedAt = ts(*s.DecidedAt)
		}
		d.Steps = append(d.Steps, sd)
	}
	return d
}

func toPRDTOs(ps []domain.PurchaseRequest) []prDTO {
	out := make([]prDTO, len(ps))
	for i, p := range ps {
		out[i] = toPRDTO(p)
	}
	return out
}

type historyDTO struct {
	ID        int64  `json:"id"`
	PRID      int64  `json:"prId"`
	Seq       int    `json:"seq"`
	ActorID   int64  `json:"actorId"`
	ActorName string `json:"actorName"`
	// 위임 결재면 위임자 ID(아니면 생략).
	OnBehalfOfID   int64  `json:"onBehalfOfId,omitempty"`
	OnBehalfOfName string `json:"onBehalfOfName,omitempty"`
	Action         string `json:"action"`
	Comment        string `json:"comment"`
	At             string `json:"at"`
}

type itemDTO struct {
	ID          int64  `json:"id"`
	SKU         string `json:"sku"`
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	SafetyStock int64  `json:"safetyStock"`
	TrackLot    bool   `json:"trackLot"`
}

func toItemDTO(i domain.Item) itemDTO {
	return itemDTO{ID: i.ID, SKU: i.SKU, Name: i.Name, Unit: i.Unit, SafetyStock: i.SafetyStock, TrackLot: i.TrackLot}
}

type lotDTO struct {
	LotID      int64  `json:"lotId"`
	LotNo      string `json:"lotNo"`
	ExpiryDate string `json:"expiryDate"`
	Quantity   int64  `json:"quantity"`
}

type stockDTO struct {
	Item        itemDTO  `json:"item"`
	OnHand      int64    `json:"onHand"`
	Lots        []lotDTO `json:"lots"`
	BelowSafety bool     `json:"belowSafety"`
	// B24: 창고별 재고(없으면 빈 배열).
	ByWarehouse   []warehouseQtyDTO `json:"byWarehouse"`
	WarehouseLots []warehouseLotDTO `json:"warehouseLots"`
	// B27: 평균 단가·재고 금액(재고 평가 조회 권한이 있고 평가가 있을 때만 나간다).
	AvgCost     *int64 `json:"avgCost,omitempty"`
	StockAmount *int64 `json:"stockAmount,omitempty"`
	// B51: 전체 모드(그룹 범위)에서는 법인별로 나눈 평균 단가·재고 금액(단가는 합산하지 않는다).
	Valuations []entityValuationDTO `json:"valuations,omitempty"`
}

type entityValuationDTO struct {
	EntityID    int64  `json:"entityId"`
	EntityName  string `json:"entityName"`
	AvgCost     int64  `json:"avgCost"`
	StockAmount int64  `json:"stockAmount"`
}

type warehouseLotDTO struct {
	WarehouseID   int64  `json:"warehouseId"`
	WarehouseName string `json:"warehouseName"`
	LotID         int64  `json:"lotId"`
	LotNo         string `json:"lotNo"`
	ExpiryDate    string `json:"expiryDate"`
	Quantity      int64  `json:"quantity"`
}

type warehouseQtyDTO struct {
	WarehouseID   int64  `json:"warehouseId"`
	WarehouseName string `json:"warehouseName"`
	OnHand        int64  `json:"onHand"`
}

func toStockDTO(s domain.ItemStock) stockDTO {
	d := stockDTO{Item: toItemDTO(s.Item), OnHand: s.OnHand, BelowSafety: s.BelowSafety, Lots: []lotDTO{}, ByWarehouse: []warehouseQtyDTO{}, WarehouseLots: []warehouseLotDTO{}}
	for _, l := range s.WarehouseLots {
		d.WarehouseLots = append(d.WarehouseLots, warehouseLotDTO{WarehouseID: l.WarehouseID, WarehouseName: l.WarehouseName, LotID: l.LotID, LotNo: l.LotNo, ExpiryDate: l.ExpiryDate, Quantity: l.Quantity})
	}
	for _, w := range s.ByWarehouse {
		d.ByWarehouse = append(d.ByWarehouse, warehouseQtyDTO{WarehouseID: w.WarehouseID, WarehouseName: w.WarehouseName, OnHand: w.OnHand})
	}
	for _, l := range s.Lots {
		d.Lots = append(d.Lots, lotDTO{LotID: l.LotID, LotNo: l.LotNo, ExpiryDate: l.ExpiryDate, Quantity: l.Quantity})
	}
	if s.HasValuation {
		a, m := s.AvgCost, s.StockAmount
		d.AvgCost, d.StockAmount = &a, &m
	}
	for _, v := range s.ValuationByEntity {
		d.Valuations = append(d.Valuations, entityValuationDTO{EntityID: v.EntityID, EntityName: v.EntityName, AvgCost: v.AvgCost, StockAmount: v.StockAmount})
	}
	return d
}

type poLineDTO struct {
	ID          int64  `json:"id"`
	ItemID      int64  `json:"itemId"`
	ItemName    string `json:"itemName"`
	Quantity    int64  `json:"quantity"`
	ReceivedQty int64  `json:"receivedQty"`
	UnitPrice   int64  `json:"unitPrice"`
}

type poDTO struct {
	ID          int64       `json:"id"`
	No          string      `json:"no"`
	PRID        int64       `json:"prId"`
	VendorID    int64       `json:"vendorId"`
	Status      string      `json:"status"`
	TotalAmount int64       `json:"totalAmount"`
	Lines       []poLineDTO `json:"lines"`
	CreatedAt   string      `json:"createdAt"`
	// B25: 구매요청에서 복사한 희망 납기(없으면 빈 값)와 납품처.
	DueDate       string `json:"dueDate"`
	DeliveryPlace string `json:"deliveryPlace"`
	// 거래처가 그룹의 다른 법인이면 그 법인 이름(법인 간 거래 배지, B44). 아니면 빠진다.
	Intercompany string `json:"intercompany,omitempty"`
}

func toPODTO(p domain.PurchaseOrder) poDTO {
	d := poDTO{ID: p.ID, No: p.No, PRID: p.PRID, VendorID: p.VendorID, Status: string(p.Status), TotalAmount: p.TotalAmount, CreatedAt: ts(p.CreatedAt), Lines: []poLineDTO{}, DueDate: p.DueDate, DeliveryPlace: p.DeliveryPlace, Intercompany: p.Intercompany}
	for _, l := range p.Lines {
		d.Lines = append(d.Lines, poLineDTO{ID: l.ID, ItemID: l.ItemID, ItemName: l.ItemName, Quantity: l.Quantity, ReceivedQty: l.ReceivedQty, UnitPrice: l.UnitPrice})
	}
	return d
}

type receiptLineDTO struct {
	POLineID   int64  `json:"poLineId"`
	ItemID     int64  `json:"itemId"`
	Quantity   int64  `json:"quantity"`
	LotNo      string `json:"lotNo"`
	ExpiryDate string `json:"expiryDate"`
}

type receiptDTO struct {
	ID         int64            `json:"id"`
	POID       int64            `json:"poId"`
	ReceivedBy int64            `json:"receivedBy"`
	ReceivedAt string           `json:"receivedAt"`
	Lines      []receiptLineDTO `json:"lines"`
}

func toReceiptDTO(r domain.Receipt) receiptDTO {
	d := receiptDTO{ID: r.ID, POID: r.POID, ReceivedBy: r.ReceivedBy, ReceivedAt: ts(r.ReceivedAt), Lines: []receiptLineDTO{}}
	for _, l := range r.Lines {
		d.Lines = append(d.Lines, receiptLineDTO{POLineID: l.POLineID, ItemID: l.ItemID, Quantity: l.Quantity, LotNo: l.LotNo, ExpiryDate: l.ExpiryDate})
	}
	return d
}

type vendorDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	BusinessNo string `json:"businessNo"`
	AccountNo  string `json:"accountNo"`
	Phone      string `json:"phone"`
	Email      string `json:"email"`
}

package apiv1

import (
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 응답 표현(명세 7.4). 필드 이름은 snake_case 고정이고 번호·금액은 정수, 시각은 RFC3339 UTC 문자열이다.
// 사람 정보(작성자·승인자·사번·연락처)는 어떤 표현에도 없다. OpenAPI 스키마는 이 구조체에서 생성한다(D14).

// Item 은 품목과 현재 재고(원장 합)다.
type Item struct {
	ID          int64  `json:"id" doc:"품목 번호"`
	SKU         string `json:"sku" doc:"품목 코드"`
	Name        string `json:"name" doc:"품목 이름"`
	Unit        string `json:"unit" doc:"단위"`
	SafetyStock int64  `json:"safety_stock" doc:"안전재고"`
	TrackLot    bool   `json:"track_lot" doc:"로트 관리 여부"`
	OnHand      int64  `json:"on_hand" doc:"현재 재고 수량(원장 합)"`
}

// Vendor 는 거래처다. 사업자번호·계좌·전화·이메일은 API 채널이 원문을 낼 수 없어 항상 가려진 값이다.
type Vendor struct {
	ID         int64  `json:"id" doc:"거래처 번호"`
	Name       string `json:"name" doc:"거래처 이름"`
	BusinessNo string `json:"business_no" doc:"사업자번호(가려진 값)"`
	AccountNo  string `json:"account_no" doc:"계좌번호(가려진 값)"`
	Phone      string `json:"phone" doc:"전화(가려진 값)"`
	Email      string `json:"email" doc:"이메일(가려진 값)"`
}

// POLine 은 발주 줄이다.
type POLine struct {
	ItemID           int64 `json:"item_id" doc:"품목 번호"`
	Quantity         int64 `json:"quantity" doc:"발주 수량"`
	UnitPrice        int64 `json:"unit_price" doc:"단가(원)"`
	ReceivedQuantity int64 `json:"received_quantity" doc:"지금까지 입고한 수량"`
}

// PurchaseOrder 는 발주다(구매요청 본문·결재 이력은 내지 않는다).
type PurchaseOrder struct {
	ID        int64    `json:"id" doc:"발주 번호(내부)"`
	No        string   `json:"no" doc:"발주 번호(PO-000001 형식)"`
	VendorID  int64    `json:"vendor_id" doc:"거래처 번호(없으면 0)"`
	Status    string   `json:"status" doc:"open | partial | received"`
	CreatedAt string   `json:"created_at" doc:"발주 시각(RFC3339 UTC)"`
	DueDate   string   `json:"due_date" doc:"희망 납기(YYYY-MM-DD, 없으면 빈 값)"`
	Lines     []POLine `json:"lines" doc:"발주 줄"`
}

// ReceiptLine 은 입고 줄이다.
type ReceiptLine struct {
	POLineID int64  `json:"po_line_id" doc:"발주 줄 번호"`
	ItemID   int64  `json:"item_id" doc:"품목 번호"`
	LotNo    string `json:"lot_no" doc:"로트 번호(없으면 빈 값)"`
	Quantity int64  `json:"quantity" doc:"입고 수량"`
}

// Receipt 는 입고 한 건이다(입고자 정보 없음).
type Receipt struct {
	ID         int64         `json:"id" doc:"입고 번호"`
	POID       int64         `json:"po_id" doc:"발주 번호(내부)"`
	PONo       string        `json:"po_no" doc:"발주 번호(PO-000001 형식)"`
	ReceivedAt string        `json:"received_at" doc:"입고 시각(RFC3339 UTC)"`
	Lines      []ReceiptLine `json:"lines" doc:"입고 줄"`
}

// TaxInvoice 는 세금계산서 한 건이다(B43). 사업자번호는 항상 가린 값이고 이메일·주소·대표자·품목 이름은 내지 않는다.
type TaxInvoice struct {
	ID          int64  `json:"id" doc:"세금계산서 번호(내부)"`
	No          string `json:"no" doc:"세금계산서 번호(TI-000001 형식)"`
	Direction   string `json:"direction" doc:"sale(매출) | purchase(매입)"`
	TaxType     string `json:"tax_type" doc:"taxable(과세) | zero(영세율) | exempt(면세)"`
	Status      string `json:"status" doc:"draft | issued | sent | accepted | failed | canceled"`
	ApprovalNo  string `json:"approval_no" doc:"국세청 승인번호(24자, 아직 없으면 빈 값)"`
	IssueDate   string `json:"issue_date" doc:"작성일(YYYY-MM-DD)"`
	SupplierBiz string `json:"supplier_biz_no" doc:"공급자 사업자번호(가린 값)"`
	SupplierNm  string `json:"supplier_name" doc:"공급자 상호"`
	BuyerBiz    string `json:"buyer_biz_no" doc:"공급받는자 사업자번호(가린 값)"`
	BuyerNm     string `json:"buyer_name" doc:"공급받는자 상호"`
	SupplyTotal int64  `json:"supply_total" doc:"공급가액(원)"`
	TaxTotal    int64  `json:"tax_total" doc:"세액(원)"`
	Total       int64  `json:"total" doc:"합계(원)"`
	AmendOfNo   string `json:"amend_of_no,omitempty" doc:"수정세금계산서면 정정하는 원 건의 번호"`
	AmendReason string `json:"amend_reason,omitempty" doc:"수정 사유 코드(수정세금계산서만)"`
	LineCount   int    `json:"line_count" doc:"품목 줄 수"`
}

// JournalLine 은 전표 줄이다.
type JournalLine struct {
	Seq         int    `json:"seq" doc:"줄 순번"`
	AccountCode string `json:"account_code" doc:"계정 코드"`
	Side        string `json:"side" doc:"D(차변) | C(대변)"`
	Amount      int64  `json:"amount" doc:"금액(원)"`
	VendorID    int64  `json:"vendor_id,omitempty" doc:"거래처 번호(채무 줄)"`
	Memo        string `json:"memo,omitempty" doc:"줄 메모"`
}

// Journal 은 전표다(작성자·승인자는 내지 않는다).
type Journal struct {
	ID          int64         `json:"id" doc:"전표 번호(내부)"`
	No          string        `json:"no" doc:"전표 번호"`
	Kind        string        `json:"kind" doc:"전표 종류"`
	Status      string        `json:"status" doc:"draft | requested | approved | canceled"`
	PostingDate string        `json:"posting_date" doc:"전기일(YYYY-MM-DD)"`
	TradeDate   string        `json:"trade_date" doc:"거래일(YYYY-MM-DD)"`
	TotalAmount int64         `json:"total_amount" doc:"합계 금액(원)"`
	SourceType  string        `json:"source_type" doc:"출처 종류(없으면 빈 값)"`
	SourceID    int64         `json:"source_id" doc:"출처 번호"`
	Lines       []JournalLine `json:"lines" doc:"전표 줄"`
}

// Payable 은 매입 채무 항목이다.
type Payable struct {
	ID        int64  `json:"id" doc:"채무 항목 번호"`
	VendorID  int64  `json:"vendor_id" doc:"거래처 번호"`
	JournalNo string `json:"journal_no" doc:"원 전표 번호"`
	TradeDate string `json:"trade_date" doc:"거래일(YYYY-MM-DD)"`
	DueDate   string `json:"due_date" doc:"만기(YYYY-MM-DD)"`
	Amount    int64  `json:"amount" doc:"채무 금액(원)"`
	Paid      int64  `json:"paid_amount" doc:"지급한 금액(원)"`
	Status    string `json:"status" doc:"open | partial | paid | canceled"`
}

// Change 는 변경 피드 한 줄이다(값 없음).
type Change struct {
	Seq      int64  `json:"seq" doc:"피드 번호(증가 순서 = 커밋 순서)"`
	SeqStr   string `json:"seq_str" doc:"seq 의 문자열 표현(자릿수가 큰 클라이언트용)"`
	At       string `json:"at" doc:"기록 시각(RFC3339 UTC)"`
	Resource string `json:"resource" doc:"item | vendor | purchase_order | receipt | journal | payable"`
	ID       int64  `json:"id" doc:"대상 번호"`
	Op       string `json:"op" doc:"created | updated | status_changed | canceled"`
	Status   string `json:"status,omitempty" doc:"새 상태 코드(상태가 있는 자원만)"`
}

// ChangeHead 는 현재 피드 위치다(최초 적재 시작점).
type ChangeHead struct {
	Cursor string `json:"cursor" doc:"현재 가장 큰 번호의 커서"`
}

// ErrorBody 는 오류 응답이다. 메시지에 내부 식별자·SQL·원문 값은 없다.
type ErrorBody struct {
	Code      string `json:"code" doc:"unauthenticated | forbidden_scope | not_found | invalid_argument | rate_limited | cursor_expired | conflict | unavailable | api_disabled"`
	Message   string `json:"message" doc:"한국어 설명"`
	RequestID string `json:"request_id" doc:"X-Request-Id 와 같은 값"`
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toItem(v domain.APIItem) Item {
	return Item{ID: v.ID, SKU: v.SKU, Name: v.Name, Unit: v.Unit, SafetyStock: v.SafetyStock, TrackLot: v.TrackLot, OnHand: v.OnHand}
}

func toVendor(v domain.Vendor) Vendor {
	return Vendor{ID: v.ID, Name: v.Name, BusinessNo: v.BusinessNo, AccountNo: v.AccountNo, Phone: v.Phone, Email: v.Email}
}

func toPO(po domain.PurchaseOrder) PurchaseOrder {
	out := PurchaseOrder{ID: po.ID, No: po.No, VendorID: po.VendorID, Status: string(po.Status), CreatedAt: ts(po.CreatedAt), DueDate: po.DueDate, Lines: []POLine{}}
	for _, l := range po.Lines {
		out.Lines = append(out.Lines, POLine{ItemID: l.ItemID, Quantity: l.Quantity, UnitPrice: l.UnitPrice, ReceivedQuantity: l.ReceivedQty})
	}
	return out
}

func toReceipt(r domain.APIReceipt) Receipt {
	out := Receipt{ID: r.ID, POID: r.POID, PONo: r.PONo, ReceivedAt: ts(r.ReceivedAt), Lines: []ReceiptLine{}}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, ReceiptLine{POLineID: l.POLineID, ItemID: l.ItemID, LotNo: l.LotNo, Quantity: l.Quantity})
	}
	return out
}

func toJournal(j domain.Journal) Journal {
	out := Journal{ID: j.ID, No: j.No, Kind: j.Kind, Status: j.Status, PostingDate: j.PostingDate, TradeDate: j.TradeDate, TotalAmount: j.TotalAmount,
		SourceType: j.SourceType, SourceID: j.SourceID, Lines: []JournalLine{}}
	for _, l := range j.Lines {
		out.Lines = append(out.Lines, JournalLine{Seq: l.Seq, AccountCode: l.AccountCode, Side: l.Side, Amount: l.Amount, VendorID: l.VendorID, Memo: l.Memo})
	}
	return out
}

func toPayable(p domain.APIPayable) Payable {
	return Payable{ID: p.ID, VendorID: p.VendorID, JournalNo: p.JournalNo, TradeDate: p.TradeDate, DueDate: p.DueDate, Amount: p.Amount, Paid: p.Paid, Status: p.Status}
}

func toTaxInvoice(v domain.APITaxInvoice) TaxInvoice {
	return TaxInvoice{ID: v.ID, No: v.No, Direction: v.Direction, TaxType: v.TaxType, Status: v.Status, ApprovalNo: v.ApprovalNo, IssueDate: v.IssueDate, SupplierBiz: v.SupplierBiz, SupplierNm: v.SupplierNm,
		BuyerBiz: v.BuyerBiz, BuyerNm: v.BuyerNm, SupplyTotal: v.SupplyTotal, TaxTotal: v.TaxTotal, Total: v.Total, AmendOfNo: v.AmendOfNo, AmendReason: v.AmendReason, LineCount: v.LineCount}
}

func toChange(e changes.Event) Change {
	return Change{Seq: e.Seq, SeqStr: itoa(e.Seq), At: e.At, Resource: e.Resource, ID: e.ObjectID, Op: e.Op, Status: e.Status}
}

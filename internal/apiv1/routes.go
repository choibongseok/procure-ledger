package apiv1

import (
	"net/http"
	"reflect"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Param 은 쿼리 매개변수 한 개의 선언이다(OpenAPI 생성이 쓴다).
type Param struct {
	Name     string
	Desc     string
	Type     string // integer | string
	Required bool
	In       string // query(기본) | path
}

// RouteDef 는 /api/v1 라우트 한 개의 선언이다. 이 표가 라우트 등록과 OpenAPI 문서의 유일한 정본이다(D14): 라우트를 더하면 문서가 같이 바뀌고,
// 문서와 코드가 어긋나면 시험이 실패한다.
type RouteDef struct {
	Method  string
	Path    string
	Scope   string // 필요한 범위 이름("" 이면 인증 없는 공개 경로)
	Summary string
	Params  []Param
	Item    reflect.Type // 응답 data 의 항목(또는 단건) 형. nil 이면 응답이 따로 정해진 형(openapi)
	Kind    string       // list | changes | head | openapi | write
	// 쓰기 라우트(Kind "write")의 요청 본문 형, 성공 상태 코드, Idempotency-Key 를 받는지.
	Req    reflect.Type
	Status int
	Idem   bool
}

var listParamsDef = []Param{
	{Name: "limit", Desc: "한 번에 받을 건수(1~200, 기본 100)", Type: "integer"},
	{Name: "after", Desc: "마지막으로 받은 번호. 이 번호 다음부터 번호 오름차순으로 읽는다(기본 0 = 처음부터)", Type: "integer"},
	{Name: "cursor", Desc: "이전 응답의 next_cursor(p1.<번호>). after 대신 쓸 수 있다", Type: "string"},
	{Name: "id", Desc: "한 건만 읽는다(웹훅 본문 api_url 의 ?id=). 이 번호의 항목만 돌려주고 없으면 빈 목록이다. 주면 after·cursor·limit 는 무시한다", Type: "integer"},
}

// Routes 는 /api/v1 의 모든 라우트 선언이다.
var Routes = []RouteDef{
	{Method: "GET", Path: "/api/v1/items", Scope: "items:read", Summary: "품목과 현재 재고 수량 목록", Params: listParamsDef, Item: reflect.TypeOf(Item{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/vendors", Scope: "vendors:read", Summary: "거래처 목록(사업자번호·계좌·연락처는 항상 가려진 값)", Params: listParamsDef, Item: reflect.TypeOf(Vendor{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/purchase-orders", Scope: "purchase_orders:read", Summary: "발주 목록", Params: listParamsDef, Item: reflect.TypeOf(PurchaseOrder{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/receipts", Scope: "receipts:read", Summary: "입고 목록", Params: listParamsDef, Item: reflect.TypeOf(Receipt{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/journals", Scope: "journals:read", Summary: "전표 목록(줄 포함, 금액 포함)", Params: listParamsDef, Item: reflect.TypeOf(Journal{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/payables", Scope: "payables:read", Summary: "매입 채무 항목 목록", Params: listParamsDef, Item: reflect.TypeOf(Payable{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/tax-invoices", Scope: "tax_invoices:read", Summary: "세금계산서 목록(사업자번호는 항상 가려진 값, 이메일·주소·대표자·품목 이름 없음)", Params: listParamsDef, Item: reflect.TypeOf(TaxInvoice{}), Kind: "list"},
	{Method: "GET", Path: "/api/v1/changes", Scope: ScopeChangesRead, Summary: "변경 피드: 커서 다음의 변경(값 없음, 읽기 범위를 가진 자원만)", Item: reflect.TypeOf(Change{}), Kind: "changes",
		Params: []Param{
			{Name: "cursor", Desc: "시작 위치(/changes/head 또는 이전 응답의 next_cursor, c1.<번호>)", Type: "string", Required: true},
			{Name: "limit", Desc: "한 번에 받을 건수(1~500, 기본 200)", Type: "integer"},
			{Name: "resource", Desc: "쉼표로 구분한 자원 이름(item·vendor·purchase_order·receipt·journal·payable·tax_invoice). 비우면 읽기 범위가 있는 모든 자원", Type: "string"},
		}},
	{Method: "GET", Path: "/api/v1/changes/head", Scope: ScopeChangesRead, Summary: "현재 변경 피드 위치(최초 적재를 시작하기 전에 받아 둔다)", Item: reflect.TypeOf(ChangeHead{}), Kind: "head"},
	// 쓰기(D10): 거래처·품목 가져오기(미리보기 → 반영)와 전표 초안만. 서비스 층의 같은 함수를 거치므로 상태 전이·감사·SoD 를 우회하지 않는다.
	{Method: "POST", Path: "/api/v1/imports/vendors/preview", Scope: ScopeVendorsWrite, Summary: "거래처 가져오기 미리보기(아무것도 반영하지 않는다)", Req: reflect.TypeOf(ImportRequest{}), Item: reflect.TypeOf(ImportPreview{}), Kind: "write", Status: 200},
	{Method: "POST", Path: "/api/v1/imports/vendors/batches/{batch}/apply", Scope: ScopeVendorsWrite, Summary: "거래처 가져오기 반영(미리보기와 같은 행과 반영 토큰, 오류가 하나라도 있으면 반영하지 않는다)", Req: reflect.TypeOf(ImportApplyRequest{}), Item: reflect.TypeOf(ImportApplied{}), Kind: "write", Status: 200, Idem: true,
		Params: []Param{{Name: "batch", Desc: "미리보기가 돌려준 묶음 번호", Type: "string", Required: true, In: "path"}}},
	{Method: "POST", Path: "/api/v1/imports/items/preview", Scope: ScopeItemsWrite, Summary: "품목 가져오기 미리보기(아무것도 반영하지 않는다)", Req: reflect.TypeOf(ImportRequest{}), Item: reflect.TypeOf(ImportPreview{}), Kind: "write", Status: 200},
	{Method: "POST", Path: "/api/v1/imports/items/batches/{batch}/apply", Scope: ScopeItemsWrite, Summary: "품목 가져오기 반영(미리보기와 같은 행과 반영 토큰)", Req: reflect.TypeOf(ImportApplyRequest{}), Item: reflect.TypeOf(ImportApplied{}), Kind: "write", Status: 200, Idem: true,
		Params: []Param{{Name: "batch", Desc: "미리보기가 돌려준 묶음 번호", Type: "string", Required: true, In: "path"}}},
	{Method: "POST", Path: "/api/v1/journals", Scope: ScopeJournalsDraft, Summary: "전표 초안 제출(초안까지만 만든다. 승인 요청·승인·취소 경로는 없다)", Req: reflect.TypeOf(JournalDraftRequest{}), Item: reflect.TypeOf(JournalDraft{}), Kind: "write", Status: 201, Idem: true},
	{Method: "GET", Path: openAPIPath, Scope: "", Summary: "이 API 의 OpenAPI 문서(스키마만, 값 없음)", Kind: "openapi"},
}

// Register 는 /api/v1 라우트를 등록한다. 인증 라우트는 모두 내부 권한 api:access 를 선언하고(주체는 Wrap 이 싣는다), 범위 검사는 핸들러 앞의 scoped 가 한다.
func (s *Service) Register(r contract.Registrar) {
	for _, d := range Routes {
		var h http.HandlerFunc
		perm := contract.PermAPIAccess
		switch d.Method + " " + d.Path {
		case "POST /api/v1/imports/vendors/preview":
			h = s.importPreviewHandler(d.Scope, "vendors")
		case "POST /api/v1/imports/vendors/batches/{batch}/apply":
			h = s.importApplyHandler(d.Scope, "vendors")
		case "POST /api/v1/imports/items/preview":
			h = s.importPreviewHandler(d.Scope, "items")
		case "POST /api/v1/imports/items/batches/{batch}/apply":
			h = s.importApplyHandler(d.Scope, "items")
		case "POST /api/v1/journals":
			h = s.journalDraftHandler(d.Scope)
		case "GET /api/v1/items":
			h = listHandler(d.Scope, s.read.Items, toItem, func(v itemRow) int64 { return v.ID })
		case "GET /api/v1/vendors":
			h = listHandler(d.Scope, s.read.Vendors, toVendor, func(v vendorRow) int64 { return v.ID })
		case "GET /api/v1/purchase-orders":
			h = listHandler(d.Scope, s.read.PurchaseOrders, toPO, func(v poRow) int64 { return v.ID })
		case "GET /api/v1/receipts":
			h = listHandler(d.Scope, s.read.Receipts, toReceipt, func(v receiptRow) int64 { return v.ID })
		case "GET /api/v1/journals":
			h = listHandler(d.Scope, s.read.Journals, toJournal, func(v journalRow) int64 { return v.ID })
		case "GET /api/v1/payables":
			h = listHandler(d.Scope, s.read.Payables, toPayable, func(v payableRow) int64 { return v.ID })
		case "GET /api/v1/tax-invoices":
			h = listHandler(d.Scope, s.read.TaxInvoices, toTaxInvoice, func(v taxInvRow) int64 { return v.ID })
		case "GET /api/v1/changes":
			h = s.changesHandler()
		case "GET /api/v1/changes/head":
			h = s.headHandler()
		case "GET " + openAPIPath:
			h = openAPIHandler
			perm = contract.PermPublic
		default:
			continue
		}
		r.HandleFunc(d.Method, d.Path, perm, h)
	}
}

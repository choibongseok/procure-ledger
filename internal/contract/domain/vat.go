package domain

import "context"

// B32 부가세 매입 집계( 3.4). 승인된 전표만 세고 기준은 거래일(공급 시기)이다. 신고 서식 전송은 하지 않는다.

// VATFilter 는 집계 기간이다. Unit 은 month(Month), quarter(Year·Quarter), custom(From·To, 최대 12개월)이다. 비면 이번 달이다.
type VATFilter struct {
	Unit    string
	Month   string // YYYY-MM
	Year    int
	Quarter int // 1~4
	From    string
	To      string
	// SiteID 는 부가세 신고 단위인 사업장(entity_sites.id)이다. 0 이면 법인 전체. 주사업장을 고르면 총괄납부 사업장이 함께 합산된다(B44 14.2).
	SiteID int64
}

// VATVendorRow 는 표 1(매입처별 세금계산서) 한 줄이다. BusinessNo 는 업무 식별자라 가리지 않는다.
type VATVendorRow struct {
	VendorID   int64
	VendorName string
	BusinessNo string
	Count      int
	Supply     int64
	VAT        int64
}

// VATUnreceivedRow 는 표 2(세금계산서 미수취) 한 줄이다. 매입세액 공제 합계에 넣지 않는다.
type VATUnreceivedRow struct {
	JournalNo  string
	VendorName string
	Supply     int64
	VAT        int64
	TradeDate  string
}

// VATUnlinkedRow 는 표 4(수신했지만 매입 전표와 연결되지 않은 세금계산서) 한 줄이다(B43 D10). 매입세액 공제 합계에 넣지 않는다: 입고·전표와 연결되어야 표 1 에 들어간다.
type VATUnlinkedRow struct {
	InvoiceNo  string
	ApprovalNo string
	Supplier   string
	IssueDate  string
	Supply     int64
	VAT        int64
	State      string // 대조 상태 이름(한국어): 제안 대기·후보 없음 등
}

// VATDiffRow 는 세금계산서와 연결된 매입 전표의 부가세가 다른 건의 차이 목록 한 줄이다(B43 D10). 집계 금액은 전표 값이고 이 목록은 알려 주기만 한다.
type VATDiffRow struct {
	InvoiceNo  string
	JournalNos string
	IssueDate  string
	InvoiceVAT int64
	JournalVAT int64
	Diff       int64 // 전표 − 세금계산서
}

// VATCardRow 는 표 3(카드 매입) 한 줄이다. Kind 는 deductible | nondeductible, 불공제는 사유별로 나뉜다.
type VATCardRow struct {
	Kind        string
	Reason      string // 불공제 사유 코드(공제면 빈 값)
	ReasonLabel string
	Count       int
	Supply      int64
	VAT         int64
	Total       int64
}

// VATPurchase 는 집계 결과다.
type VATPurchase struct {
	From, To      string
	Vendors       []VATVendorRow
	Unreceived    []VATUnreceivedRow
	Cards         []VATCardRow
	VendorVAT     int64 // 표 1 부가세 합계
	CardDeductVAT int64 // 표 3 공제 부가세 합계
	DeductibleVAT int64 // 매입세액 공제 합계 = 표 1 + 표 3 공제
	UnreceivedVAT int64 // 표 2 부가세 합계(공제에 넣지 않는다)
	LedgerVAT     int64 // 계정 1310 부가세대급금 기간 차변 합계(승인된 전표, 거래일 기준)
	Difference    int64 // LedgerVAT − DeductibleVAT
	Matches       bool
	Unlinked      []VATUnlinkedRow // 표 4: 연결되지 않은 수신 세금계산서(B43)
	UnlinkedVAT   int64
	Diffs         []VATDiffRow // 세금계산서 ↔ 전표 부가세 차이(B43)
	Hints         []string
	ExcludedCount int // 기간의 승인 전(임시·승인 요청) 매입·카드 비용 전표 수
	// 사업장(B44 14.2). 법인에 사업장이 하나뿐이면 아래 값은 모두 비어 있다.
	SiteID         int64
	SiteName       string
	Sites          []VATSiteRow // 사업장별 매입 부가세 내역(법인 전체 기준, 접어서 보인다)
	ConsolidatedIn []string     // 이 집계에 합산된 총괄납부 사업장 이름(주사업장을 골랐을 때)
	SiteScoped     bool         // 사업장 단위 집계다(표 4 미연결 세금계산서는 법인 단위로만 보인다)
}

// VATSiteRow 는 사업장별 내역 한 줄이다. VAT 는 계정 1310 의 기간 차변 합계다(승인된 전표, 거래일 기준). 사업장이 지정되지 않은 전표(0)는 주사업장에 넣는다.
type VATSiteRow struct {
	SiteID       int64
	SiteNo, Name string
	Kind         string // head | branch | factory
	VATMode      string // own | consolidated
	Count        int    // 매입 전표 수
	VAT          int64
}

// SiteInfo 는 법인의 사업장 한 곳이다(선택기용).
type SiteInfo struct {
	ID      int64
	SiteNo  string
	Name    string
	Kind    string
	VATMode string
}

// SiteService 는 현재 법인의 활성 사업장 목록이다(전표·창고·부가세 화면의 선택기). 로그인한 사람이면 읽을 수 있다.
type SiteService interface {
	List(ctx context.Context) ([]SiteInfo, error)
}

// VATService 는 부가세 매입 집계다. 조회 vat:read(재무·관리자·감사).
type VATService interface {
	PurchaseSummary(ctx context.Context, f VATFilter) (VATPurchase, error)
}

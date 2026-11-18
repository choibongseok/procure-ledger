package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 시드 사용자 ID. 부서 1=구매팀, 2=영업팀, 3=재무팀.
const (
	uStaff    = 1  // 구매팀 직원(요청자)
	uLead     = 2  // 구매팀 팀장
	uHead     = 3  // 구매팀 부서장
	uFinance  = 4  // 재무
	uStaff2   = 5  // 영업팀 직원
	uAuditor  = 6  // 감사
	uAdmin    = 7  // 관리자
	uLead2    = 8  // 영업팀 팀장
	uFinance2 = 9  // 재무 2(재무 요청자 케이스용)
	uLead3    = 10 // 구매팀 팀장 2(구매2팀 팀장)
	uCEO      = 11 // 대표(회사 조직장)
	deptBuy   = 1
	deptSales = 2
	deptFin   = 3
)

var skuSeq int

// leftAwareOrg 는 가짜 조직에 LeftUsers(퇴사 상태 사용자, 실제 org 서비스의 같은 이름 메서드)를 더한다.
type leftAwareOrg struct{ *fake.Org }

func (o leftAwareOrg) LeftUsers(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, id := range ids {
		if p, err := o.Org.Person(ctx, id, ""); err == nil && !p.Active {
			out[id] = true
		}
	}
	return out, nil
}

type env struct {
	db           *sql.DB
	org          *fake.Org
	rec          *fake.Recorder
	purchase     *service.PurchaseService
	approval     *service.ApprovalService
	inventory    *service.InventoryService
	delegation   *service.DelegationService
	notification *service.NotificationService
	poPrint      *service.POPrintService
	company      *service.CompanyService
	monthClose   *service.MonthCloseService
	exportLists  *service.ExportListService
	reports      *service.ReportService
	search       *service.DocSearchService
	accounts     *service.AccountService
	journals     *service.JournalService
	tax          *service.TaxInvoiceService
	jqueue       *service.JournalQueueService
	payments     *service.PaymentService
	funds        *service.FundService
	cards        *service.CardService
	rating       *service.VendorRatingService
	budget       *service.BudgetService
	retention    *service.RetentionService
	quotes       *service.QuoteService
	quoteFake    *fakeQuoteReader
	stdVendor    *domain.Vendor // vendor() 가 한 번 만든 표준 거래처(같은 이름·사업자번호 중복 등록은 막힌다)
	line         *service.LineService
	now          time.Time
}

func newEnv(t *testing.T) *env { return newEnvAt(t, ":memory:") }

// envSoD 는 newEnvSoD 가 한 번만 쓰는 권한 분리 판정기다(newEnvAt 이 서비스 묶음에 꽂는다). nil 이면 판정기 없는 조립(locked 규칙만 내장 판정)이다.
var envSoD sod.Evaluator

// newEnvSoD 는 판정기를 꽂은 환경을 만든다.
func newEnvSoD(t *testing.T, ev sod.Evaluator) *env {
	t.Helper()
	envSoD = ev
	defer func() { envSoD = nil }()
	return newEnv(t)
}

func newEnvAt(t *testing.T, path string) *env {
	t.Helper()
	// 계좌번호는 암호화해서만 저장한다(B32 D1 후속). 시험은 고정 키를 쓴다.
	if c, cerr := acctsecret.New([]byte("0123456789abcdef0123456789abcdef")); cerr == nil {
		repository.SetAccountCipher(c)
	}
	var db *sql.DB
	if dbtest.PG() { // PG 는 연결이 여럿이라 파일이 필요 없다(경합 시험도 같은 DB 를 쓴다)
		db = dbtest.Open(t)
	} else {
		if path != ":memory:" {
			path = filepath.Join(t.TempDir(), path)
		}
		var err error
		if db, err = dbutil.Open(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
	}
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(context.Background(), db); err != nil { // 실효 권한(B33): 역할 템플릿 권한을 DB 에 맞춘다
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO departments (id, name) VALUES (1,'구매팀'),(2,'영업팀'),(3,'재무팀')`,
		`INSERT INTO users (id, email, name, role, position, department_id) VALUES
		 (1,'staff@t','직원','staff',NULL,1),
		 (2,'lead@t','팀장','manager','team_lead',1),
		 (3,'head@t','부서장','manager','dept_head',1),
		 (4,'fin@t','재무','finance',NULL,3),
		 (5,'staff2@t','영업직원','staff',NULL,2),
		 (6,'aud@t','감사','auditor',NULL,3),
		 (7,'admin@t','관리자','admin',NULL,3),
		 (8,'lead2@t','영업팀장','manager','team_lead',2),
		 (9,'fin2@t','재무2','finance',NULL,3),
		 (10,'lead3@t','구매팀장2','manager','team_lead',1),
		 (11,'ceo@t','대표','manager','dept_head',3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{db: db, rec: fake.NewRecorder(), org: seedOrg(), now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	b := service.Base{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }, SoD: envSoD}
	e.purchase, e.approval, e.inventory = service.NewPurchaseService(b), service.NewApprovalService(b), service.NewInventoryService(b)
	e.delegation = service.NewDelegationService(b)
	e.notification = service.NewNotificationService(b)
	e.poPrint = service.NewPOPrintService(b)
	e.company = service.NewCompanyService(b)
	e.monthClose = service.NewMonthCloseService(b)
	e.exportLists = service.NewExportListService(b)
	e.rating = service.NewVendorRatingService(b)
	e.reports = service.NewReportService(b, e.rating)
	e.search = service.NewDocSearchService(b)
	e.accounts = service.NewAccountService(b)
	e.journals = service.NewJournalService(b, service.NewApprovalDocService(b))
	e.tax = service.NewTaxInvoiceService(b)
	e.jqueue = service.NewJournalQueueService(b)
	e.payments = service.NewPaymentService(b, service.NewApprovalDocService(b), e.journals)
	e.funds = service.NewFundService(b)
	e.cards = service.NewCardService(b)
	e.budget = service.NewBudgetService(b)
	e.retention = service.NewRetentionService(b)
	e.quoteFake = &fakeQuoteReader{}
	e.quotes = service.NewQuoteService(e.purchase)
	e.quotes.Extractor = func() service.QuoteReader { return e.quoteFake }
	e.line = service.NewLineService(b)
	return e
}

// 조직 단위 번호. 부서(1~3)는 users.department_id 와 같은 번호다(B20 호환 투영과 같다).
const (
	unitCompany = 100
	unitBuy     = 1  // 구매팀(부서)
	unitSales   = 2  // 영업팀(부서)
	unitFin     = 3  // 재무팀(부서)
	unitBuy1    = 11 // 구매1팀(팀): 팀장 lead(2)
	unitBuy2    = 12 // 구매2팀(팀): 팀장 lead3(10)
	unitSales1  = 13 // 영업1팀(팀): 팀장 lead2(8)
)

// seedOrg 는 시드 사용자를 가짜 조직(회사 > 부서 3개 > 팀)에 앉힌다. 구매팀 부서장은 head(3), 대표는 ceo(11)다.
func seedOrg() *fake.Org {
	o := fake.NewOrg()
	o.AddUnit(unitCompany, "PL", "PL 주식회사", contract.OrgCompany, 0)
	o.AddUnit(unitBuy, "BUY", "구매팀", contract.OrgDepartment, unitCompany)
	o.AddUnit(unitSales, "SAL", "영업팀", contract.OrgDepartment, unitCompany)
	o.AddUnit(unitFin, "FIN", "재무팀", contract.OrgDepartment, unitCompany)
	o.AddUnit(unitBuy1, "BUY1", "구매1팀", contract.OrgTeam, unitBuy)
	o.AddUnit(unitBuy2, "BUY2", "구매2팀", contract.OrgTeam, unitBuy)
	o.AddUnit(unitSales1, "SAL1", "영업1팀", contract.OrgTeam, unitSales)
	add := func(id int64, name string, unit int64) { o.AddPerson(id, name, "과장", unit, "", "", "") }
	add(uStaff, "직원", unitBuy1)
	add(uLead, "팀장", unitBuy1)
	add(uHead, "부서장", unitBuy)
	add(uFinance, "재무", unitFin)
	add(uStaff2, "영업직원", unitSales1)
	add(uAuditor, "감사", unitFin)
	add(uAdmin, "관리자", unitFin)
	add(uLead2, "영업팀장", unitSales1)
	add(uFinance2, "재무2", unitFin)
	add(uLead3, "구매팀장2", unitBuy2)
	add(uCEO, "대표", unitCompany)
	o.SetHead(unitBuy1, uLead).SetHead(unitBuy2, uLead3).SetHead(unitSales1, uLead2).SetHead(unitBuy, uHead).SetHead(unitCompany, uCEO)
	return o
}

var users = map[int64]contract.Principal{
	uStaff:    {UserID: uStaff, Name: "직원", Role: contract.RoleStaff, DepartmentID: deptBuy},
	uLead:     {UserID: uLead, Name: "팀장", Role: contract.RoleManager, DepartmentID: deptBuy},
	uHead:     {UserID: uHead, Name: "부서장", Role: contract.RoleManager, DepartmentID: deptBuy},
	uFinance:  {UserID: uFinance, Name: "재무", Role: contract.RoleFinance, DepartmentID: deptFin},
	uStaff2:   {UserID: uStaff2, Name: "영업직원", Role: contract.RoleStaff, DepartmentID: deptSales},
	uAuditor:  {UserID: uAuditor, Name: "감사", Role: contract.RoleAuditor, DepartmentID: deptFin},
	uAdmin:    {UserID: uAdmin, Name: "관리자", Role: contract.RoleAdmin, DepartmentID: deptFin},
	uLead2:    {UserID: uLead2, Name: "영업팀장", Role: contract.RoleManager, DepartmentID: deptSales},
	uFinance2: {UserID: uFinance2, Name: "재무2", Role: contract.RoleFinance, DepartmentID: deptFin},
	uLead3:    {UserID: uLead3, Name: "구매팀장2", Role: contract.RoleManager, DepartmentID: deptBuy},
	uCEO:      {UserID: uCEO, Name: "대표", Role: contract.RoleManager, DepartmentID: deptFin},
}

// as 는 시드 사용자로 호출하는 context 다(웹 경로).
func as(id int64) context.Context {
	p := users[id]
	p.Channel = contract.ChannelWeb
	return contract.WithPrincipal(context.Background(), p)
}

func (e *env) item(t *testing.T, sku string, safety int64, trackLot bool) domain.Item {
	t.Helper()
	it, err := e.inventory.CreateItem(as(uFinance), service.ItemInput{SKU: sku, Name: "품목 " + sku, Unit: "EA", SafetyStock: safety, TrackLot: trackLot})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (e *env) vendor(t *testing.T) domain.Vendor {
	t.Helper()
	if e.stdVendor != nil {
		return *e.stdVendor
	}
	v, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "대한문구", BusinessNo: "123-45-67890", AccountNo: "110-123-456789", Phone: "02-123-4567"})
	if err != nil {
		t.Fatal(err)
	}
	e.stdVendor = &v
	return v
}

// draft 는 금액이 amount 인 임시저장을 만든다(수량 1, 단가 amount).
func (e *env) draft(t *testing.T, by int64, amount int64) domain.PurchaseRequest {
	t.Helper()
	skuSeq++
	it := e.item(t, fmt.Sprintf("SKU-%d", skuSeq), 0, false)
	v := e.vendor(t)
	pr, err := e.purchase.CreateDraft(as(by), domain.DraftInput{
		VendorID: v.ID, Title: "구매 요청", Reason: "사무용",
		Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 1, UnitPrice: amount}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func (e *env) submitted(t *testing.T, by int64, amount int64) domain.PurchaseRequest {
	t.Helper()
	pr := e.draft(t, by, amount)
	pr, err := e.purchase.Submit(as(by), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("오류 %v, 기대 %v", err, want)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var errBoom = errors.New("디스크 가득 참")

func contractNoUser() context.Context { return context.Background() }

func isErr(err, want error) bool { return errors.Is(err, want) }

// jsonField 는 JSON 열의 문자열 필드를 읽는 식이다(SQLite json_extract, PG ->>).
func jsonField(col, key string) string {
	if dbtest.PG() {
		return col + "::json->>'" + key + "'"
	}
	return "json_extract(" + col + ", '$." + key + "')"
}

// strAgg 는 문자열 이어 붙이기 집계식이다(SQLite group_concat, PG string_agg).
func strAgg(expr string) string {
	if dbtest.PG() {
		return "string_agg(" + expr + ", ',')"
	}
	return "group_concat(" + expr + ")"
}

// argsIf 는 질의에 자리표시자가 있을 때만 인자를 넘긴다.
func argsIf(q string, args ...any) []any {
	if strings.Contains(q, "?") {
		return args
	}
	return nil
}

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/apiwrite"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
	"github.com/choibongseok/procure-ledger/internal/taxasp"
	"github.com/choibongseok/procure-ledger/internal/taxasp/mock"
	"github.com/choibongseok/procure-ledger/internal/taxaspadmin"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/docs/policies"
	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/entity"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// WireERP 는 erp 서비스를 만들어 d 의 Purchase·Approval·Inventory 에 채운다.
// 저장소는 d.DB, 감사는 d.Audit·d.Txs 를 받아 만든다(wirePlatform 이 먼저 채운다).
// 감사 기록기 없이는 쓰기 서비스를 만들지 않는다(기록 없는 쓰기 금지).
func WireERP(cfg config.Config, d *deps.Deps) error {
	if d.Audit == nil || d.Txs == nil || d.DB == nil {
		return errors.New("wireERP: DB·트랜잭션 관리자·감사 기록기가 먼저 준비되어야 합니다")
	}
	if d.Privacy == nil { // 원문이 나가는 서비스 경로는 조회 기록 없이 열 수 없다(fail-closed): wirePlatform 이 먼저 채운다
		return errors.New("wireERP: 개인정보 조회 기록기가 없습니다(wirePlatform 이 먼저 채워야 합니다)")
	}
	if d.SoD == nil { // 권한 분리 판정기 없이는 지급·전표 같은 견제 단계를 열 수 없다(fail-closed)
		return errors.New("wireERP: 권한 분리 판정기가 없습니다(wirePlatform 이 먼저 채워야 합니다)")
	}
	// 첨부 상한: 기동 때 잘못된 값(숫자 아님·0 이하)을 거부한다. config.Load 가 그런 값을 -1 로 읽는다. 설정이 비어 있는 구성(시험)은 기본값이다.
	if cfg.AttachMaxFileBytes < 0 {
		return errors.New("PL_ATTACH_MAX_FILE_BYTES 는 1 이상의 정수여야 합니다")
	}
	if cfg.AttachMaxFiles < 0 {
		return errors.New("PL_ATTACH_MAX_FILES 는 1 이상의 정수여야 합니다")
	}
	if cfg.AttachMaxTotalBytes < 0 {
		return errors.New("PL_ATTACH_MAX_TOTAL_BYTES 는 1 이상의 정수여야 합니다")
	}
	if err := wireAccountSecret(cfg, d); err != nil {
		return err
	}
	limits := service.AttachLimits{MaxFileBytes: cfg.AttachMaxFileBytes, MaxFiles: cfg.AttachMaxFiles, MaxTotalBytes: cfg.AttachMaxTotalBytes}.OrDefault()
	d.AttachMaxFileBytes = limits.MaxFileBytes
	b := service.Base{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Masker: d.Masker, Org: d.Org, Attach: limits, Privacy: d.Privacy, SoD: d.SoD}
	if d.Modules != nil { // 업무 모듈 켜기 상태(B50): 휴가 결재 훅·서비스가 꺼짐을 따른다
		b.Modules = d.Modules
	}
	if d.Notify != nil { // 앱 안 알림을 메일 발송 건으로도 큐에 올린다(B36)
		b.Deliver = d.Notify.Enqueue
	}
	purchase := service.NewPurchaseService(b)
	d.Purchase = purchase
	d.Approval = service.NewApprovalService(b)
	inventorySvc := service.NewInventoryService(b)
	d.Inventory = inventorySvc
	d.Delegation = service.NewDelegationService(b)
	d.Notification = service.NewNotificationService(b)
	d.Line = service.NewLineService(b)
	d.Reassign = service.NewReassignService(b)
	d.Forms = service.NewFormService(b)
	docs := service.NewApprovalDocService(b)
	leaveSvc := service.NewLeaveService(b, docs)
	d.Leave = leaveSvc                                                     // 근태·휴가(B50)
	if o, ok := d.Org.(interface{ SetAbsenceHook(org.AbsenceHook) }); ok { // 휴직 발령이 승인된 휴가의 연차를 복원한다(B50 16절)
		o.SetAbsenceHook(leaveSvc.RestoreForAbsence)
	}
	if o, ok := d.Org.(interface {
		NotifyOverdueReturns(context.Context, time.Time) (int, error)
	}); ok { // 예정 복직일 경과 알림은 근태 일일 작업이 부른다(B50 8.1)
		leaveSvc.OverdueReturns = o.NotifyOverdueReturns
	}
	d.Attendance = service.NewAttendanceService(b) // 출퇴근·근무 유형(B50 5단계)
	d.Docs = docs
	d.Attachments = service.NewAttachmentService(b, docs)
	d.PersonalLines = service.NewPersonalLineService(b)
	d.Inbox = service.NewInboxService(b)
	d.Receipts = service.NewReceiptService(b)
	auth := service.NewAuthorityService(b)
	auth.PolicyDocs = policyDocRefs
	auth.Clauses = func() domain.ClauseReader { r, _ := d.Policy.(domain.ClauseReader); return r } // 규정 검색은 WireAI 가 나중에 채우므로 호출 때 읽는다
	d.Authority = auth
	d.Warehouses = service.NewWarehouseService(b)
	d.StockIssue = service.NewStockIssueService(b)
	d.StockTransfer = service.NewStockTransferService(b)
	d.StockCount = service.NewStockCountService(b)
	d.Ledger = service.NewLedgerService(b)
	// B27: 평가 줄이 없는 기존 원장 행을 기동 때 한 번 재생한다(멱등).
	if err := service.BackfillValuation(context.Background(), b); err != nil {
		return err
	}
	// B51: 다법인 품목의 옛 평가 줄(품목 전체 기준)을 법인 기준으로 옮긴다(닫힌 달 보존, 멱등, 실패는 부팅을 막지 않는다).
	if err := service.SplitValuationByEntity(context.Background(), b); err != nil {
		return err
	}
	// B29: 검색 텍스트가 빈 기존 문서를 기동 때 한 번 채운다(멱등).
	if err := service.BackfillSearchText(context.Background(), b); err != nil {
		return err
	}
	d.DocSearch = service.NewDocSearchService(b)
	d.Nudge = service.NewNudgeService(b)
	d.Accounts = service.NewAccountService(b)
	jsvc := service.NewJournalService(b, docs)
	d.Journals = jsvc
	d.APIRead = service.NewAPIReadService(b, inventorySvc, jsvc) // 연동 API(B42) 읽기 자원: 같은 서비스 함수·규칙으로 번호 오름차순 증분 읽기
	if d.APITokens != nil {                                      // 연동 API(B42): 읽기 서비스 위에 /api/v1 라우트와 토큰 인증기를 올린다
		d.APIv1 = apiv1.New(apiv1.Options{Read: d.APIRead, DB: d.DB, Auth: d.APITokens,
			Write: apiwrite.New(apiwrite.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Import: d.Import, Journals: jsvc})}) // 쓰기(B42 3단계): 가져오기 공통 흐름·전표 초안 서비스 위
	}
	// 기초 데이터 가져오기(B35): 대상별 반영 7개를 공통 흐름(WirePlatform 이 만든 d.Import)에 붙인다.
	if d.Import != nil {
		for _, h := range []domain.ImportTargetHandler{
			service.NewImportAccount(b), service.NewImportWarehouse(b), service.NewImportVendor(b), service.NewImportItem(b), service.NewImportBudget(b),
			service.NewImportOpeningStock(b, jsvc), service.NewImportOpeningPayable(b, jsvc),
		} {
			d.Import.RegisterTarget(h)
		}
	}
	d.JournalHook = jsvc // 증빙 판정 확정 → 자동 매입 전표(docverify 가 호출, wire_ai 가 이 값을 받는다)
	d.JournalQueue = service.NewJournalQueueService(b)
	d.Payments = service.NewPaymentService(b, docs, jsvc)
	d.Funds = service.NewFundService(b)
	d.VAT = service.NewVATService(b)
	d.Consolidated = service.NewConsolidatedService(b)
	// 사내 지식 검색(B47): 검색은 결재 문서 열람 판정(DocViewService)을 빌려 쓰고, 색인은 대기열 워커가 만든다(서버만 돌린다).
	kworker := &knowledge.Worker{Txs: d.Txs, Now: b.Now}
	d.Knowledge = &knowledge.Service{Txs: d.Txs, DB: d.DB, Audit: d.Audit, Masker: d.Masker, Opener: service.NewDocViewService(b), Store: knowledge.NewStore(d.DB), Privacy: d.Privacy}
	d.KnowledgeAdmin = &knowledge.Admin{Txs: d.Txs, DB: d.DB, Audit: d.Audit, Worker: kworker}
	d.KnowledgeWorker = kworker
	d.Sites = service.NewSiteService(b)
	// 전자세금계산서(B43): 서비스는 상태 전이의 유일한 입구이고, 전송 작업(taxasp.Worker)은 그 입구(TaxDispatchStore)로만 상태를 바꾼다.
	// 대행사 설정 원천은 tax_asp_settings 다(봉인 용도 tax-asp). 설정 화면·시험 연결·인증서 만료 알림도 같은 서비스가 맡는다.
	taxSvc := service.NewTaxInvoiceService(b)
	taxReg := taxasp.NewRegistry()
	taxReg.Register("mock", mock.Factory(mock.NewStore()))
	aspOpts := taxaspadmin.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Registry: taxReg, AllowLocal: cfg.NotifyInsecureLocal, Production: !cfg.DevIDP, Rehearsal: cfg.Rehearsal,
		Holders: func(ctx context.Context, tx contract.Tx, perm contract.Permission, entityID int64, day contract.Day) ([]int64, error) {
			return entity.PermissionHolders(ctx, tx, perm, entityID, day)
		}}
	if d.Notify != nil {
		aspOpts.Deliver = d.Notify.Enqueue
	}
	if key, kerr := acctsecret.DecodeKey(cfg.AccountSecretKey); kerr == nil && key != nil {
		if c, cerr := acctsecret.New(key); cerr == nil {
			aspOpts.Sealer = c
		}
	}
	aspAdmin := taxaspadmin.New(aspOpts)
	d.TaxASP = aspAdmin
	taxSvc.ASPReady = aspAdmin.Ready
	d.TaxInvoice = taxSvc
	d.TaxMatch, d.TaxSyncCtl, d.TaxContact = taxSvc, taxSvc, taxSvc
	taxSrc := taxasp.WithDemoSeed{Inner: aspAdmin, DB: d.DB}
	d.TaxSyncer = &taxasp.Syncer{Store: taxSvc, Settings: taxSrc, Registry: taxReg, Rehearsal: cfg.Rehearsal}
	d.TaxWorker = &taxasp.Worker{Store: taxSvc, Settings: taxSrc, Registry: taxReg, Rehearsal: cfg.Rehearsal}
	d.Company = service.NewCompanyService(b)
	d.ImportReconcile = service.NewImportReconcile(b)
	cards := service.NewCardService(b)
	d.CardSvc = cards
	d.Cards = cards // MCP get_card_unreconciled 가 읽는다(본인 소지분)
	d.Payables = service.NewPayableService(b)
	d.LedgerReport = service.NewLedgerReportService(b)
	d.Budget = service.NewBudgetService(b)
	d.Retention = service.NewRetentionService(b)
	d.POPrint = service.NewPOPrintService(b)
	mc := service.NewMonthCloseService(b)
	d.MonthClose = mc
	if d.Notify != nil { // 알림 채널(B36): 영업일 판정을 B34 의 한 정의로 꽂고, 지급 예정·월 마감 점검 주기 알림을 붙인다
		d.Notify.SetBusinessDay(service.BusinessDayFunc(b))
		if d.PrivReport != nil {
			d.PrivReport.SetBusinessDay(service.BusinessDayFunc(b)) // 월별 점검 보고서 알림의 첫 영업일(B34 달력)
		}
		d.NotifyEvents = service.NewPeriodicNotifier(b, mc)
	}
	d.ExportLists = service.NewExportListService(b)
	d.MonthStock = service.NewMonthStockService(b)
	rating := service.NewVendorRatingService(b)
	d.VendorRating = rating
	d.Reports = service.NewReportService(b, rating)
	quotes := service.NewQuoteService(purchase)
	// 견적서 추출기는 LLM 게이트웨이(WireAI 가 나중에 d.LLM 에 채운다)를 쓰므로 호출 때 읽는다. 아직 없으면 견적서는 "읽지 못함"으로 저장된다.
	quotes.Extractor = func() service.QuoteReader {
		if d.LLM == nil {
			return nil
		}
		return docverify.QuoteExtractor{LLM: d.LLM, Masker: d.Masker}
	}
	d.Quotes = quotes
	return nil
}

// policyDocRefs 는 규정 문서(내장본)의 slug 와 한국어 제목이다. 읽지 못하면 빈 목록이다(화면은 제목 없이 조항 번호만 보인다).
func policyDocRefs() []domain.PolicyRef {
	res, err := policysearch.Load(policies.FS)
	if err != nil || res.Index == nil {
		return nil
	}
	var out []domain.PolicyRef
	for _, d := range res.Index.Docs() {
		out = append(out, domain.PolicyRef{Slug: d.Slug, Title: d.Title})
	}
	return out
}

// wireAccountSecret 는 계좌번호 암호화 키(PL_ACCOUNT_SECRET_KEY)를 저장소에 넣고, 기존 평문 계좌번호를 한 번 봉한다(멱등: 이미 봉한 행은 건드리지 않는다).
// 키가 형식에 맞지 않으면 기동을 거부한다. 키가 없으면 기동은 하되 경고를 남기고 계좌번호를 새로 저장하는 일은 서비스가 거부한다.
func wireAccountSecret(cfg config.Config, d *deps.Deps) error {
	key, err := acctsecret.DecodeKey(cfg.AccountSecretKey)
	if err != nil {
		return fmt.Errorf("wireERP: %w", err)
	}
	if key == nil {
		repository.SetAccountCipher(nil)
		n := 0
		_ = d.Txs.WithTx(context.Background(), func(tx contract.Tx) error {
			var e error
			n, e = repository.PlainAccountCount(context.Background(), tx)
			return e
		})
		if cfg.DevIDP || cfg.DevSeedPassword != "" {
			log.Printf("bootstrap: [개발 모드 안내] 계좌번호 암호화 키가 없어 거래처·회사 계좌 저장이 막힙니다. 키를 만들어 넣고 다시 기동하세요: PL_ACCOUNT_SECRET_KEY=$(openssl rand -base64 32) ./bin/server (같은 키를 계속 써야 저장한 계좌번호를 읽을 수 있습니다)")
		}
		log.Printf("bootstrap: PL_ACCOUNT_SECRET_KEY 가 없어 계좌번호를 저장할 수 없습니다(거래처·회사 계좌 등록 거부). 이관하지 못한 평문 계좌번호 %d건", n)
		return nil
	}
	c, err := acctsecret.New(key)
	if err != nil {
		return fmt.Errorf("wireERP: 계좌번호 암호화 키: %w", err)
	}
	repository.SetAccountCipher(c)
	var moved int
	if err := d.Txs.WithTx(context.Background(), func(tx contract.Tx) error {
		var e error
		moved, e = repository.MigrateAccountSecrets(context.Background(), tx)
		return e
	}); err != nil {
		return fmt.Errorf("wireERP: 기존 계좌번호 암호화 이관: %w", err)
	}
	if moved > 0 {
		log.Printf("bootstrap: 평문 계좌번호 %d건을 암호화해 저장했습니다", moved)
	}
	return nil
}

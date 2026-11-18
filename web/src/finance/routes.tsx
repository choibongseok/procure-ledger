// (web/src/finance). 재무 화면 모듈(B31): 전표·계정과목. 이후 단계에서 전표 대기·매입 채무·원장·시산표가 더해진다.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { AccountsPage } from "./AccountsPage";
import { JournalDetailPage, JournalEditPage, JournalListPage } from "./JournalPages";
import { VatPurchasePage } from "./VatPage";
import { ConsolidatedPage } from "./ConsolidatedPage";
import { TaxInvoiceDetailPage, TaxInvoiceEditPage, TaxInvoiceListPage, TaxReceivePage } from "./TaxInvoicePages";
import { TaxMatchPage } from "./TaxMatchPage";
import { CardsAdminPage, CardsConfirmPage, CardsMinePage } from "./CardPages";
import { BankAccountsPage, FundDailyPage, PaymentCalendarPage } from "./FundPages";
import { PaymentDetailPage, PaymentEditPage, PaymentListPage } from "./PaymentPages";
import { QueuePage } from "./QueuePage";
import { LedgerPage, PayablesPage, TrialBalancePage } from "./ReportPages";

export const financeModule: RouteModule = {
  name: "finance",
  routes: [
    { path: "/finance/journals", element: guarded("ledger:read", <JournalListPage />) },
    { path: "/finance/journals/new", element: guarded("ledger:write", <JournalEditPage />) },
    { path: "/finance/journals/:no", element: guarded("ledger:read", <JournalDetailPage />) },
    { path: "/finance/journals/:no/edit", element: guarded("ledger:write", <JournalEditPage />) },
    { path: "/finance/journal-queue", element: guarded("ledger:read", <QueuePage />) },
    { path: "/finance/payables", element: guarded("ledger:read", <PayablesPage />) },
    { path: "/finance/ledger", element: guarded("ledger:read", <LedgerPage />) },
    { path: "/finance/trial-balance", element: guarded("ledger:read", <TrialBalancePage />) },
    { path: "/finance/payments", element: guarded("payment:read", <PaymentListPage />) },
    { path: "/finance/payments/new", element: guarded("payment:write", <PaymentEditPage />) },
    { path: "/finance/payments/:no", element: guarded("payment:read", <PaymentDetailPage />) },
    { path: "/finance/payment-calendar", element: guarded("payment:read", <PaymentCalendarPage />) },
    { path: "/finance/fund-daily", element: guarded("fund:read", <FundDailyPage />) },
    { path: "/finance/bank-accounts", element: guarded("fund:read", <BankAccountsPage />) },
    { path: "/finance/cards", element: guarded("card:read_all", <CardsAdminPage />) },
    { path: "/finance/cards/mine", element: guarded("card:explain", <CardsMinePage />) },
    { path: "/finance/cards/confirm", element: guarded("card:confirm", <CardsConfirmPage />) },
    { path: "/finance/vat-purchase", element: guarded("vat:read", <VatPurchasePage />) },
    { path: "/finance/consolidated", element: guarded("consolidated:read", <ConsolidatedPage />) },
    { path: "/finance/tax-invoices", element: guarded("taxinvoice:read", <TaxInvoiceListPage />) },
    { path: "/finance/tax-matches", element: guarded("taxinvoice:read", <TaxMatchPage />) },
    { path: "/finance/tax-invoices/new", element: guarded("taxinvoice:write", <TaxInvoiceEditPage />) },
    { path: "/finance/tax-invoices/receive", element: guarded("taxinvoice:write", <TaxReceivePage />) },
    { path: "/finance/tax-invoices/:id", element: guarded("taxinvoice:read", <TaxInvoiceDetailPage />) },
    { path: "/finance/tax-invoices/:id/edit", element: guarded("taxinvoice:write", <TaxInvoiceEditPage />) },
    { path: "/finance/accounts", element: guarded("ledger:read", <AccountsPage />) },
  ],
  menu: [
    { label: "전표", path: "/finance/journals", permission: "ledger:read", order: 42.2, group: "finance", section: "전표" },
    { label: "전표 대기", path: "/finance/journal-queue", permission: "ledger:read", order: 42.1, group: "finance", section: "전표" },
    { label: "매입 채무", path: "/finance/payables", permission: "ledger:read", order: 44.3, group: "finance", section: "지급" },
    { label: "계정별 원장", path: "/finance/ledger", permission: "ledger:read", order: 42.3, group: "finance", section: "전표" },
    { label: "시산표", path: "/finance/trial-balance", permission: "ledger:read", order: 42.4, group: "finance", section: "전표" },
    { label: "연결 재무 집계", path: "/finance/consolidated", permission: "consolidated:read", order: 42.45, group: "finance", section: "전표", multiEntityOnly: true },
    { label: "지급 요청", path: "/finance/payments", permission: "payment:read", order: 44.1, group: "finance", section: "지급" },
    { label: "지급 일정", path: "/finance/payment-calendar", permission: "payment:read", order: 44.2, group: "finance", section: "지급" },
    { label: "자금 일보", path: "/finance/fund-daily", permission: "fund:read", order: 45.2, group: "finance", section: "자금" },
    { label: "자금 계좌", path: "/finance/bank-accounts", permission: "fund:read", order: 45.1, group: "finance", section: "자금" },
    { label: "법인카드", path: "/finance/cards", permission: "card:read_all", order: 46.1, group: "finance", section: "카드" },
    { label: "내 카드 내역", path: "/finance/cards/mine", permission: "card:explain", order: 46.2, group: "finance", section: "카드" },
    { label: "카드 소명 확인", path: "/finance/cards/confirm", permission: "card:confirm", order: 46.3, group: "finance", section: "카드" },
    { label: "부가세 매입", path: "/finance/vat-purchase", permission: "vat:read", order: 47.1, group: "finance", section: "세무" },
    { label: "세금계산서", path: "/finance/tax-invoices", permission: "taxinvoice:read", order: 47.2, group: "finance", section: "세무" },
    { label: "세금계산서 대조", path: "/finance/tax-matches", permission: "taxinvoice:read", order: 47.3, group: "finance", section: "세무" },
    { label: "계정과목", path: "/finance/accounts", permission: "ledger:read", order: 42.5, group: "finance", section: "전표" },
  ],
};

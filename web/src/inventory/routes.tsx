// (web/src/inventory). 재고·입고 화면 모듈. 재고 화면 위 탭(출고·창고 이동·실사·수불부)은 새 메뉴 없이 이 경로들로 묶는다.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { CountPage } from "./CountPage";
import { IssuePage } from "./IssuePage";
import { LedgerPage } from "./LedgerPage";
import { MonthClosePage } from "./MonthClosePage";
import { MonthStockPage } from "./MonthStockPage";
import { ReceiptPage } from "./ReceiptPage";
import { StockPage } from "./StockPage";
import { TransferPage } from "./TransferPage";
import { inventoryHomeCards } from "./homeCards";

export const inventoryModule: RouteModule = {
  name: "inventory",
  homeCards: inventoryHomeCards,
  routes: [
    { path: "/inventory", element: guarded("inventory:read", <StockPage />) },
    { path: "/inventory/issues", element: guarded("inventory:manage", <IssuePage />) },
    { path: "/inventory/transfers", element: guarded("inventory:manage", <TransferPage />) },
    { path: "/inventory/counts", element: guarded("inventory:manage", <CountPage />) },
    { path: "/inventory/ledger", element: guarded("inventory:read", <LedgerPage />) },
    { path: "/inventory/close", element: guarded("inventory:read", <MonthClosePage />) },
    { path: "/inventory/month-stock", element: guarded("inventory:read", <MonthStockPage />) },
    { path: "/receipts", element: guarded("inventory:manage", <ReceiptPage />) },
  ],
  menu: [
    { label: "재고", path: "/inventory", permission: "inventory:read", order: 30, group: "inventory" },
    { label: "입고", path: "/receipts", permission: "inventory:manage", order: 40, group: "inventory" },
  ],
};

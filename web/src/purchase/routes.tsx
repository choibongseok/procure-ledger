// (web/src/purchase). 구매요청 목록·작성·상세 화면 모듈.
import { HomeTodoAside } from "./HomeTodoAside";
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { BudgetPage } from "./BudgetPage";
import { POPrintPage } from "./POPrintPage";
import { VendorRatingPage } from "./VendorRatingPage";
import { QuotePage } from "./QuotePage";
import { PurchaseDetail } from "./PurchaseDetail";
import { PurchaseForm } from "./PurchaseForm";
import { PurchaseList } from "./PurchaseList";
import { Navigate } from "react-router-dom";
import { ApprovalReportPage } from "../reports/ApprovalReportPage";
import { PurchaseReportPage } from "../reports/PurchaseReportPage";
import { purchaseHomeCards } from "./homeCards";

export const purchaseModule: RouteModule = {
  name: "purchase",
  homeCards: purchaseHomeCards,
  homeAside: [{ id: "todo", title: "할 일·알림", permission: "notification:read", order: 10, Component: HomeTodoAside }],
  routes: [
    { path: "/purchase-requests", element: guarded("purchase:read", <PurchaseList />) },
    { path: "/purchase-requests/new", element: guarded("purchase:create", <PurchaseForm />) },
    { path: "/purchase-requests/:id", element: guarded("purchase:read", <PurchaseDetail />) },
    { path: "/purchase-requests/:id/po", element: guarded("purchase:read", <POPrintPage />) },
    { path: "/vendors/rating", element: guarded("vendor:rating_read", <VendorRatingPage />) },
    { path: "/purchase-requests/:id/quotes", element: guarded("purchase:read", <QuotePage />) },
    { path: "/budget", element: guarded("budget:read", <BudgetPage />) },
    // 업무 보고서(B28): 화면은 web/src/reports, 라우트는 이 모듈에 둔다(공용 라우트 모음을 건드리지 않는다).
    { path: "/reports", element: guarded("report:read", <Navigate to="/reports/purchase" replace />) },
    { path: "/reports/purchase", element: guarded("report:read", <PurchaseReportPage />) },
    { path: "/reports/vendors", element: guarded("vendor:rating_read", <VendorRatingPage report />) },
    { path: "/reports/approvals", element: guarded("report:read", <ApprovalReportPage />) },
    { path: "/purchase-requests/:id/edit", element: guarded("purchase:create", <PurchaseForm />) },
  ],
  menu: [
    { label: "구매요청", path: "/purchase-requests", permission: "purchase:read", order: 10, group: "purchase" },
    { label: "예산 현황", path: "/budget", permission: "budget:read", order: 11, group: "purchase" },
    { label: "거래처 평가", path: "/vendors/rating", permission: "vendor:rating_read", order: 12, group: "purchase" },
    { label: "보고서", path: "/reports", permission: "report:read", order: 17, group: "purchase" },
  ],
};

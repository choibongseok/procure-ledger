// (web/src/document). 결재 양식 문서 화면 모듈(B23): 목록·작성·상세, 양식 관리.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { DocForm } from "./DocForm";
import { DocList } from "./DocList";
import { AuthorityAdmin } from "./AuthorityAdmin";
import { DocNew } from "./DocNew";
import { DocSearchPage } from "./DocSearchPage";
import { DocView } from "./DocView";
import { FormAdminList, FormVersionEditor } from "./FormAdmin";
import { PrintView } from "./PrintView";

export const documentModule: RouteModule = {
  name: "document",
  routes: [
    { path: "/docs", element: guarded("draft:read", <DocList />) },
    { path: "/docs/search", element: guarded("draft:read", <DocSearchPage />) },
    { path: "/docs/new", element: guarded("draft:create", <DocNew />) },
    { path: "/docs/new/:code", element: guarded("draft:create", <DocForm />) },
    { path: "/docs/:id", element: guarded("draft:read", <DocView />) },
    { path: "/docs/:id/edit", element: guarded("draft:create", <DocForm />) },
    { path: "/docs/:id/print", element: guarded("draft:read", <PrintView kind="doc" />) },
    { path: "/purchase-requests/:id/print", element: guarded("purchase:read", <PrintView kind="purchase" />) },
    { path: "/authority", element: guarded("authority:manage", <AuthorityAdmin />) },
    { path: "/forms", element: guarded("form:manage", <FormAdminList />) },
    { path: "/forms/versions/by-form/:code", element: guarded("form:manage", <FormVersionEditor />) },
    { path: "/forms/versions/:id", element: guarded("form:manage", <FormVersionEditor />) },
  ],
  menu: [
    { label: "전자결재", path: "/docs", permission: "draft:read", order: 12, group: "approval" },
    { label: "결재 양식", path: "/forms", permission: "form:manage", order: 13, group: "approval" },
    { label: "전결 규정", path: "/authority", permission: "authority:manage", order: 14, group: "approval" },
  ],
};

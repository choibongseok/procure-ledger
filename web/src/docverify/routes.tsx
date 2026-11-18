// (web/src/docverify). 증빙 문서 검증 화면 모듈.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { DocVerifyPage } from "./DocVerifyPage";
import { docverifyHomeCards } from "./homeCards";

export const docverifyModule: RouteModule = {
  name: "docverify",
  routes: [{ path: "/documents", element: guarded("document:read", <DocVerifyPage />) }],
  menu: [{ label: "문서 검증", path: "/documents", permission: "document:read", order: 50, group: "docai" }],
  homeCards: docverifyHomeCards,
};

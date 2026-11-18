// (web/src/dataimport). 기초 데이터 가져오기 화면 모듈(B35): 허브·대상별 단계 화면·이력.
import { guarded, guardedAny } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { ImportHistoryPage } from "./ImportHistoryPage";
import { ImportHubPage } from "./ImportHubPage";
import { ImportTargetPage } from "./ImportTargetPage";

// 허브와 대상 화면은 마스터(import:master) 또는 금액성(import:balance) 권한이 있으면 열린다. 대상별 권한은 서버가 라우트마다 다시 막는다.
const importers = ["import:master", "import:balance"] as const;

export const dataImportModule: RouteModule = {
  name: "dataimport",
  routes: [
    { path: "/admin/import", element: guardedAny([...importers, "import:read"], <ImportHubPage />) },
    { path: "/admin/import/history", element: guarded("import:read", <ImportHistoryPage />) },
    { path: "/admin/import/:target", element: guardedAny([...importers], <ImportTargetPage />) },
  ],
  menu: [
    { label: "기초 데이터 가져오기", path: "/admin/import", permission: "import:master", anyOf: [...importers], order: 88.1, group: "admin" },
    { label: "가져오기 이력", path: "/admin/import/history", permission: "import:read", order: 88.2, group: "admin" },
  ],
};

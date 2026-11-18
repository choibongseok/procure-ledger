// (web/src/importrecon). 기초 데이터 대조 보고서 화면 모듈(B35).
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { ReconcilePage } from "./ReconcilePage";

export const importReconModule: RouteModule = {
  name: "importrecon",
  routes: [{ path: "/admin/import/reconcile", element: guarded("import:read", <ReconcilePage />) }],
  menu: [{ label: "기초 데이터 대조", path: "/admin/import/reconcile", permission: "import:read", order: 88.3, group: "admin" }],
};

// (web/src/org). 조직도·조직 관리·인사 발령 화면 모듈.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { AppointmentsPage } from "./AppointmentsPage";
import { orgHomeCards } from "./homeCards";
import { ImportPage } from "./ImportPage";
import { OrgChartPage } from "./OrgChartPage";
import { OrgManagePage } from "./OrgManagePage";
import "./org.css";

export const orgModule: RouteModule = {
  name: "org",
  homeCards: orgHomeCards,
  routes: [
    { path: "/org", element: guarded("org:read", <OrgChartPage />) },
    { path: "/org/manage", element: guarded("org:manage", <OrgManagePage />) },
    { path: "/org/appointments", element: guarded("org:manage", <AppointmentsPage />) },
    { path: "/org/import", element: guarded("org:manage", <ImportPage />) },
  ],
  menu: [
    { label: "조직도", path: "/org", permission: "org:read", order: 55, group: "org" },
    { label: "조직 관리", path: "/org/manage", permission: "org:manage", order: 72, group: "org" },
    { label: "인사 발령", path: "/org/appointments", permission: "org:manage", order: 74, group: "org" },
    { label: "조직 일괄 반영", path: "/org/import", permission: "org:manage", order: 75, group: "org" },
  ],
};

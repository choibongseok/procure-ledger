// (web/src/company). 회사 설정 화면 모듈(B34).
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { CompanyPage } from "./CompanyPage";

export const companyModule: RouteModule = {
  name: "company",
  routes: [{ path: "/admin/company", element: guarded("company:manage", <CompanyPage />) }],
  menu: [{ label: "회사 설정", path: "/admin/company", permission: "company:manage", order: 87.5, group: "admin" }],
};

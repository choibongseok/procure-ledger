// 조립 지점(). 영역은 이 파일을 고치지 않고 자기 routes.tsx 의 모듈만 고친다.
//
//   platform: auth/routes.tsx, admin/routes.tsx, org/routes.tsx
//   erp: purchase/routes.tsx, document/routes.tsx, approval/routes.tsx, inventory/routes.tsx, notification/routes.tsx
//   ai: docverify/routes.tsx, llm/routes.tsx
// 홈 카드(B8)는 각 모듈의 homeCards 로 선언한다(layout/routeModule.ts 의 HomeCard).
import { createElement } from "react";
import { adminModule } from "./admin/routes";
import { attendanceModule } from "./attendance/routes";
import { approvalModule } from "./approval/routes";
import { authModule } from "./auth/routes";
import { docverifyModule } from "./docverify/routes";
import { companyModule } from "./company/routes";
import { dataImportModule } from "./dataimport/routes";
import { importReconModule } from "./importrecon/routes";
import { financeModule } from "./finance/routes";
import { documentModule } from "./document/routes";
import { inventoryModule } from "./inventory/routes";
import { llmModule } from "./llm/routes";
import { notificationModule } from "./notification/routes";
import { orgModule } from "./org/routes";
import { purchaseModule } from "./purchase/routes";
import { searchModule } from "./search/routes";
import { HomePage } from "./layout/HomePage";
import type { HomeAsideSection, HomeCard, MenuItem, RouteModule } from "./layout/routeModule";

/** 업무 화면 모듈. 홈 카드는 여기 모듈들의 homeCards 에서 모은다(B8). */
const businessModules: RouteModule[] = [purchaseModule, documentModule, approvalModule, notificationModule, inventoryModule, financeModule, docverifyModule, orgModule, attendanceModule, llmModule, adminModule, companyModule, dataImportModule, importReconModule, searchModule];

/** 홈 카드 전체(권한 필터는 HomePage 가 한다). */
export const allHomeCards: HomeCard[] = businessModules.flatMap((m) => m.homeCards ?? []);

/** 홈 우측 패널 절 전체(권한 필터는 HomePage 가 한다). */
export const allHomeAside: HomeAsideSection[] = businessModules.flatMap((m) => m.homeAside ?? []);

/** 로그인 뒤 첫 화면. 로그인만 되어 있으면 열리고, 카드는 카드별 권한으로 거른다. */
export const homeModule: RouteModule = {
  name: "home",
  routes: [{ path: "/", element: createElement(HomePage, { cards: allHomeCards, asideSections: allHomeAside }) }],
  menu: [{ label: "홈", path: "/", permission: "authenticated", order: 0, group: "home" }],
};

/** 레이아웃 안에 그려지는 모듈(메뉴가 있는 업무 화면). */
export const appModules: RouteModule[] = [homeModule, ...businessModules];

/** 레이아웃 밖 최상위 모듈(로그인). */
export const publicModules: RouteModule[] = [authModule];

export const allMenu: MenuItem[] = appModules.flatMap((m) => m.menu);

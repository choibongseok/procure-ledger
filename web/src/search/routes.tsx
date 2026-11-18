// (web/src/search). 통합 검색 라우트. 메뉴는 없다(GNB 검색 입력에서만 들어온다).
// 화면 안에서 절별 권한으로 나누므로 라우트는 로그인만 있으면 연다.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { KnowledgeDocPage } from "./KnowledgeDocPage";
import { SearchPage } from "./SearchPage";

export const searchModule: RouteModule = {
  name: "search",
  routes: [
    { path: "/search", element: guarded("authenticated", <SearchPage />) },
    { path: "/knowledge/:id", element: guarded("knowledge:search", <KnowledgeDocPage />) },
  ],
  menu: [],
};

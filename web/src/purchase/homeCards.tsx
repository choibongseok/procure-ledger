// 홈 화면 카드 선언(내 요청 진행). 권한이 없으면 홈이 이 카드를 그리지 않으므로 API 도 호출되지 않는다.
import type { HomeCard } from "../layout/routeModule";
import { MyRequestsCard } from "./MyRequestsCard";

export const purchaseHomeCards: HomeCard[] = [
  { id: "my-requests", title: "내 요청 진행", permission: "purchase:create", order: 20, Component: MyRequestsCard },
];

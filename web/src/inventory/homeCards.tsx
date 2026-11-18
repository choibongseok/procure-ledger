// 홈 화면 카드 선언(재고 경보). 권한이 없으면 홈이 이 카드를 그리지 않으므로 API 도 호출되지 않는다.
import type { HomeCard } from "../layout/routeModule";
import { StockAlertCard } from "./StockAlertCard";

export const inventoryHomeCards: HomeCard[] = [
  {
    id: "stock-alerts",
    title: "재고 경보",
    permission: "inventory:read",
    order: 30,
    Component: StockAlertCard,
  },
];

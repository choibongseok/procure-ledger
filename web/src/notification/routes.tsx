// (web/src/notification). 내 알림 화면 모듈. 홈 카드와 메뉴는 notification:read 권한(전 역할)으로 건다.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { NotificationCard } from "./NotificationCard";
import { NotificationsPage } from "./NotificationsPage";
import { NotifyPrefsPage } from "./NotifyPrefsPage";

export const notificationModule: RouteModule = {
  name: "notification",
  homeCards: [{ id: "notifications", title: "알림", permission: "notification:read", order: 5, Component: NotificationCard }],
  routes: [
    { path: "/notifications", element: guarded("notification:read", <NotificationsPage />) },
    { path: "/notifications/settings", element: guarded("notification:read", <NotifyPrefsPage />) }, // 내 알림 설정(B36)
  ],
  menu: [{ label: "알림", path: "/notifications", permission: "notification:read", order: 15, group: "user" }],
};

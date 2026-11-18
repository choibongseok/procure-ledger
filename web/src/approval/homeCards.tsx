// 홈 화면 카드 선언(결재 대기). 권한이 없으면 홈이 이 카드를 그리지 않으므로 API 도 호출되지 않는다.
import type { HomeCard } from "../layout/routeModule";
import { ApprovalCard } from "./ApprovalCard";

export const approvalHomeCards: HomeCard[] = [
  { id: "approval-pending", title: "결재 대기", permission: "approval:decide", order: 10, Component: ApprovalCard },
];

// (web/src/approval). 결재함·결재 화면 모듈.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { ApprovalDecision } from "./ApprovalDecision";
import { ApprovalInbox } from "./ApprovalInbox";
import { AbsencePage } from "./DelegationPanel";
import { approvalHomeCards } from "./homeCards";

export const approvalModule: RouteModule = {
  name: "approval",
  homeCards: approvalHomeCards,
  routes: [
    { path: "/approvals", element: guarded("approval:read", <ApprovalInbox />) },
    { path: "/approvals/absence", element: guarded("approval:delegate", <AbsencePage />) },
    { path: "/approvals/:id", element: guarded("approval:read", <ApprovalDecision />) },
  ],
  menu: [{ label: "결재함", path: "/approvals", permission: "approval:read", order: 11, group: "approval" }],
};

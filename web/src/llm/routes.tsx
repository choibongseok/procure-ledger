// (web/src/llm). AI 사용량 화면 모듈.
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { EvalHistoryPage } from "./EvalHistoryPage";
import { EvalRunPage } from "./EvalRunPages";
import { EvalSetPage, EvalSetsPage } from "./EvalSetPages";
import { llmHomeAside } from "./homeAside";
import { llmHomeCards } from "./homeCards";
import { LLMUsagePage } from "./LLMUsagePage";
import { AiCostAdminPage, AiCostMyPage } from "../aicost/AiCostPages";
import { AiCostBudgetsPage } from "../aicost/AiCostRunPanel";
import { AiCostPreviewPage, AiCostRulesPage } from "../aicost/AiCostRulesPage";
import { McpToolEditPage, McpToolsPage } from "../mcptool/McpToolPages";
import { McpUsagePage } from "./McpUsagePage";

export const llmModule: RouteModule = {
  name: "llm",
  routes: [
    { path: "/llm-usage", element: guarded("llm:usage_read", <LLMUsagePage />) },
    { path: "/eval-history", element: guarded("eval:read", <EvalHistoryPage />) },
    { path: "/mcp-usage", element: guarded("usage:read", <McpUsagePage />) },
    { path: "/admin/eval/sets", element: guarded("eval:read", <EvalSetsPage />) },
    { path: "/admin/eval/sets/:code", element: guarded("eval:read", <EvalSetPage />) },
    { path: "/admin/eval/runs/:id", element: guarded("eval:read", <EvalRunPage />) },
    { path: "/admin/ai-cost", element: guarded("cost:read", <AiCostAdminPage />) },
    { path: "/admin/ai-cost/rules", element: guarded("cost:read", <AiCostRulesPage />) },
    { path: "/admin/ai-cost/preview", element: guarded("cost:read", <AiCostPreviewPage />) },
    { path: "/admin/ai-cost/budgets", element: guarded("cost:read", <AiCostBudgetsPage />) },
    { path: "/ai-cost/my", element: guarded("report:read", <AiCostMyPage />) },
    { path: "/admin/mcp-tools", element: guarded("mcptool:read", <McpToolsPage />) },
    { path: "/admin/mcp-tools/new", element: guarded("mcptool:manage", <McpToolEditPage />) },
    { path: "/admin/mcp-tools/:id", element: guarded("mcptool:read", <McpToolEditPage />) },
  ],
  menu: [
    { label: "AI 사용량", path: "/llm-usage", permission: "llm:usage_read", order: 60, group: "docai" },
    { label: "평가 이력", path: "/eval-history", permission: "eval:read", order: 65, group: "docai" },
    { label: "평가 세트", path: "/admin/eval/sets", permission: "eval:read", order: 65.5, group: "docai" },
    { label: "AI 도구 만들기", path: "/admin/mcp-tools", permission: "mcptool:read", order: 65.6, group: "docai" },
    { label: "AI 비용", path: "/admin/ai-cost", permission: "cost:read", order: 65.7, group: "docai" },
    { label: "우리 부서 AI 비용", path: "/ai-cost/my", permission: "report:read", order: 65.8, group: "docai" },
    { label: "AI 연결 사용 현황", path: "/mcp-usage", permission: "usage:read", order: 66, group: "docai" },
  ],
  homeCards: llmHomeCards,
  homeAside: llmHomeAside,
};

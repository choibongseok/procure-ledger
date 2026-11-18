import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ROLE_PERMISSIONS } from "../auth/permissions";
import { defaultPeriod, formatMs, formatRate, McpUsagePage, toolLabel, type McpUsage } from "./McpUsagePage";
import { llmModule } from "./routes";

function json(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
}

const stats = { calls: 0, success: 0, denied: 0, authFailed: 0, rateLimited: 0, errors: 0, rejectRate: 0, avgMs: 0, p50Ms: 0, p95Ms: 0, maxMs: 0 };

const usage: McpUsage = {
  from: "2026-11-04",
  to: "2026-11-10",
  ratePerMin: 30,
  total: { ...stats, calls: 10, success: 6, denied: 2, rateLimited: 1, errors: 1, rejectRate: 0.4, avgMs: 12, p95Ms: 40, maxMs: 55 },
  tools: [
    { tool: "search_items", ...stats, calls: 8, success: 6, rateLimited: 1, errors: 1, rejectRate: 0.25, avgMs: 10, p95Ms: 30, maxMs: 40 },
    { tool: "approve_or_reject", ...stats, calls: 2, denied: 2, rejectRate: 1, avgMs: 20, p95Ms: 55, maxMs: 55 },
    { tool: "brand_new_tool", ...stats, calls: 1, success: 1, avgMs: 1, p95Ms: 1, maxMs: 1 },
  ],
  tokens: [
    { tokenId: 7, name: "내 노트북", prefix: "plp_ab12", owner: "김직원", revoked: false, lastAt: "2026-11-10T01:02:03Z", ...stats, calls: 9, success: 6 },
    { tokenId: 8, name: "구형", prefix: "plp_cd34", owner: "이팀장", revoked: true, lastAt: "2026-11-09T01:02:03Z", ...stats, calls: 1 },
    { tokenId: 0, name: "", prefix: "", owner: "", revoked: false, lastAt: "2026-11-09T02:00:00Z", ...stats, calls: 1, authFailed: 1 },
  ],
};

function renderPage(user: CurrentUser) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>
        <McpUsagePage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("AI 연결 사용 현황 화면", () => {
  it("도구는 한국어 라벨로, 토큰은 이름·앞자리·소유자로 보이고 내부 용어는 보이지 않는다", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => json(usage));
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    const tools = await screen.findByRole("table", { name: "도구별 사용량" });
    expect(within(tools).getByText("품목 검색")).toBeInTheDocument();
    expect(within(tools).getByText("결재 승인·반려")).toBeInTheDocument();
    expect(within(tools).getByText("기타 도구")).toBeInTheDocument();
    expect(screen.queryByText("search_items")).toBeNull();
    expect(screen.queryByText("approve_or_reject")).toBeNull();
    expect(within(tools).getByText("25.0%")).toBeInTheDocument();

    const tokens = screen.getByRole("table", { name: "토큰별 사용량" });
    expect(within(tokens).getByText("내 노트북 (plp_ab12…)")).toBeInTheDocument();
    expect(within(tokens).getByText("김직원")).toBeInTheDocument();
    expect(within(tokens).getByText(/폐기됨/)).toBeInTheDocument();
    expect(within(tokens).getByText("폐기·만료된 토큰으로 호출한 기록")).toBeInTheDocument();
    expect(screen.getByText(/토큰당 분당 30번/)).toBeInTheDocument();

    // 기본 기간으로 처음 불러온다.
    const url = String(spy.mock.calls[0][0]);
    const p = defaultPeriod();
    expect(url).toContain(`/api/mcp/usage?from=${p.from}&to=${p.to}`);
  });

  it("기간을 바꿔 조회하면 그 기간으로 다시 요청한다", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => json(usage));
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 });
    await screen.findByRole("table", { name: "도구별 사용량" });
    const form = screen.getByRole("form", { name: "조회 기간" });
    const from = within(form).getByLabelText("시작일");
    const to = within(form).getByLabelText("종료일");
    fireEvent.change(from, { target: { value: "2026-10-01" } });
    fireEvent.change(to, { target: { value: "2026-10-31" } });
    fireEvent.click(within(form).getByRole("button", { name: "조회" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).includes("from=2026-10-01&to=2026-10-31"))).toBe(true));
  });

  it("기록이 없으면 안내 문구를 보이고 서버 오류 문장을 그대로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...usage, ratePerMin: 0, total: stats, tools: [], tokens: [] }));
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    expect((await screen.findAllByText("이 기간에 호출 기록이 없습니다.")).length).toBe(2);
    expect(screen.getByText(/분당 호출 한도는 설정되어 있지 않습니다/)).toBeInTheDocument();
  });

  it("사용자 합산 한도가 있으면 문구에 함께 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...usage, ratePerMin: 30, ratePerUserPerMin: 60 }));
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    expect(await screen.findByText(/사용자당 분당 60번\(토큰 합산\)/)).toBeInTheDocument();
  });

  it("서버가 거절하면 오류 문장을 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({ error: "조회 기간은 최대 366일입니다" }), { status: 400 })),
    );
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    expect(await screen.findByRole("alert")).toHaveTextContent("조회 기간은 최대 366일입니다");
  });
});

describe("AI 연결 사용 현황 메뉴·라우트 권한", () => {
  it("메뉴와 라우트가 usage:read 로 막히고 그 권한은 관리자·감사 역할만 가진다", () => {
    const menu = llmModule.menu?.find((m) => m.path === "/mcp-usage");
    expect(menu?.label).toBe("AI 연결 사용 현황");
    expect(menu?.permission).toBe("usage:read");
    expect(llmModule.routes.some((r) => r.path === "/mcp-usage")).toBe(true);
    const holders = Object.entries(ROLE_PERMISSIONS)
      .filter(([, perms]) => (perms as readonly string[]).includes("usage:read"))
      .map(([role]) => role)
      .sort();
    expect(holders).toEqual(["admin", "auditor"]);
  });
});

describe("표시 형식", () => {
  it("도구 라벨·거부율·지연", () => {
    expect(toolLabel("get_stock")).toBe("재고 조회");
    expect(toolLabel("zzz")).toBe("기타 도구");
    expect(formatRate(0.123)).toBe("12.3%");
    expect(formatMs(1234)).toBe("1,234ms");
  });
  it("기본 기간은 오늘을 포함한 7일", () => {
    expect(defaultPeriod(new Date(2026, 10, 10))).toEqual({ from: "2026-11-04", to: "2026-11-10" });
  });
});

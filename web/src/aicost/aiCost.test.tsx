import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { roleHas } from "../auth/permissions";
import { llmModule } from "../llm/routes";
import { AiCostAdminPage, AiCostMyPage, fxText, money, purposeLabel, usd, won, type CostReport, type MyReport } from "./AiCostPages";

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

const row = { entityId: 1, departmentId: 901, departmentName: "영업", purpose: "docverify.extract", provider: "anthropic-main", model: "m1", calls: 3, failed: 1, inputTokens: 1000, outputTokens: 500, costMicroUsd: 2_000_000, costKrw: 2700 };

const report: CostReport = {
  month: "2026-11",
  fxUsdKrwX1000: 1_350_000,
  totalMicroUsd: 2_500_000,
  totalKrw: 3375,
  calls: 4,
  failed: 1,
  departments: [
    { entityId: 1, departmentId: 901, departmentName: "영업", calls: 3, costMicroUsd: 2_000_000, costKrw: 2700 },
    { entityId: 1, departmentId: 0, departmentName: "(부서 미지정)", calls: 1, costMicroUsd: 500_000, costKrw: 675 },
  ],
  providers: [{ provider: "anthropic-main", calls: 4, costMicroUsd: 2_500_000, costKrw: 3375, actualKrw: 4000, adjustRatio: 1.185, zeroPriceCalls: 0 }],
  rows: [row, { ...row, departmentId: 0, departmentName: "(부서 미지정)", purpose: "brand.new", calls: 1, failed: 0, costMicroUsd: 500_000, costKrw: 675 }],
  adjustments: [{ id: 1, month: "2026-11", provider: "anthropic-main", actualKrw: 4000, note: "11월 청구서", enteredByName: "재무", enteredAt: "2026-11-14T00:00:00Z" }],
  warnings: ["단가가 설정되지 않은 공급자의 호출 2건은 금액이 0 입니다. 실제 청구액을 입력하면 보정됩니다."],
};

const mine: MyReport = {
  month: "2026-11",
  departmentId: 901,
  departmentName: "영업",
  fxUsdKrwX1000: 0,
  totalMicroUsd: 3_300_000,
  totalKrw: 0,
  calls: 5,
  forecast: { elapsedDays: 14, daysInMonth: 30, costMicroUsd: 7_071_428, costKrw: 0 },
  rows: [row],
  trend: Array.from({ length: 12 }, (_, i) => ({ month: `2026-${String(i + 1).padStart(2, "0")}`, calls: i, costMicroUsd: i * 100_000, costKrw: 0 })),
  topUsers: [{ name: "영업사원", calls: 3, costMicroUsd: 2_000_000, costKrw: 0 }],
  warnings: [],
};

function renderPage(user: CurrentUser, el: ReactElement) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>{el}</AuthProvider>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("AI 비용 전사 화면", () => {
  it("부서·공급자·상세 표를 한국어로 보이고 내부 용어는 보이지 않는다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json(report));
    renderPage({ id: 1, name: "최재무", role: "finance", departmentId: 900 }, <AiCostAdminPage />);
    const depts = await screen.findByRole("table", { name: "부서별 AI 비용" });
    expect(within(depts).getByText("영업")).toBeInTheDocument();
    expect(within(depts).getByText("2,700원")).toBeInTheDocument();
    expect(within(depts).getByText("(부서 미지정)")).toBeInTheDocument();
    const detail = screen.getByRole("table", { name: "AI 비용 상세" });
    expect(within(detail).getByText("증빙 문서 판정")).toBeInTheDocument();
    expect(within(detail).getByText("기타 용도")).toBeInTheDocument();
    expect(screen.queryByText("docverify.extract")).toBeNull();
    expect(screen.queryByText("brand.new")).toBeNull();
    expect(screen.getByText(/호출 2건은 금액이 0/)).toBeInTheDocument();
    const prov = screen.getByRole("table", { name: "공급자별 AI 비용" });
    expect(within(prov).getByText("1.19배")).toBeInTheDocument();
  });

  it("재무는 환율·청구액을 입력할 수 있고 서버 요청이 맞다", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => json(report));
    renderPage({ id: 1, name: "최재무", role: "finance", departmentId: 900 }, <AiCostAdminPage />);
    const fxForm = await screen.findByRole("form", { name: "환율 입력" });
    fireEvent.change(within(fxForm).getByLabelText(/1달러당 원/), { target: { value: "1350.5" } });
    fireEvent.click(within(fxForm).getByRole("button", { name: "환율 저장" }));
    await waitFor(() => {
      const call = spy.mock.calls.find((c) => String(c[0]).includes("/api/admin/ai-cost/fx"));
      expect(call).toBeTruthy();
      expect(JSON.parse(String((call![1] as RequestInit).body))).toEqual({ month: expect.any(String), usdKrwX1000: 1_350_500 });
    });
    const adj = screen.getByRole("form", { name: "청구액 입력" });
    fireEvent.change(within(adj).getByLabelText("공급자"), { target: { value: "anthropic-main" } });
    fireEvent.change(within(adj).getByLabelText("실제 청구액(원)"), { target: { value: "4,000" } });
    fireEvent.click(within(adj).getByRole("button", { name: "청구액 저장" }));
    await waitFor(() => {
      const call = spy.mock.calls.find((c) => String(c[0]).includes("/api/admin/ai-cost/adjustments"));
      expect(call).toBeTruthy();
      expect(JSON.parse(String((call![1] as RequestInit).body))).toMatchObject({ provider: "anthropic-main", actualKrw: 4000 });
    });
  });

  it("관리자·감사는 조회만 하고 입력 양식이 없다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json(report));
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 }, <AiCostAdminPage />);
    await screen.findByRole("table", { name: "부서별 AI 비용" });
    expect(screen.queryByRole("form", { name: "환율 입력" })).toBeNull();
    expect(screen.getByText("환율과 청구액은 재무 담당이 입력합니다.")).toBeInTheDocument();
  });

  it("사용 기록이 없으면 안내 문구, 환율이 없으면 달러로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...report, calls: 0, failed: 0, totalMicroUsd: 0, departments: [], providers: [], rows: [], adjustments: [], warnings: [] }));
    renderPage({ id: 1, name: "최재무", role: "finance", departmentId: 900 }, <AiCostAdminPage />);
    expect(await screen.findByText("이 달에는 AI 사용 기록이 없습니다.")).toBeInTheDocument();
  });

  it("서버가 거절하면 오류 문장을 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ error: "권한이 없습니다" }, 403));
    renderPage({ id: 1, name: "최재무", role: "finance", departmentId: 900 }, <AiCostAdminPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("권한이 없습니다");
  });
});

describe("우리 부서 AI 비용 화면", () => {
  it("본인 부서의 추이·예상·상위 사용자를 보이고 환율이 없으면 달러로 보인다", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => json(mine));
    renderPage({ id: 2, name: "영업팀장", role: "manager", departmentId: 901 }, <AiCostMyPage />);
    expect(await screen.findByText(/이번 달 예상/)).toBeInTheDocument();
    expect(screen.getByText(/영업 · 2026-11/)).toBeInTheDocument();
    expect(screen.getAllByText("$3.30").length).toBeGreaterThan(0);
    expect(within(screen.getByRole("table", { name: "월별 추이" })).getAllByRole("row")).toHaveLength(13);
    expect(within(screen.getByRole("table", { name: "상위 사용자" })).getByText("영업사원")).toBeInTheDocument();
    expect(String(spy.mock.calls[0][0])).toContain("/api/ai-cost/my?month=");
    expect(screen.queryByText("프롬프트")).toBeNull();
  });

  it("기록이 없으면 안내 문구를 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...mine, rows: [], calls: 0, totalMicroUsd: 0, forecast: undefined, topUsers: [] }));
    renderPage({ id: 2, name: "영업팀장", role: "manager", departmentId: 901 }, <AiCostMyPage />);
    expect(await screen.findByText("이 달에는 우리 부서의 AI 사용 기록이 없습니다.")).toBeInTheDocument();
  });
});

describe("서식·권한·메뉴", () => {
  it("금액·환율·용도 서식", () => {
    expect(usd(1_234_567)).toBe("$1.23");
    expect(won(1234567)).toBe("1,234,567원");
    expect(money(1_000_000, 1350, 0)).toBe("$1.00");
    expect(money(1_000_000, 1350, 1_350_000)).toBe("1,350원");
    expect(fxText(1_350_500)).toBe("1,350.5");
    expect(purposeLabel("eval.run")).toBe("평가 세트 실행");
    expect(purposeLabel("zzz")).toBe("기타 용도");
  });

  it("권한은 서버 역할표와 같다: 재무만 입력, 관리자·감사는 조회, 직원은 없음", () => {
    expect(roleHas("finance", "cost:allocate")).toBe(true);
    expect(roleHas("finance", "cost:read")).toBe(true);
    expect(roleHas("admin", "cost:read")).toBe(true);
    expect(roleHas("admin", "cost:allocate")).toBe(false);
    expect(roleHas("auditor", "cost:read")).toBe(true);
    expect(roleHas("manager", "cost:read")).toBe(false);
    expect(roleHas("staff", "cost:read")).toBe(false);
    expect(roleHas("staff", "report:read")).toBe(false);
  });

  it("라우트와 메뉴가 등록돼 있다", () => {
    expect(llmModule.routes.some((r) => r.path === "/admin/ai-cost")).toBe(true);
    expect(llmModule.routes.some((r) => r.path === "/ai-cost/my")).toBe(true);
    const labels = llmModule.menu.map((m) => m.label);
    expect(labels).toContain("AI 비용");
    expect(labels).toContain("우리 부서 AI 비용");
  });
});

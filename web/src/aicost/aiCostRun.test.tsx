import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { llmModule } from "../llm/routes";
import { AiCostMyPage, type MyReport } from "./AiCostPages";
import { AiCostBudgetsPage, RunPanel, type BudgetRow, type RunInfo } from "./AiCostRunPanel";

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

const finance: CurrentUser = { id: 1, name: "최재무", role: "finance", departmentId: 900 };
const finance2: CurrentUser = { id: 4, name: "이재무", role: "finance", departmentId: 900 };
const auditor: CurrentUser = { id: 3, name: "이감사", role: "auditor", departmentId: 1 };

function renderPage(user: CurrentUser, el: ReactElement) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>{el}</AuthProvider>
    </MemoryRouter>,
  );
}

const run: RunInfo = {
  id: 7,
  month: "2026-10",
  entityId: 1,
  ruleVersion: 2,
  status: "confirmed",
  totalKrw: 3800,
  directKrw: 3000,
  sharedKrw: 800,
  journalNo: "JV-2026-00042",
  journalStatus: "draft",
  confirmedByName: "최재무",
  confirmedAt: "2026-11-02T01:00:00Z",
  lines: [],
};

function routeRun(r: RunInfo | null, spyBody: { current?: string } = {}) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const url = String(input);
    const method = (init as RequestInit | undefined)?.method ?? "GET";
    if (url.includes("/api/admin/ai-cost/run?")) return json({ run: r });
    if (method !== "GET") {
      spyBody.current = String((init as RequestInit).body ?? "");
      return json({ ...(r ?? run) });
    }
    return json({}, 404);
  });
}

afterEach(() => vi.restoreAllMocks());

describe("월 확정 패널", () => {
  it("확정 전에는 확정 단추가 있고, 눌러 확인하면 확정 요청을 보낸다", async () => {
    const body: { current?: string } = {};
    const spy = routeRun(null, body);
    const changed = vi.fn();
    renderPage(finance, <RunPanel month="2026-10" canConfirm onChanged={changed} />);
    fireEvent.click(await screen.findByRole("button", { name: "이 달 배부 확정" }));
    const dlg = await screen.findByRole("dialog");
    expect(within(dlg).getByText(/비용 전표 초안이 만들어지고/)).toBeInTheDocument();
    fireEvent.click(within(dlg).getByRole("button", { name: "확정" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).endsWith("/api/admin/ai-cost/runs") && (c[1] as RequestInit).method === "POST")).toBe(true));
    expect(JSON.parse(body.current ?? "{}")).toMatchObject({ month: "2026-10", version: 0 });
    expect(await screen.findByText(/배부를 확정하고 비용 전표 초안을 만들었습니다/)).toBeInTheDocument();
    expect(changed).toHaveBeenCalled();
  });

  it("막는 문제가 있으면 확정 단추를 누를 수 없고, 감사는 확정 단추가 없다", async () => {
    routeRun(null);
    const { unmount } = renderPage(finance, <RunPanel month="2026-10" canConfirm={false} onChanged={() => undefined} />);
    expect(await screen.findByRole("button", { name: "이 달 배부 확정" })).toBeDisabled();
    unmount();
    routeRun(null);
    renderPage(auditor, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    expect(await screen.findByText("확정은 재무 담당이 합니다.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "이 달 배부 확정" })).toBeNull();
  });

  it("확정된 달은 확정 정보·전표 링크·해제 요청 양식을 보이고 영문 코드가 없다", async () => {
    const body: { current?: string } = {};
    const spy = routeRun(run, body);
    renderPage(finance, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    expect(await screen.findByText(/확정됨/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "JV-2026-00042" })).toHaveAttribute("href", "/finance/journals/JV-2026-00042");
    expect(screen.getByText(/임시/)).toBeInTheDocument();
    expect(screen.queryByText("draft")).toBeNull();
    expect(screen.getByText(/환율·청구액을 바꾸거나 다시 계산할 수 없습니다/)).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "해제 요청" });
    expect(within(form).getByRole("button", { name: "해제 요청" })).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("해제 사유"), { target: { value: "규칙을 잘못 골랐습니다" } });
    fireEvent.click(within(form).getByRole("button", { name: "해제 요청" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).endsWith("/runs/7/release-request"))).toBe(true));
    expect(JSON.parse(body.current ?? "{}")).toEqual({ reason: "규칙을 잘못 골랐습니다" });
  });

  it("해제 요청 중이면 요청자 본인에게는 확인 단추가 없고 다른 재무에게만 보인다", async () => {
    const pending: RunInfo = { ...run, release: { id: 1, status: "pending", reason: "틀림", requestedBy: 1, requestedByName: "최재무", requestedAt: "2026-11-03T00:00:00Z" } };
    routeRun(pending);
    const { unmount } = renderPage(finance, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    expect(await screen.findByText(/다른 재무 담당의 확인을 기다립니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "해제 확인" })).toBeNull();
    expect(screen.getByText("해제는 요청한 사람이 확인할 수 없습니다.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "요청 취소" })).toBeInTheDocument();
    unmount();
    const spy = routeRun(pending);
    renderPage(finance2, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    fireEvent.click(await screen.findByRole("button", { name: "해제 확인" }));
    const dlg = await screen.findByRole("alertdialog");
    fireEvent.click(within(dlg).getByRole("button", { name: "해제" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).endsWith("/runs/7/release-confirm"))).toBe(true));
  });

  it("전표가 취소됐으면 다시 만들기 단추를 보이고, 서버 거절 문장을 그대로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.includes("/run?")) return json({ run: { ...run, journalStatus: "canceled" } });
      if ((init as RequestInit | undefined)?.method === "POST") return json({ error: "이 달 장부가 마감되어 전표를 만들 수 없습니다. 장부 마감을 해제한 뒤 다시 확정하세요." }, 409);
      return json({}, 404);
    });
    renderPage(finance, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    fireEvent.click(await screen.findByRole("button", { name: "전표 다시 만들기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("이 달 장부가 마감되어 전표를 만들 수 없습니다");
  });
});

describe("AI 비용 예산 화면", () => {
  const rows: BudgetRow[] = [
    { entityId: 1, departmentId: 900, departmentName: "운영", amountKrw: null, usedKrw: 0, pct: 0, fxEstimated: false },
    { entityId: 1, departmentId: 901, departmentName: "영업", amountKrw: 1000, usedKrw: 810, pct: 81, fxEstimated: true },
  ];

  it("부서별 예산·사용률을 보이고 재무는 입력해 저장한다", async () => {
    const body: { current?: string } = {};
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input).includes("/budgets") && ((init as RequestInit | undefined)?.method ?? "GET") === "GET") return json({ budgets: rows });
      body.current = String((init as RequestInit).body ?? "");
      return json({});
    });
    renderPage(finance, <AiCostBudgetsPage />);
    const t = await screen.findByRole("table", { name: "부서별 AI 비용 예산" });
    expect(within(t).getByText("예산 없음")).toBeInTheDocument();
    expect(within(t).getByText("81%")).toBeInTheDocument();
    expect(within(t).getByText(/환율 어림/)).toBeInTheDocument();
    fireEvent.change(within(t).getByLabelText("운영 예산"), { target: { value: "5,000" } });
    fireEvent.click(within(t).getAllByRole("button", { name: "저장" })[0]);
    await waitFor(() => expect(spy.mock.calls.some((c) => (c[1] as RequestInit)?.method === "PUT")).toBe(true));
    expect(JSON.parse(body.current ?? "{}")).toMatchObject({ departmentId: 900, amountKrw: 5000 });
    expect(await screen.findByText("운영 예산을 저장했습니다.")).toBeInTheDocument();
  });

  it("감사는 조회만 하고 입력 칸이 없다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ budgets: rows }));
    renderPage(auditor, <AiCostBudgetsPage />);
    const t = await screen.findByRole("table", { name: "부서별 AI 비용 예산" });
    expect(within(t).queryByLabelText("운영 예산")).toBeNull();
  });

  it("라우트가 등록돼 있다", () => {
    expect(llmModule.routes.some((r) => r.path === "/admin/ai-cost/budgets")).toBe(true);
  });
});

describe("우리 부서 화면의 예산 대비", () => {
  const base: MyReport = {
    month: "2026-11",
    departmentId: 901,
    departmentName: "영업",
    fxUsdKrwX1000: 1_000_000,
    totalMicroUsd: 810_000,
    totalKrw: 810,
    calls: 3,
    rows: [],
    trend: Array.from({ length: 12 }, (_, i) => ({ month: `2026-${String(i + 1).padStart(2, "0")}`, calls: 0, costMicroUsd: 0, costKrw: 0 })),
    topUsers: [],
    warnings: [],
  };
  const lead: CurrentUser = { id: 2, name: "영업팀장", role: "manager", departmentId: 901 };

  it("80% 이상이면 경고, 100% 이상이면 오류로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...base, budget: { amountKrw: 1000, usedKrw: 810, pct: 81, fxEstimated: false } }));
    const { unmount } = renderPage(lead, <AiCostMyPage />);
    expect(await screen.findByText(/사용 810원 \/ 예산 1,000원 \(81%\)/)).toBeInTheDocument();
    expect(screen.getByText(/예산의 81% 에 닿았습니다/)).toBeInTheDocument();
    unmount();
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ ...base, budget: { amountKrw: 1000, usedKrw: 1200, pct: 120, fxEstimated: true } }));
    renderPage(lead, <AiCostMyPage />);
    expect(await screen.findByText("AI 비용이 이번 달 예산을 넘었습니다.")).toBeInTheDocument();
    expect(screen.getByText(/이전 달 환율로 어림한 값/)).toBeInTheDocument();
  });

  it("예산이 없으면 설정 요청 안내를 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json(base));
    renderPage(lead, <AiCostMyPage />);
    expect(await screen.findByText("이 부서는 이번 달 AI 비용 예산이 없습니다. 재무 담당에게 설정을 요청하세요.")).toBeInTheDocument();
  });
});

describe("법인 간 거래 표시", () => {
  it("확정 실행에 법인 간 공용 비용과 지급 법인을 보인다", async () => {
    routeRun({ ...run, groupShareKrw: 250, counterpartyName: "본사" } as RunInfo);
    renderPage(finance, <RunPanel month="2026-10" canConfirm onChanged={() => undefined} />);
    expect(await screen.findByText(/법인 간 공용 비용 250원 포함 \(지급 법인: 본사, 표시만 하며 상계·연결 조정은 하지 않습니다\)/)).toBeInTheDocument();
  });
});

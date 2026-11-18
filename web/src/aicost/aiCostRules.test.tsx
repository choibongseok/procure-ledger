import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { llmModule } from "../llm/routes";
import { AiCostPreviewPage, AiCostRulesPage, describeSelector, type PreviewResult, type RuleSet, type RuleSummary } from "./AiCostRulesPage";

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

const depts = [
  { id: 900, name: "운영", entityId: 1 },
  { id: 901, name: "영업", entityId: 1 },
];
const versions: RuleSummary[] = [{ version: 1, validFromMonth: "2026-01", note: "기본 규칙", createdByName: "재무", createdAt: "2026-11-01T00:00:00Z", poolCount: 2 }];
const v1: RuleSet = {
  version: 1,
  validFromMonth: "2026-01",
  note: "기본 규칙",
  pools: [
    { name: "평가·점검", selector: { purposes: ["eval.run"] }, driver: "usage", targetsAll: true, targets: [] },
    { name: "부서 미지정", selector: { unassignedDept: true }, driver: "headcount", targetsAll: true, targets: [] },
  ],
};

function route(over: Record<string, unknown> = {}) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const url = String(input);
    const method = (init as RequestInit | undefined)?.method ?? "GET";
    if (url.includes("/api/admin/ai-cost/departments")) return json({ departments: depts, entities: (over.entities as { id: number; name: string }[]) ?? [{ id: 1, name: "본사" }] });
    if (url.includes("/api/admin/ai-cost/run?")) return json({ run: null });
    if (url.includes("/api/admin/ai-cost/settings")) return json({ expenseAccount: "5290", options: [{ code: "5260", name: "지급수수료" }, { code: "5290", name: "기타비용" }] });
    if (url.endsWith("/api/admin/ai-cost/rules") && method === "GET") return json({ versions: (over.versions as RuleSummary[]) ?? versions });
    if (url.includes("/api/admin/ai-cost/rules/1")) return json(v1);
    if (url.endsWith("/api/admin/ai-cost/rules/default")) return json({ version: 1, warnings: [] });
    if (url.endsWith("/api/admin/ai-cost/rules") && method === "POST") return json({ version: 2, warnings: ["풀 'a' 과 'b' 의 사용 조건이 겹칩니다. 겹치는 호출은 앞의 풀에 들어갑니다."] });
    if (url.includes("/api/admin/ai-cost/preview")) return json((over.preview as PreviewResult) ?? preview);
    return json({}, 404);
  });
}

const preview: PreviewResult = {
  month: "2026-11",
  entityId: 1,
  ruleVersion: 2,
  compareVersion: 1,
  fxUsdKrwX1000: 1_000_000,
  totalKrw: 3800,
  directKrw: 3000,
  sharedKrw: 800,
  lines: [
    { departmentId: 900, departmentName: "운영", headcount: 1, calls: 1, directKrw: 0, sharedKrw: 300, totalKrw: 300, deltaKrw: 200 },
    { departmentId: 901, departmentName: "영업", headcount: 2, calls: 2, directKrw: 3000, sharedKrw: 500, totalKrw: 3500, deltaKrw: -200 },
  ],
  pools: [{ seq: 1, name: "부서 미지정", driver: "fixed", amountKrw: 300, shares: [{ departmentId: 900, name: "운영", weight: "100%", shareKrw: 300 }] }],
  adjusted: [{ provider: "anthropic-main", actualKrw: 4180 }],
  blockers: [],
  warnings: ["풀 '평가': 대상 부서의 직접 비용이 모두 0 이라 인원 비율로 나눴습니다."],
};

function renderPage(user: CurrentUser, el: ReactElement) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>{el}</AuthProvider>
    </MemoryRouter>,
  );
}
const finance: CurrentUser = { id: 1, name: "최재무", role: "finance", departmentId: 900 };
const auditor: CurrentUser = { id: 3, name: "이감사", role: "auditor", departmentId: 1 };

afterEach(() => vi.restoreAllMocks());

describe("AI 비용 배부 규칙 화면", () => {
  it("규칙이 없으면 안내와 기본 규칙 만들기 단추를 보이고 누르면 만든다", async () => {
    const spy = route({ versions: [] });
    renderPage(finance, <AiCostRulesPage />);
    expect(await screen.findByText("배부 규칙이 아직 없습니다. 기본 규칙으로 시작할 수 있습니다.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "기본 규칙 만들기" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).endsWith("/rules/default") && (c[1] as RequestInit).method === "POST")).toBe(true));
    expect(await screen.findByText("기본 규칙을 만들었습니다.")).toBeInTheDocument();
  });

  it("버전을 바탕으로 편집해 새 버전으로 저장하고 경고를 보인다", async () => {
    const spy = route();
    renderPage(finance, <AiCostRulesPage />);
    const table = await screen.findByRole("table", { name: "규칙 버전" });
    expect(within(table).getByText("기본 규칙")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "버전 1 을 바탕으로 편집" }));
    expect(await screen.findByLabelText("풀 평가·점검")).toBeTruthy();
    const form = screen.getByRole("form", { name: "규칙 편집" });
    const poolBox = within(form).getByLabelText("풀 평가·점검");
    fireEvent.change(within(poolBox).getByLabelText("나누는 기준"), { target: { value: "headcount" } });
    fireEvent.click(within(form).getByRole("button", { name: "풀 더하기" }));
    fireEvent.click(within(form).getByRole("button", { name: "새 버전으로 저장" }));
    await waitFor(() => {
      const call = spy.mock.calls.find((c) => String(c[0]).endsWith("/api/admin/ai-cost/rules") && (c[1] as RequestInit)?.method === "POST");
      expect(call).toBeTruthy();
      const body = JSON.parse(String((call![1] as RequestInit).body)) as RuleSet;
      expect(body.pools).toHaveLength(3);
      expect(body.pools[0].driver).toBe("headcount");
      expect(body.pools[0].selector.purposes).toEqual(["eval.run"]);
    });
    expect(await screen.findByText(/버전 2 으로 저장했습니다/)).toBeInTheDocument();
    expect(screen.getByText(/사용 조건이 겹칩니다/)).toBeInTheDocument();
  });

  it("서버가 거절하면 오류 문장을 그대로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.includes("departments")) return json({ departments: depts });
      if (url.includes("/settings")) return json({ expenseAccount: "5290", options: [{ code: "5290", name: "기타비용" }] });
      if (url.endsWith("/rules") && ((init as RequestInit | undefined)?.method ?? "GET") === "GET") return json({ versions });
      if (url.includes("/rules/1")) return json(v1);
      return json({ error: "대상 비율의 합이 100 이 아닙니다." }, 400);
    });
    renderPage(finance, <AiCostRulesPage />);
    const table = await screen.findByRole("table", { name: "규칙 버전" });
    fireEvent.click(within(table).getByRole("button", { name: "버전 1 을 바탕으로 편집" }));
    const form = await screen.findByRole("form", { name: "규칙 편집" });
    fireEvent.click(within(form).getByRole("button", { name: "새 버전으로 저장" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("대상 비율의 합이 100 이 아닙니다.");
  });

  it("감사는 버전 내용을 보기만 하고 저장·기본 규칙 단추가 없다", async () => {
    route();
    renderPage(auditor, <AiCostRulesPage />);
    const table = await screen.findByRole("table", { name: "규칙 버전" });
    expect(screen.queryByRole("button", { name: "새 버전으로 저장" })).toBeNull();
    fireEvent.click(within(table).getByRole("button", { name: "버전 1 내용 보기" }));
    expect(await screen.findByText(/평가·점검: 용도: 평가 세트 실행 → 사용량 비율/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "풀 더하기" })).toBeNull();
  });
});

describe("AI 비용 배부 미리보기 화면", () => {
  it("부서별 부담액·비교 증감·풀별 나눔·경고를 한국어로 보인다", async () => {
    const spy = route();
    renderPage(finance, <AiCostPreviewPage />);
    const t = await screen.findByRole("table", { name: "부서별 배부 결과" });
    expect(within(t).getByText("3,500원")).toBeInTheDocument();
    expect(within(t).getByText("+200원")).toBeInTheDocument();
    expect(within(t).getByText("-200원")).toBeInTheDocument();
    expect(screen.getByText(/대상 부서의 직접 비용이 모두 0/)).toBeInTheDocument();
    const pools = screen.getByRole("table", { name: "풀별 나눔" });
    expect(within(pools).getByText(/고정 비율/)).toBeInTheDocument();
    expect(within(pools).getByText(/운영 300원\(100%\)/)).toBeInTheDocument();
    expect(screen.getByText(/실제 청구액으로 보정한 공급자: anthropic-main/)).toBeInTheDocument();
    expect(screen.queryByText("fixed")).toBeNull();
    expect(spy.mock.calls.some((c) => String(c[0]).includes("/api/admin/ai-cost/preview?month="))).toBe(true);
  });

  it("버전·비교 버전을 고르면 그 값으로 다시 요청한다", async () => {
    const spy = route();
    renderPage(finance, <AiCostPreviewPage />);
    await screen.findByRole("table", { name: "부서별 배부 결과" });
    const form = screen.getByRole("form", { name: "미리보기 조건" });
    fireEvent.change(within(form).getByLabelText("규칙 버전"), { target: { value: "1" } });
    fireEvent.change(within(form).getByLabelText("비교할 버전"), { target: { value: "1" } });
    await waitFor(() => expect(spy.mock.calls.some((c) => /version=1&compare=1/.test(String(c[0])))).toBe(true));
  });

  it("확정을 막는 문제는 오류로 보이고 표는 보이지 않는다", async () => {
    route({ preview: { ...preview, lines: [], pools: [], blockers: ["이 달 환율이 필요합니다. 설정에서 입력한 뒤 확정하세요."], warnings: [], compareVersion: 0 } });
    renderPage(finance, <AiCostPreviewPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("이 달 환율이 필요합니다");
    expect(screen.queryByRole("table", { name: "부서별 배부 결과" })).toBeNull();
  });
});

describe("서식·메뉴", () => {
  it("풀 조건 설명과 라우트 등록", () => {
    expect(describeSelector({ purposes: ["eval.run"], unassignedDept: true })).toBe("용도: 평가 세트 실행 / 부서 미지정 호출");
    expect(describeSelector({})).toBe("조건 없음");
    expect(llmModule.routes.some((r) => r.path === "/admin/ai-cost/rules")).toBe(true);
    expect(llmModule.routes.some((r) => r.path === "/admin/ai-cost/preview")).toBe(true);
  });
});

describe("법인 간 공용 풀과 비용 계정 설정", () => {
  it("법인이 둘 이상이면 범위를 고를 수 있고, 법인 간 고정 비율은 법인을 대상으로 저장한다", async () => {
    const spy = route({ entities: [{ id: 1, name: "본사" }, { id: 2, name: "계열사" }] });
    renderPage(finance, <AiCostRulesPage />);
    const table = await screen.findByRole("table", { name: "규칙 버전" });
    fireEvent.click(within(table).getByRole("button", { name: "버전 1 을 바탕으로 편집" }));
    const form = await screen.findByRole("form", { name: "규칙 편집" });
    const pool = within(form).getByLabelText("풀 평가·점검");
    fireEvent.change(within(pool).getByLabelText("나누는 범위"), { target: { value: "group" } });
    expect(within(pool).getByText(/모든 법인에 직접 비용 비율로 나눈 뒤/)).toBeInTheDocument();
    fireEvent.change(within(pool).getByLabelText("나누는 기준"), { target: { value: "fixed" } });
    const targets = within(pool).getByRole("group", { name: "대상 법인" });
    fireEvent.click(within(targets).getByLabelText("본사"));
    fireEvent.click(within(targets).getByLabelText("계열사"));
    const pcts = within(targets).getAllByLabelText(/비율\(%\)/);
    fireEvent.change(pcts[0], { target: { value: "70" } });
    fireEvent.change(pcts[1], { target: { value: "30" } });
    fireEvent.click(within(form).getByRole("button", { name: "새 버전으로 저장" }));
    await waitFor(() => {
      const call = spy.mock.calls.find((c) => String(c[0]).endsWith("/api/admin/ai-cost/rules") && (c[1] as RequestInit)?.method === "POST");
      expect(call).toBeTruthy();
      const body = JSON.parse(String((call![1] as RequestInit).body)) as RuleSet;
      expect(body.pools[0]).toMatchObject({ scope: "group", driver: "fixed", targetsAll: false, targets: [{ departmentId: 1, weightPct: 70 }, { departmentId: 2, weightPct: 30 }] });
    });
  });

  it("법인이 하나뿐이면 범위 선택이 없다", async () => {
    route();
    renderPage(finance, <AiCostRulesPage />);
    const table = await screen.findByRole("table", { name: "규칙 버전" });
    fireEvent.click(within(table).getByRole("button", { name: "버전 1 을 바탕으로 편집" }));
    const pool = await screen.findByLabelText("풀 평가·점검");
    expect(within(pool).queryByLabelText("나누는 범위")).toBeNull();
  });

  it("재무는 비용 계정을 바꾸고, 감사는 보기만 한다", async () => {
    const spy = route();
    const { unmount } = renderPage(finance, <AiCostRulesPage />);
    const form = await screen.findByRole("form", { name: "비용 계정 설정" });
    fireEvent.change(within(form).getByLabelText(/차변 계정/), { target: { value: "5260" } });
    fireEvent.click(within(form).getByRole("button", { name: "계정 저장" }));
    await waitFor(() => expect(spy.mock.calls.some((c) => String(c[0]).endsWith("/api/admin/ai-cost/settings") && (c[1] as RequestInit)?.method === "PUT")).toBe(true));
    expect(await screen.findByText(/다음 확정부터 적용됩니다/)).toBeInTheDocument();
    unmount();
    route();
    renderPage(auditor, <AiCostRulesPage />);
    expect(await screen.findByText(/차변 계정: 5290 기타비용/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "비용 계정 설정" })).toBeNull();
  });

  it("미리보기에 법인 간 공용 몫과 거래 표시를 보인다", async () => {
    route({ preview: { ...preview, compareVersion: 0, groupShareKrw: 250, counterpartyEntityId: 1, pools: [{ seq: 1, name: "평가(법인 간)", driver: "headcount", group: true, amountKrw: 250, shares: [] }] } });
    renderPage(finance, <AiCostPreviewPage />);
    expect(await screen.findByText(/법인 간 공용 풀에서 이 법인이 받은 몫: 250원/)).toBeInTheDocument();
    expect(screen.getByText(/기본 법인이 대신 지급한 공용 비용이 들어 있어 법인 간 거래로 표시됩니다/)).toBeInTheDocument();
    expect(screen.getByText(/\(법인 간 공용 몫\)/)).toBeInTheDocument();
  });
});

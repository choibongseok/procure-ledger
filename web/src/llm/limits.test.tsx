import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { LLMUsagePage, isNearLimit, isOverLimit, modelTotals, usageRate, type UsageRow } from "./LLMUsagePage";

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

function renderPage(user: CurrentUser) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>
        <LLMUsagePage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

const row = (o: Partial<UsageRow>): UsageRow => ({
  day: "2026-11-11", role: "finance", model: "claude-sonnet-4-5", purpose: "docverify.extract",
  calls: 1, blocked: 0, inputTokens: 100, outputTokens: 10, costMicroUsd: 1000, maskedCount: 0, ...o,
});

function mock(rows: UsageRow[], roles: unknown, usageStatus = 200) {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    if (url.includes("/usage/limits")) return json({ roles });
    if (url.includes("/usage?")) return usageStatus === 200 ? json({ rows }) : json({ error: "x" }, usageStatus);
    if (url.includes("/policies")) return json({ policies: [] });
    if (url.includes("/quotas")) return json({ quotas: [] });
    return json({});
  });
}

afterEach(() => vi.restoreAllMocks());

describe("AI 사용량: 오늘 한도와 모델별 표", () => {
  it("관리자는 역할별 행과 사용률을 보고, 한도의 80% 를 넘은 역할은 주의 문구를 받는다", async () => {
    mock([row({})], [
      { role: "finance", usedTokens: 168400, dailyTokenLimit: 200000 },
      { role: "staff", usedTokens: 14200, dailyTokenLimit: 50000 },
      { role: "approver", usedTokens: 0, dailyTokenLimit: null },
    ]);
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    const t = await screen.findByRole("table", { name: "오늘 사용량과 일일 한도" });
    const rows = within(t).getAllByRole("row");
    expect(rows).toHaveLength(4);
    expect(rows[1]).toHaveTextContent("재무168,400200,00084%");
    expect(rows[2]).toHaveTextContent("직원14,20050,00028%");
    expect(rows[3]).toHaveTextContent("한도 없음");
    const notes = screen.getAllByTestId("near-limit");
    expect(notes).toHaveLength(1);
    expect(notes[0]).toHaveTextContent(
      "오늘 한도의 80% 를 넘었습니다(재무 168,400 / 200,000). 한도에 닿으면 증빙 판정과 도구 선택이 멈춥니다.",
    );
    expect(within(t).queryByText("한도 초과")).toBeNull();
  });

  it("재무는 본인 역할 한 줄만 받고, 100% 이상이면 한도 초과 배지가 붙는다", async () => {
    mock([row({})], [{ role: "finance", usedTokens: 200000, dailyTokenLimit: 200000 }]);
    renderPage({ id: 2, name: "박재무", role: "finance", departmentId: 1 });
    const t = await screen.findByRole("table", { name: "오늘 사용량과 일일 한도" });
    expect(within(t).getAllByRole("row")).toHaveLength(2);
    expect(within(t).getByText("한도 초과")).toBeInTheDocument();
    expect(t).toHaveTextContent("100%");
  });

  it("모델별 사용량은 일별 표의 같은 모델 행을 더한다", async () => {
    mock(
      [
        row({ calls: 14, inputTokens: 31200, outputTokens: 4100, costMicroUsd: 155100 }),
        row({ day: "2026-11-10", calls: 26, blocked: 1, inputTokens: 56200, outputTokens: 7700, costMicroUsd: 284100 }),
        row({ model: "claude-haiku-4-5", calls: 45, inputTokens: 19800, outputTokens: 2300, costMicroUsd: 31300 }),
      ],
      [],
    );
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 });
    const t = await screen.findByRole("table", { name: "모델별 사용량" });
    expect(within(t).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["모델", "호출", "차단", "입력 토큰", "출력 토큰", "비용"]);
    const rows = within(t).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("claude-sonnet-4-5401" + "87,40011,800$0.4392");
    expect(rows[2]).toHaveTextContent("claude-haiku-4-5450" + "19,8002,300$0.0313");
  });

  it("사용 기록이 없으면 안내 문구, 불러오기 실패하면 오류 문구", async () => {
    mock([], []);
    const first = renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 });
    expect(await screen.findByText("최근 7일 동안 AI 사용 기록이 없습니다.")).toBeInTheDocument();
    first.unmount();
    vi.restoreAllMocks();
    mock([], [], 500);
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 });
    await waitFor(() => expect(screen.getByText("AI 사용량을 불러오지 못했습니다. 잠시 뒤 다시 시도하세요.")).toBeInTheDocument());
  });

  it("집계 도우미: 합·사용률·80%·100% 경계", () => {
    expect(modelTotals([row({ calls: 2 }), row({ calls: 3 })])[0].calls).toBe(5);
    expect(usageRate(35300, 200000)).toBe(18);
    expect(usageRate(5, null)).toBeNull();
    expect(usageRate(5, 0)).toBeNull();
    const r = (used: number) => ({ role: "staff", usedTokens: used, dailyTokenLimit: 100 });
    expect(isNearLimit(r(79))).toBe(false);
    expect(isNearLimit(r(80))).toBe(true);
    expect(isOverLimit(r(99))).toBe(false);
    expect(isOverLimit(r(100))).toBe(true);
  });
});

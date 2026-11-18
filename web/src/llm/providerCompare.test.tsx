import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider } from "../auth/AuthProvider";
import { ProviderCompare } from "./EvalHistoryPage";

function json(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
}

afterEach(() => vi.restoreAllMocks());

const run = (id: number, provider: string, model: string, reference: boolean, extra = {}) => ({ id, provider, model, passRatePct: 100, criticalFailures: 0, regressions: 0, criticalRegressions: 0, latencyMs: 10, costMicroUsd: 0, reference, ...extra });

describe("공급자 비교 표", () => {
  it("케이스 × 공급자 표에 기준 대비 회귀와 치명 경고를 보인다(영문 코드 없음)", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      json({
        suiteHash: "h1",
        runs: [run(1, "mock", "mock-1", true), run(2, "bedrock", "m1", false, { passRatePct: 50, criticalFailures: 1, regressions: 1, criticalRegressions: 1 })],
        cases: [
          { key: "docs/a", critical: true, results: ["pass", "fail"] },
          { key: "tools/b", critical: false, results: ["pass", "pass"] },
        ],
        excluded: 1,
        warning: "기준 공급자에서 통과하던 치명 케이스가 다른 공급자·모델에서 실패했습니다.",
      }),
    );
    const { container } = render(
      <AuthProvider initialUser={{ id: 3, name: "이감사", role: "auditor", departmentId: 1 }}>
        <ProviderCompare />
      </AuthProvider>,
    );
    const table = await screen.findByRole("table", { name: "공급자 비교 표" });
    expect(within(table).getByText("실패(회귀)")).toBeTruthy();
    expect(within(table).getByText(/AWS Bedrock \(m1\)/)).toBeTruthy();
    expect(within(table).getByText(/기준/)).toBeTruthy();
    expect(screen.getByText(/치명 케이스가 다른 공급자/)).toBeTruthy();
    expect(screen.getByText(/골든셋이 달라 표에서 뺀 실행이 1개/)).toBeTruthy();
    expect(container.textContent).not.toMatch(/suiteHash|critical|bedrock\b/);
  });

  it("실행이 둘 미만이면 안내만 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ suiteHash: "h", runs: [run(1, "mock", "mock-1", true)], cases: [], excluded: 0, warning: "" }));
    render(
      <AuthProvider initialUser={{ id: 3, name: "이감사", role: "auditor", departmentId: 1 }}>
        <ProviderCompare />
      </AuthProvider>,
    );
    await waitFor(() => expect(screen.getByText(/둘 이상 있으면 비교 표가 나옵니다/)).toBeTruthy());
  });
});

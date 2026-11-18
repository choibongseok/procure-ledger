import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { AuthProvider } from "../auth/AuthProvider";
import { EvalHistoryPage, caseLabel, formatRunAt, runStatus, type EvalRun } from "./EvalHistoryPage";

function json(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
}

const base: EvalRun = {
  id: 2,
  at: "2026-11-11T01:02:03Z",
  provider: "mock",
  model: "mock-1",
  total: 55,
  passed: 54,
  passRatePct: 98,
  thresholdPct: 90,
  gatePassed: false,
  newFailures: ["docs/match-basic"],
  newPasses: [],
  cases: [{ suite: "docs", id: "match-basic", pass: false, critical: false, detail: "판정 hold (기대 match)" }],
};
const okRun: EvalRun = { ...base, id: 1, passRatePct: 100, passed: 55, gatePassed: true, newFailures: [], newPasses: ["tools/pending-list"], cases: [] };

afterEach(() => vi.restoreAllMocks());

describe("평가 이력 화면", () => {
  it("실행 이력을 한국어 표로 보여 주고 회귀를 표시한다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ runs: [base, okRun] }));
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 3, name: "이감사", role: "auditor", departmentId: 1 }}>
          <EvalHistoryPage />
        </AuthProvider>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole("table", { name: "평가 실행 이력" })).toBeInTheDocument());
    expect(screen.getByText("회귀 감지")).toBeInTheDocument();
    expect(screen.getByText("통과")).toBeInTheDocument();
    expect(screen.getByText("문서 판정 · match-basic")).toBeInTheDocument();
    expect(screen.getByText("도구 선택 · pending-list")).toBeInTheDocument();
    expect(screen.getAllByText("시험용 모의 응답")).toHaveLength(2);
    expect(screen.getAllByText(formatRunAt("2026-11-11T01:02:03Z")).length).toBeGreaterThan(0);
    expect(screen.queryByText(/T01:02:03/)).toBeNull();
    expect(formatRunAt("읽을 수 없는 값")).toBe("-");
    expect(screen.queryByText("docs/match-basic")).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: /케이스 보기/ })[0]);
    expect(screen.getByRole("table", { name: "케이스별 결과" })).toHaveTextContent("판정 hold (기대 match)");
  });

  it("이력이 없으면 안내를 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ runs: [] }));
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 4, name: "최관리", role: "admin", departmentId: 1 }}>
          <EvalHistoryPage />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText("저장된 평가 실행이 없습니다.")).toBeInTheDocument();
  });

  it("판정 문구와 케이스 이름 변환", () => {
    expect(runStatus(base)).toBe("회귀 감지");
    expect(runStatus({ ...base, newFailures: [] })).toBe("기준 미달");
    expect(runStatus(okRun)).toBe("통과");
    expect(caseLabel("tools/x")).toBe("도구 선택 · x");
  });

  it("평가 이력 메뉴는 관리자·감사에게만 보인다", () => {
    for (const [role, visible] of [["staff", false], ["finance", false], ["admin", true], ["auditor", true]] as const) {
      vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ runs: [] }));
      const { unmount } = render(
        <MemoryRouter initialEntries={["/"]}>
          <AuthProvider initialUser={{ id: 1, name: "x", role, departmentId: 1 }}>
            <App />
          </AuthProvider>
        </MemoryRouter>,
      );
      // 3층 내비: GNB 모듈 링크 "문서·AI" 를 누르면 SNB("문서·AI 메뉴")에 그 모듈 항목이 나온다.
      fireEvent.click(within(screen.getByRole("navigation", { name: "모듈" })).getByRole("link", { name: "문서·AI" }));
      const snb = screen.getByRole("navigation", { name: "문서·AI 메뉴" });
      expect(snb.textContent?.includes("평가 이력")).toBe(visible);
      unmount();
    }
  });
});

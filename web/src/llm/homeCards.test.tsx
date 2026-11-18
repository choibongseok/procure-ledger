import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import type { Role } from "../auth/permissions";
import { docverifyHomeCards } from "../docverify/homeCards";
import { HomePage } from "../layout/HomePage";
import { llmHomeCards } from "./homeCards";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const cards = [...docverifyHomeCards, ...llmHomeCards];

function renderHome(role: Role) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "홍길동", role, departmentId: 1 }}>
        <HomePage cards={cards} />
      </AuthProvider>
    </MemoryRouter>,
  );
}

const pending = {
  total: 3,
  items: [{ id: 9, poId: 4, outcome: "mismatch", supplier: "한빛상사", totalAmount: 7000, createdAt: "2026-11-11T00:00:00Z" }],
};
const today = {
  day: "2026-11-11",
  mine: { calls: 2, blocked: 0, tokens: 1200 },
  all: { calls: 5, blocked: 1, tokens: 9000 },
  limit: { scope: "role", dailyTokenLimit: 20000, usedTokens: 5000, nearLimit: false },
};

beforeEach(() => {
  get.mockReset();
  get.mockImplementation((path: string) => Promise.resolve(path.startsWith("/api/documents/pending-confirm") ? pending : today));
});

describe("AI 홈 카드", () => {
  it("재무는 확정 대기 건수와 상위 건, 오늘 사용량(한도 대비)을 본다", async () => {
    renderHome("finance");
    expect(await screen.findByText("한빛상사", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(screen.getByText(/AI 판정 불일치/)).toBeInTheDocument();
    expect(await screen.findByText(/일일 한도 20,000 토큰 중 5,000 사용 \(25%\)/)).toBeInTheDocument();
    expect(screen.getByText(/전체 호출/)).toBeInTheDocument();
  });

  it("직원은 확정 대기 카드가 없고 그 API 도 호출하지 않으며, 본인 사용량만 본다", async () => {
    get.mockImplementation(() => Promise.resolve({ day: "2026-11-11", mine: { calls: 1, blocked: 0, tokens: 300 } }));
    renderHome("staff");
    await waitFor(() => expect(screen.getByText(/내 호출/)).toBeInTheDocument());
    expect(screen.queryByLabelText("판정 확정 대기")).toBeNull();
    expect(screen.queryByText(/전체 호출/)).toBeNull();
    expect(get.mock.calls.map((c) => c[0])).toEqual(["/api/llm/usage/today"]);
  });

  it("범위 합계 없이 한도 임박만 오면 사용량 숫자 없이 경고만 보인다", async () => {
    get.mockImplementation(() =>
      Promise.resolve({ day: "2026-11-11", mine: { calls: 1, blocked: 0, tokens: 300 }, limit: { scope: "role", dailyTokenLimit: 20000, nearLimit: true } }),
    );
    renderHome("staff");
    expect(await screen.findByText(/한도에 가까워졌습니다/)).toBeInTheDocument();
    expect(screen.getByText(/일일 한도 20,000 토큰/)).toBeInTheDocument();
    expect(screen.queryByText(/사용 \(/)).toBeNull();
  });

  it("권한이 없는 역할(결재자)에게도 확정 대기 API 는 호출되지 않는다", async () => {
    renderHome("manager");
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(get.mock.calls.some((c) => String(c[0]).includes("pending-confirm"))).toBe(false);
  });

  it("확정 대기가 없으면 안내 문구를 보인다", async () => {
    get.mockImplementation((path: string) => Promise.resolve(path.includes("pending-confirm") ? { total: 0, items: [] } : today));
    renderHome("finance");
    expect(await screen.findByText("확정을 기다리는 판정이 없습니다.")).toBeInTheDocument();
  });

  it("불러오기에 실패하면 일반 문구만 보인다", async () => {
    get.mockRejectedValue(new Error("boom"));
    renderHome("finance");
    expect(await screen.findByText("확정 대기 현황을 불러오지 못했습니다.")).toBeInTheDocument();
    expect(screen.getByText("사용량을 불러오지 못했습니다.")).toBeInTheDocument();
  });
});

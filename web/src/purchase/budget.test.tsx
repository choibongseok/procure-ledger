import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { BudgetCheckCard, BudgetOverBadge, monthText } from "./BudgetCheckCard";
import { BudgetPage } from "./BudgetPage";
import type { BudgetCheck } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const base: BudgetCheck = {
  month: "2026-11", departmentId: 1, departmentName: "구매팀", registered: true,
  budget: 1_000_000, used: 300_000, inProgress: 200_000, balance: 500_000, amount: 400_000, over: 0, after: 100_000, state: "within",
};

function showCheck(c: BudgetCheck) {
  get.mockResolvedValue(c);
  render(<BudgetCheckCard amount={c.amount || 1} />);
}

describe("예산 확인 카드(B25)", () => {
  beforeEach(() => get.mockReset());

  it("월 표기", () => {
    expect(monthText("2026-11")).toBe("2026년 11월");
    expect(monthText(undefined)).toBe("");
  });

  it("예산 이내면 잔액과 요청 뒤 잔액을 보이고 초과 안내는 없다", async () => {
    showCheck(base);
    expect(await screen.findByText("예산 이내")).toBeInTheDocument();
    expect(screen.getByText("500,000원")).toBeInTheDocument();
    expect(screen.queryByText(/넘습니다/)).toBeNull();
  });

  it("초과면 초과 금액과 재무 단계 추가 안내를 보인다", async () => {
    showCheck({ ...base, amount: 700_000, over: 200_000, after: -200_000, state: "over" });
    expect(await screen.findByText(/예산을 200,000원 넘습니다/)).toBeInTheDocument();
    expect(screen.getByText(/재무 단계가 더해집니다/)).toBeInTheDocument();
  });

  it("예산이 등록되지 않았으면 확인하지 않는다는 문구만 보인다", async () => {
    showCheck({ ...base, registered: false, budget: 0, state: "unregistered" });
    expect(await screen.findByText(/예산이 등록되지 않아 예산 확인을 하지 않습니다/)).toBeInTheDocument();
    expect(screen.queryByText("월 예산")).toBeNull();
  });

  it("금액이 없으면 조회하지 않는다", () => {
    render(<BudgetCheckCard amount={0} />);
    expect(screen.getByText("금액을 입력하면 부서 예산을 확인합니다.")).toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });

  it("예산 초과 배지는 초과로 판정된 건에만 붙는다", () => {
    const { rerender } = render(<BudgetOverBadge budget={{ state: "over", month: "2026-11", budget: 1, used: 0, inProgress: 0, amount: 2, over: 1 }} />);
    expect(screen.getByText("예산 초과 요청")).toBeInTheDocument();
    rerender(<BudgetOverBadge budget={{ state: "within", month: "2026-11", budget: 1, used: 0, inProgress: 0, amount: 1, over: 0 }} />);
    expect(screen.queryByText("예산 초과 요청")).toBeNull();
  });
});

describe("예산 현황 화면(B25)", () => {
  const user = (role: "finance" | "auditor"): CurrentUser => ({ id: 4, name: "재무", role, departmentId: 3 });
  const rows = [
    { departmentId: 1, departmentName: "구매팀", month: "2026-11", amount: 1_000_000, used: 500_000, inProgress: 400_000, balance: 100_000, usagePct: 90, badge: "near" },
    { departmentId: 2, departmentName: "영업팀", month: "2026-11", amount: 0, used: 0, inProgress: 0, balance: 0, usagePct: -1, badge: "ok" },
  ];
  function setup(role: "finance" | "auditor", data = rows) {
    get.mockReset();
    get.mockImplementation((u?: string) => {
      const url = String(u ?? "");
      if (url.startsWith("/api/budgets/revisions")) return Promise.resolve([{ departmentName: "구매팀", month: "2026-11", newAmount: 1_000_000, reason: "계획", actorName: "재무 담당", at: "2026-11-01T00:00:00Z" }]);
      if (url.startsWith("/api/budgets")) return Promise.resolve(data);
      return Promise.resolve([{ id: 1, name: "구매팀" }]);
    });
    return render(
      <MemoryRouter>
        <AuthProvider initialUser={user(role)}>
          <BudgetPage />
        </AuthProvider>
      </MemoryRouter>,
    );
  }

  it("표에 사용률과 배지가 보이고 월 예산 0원의 사용률은 줄표다", async () => {
    setup("finance");
    expect(await screen.findByText("한도 근접")).toBeInTheDocument();
    expect(screen.getByText("90%")).toBeInTheDocument();
    expect(screen.getByText("여유")).toBeInTheDocument();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
    expect(screen.getByText("신규 등록")).toBeInTheDocument();
  });

  it("재무에게는 수정 폼이 보이고 감사에게는 보이지 않는다", async () => {
    const { unmount } = setup("finance");
    expect(await screen.findByRole("region", { name: "예산 등록·수정" })).toBeInTheDocument();
    unmount();
    setup("auditor");
    await waitFor(() => expect(screen.getByText("한도 근접")).toBeInTheDocument());
    expect(screen.queryByRole("region", { name: "예산 등록·수정" })).toBeNull();
  });

  it("등록된 예산이 없으면 빈 상태를 보인다", async () => {
    setup("auditor", []);
    expect(await screen.findByText(/에 등록된 예산이 없습니다/)).toBeInTheDocument();
  });
});

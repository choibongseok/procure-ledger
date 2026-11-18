import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ROLES, type Role } from "../auth/permissions";
import { HomePage } from "../layout/HomePage";
import type { HomeCard } from "../layout/routeModule";
import { approvalHomeCards } from "../approval/homeCards";
import { inventoryHomeCards } from "../inventory/homeCards";
import { purchaseHomeCards } from "./homeCards";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

function show(card: HomeCard) {
  const C = card.Component;
  return render(
    <MemoryRouter>
      <C />
    </MemoryRouter>,
  );
}

beforeEach(() => get.mockReset());

describe("홈 카드 선언", () => {
  it("각 카드는 해당 업무 권한을 선언한다", () => {
    expect(approvalHomeCards.map((c) => [c.id, c.permission])).toEqual([["approval-pending", "approval:decide"]]);
    expect(purchaseHomeCards.map((c) => [c.id, c.permission])).toEqual([["my-requests", "purchase:create"]]);
    expect(inventoryHomeCards.map((c) => [c.id, c.permission])).toEqual([["stock-alerts", "inventory:read"]]);
  });

  it("카드는 마운트되기 전에는 API 를 부르지 않는다", () => {
    expect(get).not.toHaveBeenCalled();
  });
});

describe("결재 대기 카드", () => {
  it("건수와 상위 3건을 보이고 나머지는 결재함으로 안내한다", async () => {
    const mk = (id: number) => ({ id, no: `PR-${id}`, title: `요청 ${id}`, requesterName: "김직원", totalAmount: 100000, steps: [] });
    get.mockResolvedValue([mk(1), mk(2), mk(3), mk(4), mk(5)]);
    show(approvalHomeCards[0]);
    await waitFor(() => expect(screen.getByText("5건")).toBeTruthy());
    expect(get).toHaveBeenCalledWith("/api/approvals/pending");
    expect(screen.getByText("요청 3")).toBeTruthy();
    expect(screen.queryByText("요청 4")).toBeNull();
    expect(screen.getByText("결재함에서 2건 더 보기")).toBeTruthy();
  });

  it("없으면 안내 문구를 보인다", async () => {
    get.mockResolvedValue([]);
    show(approvalHomeCards[0]);
    await waitFor(() => expect(screen.getByText("결재할 건이 없습니다.")).toBeTruthy());
  });
});

describe("내 요청 진행 카드", () => {
  it("상태를 업무 용어로 보이고 결재자 없음을 알린다", async () => {
    get.mockResolvedValue({ total: 3, byStatus: { draft: 1, in_review: 2 }, noApprover: 1 });
    show(purchaseHomeCards[0]);
    await waitFor(() => expect(screen.getByText("임시저장 1건")).toBeTruthy());
    expect(screen.getByText("결재중 2건")).toBeTruthy();
    expect(screen.queryByText(/in_review|draft/)).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("결재자 없음 1건");
    expect(get).toHaveBeenCalledWith("/api/purchase-requests/my-summary");
  });

  it("결재자 없음이 없으면 경고를 그리지 않는다", async () => {
    get.mockResolvedValue({ total: 1, byStatus: { submitted: 1 }, noApprover: 0 });
    show(purchaseHomeCards[0]);
    await waitFor(() => expect(screen.getByText("상신 1건")).toBeTruthy());
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("재고 경보 카드", () => {
  it("미달 품목 수와 임박 로트 수를 보인다", async () => {
    get.mockImplementation(async (...a: unknown[]) => {
      return String(a[0]).startsWith("/api/stock/below-safety") ? [{}, {}] : [{}];
    });
    show(inventoryHomeCards[0]);
    await waitFor(() => expect(screen.getByText("안전재고 미달 2품목")).toBeTruthy());
    expect(screen.getByText("유효기한 30일 이내 1로트")).toBeTruthy();
  });

  it("경보가 없으면 안내 문구를 보인다", async () => {
    get.mockResolvedValue([]);
    show(inventoryHomeCards[0]);
    await waitFor(() => expect(screen.getByText("재고 경보가 없습니다.")).toBeTruthy());
  });
});

// 그 일을 실제로 하는 권한이 있는 역할에게만 카드가 그려지고, 없는 역할에는 DOM 에도 API 호출에도 없다.
describe("역할별 업무 카드", () => {
  const cards = [...approvalHomeCards, ...purchaseHomeCards, ...inventoryHomeCards];
  const expected: Record<Role, string[]> = {
    admin: ["stock-alerts"],
    auditor: ["stock-alerts"],
    staff: ["my-requests", "stock-alerts"],
    manager: ["approval-pending", "my-requests", "stock-alerts"],
    finance: ["approval-pending", "my-requests", "stock-alerts"],
  };
  const api_: Record<string, string> = {
    "approval-pending": "/api/approvals/pending",
    "my-requests": "/api/purchase-requests/my-summary",
  };

  it.each(ROLES)("%s", (role) => {
    get.mockResolvedValue([]);
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 1, name: "홍길동", role, departmentId: 1 }}>
          <HomePage cards={cards} />
        </AuthProvider>
      </MemoryRouter>,
    );
    const shown = Array.from(document.querySelectorAll("[data-home-card]")).map((e) => e.getAttribute("data-home-card"));
    expect(shown).toEqual(expected[role]);
    expect(shown.length).toBeGreaterThan(0); // 카드가 한 장도 없는 역할은 없다
    const called = get.mock.calls.map((c) => String(c[0]));
    for (const id of Object.keys(api_)) {
      expect(called.includes(api_[id])).toBe(expected[role].includes(id));
    }
  });
});

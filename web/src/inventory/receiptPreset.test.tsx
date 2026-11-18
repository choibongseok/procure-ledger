import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ReceiptPage } from "./ReceiptPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;

const po = (id: number, no: string) => ({
  id, no, prId: id, vendorId: 1, status: "open", totalAmount: 1000, createdAt: "2026-11-04T00:00:00Z",
  lines: [{ id: id * 10, itemId: 1, itemName: "모니터 27형", quantity: 10, receivedQty: 0, unitPrice: 100 }],
});

beforeEach(() => {
  get.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/purchase-orders?status=open,partial")) return [po(37, "PO-000037"), po(38, "PO-000038")];
    if (path.startsWith("/api/items")) return [{ id: 1, sku: "M", name: "모니터 27형", unit: "EA", safetyStock: 0, trackLot: false }];
    if (path.startsWith("/api/warehouses")) return [];
    if (path.startsWith("/api/receipt-history")) return { rows: [], more: false };
    throw new Error(path);
  });
});

describe("입고 대기 발주 쪽 이동", () => {
  it("open,partial 을 한 번에 조회하고 전체가 한 쪽을 넘으면 쪽 이동으로 다음 쪽(offset)을 부른다", async () => {
    const page = vi.fn(async (path: string) => ({ items: [po(37, "PO-000037")], total: path.includes("offset=50") ? 120 : 120 }));
    const holder = api as unknown as { getPage: unknown };
    const orig = holder.getPage;
    holder.getPage = page;
    onTestFinished(() => {
      holder.getPage = orig;
    });
    render(
      <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
        <MemoryRouter initialEntries={["/receipts"]}>
          <ReceiptPage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByText("1–50 / 전체 120건")).toBeInTheDocument();
    expect(String(page.mock.calls[0][0])).toContain("status=open,partial");
    expect(page.mock.calls.filter((c) => String(c[0]).includes("status=open,partial"))).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(await screen.findByText("51–100 / 전체 120건")).toBeInTheDocument();
    expect(String(page.mock.calls.at(-1)?.[0])).toContain("offset=50");
  });
});

describe("입고 화면의 발주 미리 선택", () => {
  it("?po= 가 있으면 그 발주의 입고 처리 칸이 바로 열린다", async () => {
    render(
      <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
        <MemoryRouter initialEntries={["/receipts?po=38"]}>
          <ReceiptPage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByRole("heading", { name: /PO-000038 입고 처리/ })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /PO-000037 입고 처리/ })).toBeNull();
  });

  it("?po= 가 없으면 아무것도 고르지 않는다", async () => {
    render(
      <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
        <MemoryRouter initialEntries={["/receipts"]}>
          <ReceiptPage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByText("PO-000038")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /입고 처리/ })).toBeNull();
  });

  it("입고를 처리하면 입고 이력 표를 다시 불러온다", async () => {
    post.mockResolvedValue({});
    render(
      <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
        <MemoryRouter initialEntries={["/receipts?po=38"]}>
          <ReceiptPage />
        </MemoryRouter>
      </AuthProvider>,
    );
    await screen.findByRole("heading", { name: /PO-000038 입고 처리/ });
    const historyCalls = () => get.mock.calls.filter((c) => String(c[0]).startsWith("/api/receipt-history")).length;
    const before = historyCalls();
    fireEvent.change(screen.getByLabelText("모니터 27형 입고 수량"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "입고 처리" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    await waitFor(() => expect(historyCalls()).toBeGreaterThan(before));
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { ReceiptPage } from "./ReceiptPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;

// 입고가 끝나면 그 발주는 "입고가 남은 발주" 목록에서 사라진다. 이때 성공 문구가 같이 사라지면 사용자는 처리됐는지 알 수 없고
// (화면 스모크가 간헐 실패하던 원인: 문구가 재조회 직후 곧바로 지워져 기다리는 쪽이 놓침), 문구는 목록이 바뀐 뒤에도 남아 있어야 한다.
let received = 0;
const po = () => ({
  id: 5, no: "PO-000005", prId: 5, vendorId: 1, status: received === 0 ? "open" : "partial", totalAmount: 1000, createdAt: "2026-11-04T00:00:00Z",
  lines: [{ id: 50, itemId: 1, itemName: "볼트", quantity: 10, receivedQty: received, unitPrice: 100 }],
});

beforeEach(() => {
  received = 0;
  get.mockReset();
  post.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/purchase-orders?status=open,partial")) return received < 10 ? [po()] : [];
    if (path.startsWith("/api/items")) return [{ id: 1, sku: "B", name: "볼트", unit: "EA", safetyStock: 0, trackLot: false }];
    if (path.startsWith("/api/warehouses")) return [];
    if (path.startsWith("/api/receipt-history")) return { rows: [], more: false };
    throw new Error(path);
  });
  post.mockImplementation(async (_p: string, body: { lines: { quantity: number }[] }) => {
    received += body.lines[0].quantity;
  });
});

function mount() {
  return render(
    <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
      <MemoryRouter initialEntries={["/receipts"]}>
        <ToastProvider>
          <ReceiptPage />
        </ToastProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}

async function receive(qty: string) {
  fireEvent.click(await screen.findByRole("button", { name: "PO-000005" }));
  fireEvent.change(await screen.findByLabelText("볼트 입고 수량"), { target: { value: qty } });
  fireEvent.click(screen.getByRole("button", { name: "입고 처리" }));
}

describe("입고 완료 문구", () => {
  it("부분 입고 뒤에도 성공 문구가 한 번만 보인다", async () => {
    mount();
    await receive("6");
    expect(await screen.findAllByText("입고를 처리했습니다.")).toHaveLength(1);
    await screen.findByRole("row", { name: /PO-000005 .*부분 입고/ });
    expect(screen.getAllByText("입고를 처리했습니다.")).toHaveLength(1);
  });

  it("마지막 잔량을 입고해 발주가 목록에서 사라져도 성공 문구는 남는다", async () => {
    mount();
    await receive("6");
    await screen.findByRole("row", { name: /PO-000005 .*부분 입고/ });
    await receive("4");
    // 재조회가 끝나 발주가 목록에서 사라질 때까지 기다린 뒤에도 문구가 있어야 한다.
    await waitFor(() => expect(screen.getByText("입고할 발주가 없습니다.")).toBeInTheDocument());
    expect(screen.getByRole("status")).toHaveTextContent("입고를 처리했습니다.");
    expect(screen.queryByRole("heading", { name: /입고 처리/ })).toBeNull(); // 입력 칸은 닫힌다
  });

  // 성공 문구는 토스트라 4초 뒤 스스로 닫힌다(자동 닫힘은 layout/uiParts.test.tsx). 다른 발주를 고르는 순간 지우는 동작은 없다.
});

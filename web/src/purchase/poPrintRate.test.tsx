import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { POPrintPage } from "./POPrintPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const po = {
  prId: 7, prNo: "PR-000007", poNo: "PO-000003", poDate: "2026-11-20T01:00:00Z", company: "에이엑스 주식회사", department: "구매팀", requester: "직원",
  supplierName: "한빛상사", businessNo: "123-45-67890", phone: "02-1234-4567", email: "s@h.example", contactEmpty: false, masked: false,
  deliveryPlace: "본사 창고", dueDate: "", lines: [{ no: 1, itemName: "볼트", unit: "EA", quantity: 1, unitPrice: 12345, amount: 12345 }],
  supply: 12345, vat: 2469, total: 14814, note: "납품 시 거래명세서를 지참해 주세요.",
};

function show(d: unknown) {
  get.mockReset();
  get.mockImplementation((u?: string) => Promise.resolve(String(u).endsWith("/po") ? d : { id: 7, no: "PR-000007", requesterName: "직원", status: "ordered", steps: [], createdAt: "2026-11-19T01:00:00Z", lines: [], title: "t", totalAmount: 12345 }));
  return render(
    <MemoryRouter initialEntries={["/purchase-requests/7/po"]}>
      <Routes>
        <Route path="/purchase-requests/:id/po" element={<POPrintPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("발주서 부가세 라벨(B34)", () => {
  it("라벨이 서버가 알려 준 세율을 따른다(10% 고정 아님), 값이 없으면 10%", async () => {
    const first = show({ ...po, vatRate: 20 });
    expect(await screen.findByText("부가세(20%)")).toBeInTheDocument();
    expect(screen.queryByText("부가세(10%)")).toBeNull();
    first.unmount();
    show(po);
    expect(await screen.findByText("부가세(10%)")).toBeInTheDocument();
  });
});

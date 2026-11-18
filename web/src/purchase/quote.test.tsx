import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { QuoteCard } from "./QuoteCard";
import { QuotePage } from "./QuotePage";
import type { Quote, QuoteCompare } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const put = (api as unknown as { put: ReturnType<typeof vi.fn> }).put;

const quote = (o: Partial<Quote>): Quote => ({
  id: 1, prId: 7, vendorId: 1, vendorName: "한빛상사", supplier: "한빛상사", filename: "한빛.txt", status: "read", edited: false,
  lines: [{ name: "볼트", quantity: 10, unitPrice: 1000, amount: 10000 }], total: 10000, docTotal: null, leadDays: 7, leadText: "", warnings: [],
  filled: true, selected: false, lowest: true, overLowest: 0, createdAt: "", ...o,
});

const compare = (o: Partial<QuoteCompare>): QuoteCompare => ({
  quotes: [], editable: true, selectedId: 0, reason: "", reasonRequired: false, lowestTotal: 10000, prTotal: 10000, totalMismatch: false, note: "", ...o,
});

function show(c: QuoteCompare) {
  get.mockReset();
  get.mockImplementation((u?: string) => Promise.resolve(String(u).startsWith("/api/vendors") ? [{ id: 1, name: "한빛상사" }] : c));
  return render(
    <MemoryRouter initialEntries={["/purchase-requests/7/quotes"]}>
      <Routes>
        <Route path="/purchase-requests/:id/quotes" element={<QuotePage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("견적 비교 화면(B25 ②)", () => {
  beforeEach(() => put.mockReset());

  it("견적 카드의 링크는 상세 응답의 editable 이 우선한다", async () => {
    get.mockReset();
    get.mockResolvedValue(compare({ editable: true }));
    render(
      <MemoryRouter>
        <QuoteCard prId={7} editable={false} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("link", { name: "견적 비교 보기" })).toBeInTheDocument();
  });

  it("견적서가 없으면 안내만, 한 건이면 비교할 수 없다는 안내를 보인다", async () => {
    const { unmount } = show(compare({}));
    expect(await screen.findByText("첨부된 견적서가 없습니다.")).toBeInTheDocument();
    unmount();
    show(compare({ quotes: [quote({})] }));
    expect(await screen.findByText(/견적서가 한 건뿐이라 비교할 거래처가 없습니다/)).toBeInTheDocument();
    expect(screen.queryByText("최저가")).toBeNull();
  });

  it("2건 이상이면 최저가 배지와 +차액을 보이고 최저가가 아닌 선택은 사유를 요구한다", async () => {
    show(
      compare({
        quotes: [
          quote({ id: 1, vendorName: "한빛상사", total: 15000, lowest: false, overLowest: 5000, selected: true }),
          quote({ id: 2, vendorId: 2, vendorName: "대한오피스", supplier: "대한오피스", total: 10000, lowest: true }),
        ],
        selectedId: 1, reasonRequired: true, note: "선택한 한빛상사는 최저가 대한오피스보다 5,000원 비쌉니다.",
      }),
    );
    expect(await screen.findByText("최저가")).toBeInTheDocument();
    expect(screen.getByText(/최저가보다 \+5,000원/)).toBeInTheDocument();
    const region = screen.getByRole("region", { name: "선택 사유" });
    expect(within(region).getByText(/한빛상사는 최저가 대한오피스보다 5,000원 비쌉니다/)).toBeInTheDocument();
    fireEvent.change(within(region).getByLabelText(/선택 사유 \(500자 이내\)/), { target: { value: "납기" } });
    fireEvent.click(within(region).getByRole("button", { name: "사유 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/purchase-requests/7/quote-selection", { quoteId: 1, reason: "납기" }));
  });

  it("읽지 못한 견적서는 직접 입력 안내를 보이고 선택할 수 없다", async () => {
    show(compare({ quotes: [quote({ status: "unreadable", filled: false, total: 0, lines: [], vendorId: 0, vendorName: "", supplier: "", lowest: false, filename: "스캔.pdf" })] }));
    expect(await screen.findByText(/스캔.pdf 파일은 글자를 읽을 수 없어 값을 채우지 못했습니다. 금액과 납기를 직접 입력하세요./)).toBeInTheDocument();
    expect(screen.getByText("읽지 못함")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "이 거래처 선택" })).toBeDisabled();
  });

  it("상신한 요청은 읽기 전용이다(올리기·선택·삭제 없음)", async () => {
    show(compare({ editable: false, quotes: [quote({ selected: true }), quote({ id: 2, vendorId: 2, vendorName: "다른상사", total: 12000, lowest: false, overLowest: 2000 })], selectedId: 1 }));
    expect(await screen.findByText(/상신한 요청이라 견적서와 선택은 읽기 전용입니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText("견적서 파일 선택")).toBeNull();
    expect(screen.queryByRole("button", { name: "이 거래처 선택" })).toBeNull();
    expect(screen.queryByRole("button", { name: "삭제" })).toBeNull();
  });

  it("견적 합계와 요청 금액이 다르면 경고만 보인다", async () => {
    show(compare({ quotes: [quote({ selected: true, total: 12000 })], selectedId: 1, totalMismatch: true, prTotal: 10000 }));
    expect(await screen.findByText(/견적 합계\(12,000원\)와 요청 금액\(10,000원\)이 다릅니다/)).toBeInTheDocument();
  });
});

describe("견적 카드", () => {
  it("선택한 거래처와 사유를 보이고, 불러오지 못하면 카드를 숨긴다", async () => {
    get.mockReset();
    get.mockResolvedValue(compare({ quotes: [quote({ selected: true, lowest: false, overLowest: 500 }), quote({ id: 2, lowest: true })], selectedId: 1, reason: "납기 때문", note: "선택한 한빛상사는 최저가 보다 비쌉니다.", editable: false }));
    const { unmount } = render(
      <MemoryRouter>
        <QuoteCard prId={7} />
      </MemoryRouter>,
    );
    expect(await screen.findByText("견적서 2건", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("최저가 아님")).toBeInTheDocument();
    expect(screen.getByText("납기 때문")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "견적 비교 보기" })).toBeInTheDocument();
    unmount();
    get.mockRejectedValue(new Error("x"));
    render(
      <MemoryRouter>
        <QuoteCard prId={7} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.queryByText("견적 비교")).toBeNull());
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { POPrintPage } from "./POPrintPage";
import { VendorRatingPage } from "./VendorRatingPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;

const po = {
  prId: 7, prNo: "PR-000007", poNo: "PO-000003", poDate: "2026-11-10T01:00:00Z", company: "PL 주식회사", department: "구매팀", requester: "직원",
  supplierName: "한빛상사", businessNo: "123-**-*****", phone: "02-****-4567", email: "s***@h.example", contactEmpty: false, masked: true,
  deliveryPlace: "본사 창고", dueDate: "", lines: [{ no: 1, itemName: "볼트", unit: "EA", quantity: 3, unitPrice: 12345, amount: 37035 }],
  supply: 37035, vat: 3703, total: 40738, note: "납품 시 거래명세서를 지참해 주세요.",
};
const pr = { id: 7, no: "PR-000007", requesterName: "직원", status: "ordered", steps: [], createdAt: "2026-11-09T01:00:00Z", lines: [], title: "t", totalAmount: 37035 };

function showPO(d: unknown, err?: Error) {
  get.mockReset();
  get.mockImplementation((u?: string) => {
    if (String(u).endsWith("/po")) return err ? Promise.reject(err) : Promise.resolve(d);
    return Promise.resolve(pr);
  });
  return render(
    <MemoryRouter initialEntries={["/purchase-requests/7/po"]}>
      <Routes>
        <Route path="/purchase-requests/:id/po" element={<POPrintPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("발주서 출력 화면(B25 ③)", () => {
  beforeEach(() => post.mockReset());

  it("공급가액·부가세·합계와 납기 협의, 가려진 연락처 안내를 보인다", async () => {
    showPO(po);
    expect(await screen.findByText("발 주 서")).toBeInTheDocument();
    expect(screen.getAllByText("37,035원").length).toBe(2); // 품목 금액과 공급가액
    expect(screen.getByText("3,703원")).toBeInTheDocument();
    expect(screen.getByText("40,738원")).toBeInTheDocument();
    expect(screen.getByText("협의")).toBeInTheDocument();
    expect(screen.getByText(/사업자번호와 연락처는 일부만 보입니다/)).toBeInTheDocument();
    expect(screen.getByText("123-**-*****")).toBeInTheDocument();
  });

  it("연락처가 비어 있으면 안내를 보이고, 마스킹이 없으면 안내가 없다", async () => {
    showPO({ ...po, masked: false, contactEmpty: true, phone: "", email: "" });
    expect(await screen.findByText(/거래처 연락처가 비어 있어 발주서의 연락처 칸이 비어 있습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/일부만 보입니다/)).toBeNull();
  });

  it("인쇄 버튼은 감사를 남긴 뒤 인쇄한다", async () => {
    const printSpy = vi.fn();
    vi.stubGlobal("print", printSpy);
    post.mockResolvedValue(undefined);
    showPO(po);
    fireEvent.click(await screen.findByRole("button", { name: "인쇄" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/purchase-requests/7/po/print"));
    await waitFor(() => expect(printSpy).toHaveBeenCalled());
    vi.unstubAllGlobals();
  });

  it("승인 전이면 서버 안내 문구만 보인다", async () => {
    const { ApiError } = await import("../api/client");
    showPO(null, new ApiError(409, "현재 상태에서는 할 수 없는 동작입니다: 아직 승인되지 않은 구매요청이라 발주서가 없습니다. 결재가 끝나면 발주서를 출력할 수 있습니다."));
    expect(await screen.findByText("아직 승인되지 않은 구매요청이라 발주서가 없습니다. 결재가 끝나면 발주서를 출력할 수 있습니다.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "인쇄" })).toBeNull();
  });
});

describe("거래처 평가 화면(B25 ④)", () => {
  const row = (o: object) => ({ vendorId: 1, vendorName: "한빛상사", orders: 4, onTime: 4, withDue: 4, noDue: 0, onTimePct: 100, onTimeBadge: "good", verdicts: 0, mismatches: 0, mismatchPct: -1, mismatchBadge: "none", lateOpen: 0, ...o });

  function showRating(rows: unknown[]) {
    get.mockReset();
    get.mockResolvedValue(rows);
    return render(
      <MemoryRouter>
        <VendorRatingPage />
      </MemoryRouter>,
    );
  }

  it("배지·표본 적음·제외 건수·미입고 건수를 보인다", async () => {
    showRating([
      row({}),
      row({ vendorId: 2, vendorName: "대한오피스", orders: 2, onTime: 1, withDue: 2, noDue: 1, onTimePct: 50, onTimeBadge: "small", lateOpen: 2, verdicts: 5, mismatches: 2, mismatchPct: 40, mismatchBadge: "bad" }),
    ]);
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("양호")).toBeInTheDocument();
    expect(screen.getByText("표본 적음")).toBeInTheDocument();
    expect(screen.getByText("불일치 많음")).toBeInTheDocument();
    expect(screen.getByText("납기 미지정 1건 제외")).toBeInTheDocument();
    expect(screen.getByText("납기를 넘긴 미입고 2건")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();
  });

  it("입고 완료 없이 미입고만 있는 거래처도 행으로 나오고 점수는 줄표다", async () => {
    showRating([row({ vendorId: 3, vendorName: "늦은상사", orders: 0, onTime: 0, withDue: 0, onTimePct: -1, onTimeBadge: "none", lateOpen: 2 })]);
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByRole("button", { name: "늦은상사" })).toBeInTheDocument();
    expect(screen.getByText("납기를 넘긴 미입고 2건")).toBeInTheDocument();
    expect(screen.queryByText("이 기간에 발주한 거래처가 없습니다.")).toBeNull();
  });

  it("거래가 없으면 빈 상태, 종료일이 앞서면 조회하지 않고 오류를 보인다", async () => {
    showRating([]);
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("이 기간에 발주한 거래처가 없습니다.")).toBeInTheDocument();
    get.mockClear();
    fireEvent.change(screen.getByLabelText("종료일"), { target: { value: "2000-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("종료일은 시작일 이후여야 합니다.")).toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });
});

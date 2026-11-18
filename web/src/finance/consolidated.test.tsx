import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ConsolidatedPage } from "./ConsolidatedPage";
import { VatPurchasePage } from "./VatPage";
import { visibleMenu } from "../layout/Layout";
import { financeModule } from "./routes";

vi.mock("../api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
    }
  },
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  apiDownload: vi.fn(),
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: () => true, user: { id: 1 } }) }));

const get = vi.mocked(api.get);

const report = {
  from: "2026-11-01",
  to: "2026-11-30",
  entities: [
    { id: 1, code: "MAIN", name: "에이엑스", closed: true, closedMonths: 1, months: 1 },
    { id: 2, code: "ABC", name: "계열사", closed: false, closedMonths: 0, months: 1 },
  ],
  trial: [
    { code: "1020", name: "보통예금", kind: "asset", amounts: [77000, 50000], total: 127000 },
    { code: "4110", name: "매출액", kind: "revenue", amounts: [100000, 18000], total: 118000 },
  ],
  summary: [
    { kind: "asset", label: "자산", amounts: [77000, 50000], total: 127000 },
    { kind: "net", label: "순손익(수익 − 비용)", amounts: [70000, 18000], total: 88000 },
  ],
  intercompany: [
    {
      fromId: 1,
      toId: 2,
      fromName: "에이엑스",
      toName: "계열사",
      lines: [{ code: "2110", name: "외상매입금", debit: 0, credit: 20000 }],
      payable: 20000,
      receivable: 18000,
      gap: 2000,
      counterpartRecorded: true,
    },
  ],
  partialScope: true,
  pending: 2,
};

beforeEach(() => {
  get.mockReset();
});

describe("연결 재무 집계", () => {
  it("법인별 열과 합계, 마감 배지, 일부 법인 안내, 내부 거래 참고 문구를 보인다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/finance/consolidated")) return report;
      return {};
    });
    render(
      <MemoryRouter>
        <ConsolidatedPage />
      </MemoryRouter>,
    );
    const trial = await screen.findByRole("table", { name: "법인별 시산표" });
    const headers = within(trial).getAllByRole("columnheader").map((h) => h.textContent ?? "");
    expect(headers.length).toBe(6); // 계정 코드·계정·분류·법인 2개·합계
    expect(headers[3]).toContain("에이엑스");
    expect(headers[3]).toContain("마감");
    expect(headers[4]).toContain("마감 전");
    expect(within(trial).getByText("127,000원")).toBeInTheDocument();
    expect(screen.getByText(/내가 볼 수 있는 법인만 합산/)).toBeInTheDocument();
    expect(screen.getByText(/승인 대기 전표 2건/)).toBeInTheDocument();
    expect(screen.getByText(/내부 거래가 합계에 포함되어 있습니다/)).toBeInTheDocument();
    expect(screen.getByText(/차이 2,000원/)).toBeInTheDocument();
    expect(screen.queryByText(/asset|revenue/)).toBeNull(); // 영문 분류 코드를 보이지 않는다
  });
});

describe("부가세 사업장", () => {
  const vat = {
    from: "2026-11-01",
    to: "2026-11-30",
    vendors: [],
    unreceived: [],
    cards: [],
    vendorVat: 0,
    cardDeductVat: 0,
    deductibleVat: 0,
    unreceivedVat: 0,
    ledgerVat: 3000,
    difference: 3000,
    matches: false,
    hints: [],
    excludedCount: 0,
    sites: [
      { siteId: 1, siteNo: "0000", name: "주사업장", kind: "head", vatMode: "own", count: 1, vat: 1000 },
      { siteId: 2, siteNo: "0001", name: "지점", kind: "branch", vatMode: "consolidated", count: 1, vat: 2000 },
    ],
    siteId: 1,
    siteName: "주사업장",
    siteScoped: true,
    consolidatedIn: ["지점"],
  };

  it("사업장이 둘 이상이면 선택기와 총괄납부 합산 표시를 보이고 선택한 사업장을 서버에 보낸다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/entity/sites"))
        return { sites: [{ id: 1, siteNo: "0000", name: "주사업장", kind: "head", vatMode: "own" }, { id: 2, siteNo: "0001", name: "지점", kind: "branch", vatMode: "consolidated" }] };
      return vat;
    });
    render(
      <MemoryRouter>
        <VatPurchasePage />
      </MemoryRouter>,
    );
    const select = await screen.findByLabelText("사업장");
    expect(within(select).getByText("주사업장(주사업장)")).toBeInTheDocument();
    expect(within(select).getByText("지점(총괄납부)")).toBeInTheDocument();
    expect((await screen.findAllByText("총괄납부 합산")).length).toBeGreaterThan(0);
    expect(screen.getByText(/수신했지만 연결되지 않은 세금계산서는/)).toBeInTheDocument();
    await waitFor(() => expect(get).toHaveBeenCalled());
  });

  it("사업장이 하나뿐이면 선택기가 없다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/entity/sites")) return { sites: [{ id: 1, siteNo: "0000", name: "주사업장", kind: "head", vatMode: "own" }] };
      return { ...vat, sites: undefined, siteId: undefined, siteScoped: undefined, consolidatedIn: undefined };
    });
    render(
      <MemoryRouter>
        <VatPurchasePage />
      </MemoryRouter>,
    );
    await screen.findByText("부가세 매입 집계");
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/entity/sites"));
    expect(screen.queryByLabelText("사업장")).toBeNull();
  });
});

describe("연결 재무 집계 메뉴", () => {
  it("고를 수 있는 법인이 하나면 메뉴에서 숨기고 둘 이상이면 보인다", () => {
    const all = () => true;
    expect(visibleMenu(financeModule.menu, all, false).map((m) => m.label)).not.toContain("연결 재무 집계");
    expect(visibleMenu(financeModule.menu, all, true).map((m) => m.label)).toContain("연결 재무 집계");
  });
});

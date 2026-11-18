import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { VatPurchasePage } from "./VatPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get", ReturnType<typeof vi.fn>>;

const report = (over = {}) => ({
  from: "2026-11-01", to: "2026-11-30", vendors: [{ vendorId: 1, vendorName: "한빛상사", businessNo: "123-45-67890", count: 1, supply: 10000, vat: 1000 }],
  unreceived: [{ journalNo: "JV-2026-00002", vendorName: "대한문구", supply: 20000, vat: 2000, tradeDate: "2026-11-10" }],
  cards: [{ kind: "deductible", reason: "", reasonLabel: "", count: 1, supply: 11364, vat: 1136, total: 12500 }, { kind: "nondeductible", reason: "entertainment", reasonLabel: "접대비", count: 1, supply: 0, vat: 0, total: 66000 }],
  vendorVat: 1000, cardDeductVat: 1136, deductibleVat: 2136, unreceivedVat: 2000, ledgerVat: 4136, difference: 2000, matches: false,
  hints: ["차이 중 2,000원은 세금계산서를 받지 못한 매입의 부가세입니다. 세금계산서를 받으면 공제에 들어갑니다."], excludedCount: 2, ...over,
});

beforeEach(() => {
  m.get.mockReset();
});

describe("부가세 매입 집계 화면(B32)", () => {
  it("세 표와 검산 배지, 차이 원인 힌트, 승인 대기 제외 안내를 보인다", async () => {
    m.get.mockResolvedValue(report());
    render(
      <AuthProvider initialUser={{ id: 6, name: "감사", role: "auditor", departmentId: 1 }}>
        <MemoryRouter>
          <VatPurchasePage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByText("한빛상사")).toBeInTheDocument();
    expect(screen.getByText("JV-2026-00002")).toBeInTheDocument();
    expect(screen.getByText("불공제(접대비)")).toBeInTheDocument();
    expect(screen.getByText(/차이 2,000원/)).toBeInTheDocument();
    expect(screen.getByText(/세금계산서를 받지 못한 매입의 부가세/)).toBeInTheDocument();
    expect(screen.getByText(/승인 대기 2건은 제외했습니다/)).toBeInTheDocument();
  });

  it("합계가 맞으면 일치 배지를 보이고 빈 표에는 안내 문구가 나온다", async () => {
    m.get.mockResolvedValue(report({ vendors: [], unreceived: [], cards: [], matches: true, difference: 0, hints: [], excludedCount: 0, ledgerVat: 0, deductibleVat: 0 }));
    render(
      <AuthProvider initialUser={{ id: 4, name: "재무", role: "finance", departmentId: 1 }}>
        <MemoryRouter>
          <VatPurchasePage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByText("매입세액 공제 합계와 일치")).toBeInTheDocument();
    expect(screen.getByText("세금계산서를 받지 못한 매입이 없습니다.")).toBeInTheDocument();
    expect(screen.getByText("승인된 카드 비용 전표가 없습니다.")).toBeInTheDocument();
  });
});

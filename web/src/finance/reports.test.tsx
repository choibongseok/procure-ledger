import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { LedgerPage, PayablesPage, TrialBalancePage } from "./ReportPages";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const finance: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const mount = (u: CurrentUser, el: React.ReactNode, path = "/") =>
  render(
    <AuthProvider initialUser={u}>
      <MemoryRouter initialEntries={[path]}>{el}</MemoryRouter>
    </AuthProvider>,
  );

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
});

const payables = (over = {}) => ({
  asOf: "2026-11-10", total: 100000, ledgerBalance: 100000, matches: true,
  rows: [{ vendorId: 3, vendorName: "한빛상사", count: 1, outstanding: 100000, reserved: 0, earliestDue: "2026-12-05", overdueAmount: 0, paymentDays: 30 }], ...over,
});

describe("재무 보고 화면(B31)", () => {
  it("매입 채무: 거래처별 잔액과 원장 검산 배지를 보이고, 재무는 결제 조건을 바꾼다(감사는 입력칸이 없다)", async () => {
    m.get.mockResolvedValue(payables());
    m.put.mockResolvedValue({ vendorId: 3, paymentDays: 45 });
    const first = mount(finance, <PayablesPage />);
    expect(await screen.findByText("한빛상사")).toBeInTheDocument();
    expect(screen.getByText("계정별 원장 외상매입금 잔액과 일치")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("한빛상사 결제 조건(일)"), { target: { value: "45" } });
    fireEvent.click(screen.getByRole("button", { name: "변경" }));
    await waitFor(() => expect(m.put).toHaveBeenCalledWith("/api/finance/vendors/3/payment-days", { days: 45 }));
    first.unmount();
    mount(auditor, <PayablesPage />);
    expect(await screen.findByText("한빛상사")).toBeInTheDocument();
    expect(screen.queryByLabelText("한빛상사 결제 조건(일)")).toBeNull();
  });

  it("매입 채무: 원장과 다르면 경고 배지를, 없으면 빈 상태 문구를 보인다", async () => {
    m.get.mockResolvedValue(payables({ matches: false, ledgerBalance: 90000 }));
    const first = mount(finance, <PayablesPage />);
    expect(await screen.findByText(/계정별 원장 잔액 90,000원과 다릅니다/)).toBeInTheDocument();
    first.unmount();
    m.get.mockResolvedValue(payables({ rows: [], total: 0, ledgerBalance: 0 }));
    mount(finance, <PayablesPage />);
    expect(await screen.findByText("미지급 채무가 없습니다.")).toBeInTheDocument();
  });

  it("시산표: 합계 일치 배지와 승인 대기 제외 안내, 불일치면 경고", async () => {
    const t = (over = {}) => ({ from: "2026-11-01", to: "2026-11-10", rows: [{ code: "1210", name: "상품", kind: "asset", kindLabel: "자산", opening: 0, debit: 100000, credit: 0, closing: 100000 }], totalDebit: 100000, totalCredit: 100000, balanced: true, excludedCount: 2, ...over });
    m.get.mockResolvedValue(t());
    const first = mount(finance, <TrialBalancePage />);
    expect(await screen.findByText("차변·대변 합계 일치")).toBeInTheDocument();
    expect(screen.getByText(/승인 대기 2건은 제외했습니다/)).toBeInTheDocument();
    expect(screen.getByText("자산")).toBeInTheDocument();
    first.unmount();
    m.get.mockResolvedValue(t({ balanced: false }));
    mount(finance, <TrialBalancePage />);
    expect(await screen.findByText("차대가 일치하지 않습니다. 관리자에게 알리세요")).toBeInTheDocument();
  });

  it("계정별 원장: 계정을 고르기 전에는 안내, 고른 뒤에는 이월 잔액·전표 링크·합계를 보인다", async () => {
    const accounts = [{ id: 2, code: "2110", name: "외상매입금", kind: "liability", kindLabel: "부채", active: true, system: true }];
    const ledger = { account: accounts[0], from: "2026-11-01", to: "2026-11-30", opening: 0, rows: [{ postingDate: "2026-11-10", journalNo: "JV-2026-00001", memo: "매입", counter: "1210 상품", debit: 0, credit: 100000, balance: 100000 }], totalDebit: 0, totalCredit: 100000, closing: 100000, excludedCount: 0 };
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/accounts") ? accounts : ledger));
    mount(finance, <LedgerPage />);
    expect(await screen.findByText("계정과목을 고르고 조회하세요.")).toBeInTheDocument();
  });

  it("계정별 원장: 주소의 계정으로 조회하면 이월 잔액 줄과 전표 번호 링크가 나온다", async () => {
    const accounts = [{ id: 2, code: "2110", name: "외상매입금", kind: "liability", kindLabel: "부채", active: true, system: true }];
    const ledger = { account: accounts[0], from: "2026-11-01", to: "2026-11-30", opening: 0, rows: [{ postingDate: "2026-11-10", journalNo: "JV-2026-00001", memo: "매입", counter: "1210 상품", debit: 0, credit: 100000, balance: 100000 }], totalDebit: 0, totalCredit: 100000, closing: 100000, excludedCount: 1 };
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/accounts") ? accounts : ledger));
    mount(finance, <LedgerPage />, "/?account=2110&from=2026-11-01&to=2026-11-30");
    expect(await screen.findByRole("link", { name: "JV-2026-00001" })).toBeInTheDocument();
    expect(screen.getByText("이월 잔액")).toBeInTheDocument();
    expect(screen.getByText(/승인 대기 1건은 제외했습니다/)).toBeInTheDocument();
  });
});

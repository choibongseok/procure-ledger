import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { BankAccountsPage } from "./FundPages";
import { PaymentDetailPage } from "./PaymentPages";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const finance: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 1 };
const finance2: CurrentUser = { id: 9, name: "재무2", role: "finance", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const payment = (over = {}) => ({
  id: 1, no: "PAY-2026-0001", vendorId: 3, vendorName: "한빛상사", status: "approved", statusLabel: "승인(지급 대기)", total: 1000000, dueDate: "2026-12-05", bankAccountId: 1, bankAlias: "주거래",
  payeeAccount: "***-***-**6789", memo: "", docId: 7, docNo: "PYD-2026-0001", createdBy: "재무", createdById: 4, createdAt: "2026-11-10T00:00:00Z", cancelReason: "", reverseReason: "",
  paidDate: "", transferRef: "", paidBy: "", journalNo: "", reversalNo: "", warnings: [], accountDiffers: false, items: [{ payableId: 1, journalNo: "JV-2026-00001", tradeDate: "2026-11-05", dueDate: "2026-12-05", amount: 1000000 }], ...over,
});
const mount = (u: CurrentUser, el: React.ReactNode, route: string, path: string) =>
  render(
    <AuthProvider initialUser={u}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={route} element={el} />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  );

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
});

describe("지급 화면(B32)", () => {
  it("승인된 지급 건: 작성자에게는 지급 처리 단추가 없고 안내가 보이며, 다른 재무에게는 단추가 있다", async () => {
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/bank-accounts") ? [] : payment()));
    const first = mount(finance, <PaymentDetailPage />, "/finance/payments/:no", "/finance/payments/PAY-2026-0001");
    expect(await screen.findByText(/지급 요청을 작성한 사람은 지급 처리할 수 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "지급 처리" })).toBeNull();
    expect(screen.getByText("***-***-**6789")).toBeInTheDocument();
    first.unmount();
    mount(finance2, <PaymentDetailPage />, "/finance/payments/:no", "/finance/payments/PAY-2026-0001");
    expect(await screen.findByRole("button", { name: "지급 처리" })).toBeInTheDocument();
  });

  it("감사는 지급 건을 보지만 처리·취소 단추가 없고, 원문 보기도 없다", async () => {
    m.get.mockResolvedValue(payment());
    mount(auditor, <PaymentDetailPage />, "/finance/payments/:no", "/finance/payments/PAY-2026-0001");
    expect(await screen.findByText("한빛상사")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "지급 처리" })).toBeNull();
    expect(screen.queryByRole("button", { name: "지급 요청 취소" })).toBeNull();
    expect(screen.queryByRole("button", { name: "원문 보기" })).toBeNull();
  });

  it("거래처 계좌가 바뀌었으면 경고를 보이고 지급 처리 때 확인 체크를 요구한다", async () => {
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/bank-accounts") ? [] : payment({ accountDiffers: true })));
    m.post.mockResolvedValue(payment({ status: "paid", statusLabel: "지급 완료" }));
    mount(finance2, <PaymentDetailPage />, "/finance/payments/:no", "/finance/payments/PAY-2026-0001");
    expect(await screen.findByText(/거래처 계좌가 변경되었습니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "지급 처리" }));
    fireEvent.click(await screen.findByLabelText(/거래처 계좌가 바뀐 것을 확인했습니다/));
    fireEvent.change(screen.getByLabelText("이체 참조번호(선택)"), { target: { value: "TR-1" } });
    fireEvent.click(screen.getAllByRole("button", { name: "지급 처리" }).at(-1)!);
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/payments/PAY-2026-0001/settle", expect.objectContaining({ transferRef: "TR-1", confirmAccount: true })));
  });

  it("자금 계좌: 번호는 마스킹으로 보이고 재무는 원문 보기를 누를 수 있다", async () => {
    m.get.mockImplementation(async (p: string) =>
      p.startsWith("/api/finance/bank-accounts") ? [{ id: 1, alias: "주거래", bankName: "국민은행", accountNo: "***-***-**9012", ledgerCode: "1020", ledgerName: "보통예금", openingBalance: 0, openingDate: "2026-01-01", active: true, balance: 500000 }] : [],
    );
    m.post.mockResolvedValue({ accountNo: "123-456-789012" });
    mount(finance, <BankAccountsPage />, "/", "/");
    expect(await screen.findByText(/\*\*\*-\*\*\*-\*\*9012/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "원문 보기" }));
    expect(await screen.findByText(/123-456-789012/)).toBeInTheDocument();
  });
});

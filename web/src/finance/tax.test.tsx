import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { TaxInvoiceDetailPage, TaxInvoiceEditPage, TaxInvoiceListPage, TaxReceivePage } from "./TaxInvoicePages";
import type { TaxInvoice } from "./taxTypes";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put", ReturnType<typeof vi.fn>>;

const party = (name: string, biz: string) => ({ bizNo: biz, branchNo: "", name, rep: "대표", addr: "", email: "" });
const inv = (over: Partial<TaxInvoice> = {}): TaxInvoice => ({
  id: 1, no: "TI-000001", direction: "sale", directionName: "매출", taxType: "taxable", taxTypeName: "과세", status: "draft", statusName: "작성 중", approvalNo: "", issueDate: "2026-11-09",
  supplier: party("에이엑스 주식회사", "1234567890"), buyer: party("한빛상사", "2208162517"), supplyTotal: 100000, taxTotal: 10000, total: 110000, amendOfId: 0, amendOfNo: "", amendedByNo: "",
  amendReason: "", amendReasonName: "", chargeType: "claim", remark: "", source: "manual", attempts: 0, resends: 0, lastCodeText: "", issuedAt: "", sentAt: "", acceptedAt: "", canceledAt: "", cancelReason: "",
  lines: [{ seq: 1, day: "", name: "복사용지", spec: "", qty: 10, unitPrice: 10000, supply: 100000, tax: 10000, remark: "" }], transmissions: [], warnings: [], ...over,
});

const finance = { id: 4, name: "재무", role: "finance", departmentId: 3 } as const;
const auditor = { id: 6, name: "감사", role: "auditor", departmentId: 3 } as const;

function renderAt(path: string, user: typeof finance | typeof auditor) {
  return render(
    <AuthProvider initialUser={user}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/finance/tax-invoices" element={<TaxInvoiceListPage />} />
          <Route path="/finance/tax-invoices/new" element={<TaxInvoiceEditPage />} />
          <Route path="/finance/tax-invoices/receive" element={<TaxReceivePage />} />
          <Route path="/finance/tax-invoices/:id" element={<TaxInvoiceDetailPage />} />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  m.get.mockReset();
  m.post.mockReset();
  m.put.mockReset();
});

describe("세금계산서 화면(B43)", () => {
  it("목록은 한국어 상태 이름과 상대방을 보이고 영문 상태 코드를 보이지 않는다", async () => {
    m.get.mockResolvedValue({
      rows: [inv({ status: "accepted", statusName: "국세청 수신", approvalNo: "A".repeat(24) }), inv({ id: 2, no: "TI-000002", direction: "purchase", directionName: "매입", supplier: party("대한물산", "1068123456"), status: "failed", statusName: "실패" })],
      total: 2,
    });
    renderAt("/finance/tax-invoices", finance);
    expect(await screen.findByText("한빛상사")).toBeInTheDocument();
    expect(screen.getByText("대한물산")).toBeInTheDocument();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("국세청 수신")).toBeInTheDocument();
    expect(table.getByText("실패")).toBeInTheDocument();
    expect(screen.queryByText("accepted")).toBeNull();
    expect(screen.getByRole("link", { name: "매출 세금계산서 작성" })).toBeInTheDocument();
  });

  it("읽기 전용(감사)에게는 작성·등록 단추가 없다", async () => {
    m.get.mockResolvedValue({ rows: [], total: 0 });
    renderAt("/finance/tax-invoices", auditor);
    expect(await screen.findByText(/조건에 맞는 세금계산서가 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "매출 세금계산서 작성" })).toBeNull();
    expect(screen.queryByRole("link", { name: "매입 수기 등록" })).toBeNull();
  });

  it("작성 중인 건: 접수된 증빙이 아니라는 안내와 발행 단추가 있고 발행은 확인을 거친다", async () => {
    m.get.mockResolvedValue(inv());
    m.post.mockResolvedValue(inv({ status: "issued", statusName: "발행 대기" }));
    renderAt("/finance/tax-invoices/1", finance);
    expect(await screen.findByText(/국세청에 접수된 증빙이 아닙니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "발행" }));
    expect(screen.getByText(/발행한 뒤에는 금액·작성일·거래 상대방을 고칠 수 없고/)).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "발행" }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/tax-invoices/1/issue", undefined));
  });

  it("국세청 수신 건은 승인번호가 보이고 안내가 없으며 수정 발행만 할 수 있다", async () => {
    m.get.mockResolvedValue(inv({ status: "accepted", statusName: "국세청 수신", approvalNo: "20261109ABCDEFGH12345678" }));
    renderAt("/finance/tax-invoices/1", finance);
    expect(await screen.findByText("20261109ABCDEFGH12345678")).toBeInTheDocument();
    expect(screen.queryByText(/국세청에 접수된 증빙이 아닙니다/)).toBeNull();
    expect(screen.getByRole("button", { name: "수정 발행" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "발행" })).toBeNull();
    expect(screen.queryByRole("button", { name: "취소" })).toBeNull();
  });

  it("실패한 건은 사유를 한국어로 보이고 다시 전송·포기 단추가 있다", async () => {
    m.get.mockResolvedValue(inv({ status: "failed", statusName: "실패", lastCodeText: "대행사가 내용 검증에서 반려했습니다." }));
    renderAt("/finance/tax-invoices/1", finance);
    expect(await screen.findByText(/대행사가 내용 검증에서 반려했습니다/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "다시 전송" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "포기(취소)" })).toBeInTheDocument();
  });

  it("매입 수기 등록: 입력한 값을 보내고 중복 승인번호 오류를 한국어로 보인다", async () => {
    const { ApiError } = await vi.importActual<typeof import("../api/client")>("../api/client");
    m.post.mockRejectedValue(new ApiError(400, "입력값이 올바르지 않습니다: 이미 등록된 승인번호입니다(TI-000007)."));
    renderAt("/finance/tax-invoices/receive", finance);
    fireEvent.change(screen.getByLabelText("승인번호(24자리)"), { target: { value: "20261108ABCDEFGH12345678" } });
    fireEvent.change(screen.getByLabelText("사업자번호"), { target: { value: "220-81-62517" } });
    fireEvent.change(screen.getByLabelText("상호"), { target: { value: "한빛상사" } });
    fireEvent.change(screen.getByLabelText("공급가액"), { target: { value: "200000" } });
    fireEvent.change(screen.getByLabelText("세액"), { target: { value: "20000" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    await waitFor(() => expect(m.post).toHaveBeenCalled());
    const body = m.post.mock.calls[0][1] as { approvalNo: string; supplyTotal: number; taxTotal: number; supplier: { name: string } };
    expect(body.approvalNo).toBe("20261108ABCDEFGH12345678");
    expect(body.supplyTotal).toBe(200000);
    expect(body.taxTotal).toBe(20000);
    expect(body.supplier.name).toBe("한빛상사");
    expect(await screen.findByText(/이미 등록된 승인번호입니다/)).toBeInTheDocument();
    expect(screen.queryByText(/입력값이 올바르지 않습니다:/)).toBeNull();
  });

  it("작성 화면: 수량×단가로 공급가액을 채우고 합계를 보인다", async () => {
    renderAt("/finance/tax-invoices/new", finance);
    await act(async () => {});
    fireEvent.change(screen.getByLabelText("1번째 품목명"), { target: { value: "복사용지" } });
    fireEvent.change(screen.getByLabelText("1번째 수량"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("1번째 단가"), { target: { value: "3000" } });
    expect(screen.getByLabelText("1번째 공급가액")).toHaveValue("30000");
    expect(screen.getByText(/공급가액 합계 30,000원/)).toBeInTheDocument();
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { CardsAdminPage, CardsConfirmPage, CardsMinePage } from "./CardPages";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const staff: CurrentUser = { id: 1, name: "직원", role: "staff", departmentId: 1 };
const lead: CurrentUser = { id: 2, name: "팀장", role: "manager", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const txn = (over = {}) => ({
  id: 5, cardId: 1, cardAlias: "영업 법인카드", holderId: 1, holderName: "직원", usedAt: "2026-11-08T03:00:00Z", merchant: "스타벅스", amount: 12500, kind: "approve", status: "unreconciled",
  statusLabel: "미소명", dueDate: "2026-11-15", daysLeft: 3, overdue: false, journalNo: "", refundNote: "", ...over,
});
const mount = (u: CurrentUser, el: React.ReactNode) =>
  render(
    <AuthProvider initialUser={u}>
      <MemoryRouter>{el}</MemoryRouter>
    </AuthProvider>,
  );

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
});

describe("법인카드 화면(B32)", () => {
  it("내 카드 내역: 기한 배지와 소명하기가 보이고, 용도·계정을 입력해 제출한다(카드번호는 별칭뿐)", async () => {
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/cards/explain-accounts") ? [{ code: "5220", name: "소모품비" }] : [txn(), txn({ id: 6, status: "needs_fix", statusLabel: "보완 필요", overdue: true, daysLeft: -2 })]));
    m.post.mockResolvedValue(txn({ status: "submitted" }));
    mount(staff, <CardsMinePage />);
    expect(await screen.findAllByText("영업 법인카드")).not.toHaveLength(0);
    expect(screen.getByText("기한까지 3일")).toBeInTheDocument();
    expect(screen.getByText("기한 2일 경과")).toBeInTheDocument();
    expect(screen.queryByText(/7890/)).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "소명하기" })[0]);
    fireEvent.change(await screen.findByLabelText("용도(10~200자)"), { target: { value: "고객사 방문 시 음료 구입 비용" } });
    fireEvent.change(screen.getByLabelText("계정과목"), { target: { value: "5220" } });
    fireEvent.click(screen.getByRole("button", { name: "소명 제출" }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/cards/mine/5/explain", expect.objectContaining({ accountCode: "5220", vatDeduct: true, personal: false })));
  });

  it("소명 확인: 조직장은 선택한 건을 확인하고 반려는 사유를 받는다", async () => {
    m.get.mockResolvedValue([txn({ status: "submitted", statusLabel: "소명됨(확인 대기)", explanation: { id: 1, purpose: "음료 구입", accountCode: "5220", accountName: "소모품비", vatDeduct: true, vatReason: "", vatReasonLabel: "", supply: 11364, vat: 1136, personal: false, status: "submitted", submittedAt: "2026-11-09T00:00:00Z", confirmedBy: "", returnReason: "", attachments: [] } })]);
    m.post.mockResolvedValue([{ id: 5, ok: true, error: "", journalNo: "JV-2026-00009" }]);
    mount(lead, <CardsConfirmPage />);
    expect(await screen.findByText(/5220 소모품비/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("스타벅스 선택"));
    fireEvent.click(screen.getByRole("button", { name: /선택한 소명 확인\(1건\)/ }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/cards/confirm", { ids: [5] }));
    expect(await screen.findByText("1건을 확인했습니다.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "반려" }));
    fireEvent.change(await screen.findByLabelText("반려 사유(필수)"), { target: { value: "영수증 없음" } });
    m.post.mockResolvedValue(txn());
    fireEvent.click(screen.getAllByRole("button", { name: "반려" }).at(-1)!);
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/cards/confirm/5/return", { reason: "영수증 없음" }));
  });

  it("카드 관리: 감사는 조회만 하고 카드 등록·업로드 영역이 없으며 현황에 소명 지연이 보인다", async () => {
    m.get.mockImplementation(async (p: string) =>
      p === "/api/finance/cards"
        ? [{ id: 1, alias: "영업 법인카드", issuer: "format_a", issuerLabel: "승인일시 한 칸형", holderId: 1, holderName: "직원", active: true }]
        : { statuses: { unreconciled: { count: 1, amount: 12500, label: "미소명" } }, delayed: [txn({ overdue: true, daysLeft: -35 })], noHolder: [], needsFinance: [], refundRequested: [] },
    );
    mount(auditor, <CardsAdminPage />);
    expect(await screen.findByText("승인일시 한 칸형")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "카드 등록" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "카드 내역 업로드" })).toBeNull();
    expect(await screen.findByText("소명 지연(기한 30일 경과)")).toBeInTheDocument();
    expect(screen.getByText("기한 35일 경과")).toBeInTheDocument();
  });
});

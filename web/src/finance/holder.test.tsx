import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { CardsAdminPage } from "./CardPages";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const finance: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 1 };
const admin: CurrentUser = { id: 7, name: "관리자", role: "admin", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const card = { id: 1, alias: "영업 법인카드", issuer: "format_a", issuerLabel: "승인일시 한 칸형", holderId: 1, holderName: "직원", active: true };
const noHolderTxn = { id: 9, cardId: 1, cardAlias: "영업 법인카드", holderId: 0, holderName: "", usedAt: "2026-11-08T03:00:00Z", merchant: "스타벅스", amount: 12500, kind: "approve", status: "unreconciled", statusLabel: "미소명", dueDate: "2026-11-15", daysLeft: -3, overdue: true, journalNo: "", refundNote: "" };

function route(p: string) {
  if (p === "/api/finance/cards") return [card];
  if (p === "/api/finance/cards/overview") return { statuses: {}, delayed: [], noHolder: [noHolderTxn], needsFinance: [], refundRequested: [] };
  if (p === "/api/finance/card-holders/1") return [{ holderName: "직원", fromDate: "2026-11-01", changedBy: "재무", changedAt: "2026-11-01T00:00:00Z" }];
  if (p.startsWith("/api/org/people")) return { people: [{ id: 5, name: "영업직원" }] };
  return [];
}

function mount(u: CurrentUser) {
  return render(
    <AuthProvider initialUser={u}>
      <MemoryRouter>
        <CardsAdminPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
  m.get.mockImplementation(async (p: string) => route(p));
});

describe("카드 소지자 변경·재지정 화면", () => {
  it("재무·관리자는 소지자 변경 패널에서 변경 이력을 보고 새 소지자를 골라 바꾼다", async () => {
    m.post.mockResolvedValue({ ...card, holderId: 5, holderName: "영업직원" });
    const first = mount(admin);
    fireEvent.click(await screen.findByRole("button", { name: "소지자 변경" }));
    expect(await screen.findByText(/이후 업로드되는 내역부터 적용됩니다/)).toBeInTheDocument();
    expect(await screen.findByRole("table", { name: "소지자 변경 이력" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("새 소지자(이름 검색)"), { target: { value: "영업" } });
    fireEvent.click(await screen.findByRole("button", { name: "영업직원" }));
    fireEvent.click(screen.getAllByRole("button", { name: "소지자 변경" }).at(-1)!);
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/cards/1/holder", { holderId: 5 }));
    first.unmount();
    mount(finance);
    expect(await screen.findByRole("button", { name: "소지자 변경" })).toBeInTheDocument();
  });

  it("소지자 없음 건은 재무·관리자가 새 소지자를 지정하고, 감사에게는 버튼이 없다", async () => {
    m.post.mockResolvedValue({ ...noHolderTxn, holderId: 5, holderName: "영업직원" });
    const first = mount(finance);
    fireEvent.click(await screen.findByRole("button", { name: "소지자 지정" }));
    fireEvent.change(await screen.findByLabelText("새 소지자(이름 검색)"), { target: { value: "영업" } });
    fireEvent.click(await screen.findByRole("button", { name: "영업직원" }));
    fireEvent.click(screen.getAllByRole("button", { name: "소지자 지정" }).at(-1)!);
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/cards/txns/9/reassign", { holderId: 5 }));
    first.unmount();
    mount(auditor);
    expect(await screen.findByText("영업 법인카드")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "소지자 변경" })).toBeNull();
    expect(screen.queryByRole("button", { name: "소지자 지정" })).toBeNull();
    expect(screen.getByText("재무에게 소지자 지정을 요청하세요")).toBeInTheDocument();
  });
});

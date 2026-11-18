import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { AccountsPage } from "./AccountsPage";
import { JournalDetailPage, JournalEditPage } from "./JournalPages";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;

const finance: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const accounts = [
  { id: 1, code: "1210", name: "상품", kind: "asset", kindLabel: "자산", active: true, system: true },
  { id: 2, code: "2110", name: "외상매입금", kind: "liability", kindLabel: "부채", active: true, system: true },
  { id: 3, code: "5250", name: "접대비", kind: "expense", kindLabel: "비용", active: false, system: false },
];
const journal = (over = {}) => ({
  id: 1, no: "JV-2026-00001", kind: "general", kindLabel: "일반", status: "approved", statusLabel: "승인", postingDate: "2026-11-10", tradeDate: "2026-11-05", memo: "매입", totalAmount: 110000,
  reversesNo: "", reversedByNo: "", corrected: false, docId: 7, docNo: "JVD-2026-0001", createdBy: "재무", createdAt: "2026-11-10T00:00:00Z", approvedBy: "재무팀장", approvedAt: "2026-11-10T01:00:00Z", cancelReason: "",
  lines: [{ seq: 1, accountCode: "1210", accountName: "상품", side: "D", amount: 110000, vendorId: 0, vendorName: "", memo: "" }, { seq: 2, accountCode: "2110", accountName: "외상매입금", side: "C", amount: 110000, vendorId: 1, vendorName: "대한문구", memo: "" }],
  ...over,
});

function mount(user: CurrentUser, path: string, route: string, el: React.ReactNode) {
  return render(
    <AuthProvider initialUser={user}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={route} element={el} />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
});

describe("재무 화면(B31)", () => {
  it("계정과목: 감사는 조회만, 재무는 추가·이름 변경이 보이고 시스템 계정에는 중지 단추가 없다", async () => {
    m.get.mockImplementation(async (p: string) => (p.startsWith("/api/finance/accounts") ? accounts : []));
    const first = mount(auditor, "/finance/accounts", "/finance/accounts", <AccountsPage />);
    expect(await screen.findByText("외상매입금")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "계정 추가" })).toBeNull();
    first.unmount();
    mount(finance, "/finance/accounts", "/finance/accounts", <AccountsPage />);
    expect(await screen.findByRole("button", { name: "계정 추가" })).toBeInTheDocument();
    expect(screen.getAllByText("시스템").length).toBe(2);
    expect(screen.queryAllByRole("button", { name: "중지" })).toHaveLength(0); // 중지된 접대비만 있어 "다시 사용"이 보인다
    expect(screen.getByRole("button", { name: "다시 사용" })).toBeInTheDocument();
  });

  it("전표 입력: 차대 합계와 차이를 실시간으로 보이고, 일치해야 승인 요청 단추가 켜진다", async () => {
    m.get.mockImplementation(async (p: string) => (p === "/api/vendors" ? [{ id: 1, name: "대한문구" }] : p.startsWith("/api/finance/accounts") ? accounts : null));
    mount(finance, "/finance/journals/new", "/finance/journals/new", <JournalEditPage />);
    const request = await screen.findByRole("button", { name: "저장하고 승인 요청" });
    expect(request).toBeDisabled();
    fireEvent.change(screen.getByLabelText("1번째 줄 금액"), { target: { value: "1000" } });
    expect(await screen.findByText(/차이 1,000원/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("2번째 줄 금액"), { target: { value: "1000" } });
    expect(await screen.findByText("차대 일치")).toBeInTheDocument();
    expect(request).toBeEnabled();
  });

  it("전표 상세: 승인된 전표에는 역분개 단추가, 정정된 전표에는 정정됨 배지가 보이고 수정 단추는 없다", async () => {
    m.get.mockResolvedValue(journal());
    const first = mount(finance, "/finance/journals/JV-2026-00001", "/finance/journals/:no", <JournalDetailPage />);
    expect(await screen.findByRole("button", { name: "역분개" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "수정" })).toBeNull();
    expect(screen.getByText("대한문구")).toBeInTheDocument();
    first.unmount();
    m.get.mockResolvedValue(journal({ corrected: true, reversedByNo: "JV-2026-00002" }));
    mount(finance, "/finance/journals/JV-2026-00001", "/finance/journals/:no", <JournalDetailPage />);
    expect(await screen.findAllByText(/정정됨\(JV-2026-00002\)/)).not.toHaveLength(0);
    expect(screen.queryByRole("button", { name: "역분개" })).toBeNull();
  });

  it("전표 상세: 임시 전표 취소는 사유를 받아 요청한다", async () => {
    m.get.mockResolvedValue(journal({ status: "draft", statusLabel: "임시" }));
    m.post.mockResolvedValue(journal({ status: "canceled", statusLabel: "취소" }));
    mount(finance, "/finance/journals/JV-2026-00001", "/finance/journals/:no", <JournalDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "전표 취소" }));
    fireEvent.change(await screen.findByLabelText("취소 사유(필수)"), { target: { value: "중복 입력" } });
    fireEvent.click(screen.getAllByRole("button", { name: "전표 취소" }).at(-1)!);
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/journals/JV-2026-00001/cancel", { reason: "중복 입력" }));
  });
});

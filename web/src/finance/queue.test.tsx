import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { QueuePage } from "./QueuePage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const finance: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 1 };
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };
const item = (over = {}) => ({
  id: 5, sourceType: "verdict", sourceId: 9, sourceLabel: "증빙 판정 9", reason: "rule_account_inactive", reasonText: "규칙 계정 사용 불가: 재고자산", poNo: "PO-2026-0001",
  vendorName: "한빛상사", amount: 100000, status: "pending", statusLabel: "처리 전", journalNo: "", handledBy: "", note: "", createdAt: "2026-11-10T00:00:00Z", ...over,
});
const mount = (u: CurrentUser) =>
  render(
    <AuthProvider initialUser={u}>
      <MemoryRouter>
        <QueuePage />
      </MemoryRouter>
    </AuthProvider>,
  );

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) m[k].mockReset();
});

describe("전표 대기 화면(B31)", () => {
  it("사유를 한국어로 보이고 재무는 다시 만들기를 요청한다", async () => {
    m.get.mockResolvedValue([item()]);
    m.post.mockResolvedValue(item({ status: "created", statusLabel: "전표 생성", journalNo: "JV-2026-00003" }));
    mount(finance);
    expect(await screen.findByText("규칙 계정 사용 불가: 재고자산")).toBeInTheDocument();
    expect(screen.queryByText(/rule_account_inactive/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "다시 만들기" }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/journal-queue/5/retry"));
    expect(await screen.findByText("전표 JV-2026-00003 을(를) 만들었습니다.")).toBeInTheDocument();
  });

  it("불일치 확정 건은 다시 만들기가 없고, 감사는 처리 단추가 없다", async () => {
    m.get.mockResolvedValue([item({ reason: "mismatch_confirmed", reasonText: "불일치 확정" })]);
    const first = mount(finance);
    expect(await screen.findByText("불일치 확정")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "다시 만들기" })).toBeNull();
    expect(screen.getByRole("button", { name: "전표 불필요" })).toBeInTheDocument();
    first.unmount();
    mount(auditor);
    expect(await screen.findByText("불일치 확정")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "전표 불필요" })).toBeNull();
  });

  it("비어 있으면 안내 문구를 보인다", async () => {
    m.get.mockResolvedValue([]);
    mount(finance);
    expect(await screen.findByText("전표 대기 건이 없습니다.")).toBeInTheDocument();
  });
});

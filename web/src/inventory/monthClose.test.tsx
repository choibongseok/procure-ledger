import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { MonthClosePage, monthText } from "./MonthClosePage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;

const row = (o: object) => ({ month: "2026-10", state: "open", version: 0, closedBy: "", closable: true, releasable: false, ...o });
const user = (role: CurrentUser["role"]): CurrentUser => ({ id: 4, name: "재무", role, departmentId: 3 });

function show(role: CurrentUser["role"], months: unknown[], pre: unknown) {
  get.mockReset();
  get.mockImplementation((u?: string) => {
    const url = String(u);
    if (url.endsWith("/precheck")) return Promise.resolve(pre);
    return Promise.resolve(months);
  });
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user(role)}>
        <MonthClosePage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("월 마감 화면(B27)", () => {
  beforeEach(() => post.mockReset());

  it("월 표기", () => {
    expect(monthText("2026-10")).toBe("2026년 10월");
  });

  it("마감한 달이 없으면 안내 문구를 보인다", async () => {
    show("finance", [], null);
    expect(await screen.findByText("아직 마감한 달이 없습니다. 이번 달이 끝나면 지난 달을 마감할 수 있습니다.")).toBeInTheDocument();
  });

  it("상태 배지와 금액 열(권한 있을 때만 서버가 내려준다)을 보인다", async () => {
    show("auditor", [row({ month: "2026-11", state: "closed", version: 2, closedAt: "2026-12-02T01:00:00Z", closedBy: "재무 담당", closeAmount: 26000 }), row({})], null);
    expect(await screen.findByText("마감됨")).toBeInTheDocument();
    expect(screen.getByText("열림")).toBeInTheDocument();
    expect(screen.getByText("2차 마감")).toBeInTheDocument();
    expect(screen.getByText("26,000원")).toBeInTheDocument();
    expect(screen.getByText("기말 재고 금액")).toBeInTheDocument();
  });

  it("금액이 내려오지 않으면 금액 열이 없다", async () => {
    show("staff", [row({})], null);
    await screen.findByText("열림");
    expect(screen.queryByText("기말 재고 금액")).toBeNull();
  });

  it("점검 목록에서 차단과 경고를 나눠 보이고, 차단이 있으면 마감 단추가 꺼진다", async () => {
    show(
      "finance",
      [row({})],
      { month: "2026-10", closable: false, reason: "결재 진행 중인 재고 조정 전표 1건이 있어 마감할 수 없습니다.", blockers: [{ title: "결재 진행 중인 재고 조정 전표", detail: "ADJ-2026-0001 가 결재 중입니다.", docId: 7 }], warnings: [{ title: "입고가 끝나지 않은 발주", detail: "발주 1건", docId: 0 }] },
    );
    fireEvent.click(await screen.findByRole("button", { name: "2026년 10월 점검" }));
    const card = await screen.findByRole("region", { name: "2026년 10월 마감 전 점검" });
    expect(within(card).getByText("차단")).toBeInTheDocument();
    expect(within(card).getByText("경고")).toBeInTheDocument();
    expect(within(card).getByText(/ADJ-2026-0001 가 결재 중입니다/)).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "이 달 마감" })).toBeDisabled();
  });

  it("마감 가능하면 확인 대화상자를 거쳐 마감하고, 마감 권한이 없으면 단추가 없다", async () => {
    post.mockResolvedValue({});
    const { unmount } = show("finance", [row({})], { month: "2026-10", closable: true, reason: "", blockers: [], warnings: [] });
    fireEvent.click(await screen.findByRole("button", { name: "2026년 10월 점검" }));
    expect(await screen.findByText("점검할 항목이 없습니다. 마감할 수 있습니다.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "이 달 마감" }));
    expect(await screen.findByText(/마감하면 그 달 날짜로는 출고·실사를 만들거나 고칠 수 없습니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "마감" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/inventory/months/2026-10/close"));
    unmount();
    show("auditor", [row({})], { month: "2026-10", closable: true, reason: "", blockers: [], warnings: [] });
    fireEvent.click(await screen.findByRole("button", { name: "2026년 10월 점검" }));
    await screen.findByText("점검할 항목이 없습니다. 마감할 수 있습니다.");
    expect(screen.queryByRole("button", { name: "이 달 마감" })).toBeNull();
  });
});

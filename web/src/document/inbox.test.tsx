import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { CancelSubmit } from "./CancelSubmit";
import { DocList } from "./DocList";
import type { InboxItem } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }, apiFetch: vi.fn() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const lead: CurrentUser = { id: 2, name: "구매팀장", role: "manager", departmentId: 1 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const item = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: 1, no: "구매팀-2026-0001", title: "협조 요청", formCode: "COOP", formName: "업무 협조전", formKind: "general", requesterName: "김직원", unitPath: "구매팀 > 구매1팀",
  status: "submitted", urgent: false, unread: false, updatedAt: "2026-11-10T09:00:00Z", amount: 0, receiptId: 0, receiptStatus: "", receiptUnit: "", ...over,
});
const counts = { drafted: { total: 0, unread: 0 }, pending: { total: 2, unread: 1 }, in_progress: { total: 0, unread: 0 }, done: { total: 0, unread: 0 }, cc: { total: 0, unread: 0 }, receive: { total: 1, unread: 0 }, rejected: { total: 0, unread: 0 } };

function at(path: string, user = lead) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider initialUser={user}>
        <Routes>
          <Route path="/docs" element={<DocList />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("B23 단계 2 문서함 화면", () => {
  it("탭 7개에 건수와 안 읽음이 보이고, 안 읽은 문서는 점과 긴급 표시가 붙는다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path === "/api/inbox/counts" ? counts : path.startsWith("/api/inbox/pending") ? [item({ unread: true, urgent: true })] : []));
    at("/docs");
    expect(await screen.findByRole("tab", { name: /결재 대기함 2 \(안 읽음 1\)/ })).toBeInTheDocument();
    for (const n of ["기안함", "결재 진행함", "완료함", "참조함", "수신함", "반려함"]) expect(screen.getByRole("tab", { name: new RegExp(n) })).toBeInTheDocument();
    expect(await screen.findByLabelText("안 읽음")).toBeInTheDocument();
    expect(screen.getByText("긴급")).toBeInTheDocument();
  });

  it("결재 대기함에서 고른 문서를 일괄 승인하고, 실패 건은 사유와 함께 보인다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path === "/api/inbox/counts" ? counts : path.startsWith("/api/inbox/pending") ? [item({ id: 1 }), item({ id: 2, title: "두 번째" })] : []));
    mocked.post.mockResolvedValue({ results: [{ docId: 1, ok: true, message: "처리했습니다" }, { docId: 2, ok: false, message: "지금은 결재 차례가 아닙니다" }] });
    at("/docs");
    const batchBtn = await screen.findByRole("button", { name: /일괄 승인/ });
    expect(batchBtn).toBeDisabled();
    fireEvent.click(await screen.findByLabelText("협조 요청 선택"));
    fireEvent.click(screen.getByLabelText("두 번째 선택"));
    fireEvent.click(screen.getByRole("button", { name: "선택한 2건 일괄 승인" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/approvals/batch", { docIds: [1, 2], comment: "" }));
    expect(await screen.findByText(/1건을 승인했습니다\. 1건은 처리하지 못했습니다: 구매팀-2026-0001 두 번째: 지금은 결재 차례가 아닙니다/)).toBeInTheDocument();
    expect(screen.queryByText(/#2/)).toBeNull();
  });

  it("일괄 승인 실패 문구에서 목록에 없는 문서는 내부 번호로 떨어지고 긴 제목은 잘린다", async () => {
    const long = "가".repeat(40);
    mocked.get.mockImplementation(async (path: string) => (path === "/api/inbox/counts" ? counts : path.startsWith("/api/inbox/pending") ? [item({ id: 1, title: long })] : []));
    mocked.post.mockResolvedValue({ results: [{ docId: 1, ok: false, message: "이미 처리된 문서입니다" }, { docId: 99, ok: false, message: "권한이 없습니다" }] });
    at("/docs");
    fireEvent.click(await screen.findByLabelText(`${long} 선택`));
    fireEvent.click(screen.getByRole("button", { name: "선택한 1건 일괄 승인" }));
    expect(await screen.findByText(`0건을 승인했습니다. 2건은 처리하지 못했습니다: 구매팀-2026-0001 ${"가".repeat(30)}…: 이미 처리된 문서입니다 / #99: 권한이 없습니다`)).toBeInTheDocument();
  });

  it("수신함: 접수 대기 건에 접수·반송 버튼이 있고 반송은 사유 없이는 못 한다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path === "/api/inbox/counts" ? counts : path.startsWith("/api/inbox/receive") ? [item({ receiptId: 9, receiptStatus: "pending", receiptUnit: "영업1팀" })] : []));
    mocked.post.mockResolvedValue({});
    at("/docs?box=receive");
    expect(await screen.findByText("영업1팀 · 접수 대기")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "반송" }));
    expect(screen.getByRole("button", { name: "반송하기" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("반송 사유(필수)"), { target: { value: "관할이 아님" } });
    fireEvent.click(screen.getByRole("button", { name: "반송하기" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/receipts/9/return", { reason: "관할이 아님" }));
  });

  it("상신 취소는 사유를 받아 요청하고 서버 거부 문구를 그대로 보인다", async () => {
    mocked.post.mockRejectedValueOnce(Object.assign(new (await import("../api/client")).ApiError(409, "이미 결재가 시작되어 상신을 취소할 수 없습니다."), {}));
    const done = vi.fn();
    render(<CancelSubmit path="/api/approval-docs/5/cancel-submit" onDone={done} />);
    fireEvent.click(screen.getByRole("button", { name: "상신 취소" }));
    expect(screen.getByRole("button", { name: "상신 취소하기" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("취소 사유(필수)"), { target: { value: "내용 수정" } });
    fireEvent.click(screen.getByRole("button", { name: "상신 취소하기" }));
    expect(await screen.findByText("이미 결재가 시작되어 상신을 취소할 수 없습니다.")).toBeInTheDocument();
    expect(mocked.post).toHaveBeenCalledWith("/api/approval-docs/5/cancel-submit", { reason: "내용 수정" });
    expect(done).not.toHaveBeenCalled();
  });
});

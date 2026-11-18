import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import type { PurchaseRequest } from "../purchase/types";
import { DocView } from "./DocView";
import type { DocReceipt } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }, apiFetch: vi.fn() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };
const other: CurrentUser = { id: 9, name: "다른직원", role: "staff", departmentId: 1 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const doc = (over: Partial<PurchaseRequest> = {}): PurchaseRequest =>
  ({
    id: 5, no: "구매팀-2026-0001", requesterId: 1, requesterName: "김직원", requesterUnitPath: "구매팀", title: "멈춘 문서", status: "in_review", formName: "일반 기안서", formKind: "general",
    createdAt: "2026-11-10T08:00:00Z", lines: [], recipients: [], attachments: [], formFields: [], fields: {}, noApprover: true,
    steps: [{ seq: 1, kind: "team_lead", approverId: 2, status: "approved", comment: "", decidedAt: "2026-11-10T09:00:00Z", title: "팀장", assigneeName: "구매팀장", role: "approve", stage: 1 },
      { seq: 2, kind: "dept_head", approverId: 0, status: "pending", comment: "", decidedAt: "", title: "부서장", assigneeName: "", role: "approve", stage: 2 }],
    ...over,
  }) as unknown as PurchaseRequest;

const receipt = (over: Partial<DocReceipt> = {}): DocReceipt => ({ id: 3, docId: 5, docNo: "구매팀-2026-0001", docTitle: "멈춘 문서", unitId: 2, unitName: "영업1팀", status: "pending", receivedBy: "", receivedAt: "", assigneeId: 0, assigneeName: "", doneBy: "", doneAt: "", returnedBy: "", returnedAt: "", comment: "", reason: "", canHandle: false, ...over });

function at(user: CurrentUser, d: PurchaseRequest, receipts: DocReceipt[] = []) {
  mocked.get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/approval-docs/")) return d;
    if (path.includes("/history")) return [];
    if (path.startsWith("/api/receipts")) return receipts;
    return { root: { id: 1, name: "회사", children: [] } };
  });
  mocked.post.mockResolvedValue({});
  return render(
    <MemoryRouter initialEntries={["/docs/5"]}>
      <AuthProvider initialUser={user}>
        <Routes>
          <Route path="/docs/:id" element={<DocView />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("B23 검증 수정 화면", () => {
  it("결재자 없음 문서: 기안자에게 안내와 회수 버튼이 보이고 누르면 회수 요청을 보낸다", async () => {
    at(staff, doc());
    expect(await screen.findByText(/결재자 없음: 지금 차례 단계를 결재할 수 있는 사람이 없어/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "회수(임시저장으로 되돌리기)" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/approval-docs/5/withdraw"));
    expect(await screen.findByText(/회수했습니다/)).toBeInTheDocument();
  });

  it("결재자 없음 안내는 기안자가 아니면 회수 버튼 없이 안내만 보인다", async () => {
    at(other, doc());
    expect(await screen.findByText(/결재자 없음: 지금 차례 단계를 결재할 수 있는 사람이 없어/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "회수(임시저장으로 되돌리기)" })).toBeNull();
  });

  it("수신 건: 처리 권한이 없으면 접수·반송 버튼이 없고, 권한이 있으면 보인다", async () => {
    const r1 = at(staff, doc({ noApprover: false }), [receipt({ canHandle: false })]);
    expect(await screen.findByText("영업1팀")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "접수" })).toBeNull();
    expect(screen.queryByRole("button", { name: "반송" })).toBeNull();
    r1.unmount();
    at(staff, doc({ noApprover: false }), [receipt({ canHandle: true })]);
    expect(await screen.findByRole("button", { name: "접수" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "반송" })).toBeInTheDocument();
  });
});

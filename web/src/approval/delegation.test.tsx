import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import type { Delegation } from "../purchase/types";
import { ApprovalInbox } from "./ApprovalInbox";
import { AbsencePage, delegationStatus } from "./DelegationPanel";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;

const manager: CurrentUser = { id: 2, name: "김팀장", role: "manager", departmentId: 1 };
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

const del = (over: Partial<Delegation> = {}): Delegation => ({
  id: 1, delegatorId: 2, delegatorName: "김팀장", delegateId: 3, delegateName: "이부서장",
  from: "2026-11-10T00:00:00Z", to: "2026-11-20T00:00:00Z", revokedAt: null, createdAt: "2026-11-09T00:00:00Z", ...over,
});

function mockApi(given: Delegation[], received: Delegation[], pending: unknown[] = []) {
  mocked.get.mockImplementation(async (path: string) => {
    if (path === "/api/approvals/pending") return pending;
    if (path === "/api/approvals/delegations") return { given, received };
    if (path === "/api/approvals/delegations/candidates")
      return [{ id: 3, name: "이부서장", role: "manager", position: "dept_head", departmentId: 1 }];
    throw new Error(path);
  });
}

function show(user: CurrentUser, page: "inbox" | "absence" = "inbox") {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>{page === "inbox" ? <ApprovalInbox /> : <AbsencePage />}</AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

describe("부재·대결 설정 화면", () => {
  it("위임 상태를 한국어로 보인다", () => {
    const now = new Date("2026-11-15T00:00:00Z").getTime();
    expect(delegationStatus(del(), now)).toBe("진행 중");
    expect(delegationStatus(del(), new Date("2026-11-01T00:00:00Z").getTime())).toBe("예정");
    expect(delegationStatus(del(), new Date("2026-12-01T00:00:00Z").getTime())).toBe("종료");
    expect(delegationStatus(del({ revokedAt: "2026-11-12T00:00:00Z" }), now)).toBe("해제됨");
  });

  it("결재자는 위임을 지정하고 해제한다", async () => {
    mockApi([del()], []);
    mocked.post.mockResolvedValue(del());
    mocked.del.mockResolvedValue(undefined);
    show(manager, "absence");
    expect(await screen.findByText("내가 맡긴 부재·위임")).toBeInTheDocument();
    expect(screen.queryByText(/delegat/i)).toBeNull();

    fireEvent.click(await screen.findByRole("button", { name: "부재 등록" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("부재 사유를 고르세요.");

    fireEvent.change(await screen.findByLabelText("부재 사유"), { target: { value: "vacation" } });
    fireEvent.change(await screen.findByLabelText("대결자"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("부재 시작"), { target: { value: "2026-11-10T09:00" } });
    fireEvent.change(screen.getByLabelText("부재 종료"), { target: { value: "2026-11-12T09:00" } });
    fireEvent.click(screen.getByRole("button", { name: "부재 등록" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [path, body] = mocked.post.mock.calls[0] as [string, { delegateId: number; from: string; to: string; kind: string; reason: string }];
    expect(path).toBe("/api/approvals/delegations");
    expect(body.delegateId).toBe(3);
    expect(body.kind).toBe("absence");
    expect(body.reason).toBe("vacation");
    expect(body.from).toMatch(/Z$/);

    // 등록 뒤 목록을 다시 불러오는 동안은 버튼이 잠겨 있다. 풀린 뒤에 누른다.
    await waitFor(() => expect(screen.getByRole("button", { name: "해제" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "해제" }));
    await waitFor(() => expect(mocked.del).toHaveBeenCalledWith("/api/approvals/delegations/1"));
  });

  it("종료 시각이 시작보다 앞이면 요청하지 않는다", async () => {
    mockApi([], []);
    show(manager, "absence");
    fireEvent.change(await screen.findByLabelText("부재 사유"), { target: { value: "trip" } });
    fireEvent.change(await screen.findByLabelText("대결자"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("부재 시작"), { target: { value: "2026-11-12T09:00" } });
    fireEvent.change(screen.getByLabelText("부재 종료"), { target: { value: "2026-11-10T09:00" } });
    fireEvent.click(screen.getByRole("button", { name: "부재 등록" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("부재 기간이 올바르지 않습니다");
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("위임받은 결재 건에 위임자가 표시된다", async () => {
    mockApi([], [del({ delegatorId: 4, delegatorName: "박팀장", delegateId: 2, delegateName: "김팀장" })], [
      {
        id: 9, no: "PR-000009", requesterId: 5, requesterName: "최직원", departmentId: 2, vendorId: 1, title: "토너", reason: "",
        status: "submitted", totalAmount: 100000, rejectReason: "", lines: [], purchaseOrderId: 0, createdAt: "", updatedAt: "",
        steps: [{ seq: 1, kind: "team_lead", approverId: 0, status: "pending", comment: "", decidedAt: "" }],
        delegatedFromId: 4, delegatedFromName: "박팀장",
      },
    ]);
    show(manager);
    expect(await screen.findByText(/원결재자 박팀장/)).toBeInTheDocument();
    expect(screen.getByText("대결")).toBeInTheDocument();
  });

  it("내가 부재 중이면 결재함에 '부재 중' 안내와 대결자가 보인다(B29)", async () => {
    const past = new Date(Date.now() - 3600_000).toISOString();
    const future = new Date(Date.now() + 86400_000).toISOString();
    mockApi([del({ kind: "absence", reason: "vacation", from: past, to: future })], []);
    show(manager);
    expect(await screen.findByText("부재 중")).toBeInTheDocument();
    expect(screen.getByText(/대결자 이부서장/)).toBeInTheDocument();
  });

  it("직원에게는 위임 영역이 그려지지 않는다", async () => {
    mockApi([], []);
    show(staff);
    expect(await screen.findByText("결재할 건이 없습니다.")).toBeInTheDocument();
    expect(screen.queryByText(/부재·대결 설정/)).toBeNull();
    expect(mocked.get).not.toHaveBeenCalledWith("/api/approvals/delegations");
  });
});

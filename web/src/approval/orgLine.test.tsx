import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "../auth/AuthProvider";
import { PurchaseForm } from "../purchase/PurchaseForm";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { NotificationCard } from "../notification/NotificationCard";
import { NotificationsPage } from "../notification/NotificationsPage";
import { LinePreviewCard } from "../purchase/LinePreviewCard";
import { PurchaseView } from "../purchase/PurchaseView";
import type { LinePreview, PurchaseRequest } from "../purchase/types";
import { ReassignDialog } from "./ReassignDialog";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const step = (o: Partial<LinePreview["steps"][number]>): LinePreview["steps"][number] => ({
  order: 1, kind: "team_lead", title: "팀장", unitId: 11, unitName: "구매1팀", unitPath: "구매팀 > 구매1팀", approverId: 2, approverName: "구매팀장",
  acting: false, raisedFrom: "", raiseReason: "", raisedNote: "", layerNote: "", omitted: false, missing: false, reason: "", poolSize: 0, ...o,
});

function preview(o: Partial<LinePreview>): LinePreview {
  return { requesterId: 1, requesterName: "직원", unitPath: "구매팀 > 구매1팀", asOf: "2026-11-11", steps: [step({})], complete: true, problems: [], notes: [], ...o };
}

describe("결재선 미리보기 카드", () => {
  it("금액이 없으면 안내만 보이고 서버를 부르지 않는다", () => {
    const onReady = vi.fn();
    render(<LinePreviewCard amount={0} onReady={onReady} />);
    expect(screen.getByText("금액을 입력하면 결재선이 나타납니다.")).toBeTruthy();
    expect(mocked.get).not.toHaveBeenCalled();
    expect(onReady).toHaveBeenCalledWith(null);
  });

  it("결재선 표(단계·직책·결재자·소속 조직·비고)와 올림·직무대행 표시를 보인다", async () => {
    mocked.get.mockResolvedValue(
      preview({
        steps: [
          step({ approverName: "구매부서장", title: "팀장", unitName: "구매팀", unitPath: "구매팀", raisedNote: "요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다." }),
          step({ order: 2, kind: "dept_head", title: "부서장", approverName: "윤대행", acting: true, unitName: "영업팀", unitPath: "영업팀" }),
          step({ order: 3, kind: "finance", title: "재무", unitName: "", unitPath: "", approverId: 0, approverName: "", poolSize: 2 }),
        ],
        notes: ["1단계: 요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다."],
      }),
    );
    const onReady = vi.fn();
    render(<LinePreviewCard amount={2_000_000} onReady={onReady} />);
    const table = await screen.findByRole("table");
    const heads = within(table).getAllByRole("columnheader").map((h) => h.textContent);
    expect(heads).toEqual(["단계", "직책", "결재자", "소속 조직", "비고"]);
    for (const r of within(table).getAllByRole("row").slice(1)) expect(within(r).getAllByRole("cell")).toHaveLength(5);
    expect(screen.getAllByText(/요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다\./).length).toBeGreaterThan(0);
    expect(screen.getByText("직무대행")).toBeTruthy();
    expect(screen.getByText("재무 담당(2명)")).toBeTruthy();
    expect(mocked.get).toHaveBeenCalledWith("/api/approvals/line-preview?amount=2000000");
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(true));
    expect(document.body.textContent).not.toMatch(/team_lead|dept_head|division_head|ceo|finance/);
  });

  it("결재선을 만들 수 없으면 단계별 사유와 상신 불가 안내를 오류로 보이고 상신 불가를 알린다", async () => {
    mocked.get.mockResolvedValue(
      preview({ steps: [step({ missing: true, approverName: "", unitName: "", unitPath: "" })], complete: false, problems: ["1단계(팀장): 위로 올라가도 맡을 결재자를 찾지 못했습니다(구매팀 > 구매1팀까지 확인). 관리자에게 조직장 보임을 요청하세요."] }),
    );
    const onReady = vi.fn();
    render(<LinePreviewCard amount={500_000} onReady={onReady} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("이 상태로는 상신할 수 없습니다. 임시저장은 할 수 있습니다.");
    expect(alert.textContent).toContain("1단계(팀장): 위로 올라가도 맡을 결재자를 찾지 못했습니다");
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(false));
  });

  it("조회가 실패하면 안내만 보이고 상신은 막지 않는다(서버가 다시 확인)", async () => {
    mocked.get.mockRejectedValue(new Error("boom"));
    const onReady = vi.fn();
    render(<LinePreviewCard amount={500_000} onReady={onReady} />);
    expect(await screen.findByText(/결재선을 불러오지 못했습니다/)).toBeTruthy();
    await waitFor(() => expect(onReady).toHaveBeenLastCalledWith(null));
  });
});

function pr(o: Partial<PurchaseRequest> = {}): PurchaseRequest {
  return {
    id: 7, no: "PR-000007", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 1, title: "복사용지 구매", reason: "", status: "in_review",
    totalAmount: 2_000_000, rejectReason: "", lines: [], purchaseOrderId: 0, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z",
    lineAsOf: "2026-11-10", requesterUnitPath: "구매팀 > 구매1팀", round: 1,
    steps: [
      { seq: 1, kind: "team_lead", approverId: 2, status: "approved", comment: "승인합니다", decidedAt: "2026-11-10T01:00:00Z", title: "팀장", unitPath: "구매팀 > 구매1팀", assigneeId: 2, assigneeName: "구매팀장" },
      { seq: 2, kind: "dept_head", approverId: 0, status: "pending", comment: "", decidedAt: "", title: "부서장", unitPath: "구매팀", assigneeId: 3, assigneeName: "새부서장", reassignedFrom: "구매부서장", headChangedTo: "공석" },
      { seq: 3, kind: "finance", approverId: 0, status: "waiting", comment: "", decidedAt: "", title: "재무", unitPath: "" },
    ],
    ...o,
  };
}

describe("구매요청 결재선 표(상신 당시 조직)", () => {
  it("상신 당시 조직·결재자 이름·직책·소속·상태가 보이고 영문 코드는 보이지 않는다", () => {
    render(<PurchaseView pr={pr()} history={[]} />);
    expect(screen.getByText(/상신 당시 조직: 구매팀 > 구매1팀 \(기준일 2026\. 11\. 10\.\)/)).toBeTruthy();
    const table = screen.getAllByRole("table")[1];
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["단계", "직책", "결재자", "소속 조직", "상태", "처리 시각", "의견"]);
    for (const r of within(table).getAllByRole("row").slice(1)) expect(within(r).getAllByRole("cell")).toHaveLength(7);
    expect(screen.getByText(/결재자 변경: 구매부서장 → 새부서장/)).toBeTruthy();
    expect(screen.getByText("재무 담당")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/team_lead|dept_head|division_head|finance|in_review/);
  });

  it("조직장이 상신 당시와 다르면 안내 상자를 보인다(공석 포함)", () => {
    render(<PurchaseView pr={pr()} history={[]} />);
    expect(screen.getByText(/상신 당시 부서장은 새부서장님이었고 지금은 공석입니다\./)).toBeTruthy();
    expect(screen.getByText(/결재는 상신 당시 결재자가 하며, 필요하면 결재자가 위임할 수 있습니다\./)).toBeTruthy();
  });

  it("조직도 도입 이전 건은 기록 없음 문구를 보인다", () => {
    render(<PurchaseView pr={pr({ lineAsOf: "", requesterUnitPath: "" })} history={[]} />);
    expect(screen.getByText("상신 당시 조직 기록이 없습니다(조직도 도입 이전 건).")).toBeTruthy();
  });

  it("결재자 변경 버튼은 onReassign 이 있을 때 끝나지 않은 직책 단계에만, 재무 단계는 안내 문구", () => {
    const onReassign = vi.fn();
    render(<PurchaseView pr={pr()} history={[]} onReassign={onReassign} />);
    const buttons = screen.getAllByRole("button", { name: "결재자 변경" });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    expect(onReassign).toHaveBeenCalledWith(expect.objectContaining({ seq: 2 }));
    expect(screen.getByText("재무 단계는 재무 담당 역할을 가진 사람이 맡습니다.")).toBeTruthy();
  });

  it("권한이 없으면(onReassign 없음) 결재자 변경 버튼이 없다", () => {
    render(<PurchaseView pr={pr()} history={[]} />);
    expect(screen.queryByRole("button", { name: "결재자 변경" })).toBeNull();
  });

  it("결재 이력의 결재자 변경 동작은 한국어로 보인다", () => {
    render(
      <PurchaseView
        pr={pr()}
        history={[{ id: 9, prId: 7, seq: 2, actorId: 7, actorName: "관리자", action: "reassign", comment: "결재자 변경: 구매부서장님 → 새부서장님 (구분: 퇴사) 사유: 퇴사", at: "2026-11-10T02:00:00Z" }]}
      />,
    );
    const hist = screen.getAllByRole("table")[2];
    expect(within(hist).getByText("결재자 변경")).toBeTruthy();
    expect(within(hist).getByText(/구분: 퇴사/)).toBeTruthy();
  });
});

describe("결재자 변경 대화상자", () => {
  const target = { prId: 7, seq: 2, title: "부서장", currentName: "구매부서장", currentUnit: "구매팀", stalled: false };
  const cands = [{ userId: 9, name: "윤영업", title: "부서장", acting: false, unitName: "영업팀", unitPath: "영업팀" }];

  it("재직 중 결재자 경고와 확인 체크 전에는 실행 버튼이 꺼져 있고 후보·구분·사유를 모두 채워야 켜진다", async () => {
    mocked.get.mockResolvedValue(cands);
    render(<ReassignDialog target={target} onClose={() => {}} onDone={() => {}} />);
    expect(screen.getByText(/현재 결재자 구매부서장님은 재직 중이며 결재할 수 있습니다/)).toBeTruthy();
    await screen.findByRole("option", { name: /윤영업 · 부서장 · 영업팀/ });
    fireEvent.change(screen.getByLabelText("새 결재자"), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText("구분"), { target: { value: "absence" } });
    fireEvent.change(screen.getByLabelText("사유"), { target: { value: "장기 부재" } });
    const run = screen.getByRole("button", { name: "결재자 변경" }) as HTMLButtonElement;
    expect(run.disabled).toBe(true);
    fireEvent.click(screen.getByLabelText("그래도 바꿉니다"));
    expect(run.disabled).toBe(false);
  });

  it("멈춘 단계(결재자 없음)는 경고 없이 바로 바꿀 수 있고 성공 시 안내를 넘긴다", async () => {
    mocked.get.mockResolvedValue(cands);
    mocked.post.mockResolvedValue({});
    const onDone = vi.fn();
    render(<ReassignDialog target={{ ...target, stalled: true }} onClose={() => {}} onDone={onDone} />);
    expect(screen.queryByText(/재직 중이며 결재할 수 있습니다/)).toBeNull();
    await screen.findByRole("option", { name: /윤영업/ });
    fireEvent.change(screen.getByLabelText("새 결재자"), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText("구분"), { target: { value: "leave" } });
    fireEvent.change(screen.getByLabelText("사유"), { target: { value: "퇴사" } });
    fireEvent.click(screen.getByRole("button", { name: "결재자 변경" }));
    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/api/approvals/7/steps/2/reassign", { userId: 9, cause: "leave", reason: "퇴사", confirmActive: false });
    expect(onDone.mock.calls[0][0]).toBe("결재자를 구매부서장님에서 윤영업님으로 바꿨습니다. 요청자와 새 결재자에게 알렸습니다.");
  });

  it("후보가 없으면 조직장 보임 안내, 서버 오류 문구는 그대로 보인다", async () => {
    mocked.get.mockResolvedValue([]);
    mocked.post.mockRejectedValue(Object.assign(new Error("이미 결재가 끝난 단계는 결재자를 바꿀 수 없습니다"), { status: 409 }));
    render(<ReassignDialog target={{ ...target, stalled: true }} onClose={() => {}} onDone={() => {}} />);
    expect(await screen.findByText(/이 단계를 맡을 수 있는 조직장이 없습니다/)).toBeTruthy();
  });
});

describe("알림", () => {
  const items = [
    { id: 2, title: "결재자로 지정되었습니다", body: "「복사용지」 건의 2단계 결재자로 지정되었습니다. 지금 결재 차례입니다.", link: "/approvals/7", createdAt: "2026-11-10T02:00:00Z", readAt: null },
    { id: 1, title: "결재자가 바뀌었습니다", body: "바뀌었습니다", link: "/purchase-requests/7", createdAt: "2026-11-10T01:00:00Z", readAt: "2026-11-10T03:00:00Z" },
  ];

  it("알림 화면: 안 읽음 배지와 읽음 버튼, 비었을 때 문구, 종류 코드 미노출", async () => {
    mocked.get.mockResolvedValue(items);
    mocked.post.mockResolvedValue({ ok: true });
    render(
      <MemoryRouter>
        <NotificationsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("안 읽음")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "읽음" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "읽음" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/notifications/2/read"));
    expect(document.body.textContent).not.toMatch(/approval_reassigned|approval\./);
  });

  it("알림이 없으면 문구를 보이고, 안 읽음만 필터는 다른 문구", async () => {
    mocked.get.mockResolvedValue([]);
    render(
      <MemoryRouter>
        <NotificationsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("알림이 없습니다.")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("안 읽음만"));
    expect(await screen.findByText("안 읽은 알림이 없습니다.")).toBeTruthy();
  });

  it("홈 카드: 안 읽은 건수와 최근 알림, 없으면 새 알림이 없습니다", async () => {
    mocked.get.mockImplementation((path: string) => Promise.resolve(path.includes("unread-count") ? { unread: 2 } : items.slice(0, 1)));
    const { unmount } = render(
      <MemoryRouter>
        <NotificationCard />
      </MemoryRouter>,
    );
    expect(await screen.findByText("2건")).toBeTruthy();
    expect(screen.getByText("결재자로 지정되었습니다")).toBeTruthy();
    unmount();
    mocked.get.mockImplementation((path: string) => Promise.resolve(path.includes("unread-count") ? { unread: 0 } : []));
    render(
      <MemoryRouter>
        <NotificationCard />
      </MemoryRouter>,
    );
    expect(await screen.findByText("새 알림이 없습니다.")).toBeTruthy();
  });
});

describe("구매요청 작성 화면의 결재선 미리보기 연동(AC-60·62)", () => {
  function renderForm() {
    mocked.get.mockImplementation((path: string) => {
      if (path.startsWith("/api/items")) return Promise.resolve([{ id: 1, sku: "BOLT", name: "볼트", unit: "EA", safetyStock: 0, trackLot: false }]);
      if (path.startsWith("/api/vendors")) return Promise.resolve([{ id: 1, name: "한빛상사" }]);
      if (path.startsWith("/api/approvals/line-preview")) return Promise.resolve(linePreviewFor(path));
      if (path.startsWith("/api/budgets/check")) return Promise.resolve({ month: "2026-11", departmentId: 1, departmentName: "구매팀", registered: false, budget: 0, used: 0, inProgress: 0, balance: 0, amount: 0, over: 0, after: 0, state: "unregistered" });
      return Promise.resolve({ hits: [] });
    });
    return render(
      <MemoryRouter initialEntries={["/purchase-requests/new"]}>
        <AuthProvider initialUser={{ id: 1, name: "김직원", role: "staff", departmentId: 1 }}>
          <Routes>
            <Route path="/purchase-requests/new" element={<PurchaseForm />} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
  }
  let incomplete = true;
  function linePreviewFor(_path: string): LinePreview {
    return incomplete
      ? preview({ steps: [step({ missing: true, approverName: "", unitName: "", unitPath: "" })], complete: false, problems: ["1단계(팀장): 시험 사유"] })
      : preview({ complete: true });
  }

  it("금액을 넣으면 미리보기가 갱신되고, 결재선을 만들 수 없으면 '저장 후 상신' 이 꺼진다", async () => {
    incomplete = true;
    renderForm();
    await screen.findByRole("option", { name: /볼트/ });
    fireEvent.change(screen.getByLabelText("1번째 줄 품목"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("1번째 줄 단가"), { target: { value: "500000" } });
    expect(screen.getByText("금액에 따라 결재선이 정해집니다.", { exact: false })).toBeTruthy();
    expect(await screen.findByText(/1단계\(팀장\): 시험 사유/)).toBeTruthy();
    await waitFor(() => expect((screen.getByRole("button", { name: "저장 후 상신" }) as HTMLButtonElement).disabled).toBe(true));
    expect(mocked.get).toHaveBeenCalledWith("/api/approvals/line-preview?amount=500000");
    // 임시저장 버튼은 막지 않는다.
    expect((screen.getByRole("button", { name: "임시저장" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("결재선을 만들 수 있으면 '저장 후 상신' 이 켜진다", async () => {
    incomplete = false;
    renderForm();
    await screen.findByRole("option", { name: /볼트/ });
    fireEvent.change(screen.getByLabelText("1번째 줄 품목"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("1번째 줄 단가"), { target: { value: "500000" } });
    await screen.findByRole("row", { name: /1 팀장 구매팀장/ });
    expect((screen.getByRole("button", { name: "저장 후 상신" }) as HTMLButtonElement).disabled).toBe(false);
  });
});

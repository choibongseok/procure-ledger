// 디자인 인계(design/handoff/2026-11-11.md 2절) 후보 1·2·3·6: 위임 결재 안내, 발주 안내와 입고 링크, 끝난 단계, 거래처 평가 문구.
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ApprovalDecision } from "../approval/ApprovalDecision";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ActingNotice, actingNoticeText } from "../approval/ActingNotice";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { OrderNotice } from "./OrderNotice";
import { PurchaseView, actorText } from "./PurchaseView";
import type { ApprovalStep, PurchaseRequest } from "./types";
import { VendorRatingPage } from "./VendorRatingPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const finance: CurrentUser = { id: 4, name: "재무 담당", role: "finance", departmentId: 1 };
const staff: CurrentUser = { id: 1, name: "구매직원", role: "staff", departmentId: 1 };

beforeEach(() => {
  get.mockReset();
});

describe("후보 1: 위임 결재 안내 상자", () => {
  const info = { delegatorId: 5, delegatorName: "구매부서장", from: "2026-11-10T00:00:00Z", to: "2026-11-14T09:00:00Z" };

  it("안내 문구는 아트보드 모양이고, 이력에 남는 처리자 문구와 같은 글자를 인용한다", () => {
    const text = actingNoticeText(info, "정물류");
    expect(text).toContain("위임 결재: 구매부서장 님을 대신해 결재합니다. 위임 기간 ");
    expect(text).toContain(" ~ ");
    // 결재 이력 표의 처리자 칸 문구(actorText)와 같은 값을 따옴표로 인용한다.
    const history = actorText({ id: 1, prId: 1, seq: 1, actorId: 9, actorName: "정물류", onBehalfOfId: 5, onBehalfOfName: "구매부서장", action: "approve", comment: "", at: "" });
    expect(history).toBe("정물류 (대결: 원결재자 구매부서장)");
    expect(text).toContain(`결재 이력에는 '${history}'으로 남습니다.`);
  });

  it("위임으로 결재하는 경우에만 상자를 그린다", async () => {
    get.mockResolvedValue(info);
    const { unmount } = render(<ActingNotice prId={7} myName="정물류" version="a" />);
    expect(await screen.findByText(/위임 결재: 구매부서장 님을 대신해 결재합니다/)).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith("/api/approvals/7/acting");
    unmount();

    get.mockReset();
    get.mockResolvedValue(null);
    const { container } = render(<ActingNotice prId={7} myName="정물류" version="a" />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("조회에 실패하면 빈 상자를 그리지 않는다", async () => {
    get.mockRejectedValue(new Error("x"));
    const { container } = render(<ActingNotice prId={7} myName="정물류" version="a" />);
    await act(async () => {});
    expect(container.textContent).toBe("");
  });
});

describe("후보 1: 결재 화면 배치", () => {
  const prBody = {
    id: 46, no: "PR-000046", requesterId: 1, requesterName: "구매직원", departmentId: 1, vendorId: 1, title: "사무용 노트북 교체", reason: "",
    status: "in_review", totalAmount: 12500000, rejectReason: "", lines: [], steps: [{ seq: 1, kind: "dept_head", status: "pending", approverId: 5, comment: "", decidedAt: "" }],
    purchaseOrderId: 0, createdAt: "", updatedAt: "2026-11-10T00:00:00Z",
  };
  function showDecision(acting: unknown) {
    get.mockImplementation(async (path: string) => {
      if (path.endsWith("/acting")) return acting;
      if (path.endsWith("/history")) return [];
      if (path.endsWith("/quotes")) throw new Error("none");
      return prBody;
    });
    return render(
      <MemoryRouter initialEntries={["/approvals/46"]}>
        <AuthProvider initialUser={{ id: 9, name: "정물류", role: "manager", departmentId: 2 }}>
          <Routes>
            <Route path="/approvals/:id" element={<ApprovalDecision />} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
  }

  it("위임이면 제목 위(뒤로가기 줄 아래)에 안내 상자가 먼저 나온다", async () => {
    showDecision({ delegatorId: 5, delegatorName: "구매부서장", from: "2026-11-10T00:00:00Z", to: "2026-11-14T09:00:00Z" });
    const box = await screen.findByText(/위임 결재: 구매부서장 님을 대신해 결재합니다/);
    const title = await screen.findByRole("heading", { level: 1 });
    expect(box.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(box.textContent).toContain("'정물류 (대결: 원결재자 구매부서장)'으로 남습니다.");
  });

  it("위임이 아니면 안내 상자가 없다", async () => {
    showDecision(null);
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByText(/위임 결재:/)).toBeNull();
  });
});

describe("후보 2: 발주 안내와 입고 화면 링크", () => {
  function show(user: CurrentUser) {
    get.mockResolvedValue({ poNo: "PO-000038", poDate: "2026-11-04T01:00:00Z" });
    return render(
      <MemoryRouter>
        <AuthProvider initialUser={user}>
          <OrderNotice prId={41} purchaseOrderId={38} />
        </AuthProvider>
      </MemoryRouter>,
    );
  }

  it("입고 권한이 있으면 발주 번호·발주일과 해당 발주가 선택되는 입고 링크를 보인다", async () => {
    show(finance);
    const note = await screen.findByText(/발주 PO-000038 · 발주일 2026\. 11\. 4\. · /);
    expect(note.textContent).toContain("발주가 생성되었습니다. 입고 화면에서 입고를 처리할 수 있습니다.");
    expect(within(note).getByRole("link", { name: "입고 화면으로" })).toHaveAttribute("href", "/receipts?po=38");
    expect(within(note).getByRole("link", { name: "발주서" })).toHaveAttribute("href", "/purchase-requests/41/po");
  });

  it("입고 권한이 없으면 링크를 숨기고 번호·발주일만 보인다(발주서 링크는 유지)", async () => {
    show(staff);
    const note = await screen.findByText(/발주 PO-000038 · 발주일 2026\. 11\. 4\./);
    expect(within(note).queryByRole("link", { name: "입고 화면으로" })).toBeNull();
    expect(within(note).getByRole("link", { name: "발주서" })).toBeInTheDocument();
  });
});

describe("후보 3: 결재선 표의 끝난 단계", () => {
  const pr = (steps: Partial<ApprovalStep>[]): PurchaseRequest =>
    ({
      id: 39, no: "PR-000039", requesterId: 1, requesterName: "구매직원", departmentId: 1, vendorId: 1, title: "지게차", reason: "", status: "in_review",
      totalAmount: 104000000, rejectReason: "", lines: [], purchaseOrderId: 0, createdAt: "", updatedAt: "",
      steps: steps.map((s, i) => ({ seq: i + 1, kind: "team_lead", approverId: 2, status: "approved", comment: "", decidedAt: "", ...s })),
    }) as PurchaseRequest;

  it("끝난 단계·결재 차례·재무 단계가 각자 다른 칸 내용을 보인다", () => {
    render(
      <PurchaseView
        pr={pr([
          { kind: "team_lead", status: "approved" },
          { kind: "dept_head", status: "rejected" },
          { kind: "division_head", status: "pending", assigneeName: "최본부" },
          { kind: "finance", status: "waiting" },
        ])}
        history={[]}
        onReassign={() => undefined}
      />,
    );
    const rows = within(screen.getAllByRole("table")[1]).getAllByRole("row").slice(1);
    const last = (i: number) => within(rows[i]).getAllByRole("cell").at(-1) as HTMLElement;
    expect(last(0).textContent).toBe("끝난 단계");
    expect(last(1).textContent).toBe("끝난 단계");
    expect(within(last(2)).getByRole("button", { name: "결재자 변경" })).toBeInTheDocument();
    expect(last(2).textContent).not.toContain("끝난 단계");
    expect(last(3).textContent).toBe("재무 단계는 재무 담당 역할을 가진 사람이 맡습니다.");
  });

  it("재지정 열이 없는 화면(onReassign 없음)에는 끝난 단계 글자를 더하지 않는다", () => {
    render(<PurchaseView pr={pr([{ status: "approved" }])} history={[]} />);
    expect(screen.queryByText("끝난 단계")).toBeNull();
  });
});

describe("후보 6: 거래처 평가 문구", () => {
  const row = (o: object) => ({ vendorId: 1, vendorName: "한빛상사", orders: 4, onTime: 4, withDue: 4, noDue: 0, onTimePct: 100, onTimeBadge: "good", verdicts: 0, mismatches: 0, mismatchPct: -1, mismatchBadge: "none", lateOpen: 0, ...o });
  const render1 = () =>
    render(
      <MemoryRouter>
        <VendorRatingPage />
      </MemoryRouter>,
    );

  it("도움말에 확정 판정만 센다는 문장이 있다", () => {
    get.mockResolvedValue([]);
    render1();
    expect(screen.getByText(/이 기간에 입고가 끝난 발주와 재무가 확정한 판정만 셉니다\. 확정 전 판정은 제외합니다\./)).toBeInTheDocument();
  });

  it("판정이 0건인 거래처는 불일치 열과 비율이 '-' 이고 판정이 있으면 숫자를 보인다", async () => {
    get.mockResolvedValue([row({}), row({ vendorId: 2, vendorName: "대한오피스", verdicts: 6, mismatches: 2, mismatchPct: 33, mismatchBadge: "bad" })]);
    render1();
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    const rows = (await screen.findAllByRole("row")).slice(1);
    const cells = (i: number) => within(rows[i]).getAllByRole("cell").map((c) => c.textContent);
    // 열: 거래처, 발주, 납기 내, 준수율, 납기 평가, 증빙 판정, 불일치, 불일치 비율, 판정 평가
    expect(cells(0).slice(5, 8)).toEqual(["0", "-", "-"]);
    expect(cells(1).slice(5, 8)).toEqual(["6", "2", "33%"]);
  });

  it("빈 상태·조회 실패·기간 오류 문구가 아트보드와 같다", async () => {
    get.mockResolvedValue([]);
    const { unmount } = render1();
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("이 기간에 발주한 거래처가 없습니다.")).toBeInTheDocument();
    unmount();

    get.mockReset();
    get.mockRejectedValue(new Error("서버 내부 오류"));
    render1();
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("거래처 평가를 불러오지 못했습니다. 기간을 확인하고 다시 조회하세요.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("종료일"), { target: { value: "2000-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("종료일은 시작일 이후여야 합니다.")).toBeInTheDocument();
  });
});

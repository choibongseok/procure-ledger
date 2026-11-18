import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApprovalDecision } from "../approval/ApprovalDecision";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { validateReceipt } from "../inventory/ReceiptPage";
import { ToastProvider } from "../layout/ui";
import { StockPage } from "../inventory/StockPage";
import { PR_STATUS_LABEL, STEP_KIND_LABEL, STEP_STATUS_LABEL, HISTORY_ACTION_LABEL, PO_STATUS_LABEL, label } from "./labels";
import { PurchaseDetail } from "./PurchaseDetail";
import { PurchaseList } from "./PurchaseList";
import { validateForm } from "./PurchaseForm";
import type { PurchaseRequest } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});

const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;

const manager: CurrentUser = { id: 2, name: "김팀장", role: "manager", departmentId: 1 };
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

function pr(over: Partial<PurchaseRequest> = {}): PurchaseRequest {
  return {
    id: 7, no: "PR-000007", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 1, title: "복사용지 구매",
    reason: "", status: "submitted", totalAmount: 200000, rejectReason: "",
    lines: [{ id: 1, itemId: 1, itemName: "복사용지", quantity: 10, unitPrice: 20000, amount: 200000 }],
    steps: [{ seq: 1, kind: "team_lead", approverId: 0, status: "pending", comment: "", decidedAt: "" }],
    purchaseOrderId: 0, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z", ...over,
  };
}

function renderAt(user: CurrentUser, path: string, route: string, el: import("react").JSX.Element) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider initialUser={user}>
        <ToastProvider>
          <Routes>
            <Route path={route} element={el} />
          </Routes>
        </ToastProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

describe("상태·단계 이름", () => {
  it("모든 코드가 한국어 업무 용어로 바뀐다", () => {
    const all = [PR_STATUS_LABEL, STEP_KIND_LABEL, STEP_STATUS_LABEL, HISTORY_ACTION_LABEL, PO_STATUS_LABEL];
    for (const m of all) for (const v of Object.values(m)) expect(v).toMatch(/[가-힣]/);
    expect(PR_STATUS_LABEL.in_review).toBe("결재중");
    expect(label(HISTORY_ACTION_LABEL, "withdraw")).toBe("회수");
    expect(label(PR_STATUS_LABEL, "weird_code")).toBe("알 수 없음");
  });
});

describe("구매요청 목록", () => {
  it("상태를 한국어로 보이고 영문 코드를 노출하지 않는다", async () => {
    mocked.get.mockResolvedValue([pr({ status: "in_review" }), pr({ id: 8, no: "PR-000008", status: "ordered", title: "토너" })]);
    renderAt(staff, "/purchase-requests", "/purchase-requests", <PurchaseList />);
    expect(await screen.findByText("복사용지 구매")).toBeInTheDocument();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("결재중")).toBeInTheDocument();
    expect(table.getByText("발주완료")).toBeInTheDocument();
    expect(screen.queryByText(/in_review|ordered|submitted/)).toBeNull();
    expect(screen.getByRole("link", { name: "새 구매요청 작성" })).toBeInTheDocument();
  });
});

describe("작성 입력 검사", () => {
  const line = { itemId: "1", quantity: "2", unitPrice: "1000" };
  it("제목·품목·수량을 검사한다", () => {
    expect(validateForm("", "1", [line], false)).toMatch(/제목/);
    expect(validateForm("a", "", [line], true)).toMatch(/거래처/);
    expect(validateForm("a", "", [line], false)).toBe("");
    expect(validateForm("a", "1", [], false)).toMatch(/품목/);
    expect(validateForm("a", "1", [{ ...line, itemId: "" }], false)).toMatch(/품목/);
    expect(validateForm("a", "1", [{ ...line, quantity: "0" }], false)).toMatch(/수량/);
    expect(validateForm("a", "1", [{ ...line, unitPrice: "-1" }], false)).toMatch(/단가/);
    expect(validateForm("a", "1", [line], true)).toBe("");
  });
});

describe("결재 화면", () => {
  function setup() {
    mocked.get.mockImplementation(async (path: string) => (path.includes("/history") ? [] : path.endsWith("/acting") ? null : pr()));
    renderAt(manager, "/approvals/7", "/approvals/:id", <ApprovalDecision />);
  }

  it("반려는 사유를 써야 누를 수 있다", async () => {
    setup();
    const reject = await screen.findByRole("button", { name: "반려" });
    expect(reject).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/의견/), { target: { value: "단가 재확인" } });
    expect(reject).toBeEnabled();
  });

  it("반려하면 사유와 함께 보낸다", async () => {
    setup();
    mocked.post.mockResolvedValue({ request: pr({ status: "rejected" }), purchaseOrder: null });
    fireEvent.change(await screen.findByLabelText(/의견/), { target: { value: "단가 재확인" } });
    fireEvent.click(screen.getByRole("button", { name: "반려" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/approvals/7/decision", { approve: false, comment: "단가 재확인" }));
    expect(await screen.findByRole("status")).toHaveTextContent("반려");
  });

  it("승인 결과에 발주 번호를 보인다", async () => {
    setup();
    mocked.post.mockResolvedValue({
      request: pr({ status: "ordered" }),
      purchaseOrder: { id: 3, no: "PO-000003", prId: 7, vendorId: 1, status: "open", totalAmount: 200000, lines: [], createdAt: "" },
    });
    fireEvent.click(await screen.findByRole("button", { name: "승인" }));
    expect(await screen.findByRole("status")).toHaveTextContent("PO-000003");
    expect(screen.getByRole("status")).toHaveTextContent("발주완료");
  });

  it("서버 오류 문구를 그대로 알려 준다(자기 결재 등)", async () => {
    setup();
    const { ApiError } = await import("../api/client");
    mocked.post.mockRejectedValue(new ApiError(403, "본인이 올린 요청은 결재할 수 없습니다"));
    fireEvent.click(await screen.findByRole("button", { name: "승인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("본인이 올린 요청은 결재할 수 없습니다. 결재선의 다른 결재자에게 요청하거나 관리자에게 결재자 변경을 요청하세요.");
  });
});

describe("결재 화면 오류·발주 안내", () => {
  it("서버 오류 머리말은 떼고 업무 문구만 보인다", async () => {
    mocked.get.mockResolvedValue(pr());
    const { ApiError } = await import("../api/client");
    mocked.post.mockRejectedValue(new ApiError(403, "권한이 없습니다: 결재 권한이 필요합니다"));
    renderAt(manager, "/approvals/7", "/approvals/:id", <ApprovalDecision />);
    fireEvent.click(await screen.findByRole("button", { name: "승인" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("결재 권한이 필요합니다");
    expect(alert).not.toHaveTextContent("권한이 없습니다:");
  });
  it("발주가 생성된 건은 결재 화면에도 발주 번호·입고 링크 안내가 보인다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path.endsWith("/po") ? { poNo: "PO-000003", poDate: "2026-11-12T00:00:00Z" } : pr({ status: "ordered", purchaseOrderId: 3, steps: [] })));
    renderAt(manager, "/approvals/7", "/approvals/:id", <ApprovalDecision />);
    expect(await screen.findByText(/발주가 생성되었습니다/)).toBeInTheDocument();
    expect(await screen.findByText(/발주 PO-000003/)).toBeInTheDocument();
  });
});

describe("구매요청 상세의 수정 단추는 서버 editable 만 따른다", () => {
  function load(over: Partial<PurchaseRequest>) {
    mocked.get.mockImplementation(async (path: string) => (path === "/api/purchase-requests/7" ? pr(over) : []));
    renderAt(staff, "/purchase-requests/7", "/purchase-requests/:id", <PurchaseDetail />);
  }
  it("editable 이 true 이면 내용 수정이 보인다", async () => {
    load({ status: "draft", editable: true });
    expect(await screen.findByRole("link", { name: "내용 수정" })).toBeInTheDocument();
  });
  it("editable 이 false 이면 상태가 임시저장이어도 숨긴다", async () => {
    load({ status: "draft", editable: false });
    expect(await screen.findByText("복사용지 구매")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "내용 수정" })).toBeNull();
  });
});

describe("입고 입력 검사", () => {
  const po = {
    id: 1, no: "PO-000001", prId: 1, vendorId: 1, status: "partial" as const, totalAmount: 1000, createdAt: "",
    lines: [{ id: 10, itemId: 5, itemName: "복사용지", quantity: 10, receivedQty: 7, unitPrice: 100 }],
  };
  it("잔량 초과·로트 누락을 막는다", () => {
    const lot = () => true;
    const none = () => false;
    expect(validateReceipt(po, {}, none)).toMatch(/입고할 수량/);
    expect(validateReceipt(po, { 10: { qty: "4", lotNo: "", expiry: "" } }, none)).toMatch(/남은 수량\(3\)/);
    expect(validateReceipt(po, { 10: { qty: "3", lotNo: "", expiry: "" } }, lot)).toMatch(/로트/);
    expect(validateReceipt(po, { 10: { qty: "3", lotNo: "L1", expiry: "2027/01/01" } }, lot)).toMatch(/연-월-일\(예 2026-12-18\)/);
    expect(validateReceipt(po, { 10: { qty: "3", lotNo: "L1", expiry: "2027-01-01" } }, lot)).toBe("");
    expect(validateReceipt(po, { 10: { qty: "3", lotNo: "", expiry: "" } }, none)).toBe("");
  });
});

describe("재고 화면", () => {
  it("안전재고 미달과 유효기한 임박 목록을 한국어로 보인다", async () => {
    const item = { id: 1, sku: "P", name: "복사용지", unit: "박스", safetyStock: 50, trackLot: true };
    mocked.get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/items?")) return [item];
      if (path.startsWith("/api/stock/below-safety")) return [{ item, onHand: 10, lots: [], belowSafety: true }];
      if (path.startsWith("/api/stock/expiring")) return [{ item, lot: { lotId: 1, lotNo: "L1", expiryDate: "2026-11-15", quantity: 4 }, daysToExpire: 5 }];
      return [];
    });
    renderAt(staff, "/inventory", "/inventory", <StockPage />);
    expect(await screen.findByText("5일 남음")).toBeInTheDocument();
    expect(screen.getByText("40")).toBeInTheDocument(); // 부족 수량
    expect(screen.queryByText(/ledger|belowSafety/i)).toBeNull();
    expect(screen.queryByText("품목·거래처 등록")).toBeNull(); // 관리 권한 없음
  });
});

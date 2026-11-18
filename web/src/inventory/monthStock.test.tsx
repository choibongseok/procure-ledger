import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { MonthClosePage } from "./MonthClosePage";
import { MonthStockPage } from "./MonthStockPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;
const user = (role: CurrentUser["role"]): CurrentUser => ({ id: 4, name: "재무", role, departmentId: 3 });

const row = { itemId: 1, sku: "CL-1", itemName: "마감 시험품", warehouseId: 1, warehouseName: "본사 창고", warehouseActive: true, openQty: 6, inQty: 10, outQty: 0, transferQty: 0, adjustQty: 0, closeQty: 16 };
const valuedRow = { ...row, openAmount: 6000, inAmount: 20000, outAmount: 0, closeAmount: 26000, avgCost: 1625, delta: 20000, deltaPct: 333 };

function show(role: CurrentUser["role"], rep: unknown) {
  get.mockReset();
  get.mockImplementation((u?: string) => Promise.resolve(String(u).startsWith("/api/warehouses") ? [{ id: 1, name: "본사 창고", code: "HQ", location: "", active: true }] : rep));
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user(role)}>
        <MonthStockPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("월별 재고 현황 화면(B27)", () => {
  it("마감된 달: 금액·전월 대비·합계와 마감됨 배지를 보인다", async () => {
    show("finance", { month: "2026-11", provisional: false, version: 2, hasAmounts: true, prevClosed: true, rows: [valuedRow], total: { openQty: 6, inQty: 10, outQty: 0, transferQty: 0, adjustQty: 0, closeQty: 16, closeAmount: 26000, prevAmount: 6000, roundDiff: 3 } });
    expect(await screen.findByText("마감됨 (2차)")).toBeInTheDocument();
    expect(screen.getAllByText("26,000원").length).toBeGreaterThan(1);
    expect(screen.getByText("+20,000원 (+333%)")).toBeInTheDocument();
    expect(screen.getByText("1,625원")).toBeInTheDocument();
    expect(screen.getByText(/반올림 차이 \+3원/)).toBeInTheDocument();
  });

  it("마감 전 잠정 값이면 배지를 보이고, 금액이 없으면 금액 열이 없다", async () => {
    show("staff", { month: "2026-12", provisional: true, version: 0, hasAmounts: false, prevClosed: false, rows: [row], total: { openQty: 6, inQty: 10, outQty: 0, transferQty: 0, adjustQty: 0, closeQty: 16 } });
    expect(await screen.findByText("마감 전 잠정 값입니다")).toBeInTheDocument();
    expect(screen.queryByText("기말 금액")).toBeNull();
    expect(screen.queryByText("평균 단가")).toBeNull();
    expect(screen.queryByText(/전월이 마감되지 않아/)).toBeNull(); // 금액 권한이 없으면 전월 대비 안내도 없다
  });

  it("전월이 마감되지 않았으면 안내하고, 기록이 없으면 빈 상태를 보인다", async () => {
    show("auditor", { month: "2026-10", provisional: false, version: 1, hasAmounts: true, prevClosed: false, rows: [], total: { closeQty: 0 } });
    expect(await screen.findByText("전월이 마감되지 않아 비교할 수 없습니다.")).toBeInTheDocument();
    expect(screen.getByText(/이 조건에 맞는 재고 기록이 없습니다/)).toBeInTheDocument();
  });

  it("전체 법인 보기: 법인 열과 법인별 소계를 보이고 단가는 합산하지 않으며, 제외한 스냅샷 줄을 안내한다(B51)", async () => {
    const r1 = { ...valuedRow, entityId: 1, entityName: "본사", delta: undefined, deltaPct: undefined };
    const r2 = { ...valuedRow, entityId: 2, entityName: "에이비씨", warehouseId: 9, warehouseName: "계열 창고", closeAmount: 9000, delta: undefined, deltaPct: undefined };
    delete (r1 as Partial<typeof r1>).avgCost;
    delete (r2 as Partial<typeof r2>).avgCost;
    show("finance", {
      month: "2026-11", provisional: false, version: 1, hasAmounts: true, prevClosed: false, allEntities: true, excludedLines: 2, rows: [r1, r2],
      total: { openQty: 12, inQty: 20, outQty: 0, transferQty: 0, adjustQty: 0, closeQty: 32, closeAmount: 35000, prevAmount: 0, roundDiff: 0 },
      entityTotals: [{ entityId: 1, entityName: "본사", closeQty: 16, closeAmount: 26000 }, { entityId: 2, entityName: "에이비씨", closeQty: 16, closeAmount: 9000 }],
    });
    expect(await screen.findByText(/전체 법인 보기에서는 법인마다 단가가 달라 단가를 합산하지 않습니다/)).toBeInTheDocument();
    expect(screen.getByText(/법인을 나누기 전에 마감되어 일부 줄을 제외했습니다/)).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader", { name: "법인" })).toHaveLength(2); // 본 표와 법인별 소계 표
    expect(screen.queryByText("평균 단가")).toBeNull();
    expect(screen.getByText("법인별 소계")).toBeInTheDocument();
    expect(screen.getAllByText("에이비씨").length).toBeGreaterThan(1);
  });
});

describe("마감 해제 요청(B27)", () => {
  beforeEach(() => post.mockReset());
  const months = [{ month: "2026-11", state: "closed", version: 1, closedAt: "2026-12-02T01:00:00Z", closedBy: "재무 담당", closeAmount: 26000, closable: false, releasable: true }];

  function showClose(role: CurrentUser["role"], list: unknown[]) {
    get.mockReset();
    get.mockResolvedValue(list);
    return render(
      <MemoryRouter>
        <AuthProvider initialUser={user(role)}>
          <MonthClosePage />
        </AuthProvider>
      </MemoryRouter>,
    );
  }

  it("가장 최근 마감 달에만 해제 요청 단추가 있고, 사유가 짧으면 보내지 않으며 10자 이상이면 요청서를 상신한다", async () => {
    post.mockResolvedValue({ docId: 9 });
    showClose("finance", months);
    fireEvent.click(await screen.findByRole("button", { name: "2026년 11월 마감 해제 요청" }));
    fireEvent.change(screen.getByLabelText(/해제 사유/), { target: { value: "짧음" } });
    fireEvent.click(screen.getByRole("button", { name: "요청서 상신" }));
    expect((await screen.findAllByText("마감 해제 사유를 10자 이상 입력하세요.")).length).toBeGreaterThan(0);
    expect(post).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/해제 사유/), { target: { value: "단가를 정정하기 위한 해제 요청" } });
    fireEvent.click(screen.getByRole("button", { name: "요청서 상신" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/inventory/months/2026-11/release-request", { reason: "단가를 정정하기 위한 해제 요청" }));
  });

  it("마감 권한이 없거나 가장 최근 달이 아니면 해제 요청 단추가 없다", async () => {
    const { unmount } = showClose("auditor", months);
    await screen.findByText("마감됨");
    expect(screen.queryByRole("button", { name: /마감 해제 요청/ })).toBeNull();
    unmount();
    showClose("finance", [{ ...months[0], releasable: false }]);
    await screen.findByText("마감됨");
    expect(screen.queryByRole("button", { name: /마감 해제 요청/ })).toBeNull();
  });
});

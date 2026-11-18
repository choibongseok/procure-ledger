import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PurchaseView, actorText } from "./PurchaseView";
import type { HistoryRecord, PurchaseRequest } from "./types";

const pr: PurchaseRequest = {
  id: 7, no: "PR-000007", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 1, title: "복사용지 구매",
  reason: "", status: "in_review", totalAmount: 1200000, rejectReason: "",
  lines: [{ id: 1, itemId: 1, itemName: "복사용지", quantity: 10, unitPrice: 120000, amount: 1200000 }],
  steps: [], purchaseOrderId: 0, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z",
};

const base: HistoryRecord = { id: 1, prId: 7, seq: 0, actorId: 1, actorName: "김직원", action: "submit", comment: "", at: "2026-11-10T01:00:00Z" };

describe("결재 이력 표", () => {
  it("머리글과 칸 수가 같고 처리자 이름이 보인다", () => {
    const hist: HistoryRecord[] = [
      base,
      { ...base, id: 2, actorId: 3, actorName: "박수임", onBehalfOfId: 2, onBehalfOfName: "김팀장", action: "approve" },
      { ...base, id: 3, action: "withdraw" },
    ];
    render(<PurchaseView pr={pr} history={hist} />);
    const table = screen.getAllByRole("table")[1];
    const heads = within(table).getAllByRole("columnheader");
    expect(heads.map((h) => h.textContent)).toEqual(["일시", "처리자", "동작", "의견"]);
    const rows = within(table).getAllByRole("row").slice(1);
    for (const r of rows) expect(within(r).getAllByRole("cell")).toHaveLength(heads.length);
    expect(within(rows[0]).getByText("김직원")).toBeTruthy();
    expect(within(rows[1]).getByText("박수임 (대결: 원결재자 김팀장)")).toBeTruthy();
    expect(within(rows[2]).getByText("회수")).toBeTruthy();
  });

  it("이름이 없으면 영문 식별자 대신 알 수 없음", () => {
    expect(actorText({ ...base, actorName: "" })).toBe("알 수 없음");
  });
});

describe("예산 초과 문구", () => {
  const over = { state: "over" as const, month: "2026-11", budget: 1000000, used: 0, inProgress: 0, amount: 1200000, over: 200000 };
  it("재무 단계를 더한 경우", () => {
    render(<PurchaseView pr={{ ...pr, budget: { ...over, financeAdded: true } }} history={[]} />);
    expect(screen.getByText(/예산 초과로 재무 단계가 더해졌습니다/)).toBeTruthy();
  });
  it("금액 구간으로 이미 재무 단계가 있던 경우", () => {
    render(<PurchaseView pr={{ ...pr, budget: { ...over, financeAdded: false } }} history={[]} />);
    expect(screen.getByText(/재무 단계는 이미 포함되어 있습니다/)).toBeTruthy();
    expect(screen.queryByText(/더해졌습니다/)).toBeNull();
  });
  it("알 수 없는 옛 스냅샷은 중립 문구", () => {
    render(<PurchaseView pr={{ ...pr, budget: { ...over, financeAdded: null } }} history={[]} />);
    expect(screen.getByText(/예산 초과 요청입니다/)).toBeTruthy();
    expect(screen.queryByText(/더해졌습니다|이미 포함/)).toBeNull();
  });
});

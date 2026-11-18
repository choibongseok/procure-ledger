import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { TaxMatchPage, type TaxMatch } from "./TaxMatchPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "post", ReturnType<typeof vi.fn>>;

const finance = { id: 4, name: "재무", role: "finance", departmentId: 3 } as const;
const auditor = { id: 6, name: "감사", role: "auditor", departmentId: 3 } as const;

const match = (over: Partial<TaxMatch> = {}): TaxMatch => ({
  id: 1, invoiceId: 11, invoiceNo: "TI-000011", approvalNo: "A".repeat(24), issueDate: "2026-11-10", supplierName: "한빛상사", supply: 100000, tax: 10000, total: 110000,
  status: "proposed", statusName: "제안 대기", result: "match", resultName: "일치", diff: { diff: 0 }, warnings: [],
  receipts: [{ receiptId: 5, poId: 3, poNo: "PO-000003", receivedAt: "2026-11-08", supply: 100000, journalNo: "" }],
  proposedAt: "2026-11-10T01:00:00Z", decidedAt: "", comment: "", confirmable: true, bulk: true, ...over,
});

function renderPage(user: typeof finance | typeof auditor) {
  return render(
    <AuthProvider initialUser={user}>
      <MemoryRouter>
        <TaxMatchPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

function serve(rows: TaxMatch[]) {
  m.get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/finance/tax-sync")) return { lastRunAt: "2026-11-10T00:00:00Z", lastDone: "2026-11-10", result: "ok", resultName: "정상적으로 가져왔습니다.", counts: { inserted: 2, same: 1, conflict: 0, invalid: 0, not_ours: 0 }, remaining: false };
    return { rows, total: rows.length };
  });
}

beforeEach(() => {
  m.get.mockReset();
  m.post.mockReset();
});

describe("세금계산서 대조 화면(B43)", () => {
  it("결과를 한국어로 보이고 영문 코드를 보이지 않으며 경고를 알려 준다", async () => {
    serve([match(), match({ id: 2, invoiceNo: "TI-000012", result: "amount_diff", resultName: "금액 불일치", diff: { diff: -30000 }, confirmable: false, bulk: false, warnings: ["세액이 계산값과 다릅니다."] })]);
    renderPage(finance);
    expect(await screen.findByText("TI-000011")).toBeInTheDocument();
    const t = within(screen.getByRole("table"));
    expect(t.getByText("일치")).toBeInTheDocument();
    expect(t.getByText("금액 불일치")).toBeInTheDocument();
    expect(t.getByText(/30,000원/)).toBeInTheDocument();
    expect(t.getByText("세액이 계산값과 다릅니다.")).toBeInTheDocument();
    expect(screen.queryByText("amount_diff")).toBeNull();
    expect(screen.getByText(/수신 동기화/)).toBeInTheDocument();
    // 금액 불일치는 확정 단추가 없고 기각만 있다.
    expect(t.getAllByRole("button", { name: "확정" })).toHaveLength(1);
  });

  it("일치 건을 골라 한꺼번에 확정하고, 기각은 사유를 받아 보낸다", async () => {
    serve([match(), match({ id: 2, invoiceNo: "TI-000012", result: "ambiguous", resultName: "후보가 여럿", bulk: false })]);
    m.post.mockResolvedValue({});
    renderPage(finance);
    fireEvent.click(await screen.findByRole("checkbox", { name: "TI-000011 선택" }));
    fireEvent.click(screen.getByRole("button", { name: /선택한 일치 건 확정/ }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/tax-matches/confirm-bulk", { ids: [1] }));
    // 후보가 여럿인 건은 일괄 확정 대상이 아니라 체크 칸이 없다.
    expect(screen.queryByRole("checkbox", { name: "TI-000012 선택" })).toBeNull();
    const row = screen.getByText("TI-000012").closest("tr") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "기각" }));
    fireEvent.change(await screen.findByLabelText("사유(필수)"), { target: { value: "다른 거래" } });
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "기각" }));
    await waitFor(() => expect(m.post).toHaveBeenCalledWith("/api/finance/tax-matches/2/dismiss", { reason: "다른 거래" }));
  });

  it("확정했지만 자동 매입 전표를 만들지 못한 사유를 한 줄로 보인다", async () => {
    serve([match({ status: "confirmed", statusName: "연결됨", journalNote: "자동 매입 전표를 만들지 못했습니다: 전기일이 속한 달이 마감되었습니다. 전표를 직접 작성해 연결하세요." })]);
    renderPage(finance);
    expect(await screen.findByText(/전기일이 속한 달이 마감되었습니다/)).toBeInTheDocument();
  });

  it("감사 담당자는 조회만 하고 처리 단추가 없다", async () => {
    serve([match()]);
    renderPage(auditor);
    expect(await screen.findByText("TI-000011")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "확정" })).toBeNull();
    expect(screen.queryByRole("button", { name: "기각" })).toBeNull();
    expect(screen.queryByRole("button", { name: "대조 다시 만들기" })).toBeNull();
  });
});

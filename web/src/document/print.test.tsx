import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import type { ApprovalStep, PurchaseRequest } from "../purchase/types";
import { AuthorityLine } from "./AuthorityLine";
import { PrintView, stepResult } from "./PrintView";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }, apiFetch: vi.fn() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const step = (over: Partial<ApprovalStep> = {}): ApprovalStep => ({ seq: 1, kind: "team_lead", approverId: 2, status: "approved", comment: "", decidedAt: "2026-11-10T09:00:00Z", title: "팀장", assigneeName: "구매팀장", role: "approve", ...over });

const doc = (steps: ApprovalStep[], over: Partial<PurchaseRequest> = {}): PurchaseRequest =>
  ({
    id: 5, no: "구매팀-재무-2026-0001", requesterId: 1, requesterName: "김직원", requesterUnitPath: "구매팀 > 구매1팀", title: "출장비", status: "approved", formName: "지출 결의서", formKind: "general",
    createdAt: "2026-11-10T08:00:00Z", retentionUntil: "2031-11-10", urgent: false, lines: [], steps, recipients: [], attachments: [], formFields: [{ key: "total", label: "금액", type: "amount", required: true }],
    fields: { total: 15000000 }, ...over,
  }) as unknown as PurchaseRequest;

function at(d: PurchaseRequest) {
  mocked.get.mockImplementation(async (path: string) => (path.startsWith("/api/approval-docs/") ? d : path.includes("/history") ? [{ id: 1, prId: 5, seq: 1, actorId: 2, actorName: "구매팀장", action: "approve", comment: "확인했습니다", at: "2026-11-10T09:00:00Z" }] : { root: { id: 1, name: "회사", children: [] } }));
  mocked.post.mockResolvedValue({ ok: true });
  return render(
    <MemoryRouter initialEntries={["/docs/5/print"]}>
      <AuthProvider initialUser={staff}>
        <Routes>
          <Route path="/docs/:id/print" element={<PrintView kind="doc" />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("B23 단계 3 출력·전결 화면", () => {
  it("결재란 처리 결과 문구: 승인·동의·이견·반려·전결·(전결)·대기", () => {
    expect(stepResult(step())).toBe("승인");
    expect(stepResult(step({ finalAuthority: true }))).toBe("전결");
    expect(stepResult(step({ status: "skipped", skipReason: "전결" }))).toBe("(전결)");
    expect(stepResult(step({ status: "agreed" }))).toBe("동의");
    expect(stepResult(step({ status: "objected" }))).toBe("이견");
    expect(stepResult(step({ status: "rejected" }))).toBe("반려");
    expect(stepResult(step({ status: "waiting" }))).toBe("대기");
  });

  it("출력 화면: 문서 번호·결재란(전결 표시)·결재 의견·보존 기한·전결 근거가 보이고 인쇄 때 감사 요청을 보낸다", async () => {
    const print = vi.spyOn(window, "print").mockImplementation(() => undefined);
    at(
      doc(
        [step(), step({ seq: 2, kind: "dept_head", title: "부서장", assigneeName: "구매부서장", finalAuthority: true }), step({ seq: 3, kind: "finance", title: "재무", status: "skipped", skipReason: "전결", decidedAt: "", assigneeName: "" })],
        { authority: { ruleId: 1, minAmount: 10000000, maxAmount: 30000000, finalOrder: 2, finalTitle: "부서장", skippedOrders: [3], slug: "approval-authority", clauseNo: "제2조" } },
      ),
    );
    expect(await screen.findByRole("heading", { name: "지출 결의서" })).toBeInTheDocument();
    expect(screen.getByText("구매팀-재무-2026-0001")).toBeInTheDocument();
    const sign = screen.getByRole("table", { name: "결재란" });
    expect(within(sign).getByText("전결")).toBeInTheDocument();
    expect(within(sign).getByText("(전결)")).toBeInTheDocument();
    expect(screen.getByText(/구매팀장: 확인했습니다/)).toBeInTheDocument();
    expect(screen.getByText("보존 기한: 2031-11-10")).toBeInTheDocument();
    expect(screen.getByText(/전결 근거: 제2조/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "인쇄" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/approval-docs/5/print"));
    await waitFor(() => expect(print).toHaveBeenCalled());
  });

  it("결재란이 8칸을 넘으면 줄을 바꿔 이어서 그린다", async () => {
    const steps = Array.from({ length: 9 }, (_, i) => step({ seq: i + 1, title: `단계${i + 1}`, assigneeName: `결재자${i + 1}` }));
    at(doc(steps));
    await screen.findByRole("heading", { name: "지출 결의서" });
    expect(screen.getAllByRole("table", { name: "결재란" })).toHaveLength(2); // 기안 + 9단계 = 10칸 → 8 + 2
  });

  it("임시저장 문서에는 워터마크 문구가 붙는다", async () => {
    at(doc([], { status: "draft", no: "TMP-1" }));
    expect(await screen.findByText("임시저장", { selector: ".print-mark" })).toBeInTheDocument();
  });

  it("인쇄 감사 요청이 실패하면 오류를 보이고 인쇄를 열지 않는다", async () => {
    const print = vi.spyOn(window, "print").mockImplementation(() => undefined);
    at(doc([step()]));
    await screen.findByRole("heading", { name: "지출 결의서" });
    mocked.post.mockRejectedValueOnce(Object.assign(new (await import("../api/client")).ApiError(404, "문서를 찾을 수 없습니다."), {}));
    fireEvent.click(screen.getByRole("button", { name: "인쇄" }));
    expect(await screen.findByText("문서를 찾을 수 없습니다.")).toBeInTheDocument();
    expect(print).not.toHaveBeenCalled();
  });

  it("전결 근거 한 줄: 누르면 조항 원문이 열리고, 볼 수 없으면 한국어 안내, 포기 경고는 경고 상자", async () => {
    mocked.get.mockResolvedValueOnce({ docTitle: "전결 규정", clauseNo: "제2조", heading: "금액별 최종 결재자", source: "전결 규정 제2조(금액별 최종 결재자)", text: "1천만 원 이상은 부서장이 전결한다." });
    const { unmount } = render(<AuthorityLine note={{ ruleId: 1, minAmount: 0, maxAmount: 0, finalOrder: 2, finalTitle: "부서장", skippedOrders: [3], slug: "approval-authority", clauseNo: "제2조" }} />);
    fireEvent.click(screen.getByRole("button", { name: "제2조" }));
    expect(await screen.findByText("1천만 원 이상은 부서장이 전결한다.")).toBeInTheDocument();
    unmount();
    mocked.get.mockRejectedValueOnce(new Error("x"));
    const r2 = render(<AuthorityLine note={{ ruleId: 1, minAmount: 0, maxAmount: 0, finalOrder: 2, finalTitle: "부서장", skippedOrders: [], slug: "approval-authority", clauseNo: "제2조" }} />);
    fireEvent.click(screen.getByRole("button", { name: "제2조" }));
    expect(await screen.findByText("근거 조항을 볼 수 없습니다.")).toBeInTheDocument();
    r2.unmount();
    render(<AuthorityLine note={{ ruleId: 1, minAmount: 0, maxAmount: 0, finalOrder: 0, finalTitle: "", skippedOrders: [], slug: "a", clauseNo: "b", warning: "필수 합의 단계가 뒤에 있어 전결 규정을 적용하지 않았습니다." }} />);
    expect(screen.getByText(/전결 규정을 적용하지 않았습니다/)).toBeInTheDocument();
  });
});

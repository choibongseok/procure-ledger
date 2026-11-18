import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { LinePreviewCard } from "../purchase/LinePreviewCard";
import { PurchaseList } from "../purchase/PurchaseList";
import { AbsencePage } from "./DelegationPanel";
import { ReassignDialog } from "./ReassignDialog";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const manager: CurrentUser = { id: 2, name: "김팀장", role: "manager", departmentId: 1 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const prRow = (over: Record<string, unknown> = {}) => ({
  id: 1, no: "PR-000001", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 1, title: "복사용지", reason: "",
  status: "submitted", totalAmount: 100000, rejectReason: "", lines: [], steps: [], purchaseOrderId: 0,
  createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z", ...over,
});

describe("B21 검증 보완 화면", () => {
  it("AC-65 구매요청 목록에는 소속 열이 없다(소속은 우측 패널 요약으로 옮겼다: asidePanels.test.tsx)", async () => {
    mocked.get.mockResolvedValue([
      prRow({ requesterUnitPath: "구매팀 > 구매1팀" }),
      prRow({ id: 2, no: "PR-000002", status: "draft", requesterUnitPath: "구매팀 > 구매1팀", title: "임시" }),
    ]);
    render(
      <MemoryRouter>
        <AuthProvider initialUser={manager}>
          <PurchaseList />
        </AuthProvider>
      </MemoryRouter>,
    );
    await screen.findByText("복사용지");
    const table = within(screen.getByRole("table"));
    expect(table.getAllByRole("columnheader").map((h) => h.textContent)).not.toContain("소속");
    expect(table.queryByText("구매팀 > 구매1팀")).toBeNull();
  });

  it("AC-68 위임 후보 라벨은 이름 (직책, 조직) 이다", async () => {
    mocked.get.mockImplementation(async (path: string) => {
      if (path === "/api/approvals/pending") return [];
      if (path === "/api/approvals/delegations") return { given: [], received: [] };
      if (path === "/api/approvals/delegations/candidates")
        return [{ id: 3, name: "이부서장", role: "manager", position: "dept_head", departmentId: 1, title: "부서장", unitName: "구매팀" }];
      throw new Error(path);
    });
    render(
      <MemoryRouter>
        <AuthProvider initialUser={manager}>
          <AbsencePage />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("option", { name: "이부서장 (부서장, 구매팀) · 대결 가능" })).toBeTruthy();
  });

  it("결재선을 만들 수 없으면 빈 단계 표 없이 오류 상자만 보인다", async () => {
    mocked.get.mockResolvedValue({
      steps: [{ order: 1, kind: "team_lead", title: "팀장", missing: true, approverName: "", unitName: "", unitPath: "", omitted: false }],
      complete: false, problems: ["1단계(팀장): 결재자를 찾지 못했습니다."], notes: [], unitPath: "구매팀",
    });
    render(<LinePreviewCard amount={500_000} />);
    await screen.findByRole("alert");
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("결재자 변경 대화상자에 상태 배지가 보인다", async () => {
    mocked.get.mockResolvedValue([]);
    const base = { prId: 7, seq: 2, title: "부서장", currentName: "구매부서장", currentUnit: "구매팀" };
    const { unmount } = render(<ReassignDialog target={{ ...base, stalled: true }} onClose={() => {}} onDone={() => {}} />);
    expect(screen.getByText("결재할 수 없음")).toBeTruthy();
    unmount();
    render(<ReassignDialog target={{ ...base, stalled: false }} onClose={() => {}} onDone={() => {}} />);
    expect(screen.getByText("재직 중")).toBeTruthy();
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { orgHomeCards } from "./homeCards";
import { OrphansPanel, sortOrphans, type Orphan } from "./OrphansPanel";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return {
    ...real,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  };
});
vi.mock("../approval/ReassignDialog", () => ({
  ReassignDialog: ({ target, onDone }: { target: { prId: number; seq: number; title: string }; onDone: (m: string) => void }) => (
    <div role="dialog" aria-label="결재자 변경">
      {target.prId}-{target.seq}-{target.title}
      <button onClick={() => onDone("결재자를 변경했습니다.")}>완료</button>
    </div>
  ),
}));

const get = api.get as unknown as ReturnType<typeof vi.fn>;

const row = (over: Partial<Orphan>): Orphan => ({
  prId: 1,
  requesterId: 5,
  requesterName: "구매직원",
  prTitle: "문서",
  round: 1,
  order: 1,
  seq: 1,
  title: "팀장",
  stepKind: "team_lead",
  approverId: 9,
  approverName: "최본부",
  reason: "퇴사",
  sinceDay: "2026-11-10",
  reassignable: true,
  role: "approve",
  formKind: "purchase",
  formName: "구매 품의서",
  stage: 1,
  ...over,
});

function renderPanel(role: "admin" | "staff" = "admin") {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "관리자", role, departmentId: 1 }}>
        <OrphansPanel />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => get.mockReset());

describe("결재·합의자가 사라진 진행 중 건", () => {
  it("사라진 날이 오래된 순, 같은 날은 문서 번호 순으로 정렬한다", () => {
    const sorted = sortOrphans([
      row({ prId: 9, sinceDay: "2026-11-10" }),
      row({ prId: 3, sinceDay: "2026-11-10" }),
      row({ prId: 7, sinceDay: "2026-11-01" }),
    ]);
    expect(sorted.map((o) => o.prId)).toEqual([7, 3, 9]);
  });

  it("행: 문서 종류별 링크, 양식, 단계 역할, 병렬 합의, 재무 단계 안내, 결재자 변경 버튼", async () => {
    get.mockResolvedValue([
      row({ prId: 39, prTitle: "물류센터 지게차 구매", title: "본부장", sinceDay: "2026-11-10" }),
      row({
        prId: 12,
        prTitle: "업무 협조전 A",
        formKind: "general",
        formName: "업무 협조전",
        role: "agree",
        title: "합의",
        approverName: "합의자1",
        approverId: 21,
        seq: 3,
        stage: 3,
        order: 3,
        reason: "합의 권한 상실",
        sinceDay: "2026-11-05",
      }),
      row({
        prId: 12,
        prTitle: "업무 협조전 A",
        formKind: "general",
        formName: "업무 협조전",
        role: "agree",
        title: "합의",
        approverName: "합의자2",
        approverId: 22,
        seq: 4,
        stage: 3,
        order: 4,
        reason: "퇴사",
        sinceDay: "2026-11-06",
      }),
      row({ prId: 40, prTitle: "재무 건", title: "재무", reassignable: false, stepKind: "finance", sinceDay: "2026-11-08" }),
    ]);
    renderPanel();
    expect(await screen.findByText("4건")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "물류센터 지게차 구매" })).toHaveAttribute("href", "/purchase-requests/39");
    const links = screen.getAllByRole("link", { name: "업무 협조전 A" });
    expect(links[0]).toHaveAttribute("href", "/docs/12");
    expect(screen.getAllByText("합의(병렬)")).toHaveLength(2);
    expect(screen.getByText("합의 권한 상실")).toBeInTheDocument();
    expect(screen.getByText("재무 단계는 재무 담당 역할을 가진 사람이 맡습니다.")).toBeInTheDocument();
    // 정렬: 11-05 합의자1 → 11-06 합의자2 → 11-08 재무 → 11-10 지게차
    const first = screen.getAllByRole("row")[1];
    expect(within(first).getByText("합의자1", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "결재자 변경" })).toHaveLength(3);
    // 대화상자를 열고 완료하면 목록을 다시 불러온다
    get.mockClear();
    fireEvent.click(screen.getAllByRole("button", { name: "결재자 변경" })[0]);
    expect(screen.getByRole("dialog", { name: "결재자 변경" })).toHaveTextContent("12-3-합의");
    fireEvent.click(screen.getByRole("button", { name: "완료" }));
    expect(await screen.findByText("결재자를 변경했습니다.")).toBeInTheDocument();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/approvals/orphaned"));
  });

  it("빈 상태와 오류 문구, 결재자 변경 권한이 없으면 버튼이 없다", async () => {
    get.mockResolvedValueOnce([]);
    const { unmount } = renderPanel();
    expect(await screen.findByText("결재·합의자가 사라진 진행 중 건이 없습니다.")).toBeInTheDocument();
    unmount();
    get.mockRejectedValueOnce(new Error("x"));
    const second = renderPanel();
    expect(await screen.findByText("목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.")).toBeInTheDocument();
    second.unmount();
    get.mockResolvedValueOnce([row({})]);
    renderPanel("staff");
    expect(await screen.findByText("1건")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "결재자 변경" })).toBeNull();
  });

  it("관리자 홈 카드: 건수와 점검 탭 링크, 0건이면 없음", async () => {
    const card = orgHomeCards[0];
    expect(card.permission).toBe("org:manage");
    expect(card.title).toBe("결재·합의자 소멸");
    get.mockResolvedValueOnce([row({}), row({ prId: 2 })]);
    const { unmount } = render(
      <MemoryRouter>
        <card.Component />
      </MemoryRouter>,
    );
    expect(await screen.findByText("2건")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "점검 탭 열기" })).toHaveAttribute("href", "/org/manage?tab=check");
    unmount();
    get.mockResolvedValueOnce([]);
    render(
      <MemoryRouter>
        <card.Component />
      </MemoryRouter>,
    );
    expect(await screen.findByText("없음")).toBeInTheDocument();
  });
});

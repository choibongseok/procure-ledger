import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApprovalInbox } from "../approval/ApprovalInbox";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { AsideProvider, useAsideSpec } from "../layout/aside";
import { ApprovalInfoPanel, RequestSummaryPanel, lastUnit } from "./asidePanels";
import { HomeTodoAside } from "./HomeTodoAside";
import { PurchaseList } from "./PurchaseList";
import type { HistoryRecord, PurchaseRequest } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const get = vi.mocked(api.get);
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

/** Layout 이 하는 일을 흉내 낸다: 패널 제목과 내용을 그린다(없으면 data-aside="none"). */
function AsideHost() {
  const spec = useAsideSpec();
  if (!spec) return <div data-testid="aside" data-aside="none" />;
  return (
    <aside data-testid="aside" aria-label={spec.title}>
      <h2>{spec.title}</h2>
      {spec.content}
    </aside>
  );
}

function mount(ui: React.ReactElement) {
  return render(
    <AuthProvider initialUser={staff}>
      <MemoryRouter>
        <AsideProvider>
          {ui}
          <AsideHost />
        </AsideProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}

const pr = (over: Partial<PurchaseRequest> = {}): PurchaseRequest => ({
  id: 1, no: "PR-000001", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 1, title: "복사용지",
  reason: "", status: "in_review", totalAmount: 1200000, rejectReason: "", lines: [],
  steps: [
    { seq: 1, kind: "team_lead", title: "팀장", approverId: 2, status: "approved", comment: "", decidedAt: "2026-11-10T07:00:00Z" },
    { seq: 2, kind: "dept_head", title: "부서장", approverId: 3, assigneeName: "구매부서장", status: "pending", comment: "", decidedAt: "" },
  ],
  purchaseOrderId: 0, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z",
  requesterUnitPath: "구매·물류본부 > 구매팀 > 구매1팀",
  budget: { state: "over", month: "2026-11", budget: 1000000, used: 0, inProgress: 0, amount: 1200000, over: 200000 },
  ...over,
} as PurchaseRequest);

beforeEach(() => {
  get.mockReset();
});

describe("구매요청 목록 행 선택", () => {
  it("선택하면 패널에 요약이 뜨고 소속 열은 목록에 없다, 다른 행을 고르면 하나만 선택된다", async () => {
    get.mockResolvedValue([pr(), pr({ id: 2, no: "PR-000002", title: "토너", status: "rejected" })]);
    mount(<PurchaseList />);
    await screen.findByText("복사용지");
    expect(screen.getByTestId("aside").getAttribute("data-aside")).toBe("none");
    const table = within(screen.getByRole("table"));
    expect(table.getAllByRole("columnheader").map((h) => h.textContent)).not.toContain("소속");
    for (const r of table.getAllByRole("row").slice(1)) expect(within(r).getAllByRole("cell")).toHaveLength(table.getAllByRole("columnheader").length);

    fireEvent.click(screen.getByRole("radio", { name: "PR-000002 선택" }));
    const aside = within(screen.getByTestId("aside"));
    expect(aside.getByRole("heading", { name: "선택한 요청", level: 2 })).toBeTruthy();
    expect(aside.getByText("토너")).toBeTruthy();
    expect(aside.getByText("구매1팀")).toBeTruthy();
    expect(aside.getByText("재상신하러 가기")).toBeTruthy();
    expect(aside.getByText("견적 비교").getAttribute("href")).toBe("/purchase-requests/2/quotes");
    const rows = screen.getAllByRole("row");
    expect(rows[2].getAttribute("aria-selected")).toBe("true");
    expect(rows[1].getAttribute("aria-selected")).toBeNull();

    // 다른 행을 고르면 하나만 선택된다(라디오).
    fireEvent.click(screen.getByRole("radio", { name: "PR-000001 선택" }));
    expect(screen.getAllByRole("row").filter((r) => r.getAttribute("aria-selected") === "true")).toHaveLength(1);
    expect(within(screen.getByTestId("aside")).getByText("복사용지")).toBeTruthy();
    expect(within(screen.getByTestId("aside")).queryByText("재상신하러 가기")).toBeNull();

    expect(screen.getByRole("radio", { name: "PR-000001 선택" })).toBeChecked();
  });
});

describe("결재함 행 선택", () => {
  it("소속은 마지막 조직만 보이고 결재선의 현재 단계가 강조된다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/approvals/pending") return [pr()];
      return [];
    });
    mount(<ApprovalInbox />);
    await screen.findByText("복사용지");
    fireEvent.click(screen.getByRole("radio", { name: "PR-000001 선택" }));
    const aside = within(screen.getByTestId("aside"));
    expect(aside.getByRole("heading", { name: "선택한 건", level: 2 })).toBeTruthy();
    expect(aside.getByText("구매1팀")).toBeTruthy();
    expect(aside.queryByText(/구매·물류본부/)).toBeNull();
    expect(aside.getByText("초과 (200,000원)")).toBeTruthy();
    const cur = aside.getAllByRole("listitem").find((li) => li.getAttribute("aria-current") === "step");
    expect(cur?.textContent).toContain("부서장");
    expect(aside.getByText("열기").getAttribute("href")).toBe("/approvals/1");
  });
});

describe("구매요청 목록 쪽 이동", () => {
  it("전체 건수가 한 쪽을 넘으면 쪽 이동이 나오고 다음은 offset 으로 다시 부른다, 선택은 라디오다", async () => {
    const page = vi.fn(async () => ({ items: [pr()], total: 120 }));
    (api as unknown as { getPage: unknown }).getPage = page;
    mount(<PurchaseList />);
    await screen.findByText("복사용지");
    expect(screen.getByRole("radio", { name: "PR-000001 선택" })).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: "PR-000001 선택" })).toBeNull();
    expect(screen.getByText("1–50 / 전체 120건")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await screen.findByText("51–100 / 전체 120건");
    expect((page.mock.calls.at(-1) as unknown as string[])[0]).toContain("offset=50");
  });
});

describe("결재함 선택 라디오", () => {
  it("한 행만 고를 수 있는 라디오(name inbox-select)이고 고른 행에 aria-selected 가 붙는다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/approvals/pending") return [pr(), pr({ id: 2, no: "PR-000002", title: "토너" })];
      return [];
    });
    mount(<ApprovalInbox />);
    await screen.findByText("복사용지");
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    const first = screen.getByRole("radio", { name: "PR-000001 선택" });
    const second = screen.getByRole("radio", { name: "PR-000002 선택" });
    expect(first.getAttribute("name")).toBe("inbox-select");
    expect(second.getAttribute("name")).toBe("inbox-select");
    fireEvent.click(first);
    expect(first).toBeChecked();
    expect(screen.getAllByRole("row").filter((r) => r.getAttribute("aria-selected") === "true")).toHaveLength(1);
    fireEvent.click(second);
    expect(second).toBeChecked();
    expect(first).not.toBeChecked();
    const selected = screen.getAllByRole("row").filter((r) => r.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(within(selected[0]).getByText("PR-000002")).toBeTruthy();
  });
});

describe("결재 정보 패널", () => {
  const hist: HistoryRecord[] = [
    { id: 2, prId: 1, seq: 1, actorId: 2, actorName: "구매팀장", action: "approve", comment: "확인", at: "2026-11-10T07:00:00Z" },
    { id: 1, prId: 1, seq: 0, actorId: 1, actorName: "김직원", action: "submit", comment: "", at: "2026-11-10T05:00:00Z" },
  ];
  it("이력은 시각 순이고 첨부가 없으면 한 줄 안내가 보인다", () => {
    render(
      <MemoryRouter>
        <ApprovalInfoPanel pr={pr()} history={hist} />
      </MemoryRouter>,
    );
    const order = screen.getAllByText(/^(김직원 상신|구매팀장 승인)$/).map((e) => e.textContent);
    expect(order).toEqual(["김직원 상신", "구매팀장 승인"]);
    expect(screen.getByText("첨부 파일이 없습니다.")).toBeTruthy();
  });
  it("대결은 원결재자와 함께 보이고 첨부는 개수와 열기 링크가 있다", () => {
    render(
      <MemoryRouter>
        <ApprovalInfoPanel
          pr={pr({ attachments: [{ id: 9, filename: "견적.pdf", size: 1000 }] })}
          history={[{ ...hist[0], onBehalfOfId: 3, onBehalfOfName: "구매부서장" }]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByText(/대결: 원결재자 구매부서장/)).toBeTruthy();
    expect(screen.getByText("첨부 1개")).toBeTruthy();
    expect(screen.getByText("견적.pdf").getAttribute("href")).toBe("/api/approval-docs/attachments/9");
  });
});

describe("첨부 패널의 견적서 줄", () => {
  it("첨부 파일과 견적서를 나눠 보이고 견적 비교로 가는 링크가 있다", async () => {
    get.mockResolvedValue({ quotes: [{ id: 1 }, { id: 2 }, { id: 3 }] });
    render(
      <MemoryRouter>
        <ApprovalInfoPanel pr={pr()} history={[]} />
      </MemoryRouter>,
    );
    expect(screen.getByText("첨부 파일이 없습니다.")).toBeTruthy();
    expect(await screen.findByText(/견적서 3건/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "견적 비교 열기" }).getAttribute("href")).toBe("/purchase-requests/1/quotes");
  });
  it("견적서가 없으면 따로 없다고 알린다", async () => {
    get.mockResolvedValue({ quotes: [] });
    render(
      <MemoryRouter>
        <ApprovalInfoPanel pr={pr()} history={[]} />
      </MemoryRouter>,
    );
    expect(await screen.findByText("견적서가 없습니다.")).toBeTruthy();
  });
});

describe("패널 구조", () => {
  it("결재선 ol 에 approval-line class 가 있다", () => {
    const { container } = render(
      <MemoryRouter>
        <ApprovalInfoPanel pr={pr()} history={[]} />
      </MemoryRouter>,
    );
    expect(container.querySelector("ol.approval-line li[aria-current='step']")).toBeTruthy();
  });
  it("요약의 dt/dd 는 dl 직계다", () => {
    const { container } = render(
      <MemoryRouter>
        <RequestSummaryPanel pr={pr()} />
      </MemoryRouter>,
    );
    const dl = container.querySelector("dl")!;
    expect(dl.querySelectorAll(":scope > dt").length).toBeGreaterThan(0);
    expect(dl.querySelector("div")).toBeNull();
  });
});

describe("홈 오늘 할 일 절", () => {
  it("자기 aside-section 래퍼와 h3 를 만들지 않는다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/purchase-requests/my-summary") return { total: 1, byStatus: { rejected: 1 } };
      return [{ id: 1, title: "새 도착" }];
    });
    const { container } = mount(<HomeTodoAside />);
    await screen.findByText("새 도착");
    expect(container.querySelector("section.aside-section")).toBeNull();
    expect(container.querySelector("h3")).toBeNull();
  });
  it("반려·임시저장 건수와 최근 알림을 보인다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/purchase-requests/my-summary") return { total: 3, byStatus: { rejected: 2, draft: 1 } };
      return [{ id: 1, title: "결재 요청이 도착했습니다" }];
    });
    mount(<HomeTodoAside />);
    expect(await screen.findByText("반려된 내 요청 2건")).toBeTruthy();
    expect(screen.getByText("임시저장 중인 요청 1건").getAttribute("href")).toBe("/purchase-requests?status=draft");
    expect(screen.getByText("결재 요청이 도착했습니다")).toBeTruthy();
  });
  it("알림만 있을 때 할 일 소절에 '할 일이 없습니다'", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/purchase-requests/my-summary") return { total: 0, byStatus: {} };
      return [{ id: 1, title: "새 알림" }];
    });
    mount(<HomeTodoAside />);
    expect(await screen.findByText("새 알림")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "할 일" })).toBeTruthy();
    expect(screen.getByText("할 일이 없습니다.")).toBeTruthy();
  });
  it("둘 다 0건이면 한 줄만 보인다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/purchase-requests/my-summary") return { total: 1, byStatus: { approved: 1 } };
      return [];
    });
    mount(<HomeTodoAside />);
    await waitFor(() => expect(screen.getByText("오늘 할 일이 없습니다.")).toBeTruthy());
    expect(screen.queryByRole("heading", { name: "할 일" })).toBeNull();
  });
});

describe("홈 절의 권한 분기", () => {
  const only = (perms: string[]) =>
    render(
      <AuthProvider initialUser={staff} initialPermissions={perms as never}>
        <MemoryRouter>
          <HomeTodoAside />
        </MemoryRouter>
      </AuthProvider>,
    );
  it("알림 권한만 있으면 my-summary 를 부르지 않고 알림만 보인다", async () => {
    get.mockImplementation(async () => [{ id: 1, title: "새 알림" }]);
    only(["notification:read"]);
    expect(await screen.findByText("새 알림")).toBeTruthy();
    expect(get.mock.calls.map((c) => c[0] as string).some((p) => p.includes("my-summary"))).toBe(false);
    expect(screen.queryByRole("heading", { name: "할 일" })).toBeNull();
  });
  it("알림 권한이 없으면 /api/notifications 를 부르지 않는다", async () => {
    get.mockImplementation(async () => ({ total: 1, byStatus: { draft: 1 } }));
    only(["purchase:create"]);
    expect(await screen.findByText("임시저장 중인 요청 1건")).toBeTruthy();
    expect(get.mock.calls.map((c) => c[0] as string).some((p) => p.includes("/api/notifications"))).toBe(false);
  });
});

describe("lastUnit", () => {
  it("마지막 조직만 돌려주고 비면 대시", () => {
    expect(lastUnit("구매팀 > 구매1팀")).toBe("구매1팀");
    expect(lastUnit("")).toBe("—");
    expect(lastUnit(undefined)).toBe("—");
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { roleHas, type Role } from "../auth/permissions";
import { Layout, hasNestedMenu, visibleMenu } from "../layout/Layout";
import { allMenu } from "../routes";
import { CommandForm } from "./CommandForm";
import { AppointmentsPage } from "./AppointmentsPage";
import { OrgChartPage } from "./OrgChartPage";
import { OrgManagePage } from "./OrgManagePage";
import { orgModule } from "./routes";
import type { PersonCard, TreeNode } from "./types";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return {
    ...real,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;

function node(id: number, name: string, kind: TreeNode["kind"], children: TreeNode[] = [], extra: Partial<TreeNode> = {}): TreeNode {
  return {
    id,
    code: `C${id}`,
    name,
    kind,
    closed: false,
    headUserId: 0,
    headName: "",
    headActing: false,
    direct: 1,
    total: 3,
    children,
    ...extra,
  };
}

const place = (unitId: number, name: string, path: string, extra = {}) => ({
  unitId,
  name,
  kind: "team" as const,
  path,
  fullPath: `PL 주식회사 > ${path}`,
  isHead: false,
  acting: false,
  closed: false,
  ...extra,
});
function card(id: number, name: string, extra: Partial<PersonCard> = {}): PersonCard {
  return {
    id,
    name,
    employeeNo: `E${id}`,
    rank: "대리",
    active: true,
    email: "l***@pl.test",
    phone: "010-****-2222",
    extension: "1001",
    primary: place(4, "구매1팀", "구매·물류본부 > 구매팀 > 구매1팀"),
    concurrent: [],
    acting: [],
    ...extra,
  };
}

const TREE = node(1, "PL 주식회사", "company", [
  node(2, "구매·물류본부", "division", [
    node(3, "구매팀", "department", [
      node(4, "구매1팀", "team", [], { headName: "구매팀장", headUserId: 10 }),
      node(5, "구매3팀", "team", [], { closed: true }),
    ]),
  ]),
]);

function renderChart(role: Role = "staff") {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "홍길동", role, departmentId: 1 }}>
        <OrgChartPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("조직도 화면", () => {
  it("조직이 없으면 안내 문구를, 불러오기에 실패하면 오류 문구를 보인다", async () => {
    get.mockResolvedValue({ root: null });
    renderChart();
    expect(await screen.findByText("등록된 조직이 없습니다. 관리자에게 조직 등록을 요청하세요.")).toBeInTheDocument();
    get.mockReset();
    get.mockRejectedValue(new Error("x"));
    renderChart();
    expect((await screen.findAllByText("조직도를 불러오지 못했습니다. 잠시 후 다시 시도하세요.")).length).toBeGreaterThan(0);
  });

  it("트리에 인원 수·폐지·조직장·공석이 보이고 펼침·접기가 되며 영문 상태값이 보이지 않는다", async () => {
    get.mockResolvedValue({ root: TREE });
    const { container } = renderChart();
    await screen.findByRole("tree", { name: "조직 트리" });
    expect(screen.getByText("PL 주식회사")).toBeInTheDocument();
    // 회사와 본부까지 펼쳐져 있어 부서가 보이고, 부서 아래 팀은 접혀 있다
    expect(screen.getByText("구매팀")).toBeInTheDocument();
    expect(screen.queryByText("구매1팀")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "구매팀 펼치기" }));
    expect(screen.getByText("구매1팀")).toBeInTheDocument();
    expect(screen.getByText(/구매팀장/)).toBeInTheDocument();
    expect(screen.getByText("폐지")).toBeInTheDocument();
    expect(screen.getAllByText(/직속 1명 \/ 전체 3명/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "구매팀 접기" }));
    expect(screen.queryByText("구매1팀")).toBeNull();
    expect(container.textContent).not.toMatch(/team_lead|dept_head|\bclosed\b|\bscheduled\b|\bapplied\b|\bcancelled\b|company|division/);
  });

  it("조직을 고르면 구성원이 보이고 구성원이 없으면 안내한다", async () => {
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) return { root: TREE };
      if (url.startsWith("/api/org/units/2/members"))
        return {
          members: [
            card(10, "구매팀장", { primary: place(4, "구매1팀", "구매·물류본부 > 구매팀 > 구매1팀", { isHead: true, title: "팀장" }) }),
          ],
        };
      if (url.startsWith("/api/org/units/3/members")) return { members: [] };
      return {};
    });
    renderChart();
    fireEvent.click(await screen.findByText("구매·물류본부"));
    const table = await screen.findByRole("table");
    expect(within(table).getByText("구매팀장")).toBeInTheDocument();
    expect(within(table).getByText(/구매1팀 팀장/)).toBeInTheDocument();
    // 연락처는 서버가 가린 값 그대로 보인다
    expect(within(table).getByText("010-****-2222")).toBeInTheDocument();
    fireEvent.click(screen.getByText("구매팀"));
    expect(await screen.findByText("이 조직에는 구성원이 없습니다.")).toBeInTheDocument();
  });

  it("검색 결과가 없으면 안내하고, 기준일을 바꾸면 안내 상자와 오늘로 돌아가기가 나온다", async () => {
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) return { root: TREE };
      if (url.startsWith("/api/org/people")) return { people: [] };
      return {};
    });
    renderChart();
    await screen.findByRole("tree", { name: "조직 트리" });
    fireEvent.change(screen.getByPlaceholderText("이름·조직·직책"), { target: { value: "없는사람" } });
    fireEvent.click(screen.getByRole("button", { name: "찾기" }));
    expect(await screen.findByText("검색 결과가 없습니다. 이름·조직·직책으로 다시 찾아보세요.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/기준일/), { target: { value: "2020-03-01" } });
    expect(await screen.findByText(/2020\. 3\. 1\. 기준 조직도입니다\(과거\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "오늘로 돌아가기" }));
    await waitFor(() => expect(screen.queryByText(/기준 조직도입니다/)).toBeNull());
  });

  it("선택해 둔 조직이 기준일에 없었으면 안내 문구 하나만 나온다(AC-42·E18): 오류 상자·빈 목록 문구 없음, 조직이 아예 없는 날짜도 같다", async () => {
    const OLD = node(1, "PL 주식회사", "company", [node(2, "구매·물류본부", "division")]);
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) {
        if (url.includes("asOf=2015-12-31")) return { root: null };
        return { root: url.includes("asOf=2020-03-01") ? OLD : TREE };
      }
      // 그 기준일에 없던 조직의 구성원 조회는 서버가 404 로 답한다.
      if (url.startsWith("/api/org/units/4/members") && /asOf=(2020-03-01|2015-12-31)/.test(url))
        throw new ApiError(404, "조직을 찾을 수 없습니다");
      if (url.startsWith("/api/org/units/")) return { members: [] };
      return {};
    });
    const { container } = renderChart();
    fireEvent.click(await screen.findByText("구매·물류본부"));
    fireEvent.click(await screen.findByRole("button", { name: "구매팀 펼치기" }));
    fireEvent.click(screen.getByText("구매1팀"));
    expect(await screen.findByText("구매1팀 구성원")).toBeInTheDocument();
    expect(screen.queryByText(/이 날짜에는 없던 조직입니다/)).toBeNull();
    fireEvent.change(screen.getByLabelText(/기준일/), { target: { value: "2020-03-01" } });
    expect(await screen.findByText("이 날짜에는 없던 조직입니다. 조직을 다시 선택하세요.")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText("구매1팀 구성원")).toBeNull());
    expect(container.textContent).not.toMatch(/조직도를 불러오지 못했습니다|이 조직에는 구성원이 없습니다/);
    // 조직이 하나도 없던 날짜
    fireEvent.change(screen.getByLabelText(/기준일/), { target: { value: "2015-12-31" } });
    expect(await screen.findByText("이 날짜에는 없던 조직입니다. 조직을 다시 선택하세요.")).toBeInTheDocument();
    expect(screen.getAllByText(/이 날짜에는 없던 조직입니다/)).toHaveLength(1);
    expect(container.textContent).not.toMatch(/조직도를 불러오지 못했습니다|이 조직에는 구성원이 없습니다/);
  });

  it("퇴사자 포함은 이력 권한이 있는 역할에게만 보이고 사람 카드에 이력 표가 나온다", async () => {
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) return { root: TREE };
      if (url.startsWith("/api/org/units/2/members")) return { members: [card(10, "구매팀장")] };
      if (url.startsWith("/api/org/people/10/history"))
        return {
          events: [
            {
              id: 1,
              kind: "transfer",
              kindLabel: "전보",
              subject: "person",
              subjectId: 10,
              subjectName: "구매팀장",
              effectiveDate: "2026-03-01",
              summary: "구매2팀(으)로 전보",
              reason: "조직 개편",
              createdBy: "관리자",
              status: "applied",
              corrects: 0,
            },
          ],
        };
      if (url.startsWith("/api/org/people/10")) return card(10, "구매팀장");
      return {};
    });
    const staff = renderChart("staff");
    await screen.findByRole("tree", { name: "조직 트리" });
    expect(screen.queryByLabelText(/퇴사자 포함/)).toBeNull();
    staff.unmount();
    renderChart("auditor");
    fireEvent.click(await screen.findByText("구매·물류본부"));
    expect(screen.getByLabelText(/퇴사자 포함/)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "구매팀장" }));
    expect(await screen.findByText("조직 개편")).toBeInTheDocument();
    expect(screen.getByText("적용됨")).toBeInTheDocument();
    expect(screen.queryByText("이 사람 발령 등록")).toBeNull(); // 감사 담당은 발령 등록 링크가 없다
  });
});

describe("메뉴와 권한", () => {
  it("조직도는 모든 역할에, 조직 관리·인사 발령은 admin 에게만 보인다", () => {
    for (const role of ["admin", "finance", "manager", "staff", "auditor"] as Role[]) {
      const labels = visibleMenu(orgModule.menu, (p) => p === "authenticated" || p === "public" || roleHas(role, p)).map((m) => m.label);
      expect(labels.includes("조직도"), role).toBe(true);
      expect(labels.includes("조직 관리"), role).toBe(role === "admin");
      expect(labels.includes("인사 발령"), role).toBe(role === "admin");
    }
  });
});

describe("조직 변경 폼", () => {
  const unit = node(4, "구매1팀", "team");

  function renderForm(onDone = vi.fn()) {
    render(<CommandForm kind="unit_sort" tree={TREE} ranks={[]} unit={unit} onDone={onDone} />);
    return onDone;
  }

  it("미리보기를 보기 전에는 등록 버튼이 꺼져 있고, 입력을 바꾸면 다시 꺼진다(AC-95)", async () => {
    post.mockResolvedValue({ effects: ["구매1팀 순서가 바뀝니다."], warnings: [], pendingApprovals: 0, retroactive: false });
    renderForm();
    const register = screen.getByRole("button", { name: "등록" });
    expect(register).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText("구매1팀 순서가 바뀝니다.")).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/org/events/preview", expect.objectContaining({ kind: "unit_sort", unitId: 4 }));
    await waitFor(() => expect(screen.getByRole("button", { name: "등록" })).toBeEnabled());
    fireEvent.change(screen.getByLabelText(/정렬 순서/), { target: { value: "7" } });
    expect(screen.getByRole("button", { name: "등록" })).toBeDisabled();
  });

  it("미리보기에 이 사람이 결재할 차례인 진행 중 건 수와 문서 제목 목록이 나온다", async () => {
    renderForm();
    post.mockResolvedValueOnce({
      effects: ["x"],
      warnings: [],
      pendingApprovals: 22,
      pendingDocs: [{ id: 1, no: "PR-000001", title: "복사용지", requester: "구매직원" }],
      retroactive: false,
    });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText(/결재\(합의\)할 차례인 진행 중 건이 22건 있습니다/)).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "진행 중 건 목록" });
    expect(within(list).getByText("복사용지")).toBeInTheDocument();
    expect(within(list).getByText("외 21건")).toBeInTheDocument();
  });

  it("등록하면 효력일이 오늘이면 등록 문구를, 예약이면 예약 문구를 보이고 소급 확인 요청에는 계속 버튼이 나온다", async () => {
    const onDone = renderForm();
    post.mockResolvedValueOnce({ effects: ["x"], warnings: [], pendingApprovals: 0, retroactive: false });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "등록" })).toBeEnabled());
    post.mockRejectedValueOnce(
      new ApiError(
        409,
        JSON.stringify({ needConfirm: true, days: 68, message: "68일 전으로 소급합니다. 이미 지난 날짜입니다. 계속할까요?" }),
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    expect(await screen.findByText(/68일 전으로 소급합니다/)).toBeInTheDocument();
    post.mockResolvedValueOnce({ effectiveDate: "2020-01-01", eventIds: [1] });
    fireEvent.click(screen.getByRole("button", { name: "계속" }));
    await waitFor(() => expect(onDone).toHaveBeenCalledWith("발령을 등록했습니다(효력일 2020. 1. 1.)."));
    expect(post).toHaveBeenLastCalledWith("/api/org/events", expect.objectContaining({ confirm: true }));
  });

  it("서버의 업무 문구를 그대로 보인다", async () => {
    renderForm();
    post.mockRejectedValueOnce(new ApiError(400, "자기 하위 조직 밑으로는 옮길 수 없습니다."));
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText("자기 하위 조직 밑으로는 옮길 수 없습니다.")).toBeInTheDocument();
  });
});

describe("메뉴 강조", () => {
  function renderAt(path: string) {
    render(
      <MemoryRouter initialEntries={[path]}>
        <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
          <Routes>
            <Route element={<Layout menu={allMenu} />}>
              <Route path="*" element={<p>본문</p>} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
  }

  it("하위 경로 화면에서는 상위 경로 메뉴가 함께 강조되지 않고 메뉴 하나만 강조된다", () => {
    for (const [path, label] of [
      ["/org", "조직도"],
      ["/org/manage", "조직 관리"],
      ["/org/appointments", "인사 발령"],
      ["/", "홈"],
    ] as const) {
      const { unmount } = render(<div />);
      unmount();
      renderAt(path);
      // SNB(하위 메뉴)에서는 한 항목만 aria-current="page" 이고, 홈은 SNB 가 없어 GNB 의 홈 링크가 현재 모듈이다.
      const current = (path === "/" ? Array.from(document.querySelectorAll('a[aria-current="true"]')) : screen.getAllByRole("link", { current: "page" })).map((a) => a.textContent);
      expect(current, path).toEqual([label]);
      document.body.innerHTML = "";
    }
  });

  it("다른 메뉴의 상위 경로인지 판정한다", () => {
    const menu = orgModule.menu;
    expect(hasNestedMenu(menu[0], menu)).toBe(true); // /org 는 /org/manage 의 상위
    expect(hasNestedMenu(menu[1], menu)).toBe(false);
  });
});

describe("발령 목록 쪽 나누기와 조직 점검", () => {
  const ev = (id: number) => ({
    id,
    kind: "transfer",
    kindLabel: "전보",
    subject: "person",
    subjectId: id,
    subjectName: `사람${id}`,
    effectiveDate: "2026-03-01",
    summary: "구매1팀(으)로 전보",
    reason: "",
    createdBy: "시스템",
    status: "applied",
    corrects: 0,
  });

  it("발령 목록은 쪽 단위로 받고 이전·다음과 종류·기간 조건이 요청에 실린다", async () => {
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) return { root: TREE };
      if (url.startsWith("/api/org/ranks")) return { ranks: [] };
      if (url.startsWith("/api/org/events")) return { events: [ev(1), ev(2)], total: 70 };
      return {};
    });
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
          <AppointmentsPage />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/1–30 \/ 전체 70건/)).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith(expect.stringMatching(/\/api\/org\/events\?.*limit=30.*offset=0/));
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/offset=30/)));
    fireEvent.change(screen.getByLabelText("효력일 시작"), { target: { value: "2026-01-01" } });
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/from=2026-01-01.*offset=0/)));
    fireEvent.change(screen.getAllByLabelText("종류")[1], { target: { value: "leave" } });
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/kind=leave/)));
  });

  it("점검 탭에 발령 이력과 현재 조직의 일치 여부가 한 줄로 나온다(내부 용어 이벤트 로그는 쓰지 않는다)", async () => {
    for (const [log, want] of [
      [{ ok: true, mismatches: 0, broken: "" }, "발령 이력과 현재 조직이 일치합니다."],
      [{ ok: false, mismatches: 3, broken: "" }, "발령 이력과 현재 조직이 맞지 않는 구간이 3건 있습니다. 관리자에게 알리세요."],
    ] as const) {
      get.mockReset();
      get.mockImplementation(async (url: string) => {
        if (url.startsWith("/api/org/tree")) return { root: TREE };
        if (url.startsWith("/api/org/ranks")) return { ranks: [] };
        if (url.startsWith("/api/org/consistency")) return { issues: [], log };
        return {};
      });
      const { unmount } = render(
        <MemoryRouter>
          <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
            <OrgManagePage />
          </AuthProvider>
        </MemoryRouter>,
      );
      fireEvent.click(await screen.findByRole("tab", { name: "점검" }));
      expect(await screen.findByText(want)).toBeInTheDocument();
      unmount();
    }
  });
});

describe("휴직·복직 발령(B50)", () => {
  it("휴직 폼은 구분·예정 복직일·사유를 payload 로 보내고, 사유 칸에 민감 정보 안내가 있다", async () => {
    post.mockReset();
    post.mockResolvedValue({ effects: ["가나다님: 2026. 3. 10.부터 휴직 상태가 됩니다."], warnings: [], pendingApprovals: 2, pendingDocs: [], retroactive: false });
    get.mockImplementation(async () => ({ people: [card(11, "휴직자")] }));
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
          <CommandForm kind="absence_start" tree={TREE} ranks={[]} person={card(11, "휴직자")} onDone={() => {}} />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByText(/병명·진단 내용은 적지 마세요/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("휴직 구분"), { target: { value: "childcare" } });
    fireEvent.change(screen.getByLabelText(/예정 복직일/), { target: { value: "2026-12-31" } });
    fireEvent.change(screen.getByLabelText(/휴직 사유/), { target: { value: "육아" } });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [url, body] = post.mock.calls[0] as [string, Record<string, unknown>];
    expect(url).toBe("/api/org/events/preview");
    expect(body).toMatchObject({ kind: "absence_start", userId: 11, absenceKind: "childcare", returnDay: "2026-12-31", absenceReason: "육아" });
    expect(await screen.findByText(/휴직 중인 사람의 단계는/)).toBeInTheDocument();
    expect(screen.getByText(/장기 부재/)).toBeInTheDocument();
  });

  it("발령 목록은 사유 원문을 싣지 않고 '사유 보기'를 눌러야 읽는다. 실패하면 사유 없이 오류 문구만 나온다", async () => {
    const row = {
      id: 7,
      kind: "absence_start",
      kindLabel: "휴직",
      subject: "person",
      subjectId: 11,
      subjectName: "휴직자",
      effectiveDate: "2026-03-10",
      summary: "휴직(육아), 사유 있음",
      reason: "",
      createdBy: "관리자",
      status: "applied",
      corrects: 0,
      hasAbsenceReason: true,
    };
    let fail = false;
    get.mockReset();
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/org/tree")) return { root: TREE };
      if (url.startsWith("/api/org/ranks")) return { ranks: [] };
      if (url.startsWith("/api/org/events/7/absence-reason")) {
        if (fail) throw new ApiError(503, "열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요");
        return { eventId: 7, userId: 11, reason: "아이 돌봄" };
      }
      if (url.startsWith("/api/org/events")) return { events: [row], total: 1 };
      return {};
    });
    render(
      <MemoryRouter>
        <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
          <AppointmentsPage />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/휴직\(육아\), 사유 있음/)).toBeInTheDocument();
    expect(screen.queryByText(/아이 돌봄/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "사유 보기" }));
    expect(await screen.findByText(/아이 돌봄/)).toBeInTheDocument();
    fail = true;
    fireEvent.click(screen.getByRole("button", { name: "닫기" }));
    fireEvent.click(screen.getByRole("button", { name: "사유 보기" }));
    expect(await screen.findByText(/열람 기록을 남기지 못해/)).toBeInTheDocument();
    expect(screen.queryByText(/아이 돌봄/)).toBeNull();
  });
});

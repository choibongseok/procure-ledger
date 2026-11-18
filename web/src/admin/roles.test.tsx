import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { PERMISSION_LABELS } from "./auditLabels";
import { RolesPage } from "./RolesPage";
import { SeparationPage } from "./SeparationPage";

vi.mock("../api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
    }
  },
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);
const del = vi.mocked(api.del);

const roles = [
  { id: 1, key: "admin", name: "관리자", description: "", kind: "template", version: 1, permissions: ["role:manage", "audit:read"], grantCount: 1, userCount: 1 },
  { id: 9, key: "r_9", name: "구매 전담", description: "구매만", kind: "custom", version: 3, permissions: ["purchase:create"], grantCount: 1, userCount: 2 },
];
const perms = [
  { code: "purchase:create", group: "purchase", risk: false },
  { code: "audit:read", group: "audit", risk: false },
  { code: "audit:verify", group: "audit", risk: true },
  { code: "role:manage", group: "role", risk: true },
];
const targets = { users: [{ id: 5, name: "김직원", email: "kim@x" }], units: [{ id: 4, name: "구매1팀", kind: "team" }], ranks: [{ id: 1, name: "사원" }] };

function mockAll(requests: unknown[] = []) {
  get.mockImplementation(async (path: string) => {
    if (path === "/api/admin/roles") return { roles };
    if (path === "/api/admin/permissions") return { permissions: perms };
    if (path === "/api/admin/role-targets") return targets;
    if (path === "/api/admin/role-requests") return { requests };
    if (path.endsWith("/grants")) return { grants: [{ id: 21, roleId: 9, targetType: "unit", label: "구매1팀", includeDescendants: true, source: "manual", reason: "구매 조직", users: 7 }] };
    if (path.includes("/access")) return { userId: 5, name: "김직원", roles: ["구매 전담"], permissions: ["purchase:create"], sources: [{ permission: "purchase:create", roleName: "구매 전담", via: "구매1팀 소속" }] };
    return {};
  });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <RolesPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  del.mockReset();
  vi.spyOn(window, "prompt").mockReturnValue(null);
});

describe("역할 관리 화면", () => {
  it("기본 템플릿은 읽기 전용이고 사용자 정의 역할만 지울 수 있다", async () => {
    mockAll();
    renderPage();
    await screen.findByText("구매 전담");
    expect(screen.getByText("기본 템플릿")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "관리자 삭제" })).toBeNull();
    expect(screen.getByRole("button", { name: "구매 전담 삭제" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "관리자 보기·편집" }));
    expect(await screen.findByText(/기본 템플릿, 읽기 전용/)).toBeInTheDocument();
    expect(screen.getByLabelText("이름")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "저장" })).toBeNull();
  });

  it("역할을 만들 때 권한을 그룹별로 고르고 위험 권한 표시가 보인다", async () => {
    mockAll();
    post.mockResolvedValue({ ...roles[1], id: 10 });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "새 역할 만들기" }));
    fireEvent.change(await screen.findByLabelText("이름"), { target: { value: "감사 보조" } });
    expect(screen.getAllByText("위험").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByLabelText(/감사 로그 조회/));
    fireEvent.click(screen.getByRole("button", { name: "만들기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/roles", expect.objectContaining({ name: "감사 보조", permissions: ["audit:read"], version: 0 })));
  });

  it("이미 부여된 역할에 위험 권한을 더하면 승인이 필요하다는 안내와 사유 칸이 나온다", async () => {
    mockAll();
    put.mockResolvedValue({ request: { id: 3 } });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    fireEvent.click(await screen.findByLabelText(/감사 로그 조회/)); // 위험 아님
    expect(screen.queryByLabelText("사유", { selector: "#role-reason" })).toBeNull();
    fireEvent.click(screen.getByLabelText(new RegExp(PERMISSION_LABELS["audit:verify"])));
    expect(await screen.findByText(/다른 관리자의 승인이 필요합니다/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("사유", { selector: "#role-reason" }), { target: { value: "감사 보조 지정" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/roles/9", expect.objectContaining({ reason: "감사 보조 지정", version: 3 })));
    expect(await screen.findByText(/승인을 기다립니다/)).toBeInTheDocument();
  });

  it("부여 전에 영향 인원을 미리 보고, 위험 권한이면 승인 요청으로 안내한다", async () => {
    mockAll();
    post.mockImplementation(async (path: string) => {
      if (path.endsWith("/preview")) return { affectedUsers: 7, newlyGained: 5, sample: ["김직원", "이대리"], riskPermissions: ["audit:verify"], needsApproval: true };
      return { request: { id: 4 }, affectedUsers: 0 };
    });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    expect(await screen.findByText("구매1팀 (하위 포함)")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("대상 유형"), { target: { value: "unit" } });
    fireEvent.change(screen.getByLabelText("대상"), { target: { value: "4" } });
    fireEvent.click(screen.getByLabelText("하위 조직 포함"));
    fireEvent.change(screen.getByLabelText("사유"), { target: { value: "구매 조직 전체" } });
    fireEvent.click(screen.getByRole("button", { name: "영향 미리보기" }));
    const box = await screen.findByTestId("grant-preview");
    expect(within(box).getByText(/영향 7명, 새로 권한이 생기는 사람 5명/)).toBeInTheDocument();
    expect(within(box).getByText(/다른 관리자의 승인이 필요합니다/)).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/admin/roles/9/grants/preview", { targetType: "unit", targetId: 4, unitKind: "", includeDescendants: true, reason: "구매 조직 전체" });
    fireEvent.click(within(box).getByRole("button", { name: "승인 요청 보내기" }));
    expect(await screen.findByText(/다른 관리자의 승인을 기다립니다/)).toBeInTheDocument();
  });

  it("법인이 하나뿐이면 적용 범위를 고르는 칸이 없다(이전과 같은 화면)", async () => {
    mockAll();
    post.mockResolvedValue({ affectedUsers: 3, newlyGained: 3, sample: [], riskPermissions: ["그룹 전체 범위"], needsApproval: true });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    await screen.findByText("구매1팀 (하위 포함)");
    expect(screen.queryByLabelText("적용 범위")).toBeNull();
  });

  it("법인이 둘 이상이면 적용 범위를 고르고 서버로 보낸다", async () => {
    const two = { ...targets, entities: [{ id: 1, code: "MAIN", name: "본사" }, { id: 2, code: "ABC", name: "계열사" }] };
    get.mockImplementation(async (path: string) => {
      if (path === "/api/admin/roles") return { roles };
      if (path === "/api/admin/permissions") return { permissions: perms };
      if (path === "/api/admin/role-targets") return two;
      if (path === "/api/admin/role-requests") return { requests: [] };
      if (path.endsWith("/grants")) return { grants: [{ id: 21, roleId: 9, targetType: "unit", label: "구매1팀", includeDescendants: false, source: "manual", reason: "", users: 7, scope: "entity", scopeLabel: "계열사 법인" }] };
      return {};
    });
    post.mockResolvedValue({ affectedUsers: 3, newlyGained: 3, sample: [], riskPermissions: ["그룹 전체 범위"], needsApproval: true });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    expect(await screen.findByText(/계열사 법인/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("대상"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("적용 범위"), { target: { value: "entity" } });
    fireEvent.change(screen.getByLabelText("법인"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "영향 미리보기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/roles/9/grants/preview", { targetType: "user", targetId: 5, unitKind: "", includeDescendants: false, reason: "", scope: "entity", scopeEntityId: 2 }));
    fireEvent.change(screen.getByLabelText("적용 범위"), { target: { value: "group" } });
    fireEvent.click(screen.getByRole("button", { name: "영향 미리보기" }));
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/api/admin/roles/9/grants/preview", expect.objectContaining({ scope: "group", scopeEntityId: 0 })));
    expect(await screen.findByText(/모든 법인에서 효력/)).toBeInTheDocument();
  });

  it("조직장 부여는 조직 종류를 보내고, 회수 단추가 있다", async () => {
    mockAll();
    post.mockResolvedValue({ affectedUsers: 3, newlyGained: 3, sample: [], riskPermissions: [], needsApproval: false });
    del.mockResolvedValue(undefined);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    await screen.findByText("구매1팀 (하위 포함)");
    fireEvent.change(screen.getByLabelText("대상 유형"), { target: { value: "unit_head" } });
    fireEvent.change(screen.getByLabelText("조직 종류"), { target: { value: "department" } });
    fireEvent.click(screen.getByRole("button", { name: "영향 미리보기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/roles/9/grants/preview", { targetType: "unit_head", targetId: 0, unitKind: "department", includeDescendants: false, reason: "" }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(screen.getByRole("button", { name: "구매1팀 부여 회수" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "구매1팀 부여 회수" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/role-grants/21"));
  });

  it("서버가 거부한 사유(마지막 관리자 보호 등)를 그대로 보인다", async () => {
    mockAll();
    del.mockRejectedValue(new ApiError(409, "현재 상태에서는 할 수 없는 동작입니다: 이 변경으로 역할·부여 관리(role:manage) 권한을 가진 활성 사용자가 한 명도 남지 않게 됩니다"));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "구매 전담 보기·편집" }));
    fireEvent.click(await screen.findByRole("button", { name: "구매1팀 부여 회수" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("한 명도 남지 않게 됩니다");
  });

  it("승인 대기 요청이 있으면 승인·반려할 수 있다", async () => {
    mockAll([{ id: 8, summary: "역할 '감사 검증자' 을(를) 김직원 에게 부여", reason: "감사 담당", riskPermissions: ["audit:verify"], requester: "최관리", requestedAt: "2026-11-12T00:00:00Z", expiresAt: "2026-11-13T00:00:00Z", status: "pending" }]);
    post.mockResolvedValue(undefined);
    renderPage();
    expect(await screen.findByText(/역할 '감사 검증자' 을\(를\) 김직원 에게 부여/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "승인" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/role-requests/8/approve"));
  });

  it("사용자를 고르면 권한이 어디서 왔는지(역할 ← 부여)를 보인다", async () => {
    mockAll();
    renderPage();
    const sel = await screen.findByLabelText("사용자");
    fireEvent.change(sel, { target: { value: "5" } });
    expect(await screen.findByText("구매 전담 ← 구매1팀 소속")).toBeInTheDocument();
  });
});

describe("권한 분리 점검 화면", () => {
  it("충돌 쌍에 걸린 사람과 출처 역할을 보인다", async () => {
    get.mockResolvedValue({
      findings: [
        { rule: { a: "ledger:write", b: "payment:write", reason: "전표를 쓰는 사람이 지급도 합니다" }, count: 1, users: [{ userId: 3, name: "최재무", rolesA: ["재무"], rolesB: ["재무", "회계 팀장"] }] },
        { rule: { a: "role:manage", b: "audit:verify", reason: "x" }, count: 0, users: [] },
      ],
    });
    render(
      <MemoryRouter>
        <SeparationPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("최재무")).toBeInTheDocument();
    expect(screen.getByText("재무, 회계 팀장")).toBeInTheDocument();
    expect(screen.getByText("전표를 쓰는 사람이 지급도 합니다")).toBeInTheDocument();
    expect(screen.queryByText(/역할 관리/, { selector: "h2" })).toBeNull();
  });

  it("걸린 사람이 없으면 안내만 보인다", async () => {
    get.mockResolvedValue({ findings: [{ rule: { a: "role:manage", b: "audit:verify", reason: "x" }, count: 0, users: [] }] });
    render(
      <MemoryRouter>
        <SeparationPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("충돌하는 권한 조합을 가진 사람이 없습니다.")).toBeInTheDocument();
  });
});

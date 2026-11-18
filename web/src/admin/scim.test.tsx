import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { ScimPage, type ScimToken, type SyncLogEntry } from "./ScimPage";

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

let perms: string[] = ["scim:read", "scim:manage"];
vi.mock("../auth/AuthProvider", () => ({
  useAuth: () => ({ can: (p: string) => perms.includes(p) }),
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);
const del = vi.mocked(api.del);

const settings = { enabled: true, groupMode: "off", defaultUnit: "T-PU1", defaultRank: "사원", defaultRole: "staff", adoptExisting: true };
const future = new Date(Date.now() + 90 * 24 * 3600 * 1000).toISOString();
const soon = new Date(Date.now() + 5 * 24 * 3600 * 1000).toISOString();

function tok(p: Partial<ScimToken> & { id: number; name: string }): ScimToken {
  return { source: "entra", scope: "users", prefix: "pls_abcd", createdAt: "2026-11-01T00:00:00Z", expiresAt: future, issuerActive: true, ...p };
}

const overview = (tokens: ScimToken[]) => ({
  endpoint: "https://erp.example/scim/v2", settings, tokens, maxActiveTokens: 2, sources: ["entra", "okta", "google", "other"], scopes: ["users", "users_groups"],
});

const logs: SyncLogEntry[] = [
  { id: 3, at: "2026-11-13T01:00:00Z", source: "entra", tokenName: "엔트라", method: "POST", op: "create", result: "success", httpStatus: 201, scimType: "", summary: "입사 발령: a***@corp.test", warnings: [] },
  { id: 2, at: "2026-11-13T00:50:00Z", source: "okta", tokenName: "오크타", method: "POST", op: "create", result: "conflict", httpStatus: 409, scimType: "uniqueness", summary: "같은 이메일의 사용자가 이미 있습니다", warnings: [] },
  { id: 1, at: "2026-11-13T00:40:00Z", source: "entra", tokenName: "엔트라", method: "PATCH", op: "deactivate", result: "success", httpStatus: 200, scimType: "", summary: "퇴사 발령: b***@corp.test", warnings: ["조직장 공석: 퇴사한 사람이 조직장이었습니다"] },
];

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  del.mockReset();
  perms = ["scim:read", "scim:manage"];
});

describe("계정 동기화(SCIM) 화면", () => {
  it("연결 주소·토큰 목록·상태 배지를 보인다(원문 토큰 없음)", async () => {
    get.mockResolvedValue(overview([tok({ id: 1, name: "엔트라 운영" }), tok({ id: 2, name: "곧 만료", expiresAt: soon }), tok({ id: 3, name: "퇴사자 발급", issuerActive: false }), tok({ id: 4, name: "옛 토큰", revokedAt: "2026-11-02T00:00:00Z" })]));
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("scim-endpoint")).toHaveTextContent("https://erp.example/scim/v2");
    const row = (name: string) => screen.getByText(name).closest("tr") as HTMLElement;
    expect(within(row("엔트라 운영")).getByText("사용 중")).toBeInTheDocument();
    expect(within(row("곧 만료")).getByText("만료 임박")).toBeInTheDocument();
    expect(within(row("퇴사자 발급")).getByText("발급자 퇴사")).toBeInTheDocument();
    expect(within(row("옛 토큰")).getByText("폐기됨")).toBeInTheDocument();
    expect(within(row("옛 토큰")).queryByRole("button", { name: "폐기" })).toBeNull();
    expect(screen.queryByText(/pls_[A-Za-z0-9_-]{20,}/)).toBeNull();
  });

  it("발급하면 원문을 한 번만 보이고, 저장했습니다를 누르면 사라진다", async () => {
    get.mockResolvedValue(overview([]));
    post.mockResolvedValue({ token: "pls_SECRET-ONCE-0123456789", info: {} });
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText(/이름/), { target: { value: "엔트라 운영" } });
    fireEvent.click(screen.getByRole("button", { name: "토큰 발급" }));
    expect(await screen.findByTestId("scim-fresh-token")).toHaveTextContent("pls_SECRET-ONCE-0123456789");
    expect(post).toHaveBeenCalledWith("/api/admin/scim/tokens", { name: "엔트라 운영", source: "entra", scope: "users", days: 180 });
    fireEvent.click(screen.getByRole("button", { name: "저장했습니다" }));
    expect(screen.queryByTestId("scim-fresh-token")).toBeNull();
  });

  it("폐기는 확인 대화상자를 거친다", async () => {
    get.mockResolvedValue(overview([tok({ id: 7, name: "엔트라 운영" })]));
    del.mockResolvedValue(undefined);
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "폐기" }));
    expect(del).not.toHaveBeenCalled();
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "폐기" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/scim/tokens/7"));
  });

  it("상한 초과 같은 서버 거부는 업무 문구로 보인다", async () => {
    get.mockResolvedValue(overview([]));
    post.mockRejectedValue(new ApiError(409, JSON.stringify({ error: "같은 출처의 유효한 연동 토큰이 상한(2개)에 도달했습니다." })));
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText(/이름/), { target: { value: "셋째" } });
    fireEvent.click(screen.getByRole("button", { name: "토큰 발급" }));
    expect(await screen.findByText(/상한\(2개\)에 도달/)).toBeInTheDocument();
  });

  it("scim:manage 가 없으면(감사) 발급·저장·폐기 단추가 없다", async () => {
    perms = ["scim:read"];
    get.mockResolvedValue(overview([tok({ id: 1, name: "엔트라 운영" })]));
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    await screen.findByTestId("scim-endpoint");
    expect(screen.queryByRole("button", { name: "토큰 발급" })).toBeNull();
    expect(screen.queryByRole("button", { name: "설정 저장" })).toBeNull();
    expect(screen.queryByRole("button", { name: "폐기" })).toBeNull();
  });

  it("동기화 기록: 성공·충돌과 경고를 보이고 결과 필터로 다시 불러온다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/admin/scim/log")) {
        return { items: path.includes("result=conflict") ? [logs[1]] : logs };
      }
      return overview([]);
    });
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("tab", { name: "동기화 기록" }));
    const table = await screen.findByRole("table", { name: "동기화 기록" });
    expect(within(table).getAllByText("성공")).toHaveLength(2);
    expect(within(table).getByText("충돌")).toBeInTheDocument();
    expect(within(table).getByText(/조직장 공석/)).toBeInTheDocument();
    expect(within(table).getByText("입사 발령: a***@corp.test")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/결과/), { target: { value: "conflict" } });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "동기화 기록" })).queryByText("성공")).toBeNull());
    expect(get).toHaveBeenCalledWith(expect.stringContaining("result=conflict"));
  });

  it("동기화 기록: 토큰·HTTP 상태·전보 종류를 보이고 기간 필터는 한국 시간 하루로 보낸다", async () => {
    const transfer: SyncLogEntry = { id: 9, at: "2026-11-13T02:00:00Z", source: "entra", tokenName: "엔트라 운영", method: "PATCH", op: "transfer", result: "success", httpStatus: 200, scimType: "", summary: "전보 발령: c***@corp.test", warnings: ["진행 중 결재 2건이 이 사람의 결재 차례입니다"] };
    get.mockImplementation(async (path: string) => (path.startsWith("/api/admin/scim/log") ? { items: [transfer] } : overview([])));
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("tab", { name: "동기화 기록" }));
    const table = await screen.findByRole("table", { name: "동기화 기록" });
    expect(within(table).getByText("전보")).toBeInTheDocument();
    expect(within(table).getByText("엔트라 운영")).toBeInTheDocument();
    expect(within(table).getByText("200")).toBeInTheDocument();
    expect(within(table).getByText(/진행 중 결재 2건/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/시작일/), { target: { value: "2026-11-12" } });
    fireEvent.change(screen.getByLabelText(/종료일/), { target: { value: "2026-11-13" } });
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringContaining("from=2026-11-11T15%3A00%3A00.000Z")));
    expect(get).toHaveBeenCalledWith(expect.stringContaining("to=2026-11-13T15%3A00%3A00.000Z"));
  });

  it("그룹 매핑: 방식이 꺼져 있으면 안내만 보이고, 켜져 있으면 매핑 표와 위험 역할 비활성 선택지를 보인다", async () => {
    const base = { mode: "role", unmapped: [{ source: "entra", groupExternalId: "oid-9", displayName: "oid-9", at: "2026-11-13T00:00:00Z" }], units: [{ id: 3, code: "T-PU2", name: "구매2팀" }],
      roles: [{ id: 1, key: "admin", name: "관리자", risky: true }, { id: 4, key: "manager", name: "결재자", risky: false }],
      maps: [{ id: 5, source: "okta", groupExternalId: "00g-1", displayName: "결재자 그룹", kind: "role", roleName: "결재자", members: 3 }] };
    get.mockImplementation(async (path: string) => (path.startsWith("/api/admin/scim/group-maps") ? base : overview([])));
    post.mockResolvedValue({ id: 6 });
    del.mockResolvedValue(undefined);
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("tab", { name: "그룹 매핑" }));
    const table = await screen.findByRole("table", { name: "그룹 매핑" });
    expect(within(table).getByText("3명")).toBeInTheDocument();
    expect(within(table).getByText("결재자")).toBeInTheDocument();
    const risky = screen.getByRole("option", { name: /관리자 \(위험 권한 포함/ }) as HTMLOptionElement;
    expect(risky.disabled).toBe(true);
    // 매핑이 없는 그룹에서 "매핑 만들기"를 누르면 양식이 채워진다.
    fireEvent.click(screen.getByRole("button", { name: "매핑 만들기" }));
    expect((screen.getByLabelText("그룹 식별자") as HTMLInputElement).value).toBe("oid-9");
    fireEvent.change(screen.getByLabelText("연결할 역할"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "연결" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/scim/group-maps", { source: "entra", groupExternalId: "oid-9", displayName: "oid-9", unitId: 0, roleId: 4 }));
    // 삭제는 확인 대화상자를 거친다.
    fireEvent.click(within(table).getByRole("button", { name: "삭제" }));
    expect(del).not.toHaveBeenCalled();
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "삭제" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/scim/group-maps/5"));
  });

  it("그룹 연동이 꺼져 있으면 매핑 탭에 안내만 있다", async () => {
    get.mockImplementation(async (path: string) => (path.startsWith("/api/admin/scim/group-maps") ? { mode: "off", maps: [], unmapped: [], units: [], roles: [] } : overview([])));
    render(
      <MemoryRouter>
        <ScimPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("tab", { name: "그룹 매핑" }));
    expect(await screen.findByText(/그룹 연동이 꺼져 있습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "연결" })).toBeNull();
  });
});

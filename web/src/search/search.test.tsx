import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { SearchPage } from "./SearchPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return {
    ...mod,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    apiDownload: vi.fn(),
    apiFetch: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  };
});
const get = vi.mocked(api.get);
const user: CurrentUser = {
  id: 1,
  name: "김직원",
  role: "staff",
  departmentId: 1,
};

function mount(perms: string[], url = "/search?q=노트북") {
  return render(
    <AuthProvider initialUser={user} initialPermissions={perms as never}>
      <MemoryRouter initialEntries={[url]}>
        <SearchPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

const prs = [
  { id: 7, no: "PR-2026-0007", title: "노트북 구매", status: "approved" },
];
const docs = {
  rows: [
    {
      id: 9,
      no: "DOC-1",
      title: "노트북 반출 신청",
      formName: "일반 기안",
      formKind: "general",
      requesterName: "김직원",
      status: "done",
    },
  ],
};
const people = {
  people: [
    {
      id: 4,
      name: "노트북팀장",
      rank: "팀장",
      primary: { name: "구매1팀", fullPath: "회사 > 구매1팀" },
    },
  ],
};

function answer(path: string) {
  if (path.startsWith("/api/purchase-requests")) return Promise.resolve(prs);
  if (path.startsWith("/api/docs/search")) return Promise.resolve(docs);
  return Promise.resolve(people);
}

describe("통합 검색", () => {
  beforeEach(() => {
    get.mockReset();
  });
  it("권한이 있는 세 절을 그리고 행은 상세 링크다", async () => {
    get.mockImplementation(answer);
    mount(["purchase:read", "draft:read", "org:read"]);
    const pr = screen.getByRole("region", { name: "구매요청" });
    expect(await within(pr).findByText("노트북 구매")).toBeTruthy();
    expect(within(pr).getByText("노트북 구매").getAttribute("href")).toBe(
      "/purchase-requests/7",
    );
    expect(
      within(await screen.findByRole("region", { name: "문서" }))
        .getByText("노트북 반출 신청")
        .getAttribute("href"),
    ).toBe("/docs/9");
    expect(
      within(await screen.findByRole("region", { name: "사람" }))
        .getByText("노트북팀장")
        .getAttribute("href"),
    ).toBe("/org?person=4");
  });
  it("권한 없는 절은 그리지 않고 API 도 부르지 않는다", async () => {
    get.mockImplementation(answer);
    mount(["purchase:read"]);
    await screen.findByText("노트북 구매");
    expect(screen.queryByRole("region", { name: "문서" })).toBeNull();
    expect(screen.queryByRole("region", { name: "사람" })).toBeNull();
    expect(
      get.mock.calls
        .map((c) => c[0] as string)
        .every((p) => p.startsWith("/api/purchase-requests")),
    ).toBe(true);
  });
  it("0건이면 절마다 빈 상태를 보인다", async () => {
    get.mockImplementation(async (path: string) =>
      path.startsWith("/api/org")
        ? { people: [] }
        : path.startsWith("/api/docs")
          ? { rows: [] }
          : [],
    );
    mount(["purchase:read", "draft:read", "org:read"]);
    for (const name of ["구매요청", "문서", "사람"]) {
      const sec = screen.getByRole("region", { name });
      await waitFor(() =>
        expect(within(sec).getByText("검색 결과가 없습니다")).toBeTruthy(),
      );
    }
  });
  it("전체가 권한 밖이면 안내 한 줄만 보이고 아무것도 부르지 않는다", () => {
    mount([]);
    expect(screen.getByText(/검색할 수 있는 항목이 없습니다/)).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  });
  it("문서는 한 글자 검색어로 부르지 않는다", async () => {
    get.mockImplementation(answer);
    mount(["draft:read"], "/search?q=a");
    expect(await screen.findByText(/2자 이상/)).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  });
});

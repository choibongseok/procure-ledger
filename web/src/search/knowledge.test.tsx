import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
const user: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

function mount(perms: string[], url = "/search?q=장비 교체") {
  return render(
    <AuthProvider initialUser={user} initialPermissions={perms as never}>
      <MemoryRouter initialEntries={[url]}>
        <SearchPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

const hit = (n: number) => ({
  kind: "approval_doc",
  kindLabel: "결재 문서",
  title: `점검 ${n}`,
  docNo: `AB-2026-000${n}`,
  snippet: `…장비 교체 절차 ${n}…`,
  source: `결재 문서 AB-2026-000${n} 「점검 ${n}」 · 본문`,
  link: `/docs/${n}`,
});

beforeEach(() => {
  get.mockReset();
});

describe("통합 검색의 내용 검색 절(B47)", () => {
  it("권한이 없으면 절을 그리지 않고 API 도 부르지 않는다", () => {
    mount(["purchase:read"]);
    expect(screen.queryByRole("region", { name: "내용 검색" })).toBeNull();
    expect(get.mock.calls.some(([p]) => String(p).startsWith("/api/knowledge/search"))).toBe(false);
  });

  it("출처 한 줄·발췌·문서 열기 링크를 보이고, 더 보기는 건수 없이 이어 본다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/knowledge/search") && path.includes("offset=0")) return { hits: [hit(1), hit(2)], more: true };
      if (path.startsWith("/api/knowledge/search")) return { hits: [hit(3)], more: false };
      return {};
    });
    mount(["knowledge:search"]);
    const section = await screen.findByRole("region", { name: "내용 검색" });
    await within(section).findByText("결재 문서 AB-2026-0001 「점검 1」 · 본문");
    expect(within(section).getAllByRole("link", { name: "문서 열기" }).map((a) => a.getAttribute("href"))).toEqual(["/docs/1", "/docs/2"]);
    expect(within(section).getByText(/…장비 교체 절차 1…/)).toBeInTheDocument();
    expect(within(section).queryByText(/건$/)).toBeNull(); // 총 건수 표시 없음
    fireEvent.click(within(section).getByRole("button", { name: "더 보기" }));
    await within(section).findByText("결재 문서 AB-2026-0003 「점검 3」 · 본문");
    expect(within(section).queryByRole("button", { name: "더 보기" })).toBeNull();
    expect(get).toHaveBeenCalledWith(expect.stringContaining("offset=2"));
    expect(within(section).getByText(/글자를 읽지 못하는 파일/)).toBeInTheDocument();
    expect(section.textContent).not.toMatch(/approval_doc|attachment/); // 내부 종류 코드를 보이지 않는다
  });

  it("결과가 없으면 한 가지 문구만 보이고, 오류와 2자 미만 검색어는 정해진 문구를 보인다", async () => {
    get.mockResolvedValue({ hits: [], more: false });
    const { unmount } = mount(["knowledge:search"]);
    await screen.findByText("일치하는 내용을 찾지 못했습니다.");
    unmount();
    get.mockReset();
    get.mockRejectedValue(new Error("boom"));
    const second = mount(["knowledge:search"]);
    await waitFor(() => expect(screen.getByText("내용 검색을 불러오지 못했습니다. 잠시 뒤 다시 시도하세요.")).toBeInTheDocument());
    second.unmount();
    get.mockReset();
    mount(["knowledge:search"], "/search?q=장");
    expect(screen.getByText("내용 검색은 2자 이상 입력하세요.")).toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });
});

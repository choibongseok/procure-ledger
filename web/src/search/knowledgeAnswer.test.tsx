import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
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
const post = vi.mocked(api.post);
const user: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

function mount() {
  return render(
    <AuthProvider initialUser={user} initialPermissions={["knowledge:search"] as never}>
      <MemoryRouter initialEntries={["/search?q=출장 숙박비"]}>
        <SearchPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  get.mockImplementation(async (path: string) => (path.startsWith("/api/knowledge/search") ? { hits: [], more: false } : {}));
});

describe("질문하기(B47 3단계)", () => {
  it("답과 출처 목록을 보이고 근거 없는 내용을 뺐다고 알린다", async () => {
    post.mockResolvedValue({ text: "숙박비는 십이만 원까지이다. [1]", found: true, sources: [{ n: 1, source: "규정 「출장비 규정」 제1조", link: "/knowledge/3", kindLabel: "규정" }] });
    mount();
    const section = await screen.findByRole("region", { name: "내용 검색" });
    fireEvent.click(within(section).getByRole("button", { name: "질문하기" }));
    const card = await screen.findByRole("region", { name: "답변" });
    expect(within(card).getByText("숙박비는 십이만 원까지이다. [1]")).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "규정 「출장비 규정」 제1조" })).toHaveAttribute("href", "/knowledge/3");
    expect(within(card).getByText("근거 없는 내용은 답에서 뺐습니다.")).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/knowledge/answer", { question: "출장 숙박비" });
  });

  it("찾지 못하면 고정 문구만 보이고 출처 목록은 없다", async () => {
    post.mockResolvedValue({ text: "사내 문서에서 근거가 되는 내용을 찾지 못했습니다.", found: false, sources: [] });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "질문하기" }));
    const card = await screen.findByRole("region", { name: "답변" });
    expect(within(card).getByText("사내 문서에서 근거가 되는 내용을 찾지 못했습니다.")).toBeInTheDocument();
    expect(within(card).queryByRole("list")).toBeNull();
  });

  it("실패하면 정해진 문구를 보이고, 한도 초과는 서버 문구를 그대로 보인다", async () => {
    post.mockRejectedValueOnce(new ApiError(500, "내부 오류 상세"));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "질문하기" }));
    expect(await screen.findByText("답변을 만들지 못했습니다. 검색 결과를 직접 확인하세요.")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("내부 오류 상세");
    post.mockRejectedValueOnce(new ApiError(429, "오늘 사용 한도를 넘었습니다"));
    fireEvent.click(screen.getByRole("button", { name: "질문하기" }));
    await waitFor(() => expect(screen.getByText("오늘 사용 한도를 넘었습니다")).toBeInTheDocument());
  });
});

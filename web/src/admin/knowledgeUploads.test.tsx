import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, apiFetch } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { KnowledgeDocPage } from "../search/KnowledgeDocPage";
import { KnowledgeUploads } from "./KnowledgeUploads";

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
const put = vi.mocked(api.put);
const del = vi.mocked(api.del);
const fetchMock = vi.mocked(apiFetch);
const user: CurrentUser = { id: 2, name: "관리자", role: "admin", departmentId: 1 };

const row = (over: Record<string, unknown> = {}) => ({
  id: 7,
  entityId: 1,
  entityName: "에이엑스",
  kind: "regulation",
  kindLabel: "규정",
  title: "구매 규정",
  version: 2,
  roles: "finance,staff",
  departments: "구매팀",
  filename: "구매규정.pdf",
  size: 1000,
  uploadedByName: "관리자",
  uploadedAt: "2026-11-14T01:00:00Z",
  state: "ready",
  note: "",
  ...over,
});

function mount(ui: React.ReactNode, path = "/") {
  return render(
    <AuthProvider initialUser={user} initialPermissions={["knowledge:manage", "knowledge:search"] as never}>
      <MemoryRouter initialEntries={[path]}>
        <ToastProvider>{ui}</ToastProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  put.mockReset();
  del.mockReset();
  fetchMock.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path.includes("/uploads")) return { uploads: [row(), row({ id: 8, title: "스캔본", state: "no_text", version: 1, roles: "all", departments: "all", note: "글자 없음" })] };
    if (path.includes("/departments")) return { departments: ["구매팀", "영업팀"] };
    return {};
  });
});

describe("업로드 매뉴얼·규정 관리(B47)", () => {
  it("목록은 한국어 공개 범위·검색 상태를 보이고 내부 코드를 보이지 않는다", async () => {
    mount(<KnowledgeUploads />);
    const table = await screen.findByRole("table", { name: "업로드 매뉴얼·규정" });
    expect(within(table).getByText("재무·직원 · 구매팀")).toBeInTheDocument();
    expect(within(table).getByText("모든 직원 · 모든 부서")).toBeInTheDocument();
    expect(within(table).getByText("검색됨")).toBeInTheDocument();
    expect(within(table).getAllByText("글자 없음").length).toBeGreaterThan(0);
    expect(within(table).getByText("v2")).toBeInTheDocument();
    expect(table.textContent).not.toMatch(/no_text|ready|regulation|finance/);
  });

  it("파일 없이 올리면 안내하고, 올리면 multipart 로 보낸다", async () => {
    fetchMock.mockResolvedValue({});
    mount(<KnowledgeUploads />);
    const form = await screen.findByRole("form", { name: "업로드 양식" });
    fireEvent.click(within(form).getByRole("button", { name: "올리기" }));
    expect(await screen.findByText("파일을 선택하세요.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("파일"), { target: { files: [new File(["## 제1조\n내용"], "규정.md", { type: "text/markdown" })] } });
    fireEvent.change(within(form).getByLabelText("제목"), { target: { value: "새 규정" } });
    fireEvent.click(within(form).getByLabelText("모든 직원"));
    fireEvent.click(within(form).getByLabelText("재무"));
    fireEvent.click(within(form).getByRole("button", { name: "올리기" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, init] = fetchMock.mock.calls[0] as [string, { method: string; body: FormData }];
    expect(path).toBe("/api/admin/knowledge/uploads");
    expect(init.body.get("title")).toBe("새 규정");
    expect(init.body.get("kind")).toBe("regulation");
    expect(init.body.get("roles")).toBe("finance");
    expect(init.body.get("departments")).toBe("all");
    expect((init.body.get("file") as File).name).toBe("규정.md");
  });

  it("업로드 응답의 개인정보 경고는 닫을 때까지 보이고, 색인 대기 안내가 목록에 보인다", async () => {
    fetchMock.mockResolvedValue({ warnings: ["이 파일에 이메일·전화번호·계좌번호로 보이는 내용이 있습니다."] });
    get.mockImplementation(async (path: string) => {
      if (path.includes("/uploads")) return { uploads: [row({ id: 9, state: "pending", note: "", notice: "색인 중입니다. 보통 몇 초 안에 검색에 나옵니다." })] };
      if (path.includes("/departments")) return { departments: ["구매팀"] };
      return {};
    });
    mount(<KnowledgeUploads />);
    expect(await screen.findByText("색인 중입니다. 보통 몇 초 안에 검색에 나옵니다.")).toBeInTheDocument();
    const form = await screen.findByRole("form", { name: "업로드 양식" });
    fireEvent.change(within(form).getByLabelText("파일"), { target: { files: [new File(["## 제1조\n010-1234-5678"], "연락망.md", { type: "text/markdown" })] } });
    fireEvent.change(within(form).getByLabelText("제목"), { target: { value: "연락망" } });
    fireEvent.click(within(form).getByRole("button", { name: "올리기" }));
    expect(await screen.findByText("이 파일에 이메일·전화번호·계좌번호로 보이는 내용이 있습니다.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "확인했습니다" }));
    expect(screen.queryByText("이 파일에 이메일·전화번호·계좌번호로 보이는 내용이 있습니다.")).toBeNull();
  });

  it("공개 범위 변경·삭제는 서버에 요청한다. 서버의 거부 문구는 그대로 보인다", async () => {
    put.mockResolvedValue({});
    del.mockResolvedValue(undefined);
    mount(<KnowledgeUploads />);
    const table = await screen.findByRole("table", { name: "업로드 매뉴얼·규정" });
    fireEvent.click(within(table).getAllByRole("button", { name: "공개 범위" })[0]);
    const group = await screen.findByRole("group", { name: "공개 범위 변경" });
    fireEvent.click(within(group).getByLabelText("모든 직원"));
    fireEvent.click(within(group).getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/knowledge/uploads/7/scope", { roles: "all", departments: "구매팀" }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(within(table).getAllByRole("button", { name: "삭제" })[1]).toBeEnabled());
    fireEvent.click(within(table).getAllByRole("button", { name: "삭제" })[1]);
    const dlg = await screen.findByRole("alertdialog");
    fireEvent.click(within(dlg).getByRole("button", { name: "삭제" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/knowledge/uploads/8"));
  });
});

describe("업로드 문서 원문 보기", () => {
  it("구역을 쪽·조항·소제목 이름으로 보이고 원본 내려받기가 있다", async () => {
    get.mockResolvedValue({ id: 7, kind: "regulation", kindLabel: "규정", title: "구매 규정", version: 2, uploadedAt: "2026-11-14T01:00:00Z", filename: "a.pdf", sections: [
      { locatorKind: "clause", locator: "1쪽 제1조", heading: "목적", text: "이 규정은 구매 절차를 정한다" },
      { locatorKind: "page", locator: "3", heading: "", text: "쪽 본문" },
    ] });
    mount(
      <Routes>
        <Route path="/knowledge/:id" element={<KnowledgeDocPage />} />
      </Routes>,
      "/knowledge/7",
    );
    await screen.findByRole("heading", { name: "구매 규정" });
    expect(screen.getByRole("heading", { name: "1쪽 제1조 (목적)" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "3쪽" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "원본 내려받기" })).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/clause|locator/);
  });

  it("권한 밖·지운 문서는 같은 문구만 보인다", async () => {
    const { ApiError } = await import("../api/client");
    get.mockRejectedValue(new ApiError(404, "찾을 수 없습니다"));
    mount(
      <Routes>
        <Route path="/knowledge/:id" element={<KnowledgeDocPage />} />
      </Routes>,
      "/knowledge/9",
    );
    expect(await screen.findByText("문서를 찾을 수 없습니다.")).toBeInTheDocument();
  });
});

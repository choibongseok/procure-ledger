import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, apiFetch } from "../api/client";
import { EvalSetPage, EvalSetsPage } from "./EvalSetPages";

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
  apiFetch: vi.fn(),
  apiDownload: vi.fn(),
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

let perms: string[] = [];
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: (p: string) => perms.includes(p) }) }));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);

const set = { id: 1, code: "faq", name: "자주 묻는 질문", purpose: "text", description: "", currentVersion: 1, itemCount: 2, draftVersion: 0, baselineRunId: 0, gateDropPct: 2, active: true };
const item = { id: 11, seq: 1, question: "휴가 신청은 어떻게 하나요?", context: "", expected: "결재 문서로 신청", grader: "contains", tolerance: "", rubric: "", critical: true, tags: "", maskedCount: 1 };
const published = { ...set, versions: [{ version: 1, status: "published", revision: 3, itemCount: 1, itemsHash: "abc", note: "첫 게시", publishedAt: "t" }], selected: { version: 1, status: "published", revision: 3, itemCount: 1, itemsHash: "abc", note: "첫 게시", publishedAt: "t" }, items: [item] };
const draft = { ...set, currentVersion: 0, draftVersion: 1, versions: [{ version: 1, status: "draft", revision: 4, itemCount: 1, itemsHash: "", note: "", publishedAt: "" }], selected: { version: 1, status: "draft", revision: 4, itemCount: 1, itemsHash: "", note: "", publishedAt: "" }, items: [item] };

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  vi.mocked(apiFetch).mockReset();
  perms = ["eval:read", "eval:write"];
});

function renderSet() {
  return render(
    <MemoryRouter initialEntries={["/admin/eval/sets/faq"]}>
      <Routes>
        <Route path="/admin/eval/sets/:code" element={<EvalSetPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("평가 세트 목록 화면", () => {
  it("한국어 용도·상태로 보이고 영문 코드가 없으며 만들기가 API 로 간다", async () => {
    get.mockResolvedValue({ sets: [set] });
    post.mockResolvedValue({});
    const { container } = render(
      <MemoryRouter>
        <EvalSetsPage />
      </MemoryRouter>,
    );
    const table = await screen.findByRole("table", { name: "평가 세트 목록" });
    expect(await within(table).findByText("일반 질의")).toBeTruthy();
    expect(await within(table).findByText("v1")).toBeTruthy();
    expect(container.textContent).not.toMatch(/\b(text|published|eval:)/);
    const form = screen.getByRole("form", { name: "세트 양식" });
    fireEvent.change(within(form).getByLabelText("세트 코드"), { target: { value: "ops" } });
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "운영 점검" } });
    fireEvent.change(within(form).getByLabelText("용도"), { target: { value: "doc" } });
    fireEvent.click(within(form).getByRole("button", { name: "만들기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/eval/sets", expect.objectContaining({ code: "ops", name: "운영 점검", purpose: "doc" })));
  });

  it("읽기 권한만 있으면 만들기 양식이 없다", async () => {
    perms = ["eval:read"];
    get.mockResolvedValue({ sets: [] });
    render(
      <MemoryRouter>
        <EvalSetsPage />
      </MemoryRouter>,
    );
    await screen.findByText(/평가 세트가 없습니다/);
    expect(screen.queryByRole("form", { name: "세트 양식" })).toBeNull();
  });
});

describe("평가 세트 상세 화면", () => {
  it("게시된 버전은 고칠 수 없고 새 초안 만들기만 있다", async () => {
    get.mockResolvedValue(published);
    renderSet();
    await screen.findByRole("table", { name: "문항 목록" });
    expect(screen.queryByRole("button", { name: /문항 수정/ })).toBeNull();
    expect(screen.getByText(/게시된 버전은 고칠 수 없습니다/)).toBeTruthy();
    expect(screen.getByText("가림 1")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "새 초안 만들기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/eval/sets/faq/draft", {}));
  });

  it("초안에서 문항을 추가하면 수정 번호와 함께 저장하고 가림 안내를 보인다", async () => {
    get.mockResolvedValue(draft);
    post.mockResolvedValue({ warnings: ["개인정보 1건을 가려서 저장했습니다."] });
    renderSet();
    await screen.findByRole("table", { name: "문항 목록" });
    fireEvent.click(screen.getByRole("button", { name: "문항 추가" }));
    const form = screen.getByRole("form", { name: "문항 양식" });
    fireEvent.change(within(form).getByLabelText("질문"), { target: { value: "연차는 며칠인가요?" } });
    fireEvent.change(within(form).getByLabelText("기대 답"), { target: { value: "15" } });
    fireEvent.change(within(form).getByLabelText("채점 기준"), { target: { value: "number" } });
    fireEvent.change(within(form).getByLabelText("허용 오차"), { target: { value: "abs:1" } });
    fireEvent.click(within(form).getByRole("button", { name: "저장" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/eval/sets/faq/items", expect.objectContaining({ question: "연차는 며칠인가요?", grader: "number", tolerance: "abs:1", revision: 4 })));
    await screen.findByText(/개인정보 1건을 가려서 저장했습니다/);
  });

  it("용도에 맞지 않는 채점 기준은 고를 수 없다(규정 검색은 포함만)", async () => {
    get.mockResolvedValue({ ...draft, purpose: "policy" });
    renderSet();
    await screen.findByRole("table", { name: "문항 목록" });
    fireEvent.click(screen.getByRole("button", { name: "문항 추가" }));
    const sel = within(screen.getByRole("form", { name: "문항 양식" })).getByLabelText("채점 기준") as HTMLSelectElement;
    expect([...sel.options].map((o) => o.value)).toEqual(["contains"]);
  });
  it("LLM 채점은 기대 답을 비워 둘 수 있고 안내가 보이며, 다른 채점은 기대 답이 필수다", async () => {
    get.mockResolvedValue(draft);
    renderSet();
    await screen.findByRole("table", { name: "문항 목록" });
    fireEvent.click(screen.getByRole("button", { name: "문항 추가" }));
    const form = screen.getByRole("form", { name: "문항 양식" });
    const expected = within(form).getByLabelText("기대 답") as HTMLTextAreaElement;
    expect(expected.required).toBe(true);
    fireEvent.change(within(form).getByLabelText("채점 기준"), { target: { value: "llm" } });
    expect((within(form).getByLabelText("기대 답") as HTMLTextAreaElement).required).toBe(false);
    expect(within(form).getByText(/LLM 채점은 비워 둘 수 있습니다/)).toBeTruthy();
    fireEvent.change(within(form).getByLabelText("채점 기준"), { target: { value: "exact" } });
    expect((within(form).getByLabelText("기대 답") as HTMLTextAreaElement).required).toBe(true);
  });
});

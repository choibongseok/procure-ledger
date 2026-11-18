import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { KnowledgeAdminPage } from "./KnowledgeAdminPage";

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
const put = vi.mocked(api.put);
const user: CurrentUser = { id: 2, name: "관리자", role: "admin", departmentId: 1 };

const status = {
  docs: { approval_doc: 12, attachment: 0, upload: 0 },
  states: { ready: 12, no_text: 2 },
  chunks: 40,
  queueOpen: 3,
  oldestQueue: "2026-11-14T01:00:00Z",
  generation: 5,
  changedAt: "2026-11-14T02:00:00Z",
};
const forms = [
  { code: "GEN", name: "일반 기안서", active: true, searchable: true },
  { code: "HR", name: "인사 발령 품의", active: true, searchable: false },
];

function mount() {
  return render(
    <AuthProvider initialUser={user} initialPermissions={["knowledge:manage"] as never}>
      <MemoryRouter>
        <ToastProvider>
          <KnowledgeAdminPage />
        </ToastProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  get.mockImplementation(async (path: string) => (path.startsWith("/api/admin/knowledge/status") ? status : forms));
});

describe("관리 > 지식 색인(B47)", () => {
  it("색인 현황을 한국어 이름으로 보이고 제목은 보이지 않는다", async () => {
    mount();
    const table = await screen.findByRole("table", { name: "색인 현황" });
    expect(within(table).getByText("결재 문서 본문")).toBeInTheDocument();
    expect(within(table).getByText("12건")).toBeInTheDocument();
    expect(screen.getByText(/글자 없음\(스캔본 등\): 2건/)).toBeInTheDocument();
    expect(screen.getByText(/처리 대기 3건/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/approval_doc|no_text|unreadable/);
  });

  it("다시 색인은 확인을 거쳐 서버에 요청하고 결과 건수를 알린다", async () => {
    post.mockResolvedValue({ done: 4, skipped: 8 });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "다시 색인" }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.click(within(dlg).getByRole("button", { name: "다시 색인" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/knowledge/reindex", { entity: "", kind: "" }));
    await screen.findByText(/처리 4건, 내용이 같아 건너뜀 8건/);
  });

  it("양식별 내용 검색 포함을 바꾸면 서버에 값을 보낸다", async () => {
    put.mockResolvedValue({ searchable: false });
    mount();
    const row = (await screen.findByText("일반 기안서")).closest("tr") as HTMLElement;
    fireEvent.click(within(row).getByRole("checkbox"));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/knowledge/forms/GEN", { searchable: false }));
  });
});

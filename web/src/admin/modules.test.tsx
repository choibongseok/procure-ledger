import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { ModulesPage } from "./ModulesPage";

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
const post = vi.mocked(api.post);
const user: CurrentUser = { id: 2, name: "관리자", role: "admin", departmentId: 1 };

function mount() {
  return render(
    <AuthProvider initialUser={user} initialPermissions={["attendance:manage_module", "role:manage"] as never}>
      <MemoryRouter>
        <ToastProvider>
          <ModulesPage />
        </ToastProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}

let enabled = false;
let ready = true;

beforeEach(() => {
  enabled = false;
  ready = true;
  get.mockReset();
  put.mockReset();
  post.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path === "/api/admin/modules") return { modules: [{ key: "attendance", name: "근태·휴가", description: "휴가 신청과 출퇴근 기록", enabled }] };
    if (path.endsWith("/checklist"))
      return {
        ready,
        items: [
          { label: "2026년 공휴일 등록", required: true, ok: true, detail: "등록된 공휴일 12건" },
          { label: "모든 재직자의 조직 등록(입사일)", required: true, ok: ready, detail: ready ? "조직에 등록되지 않은 사용자 0명" : "조직에 등록되지 않은 사용자 3명" },
        ],
      };
    if (path.endsWith("/hr-template")) return { name: "인사 담당", description: "인사 담당 역할", permissions: ["attendance:hr", "attendance:export", "org:history"] };
    return {};
  });
});

describe("업무 모듈 화면(B50)", () => {
  it("꺼진 모듈은 점검표를 보이고, 필수 항목이 비면 켜기를 막는다", async () => {
    ready = false;
    mount();
    const card = await screen.findByRole("region", { name: "근태·휴가" });
    expect(await within(card).findByText("필수 미비")).toBeInTheDocument();
    expect(within(card).getByText(/조직에 등록되지 않은 사용자 3명/)).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "켜기" })).toBeDisabled();
    expect(within(card).getByText(/운영 전에 노무 검토가 필요합니다/)).toBeInTheDocument();
    expect(card.textContent).not.toMatch(/attendance|enabled|manage_module/);
  });

  it("점검표를 통과하면 켜고, 켜진 모듈은 확인 뒤에 끈다", async () => {
    put.mockImplementation(async (_p: string, body: unknown) => {
      enabled = (body as { enabled: boolean }).enabled;
      return { enabled };
    });
    mount();
    const card = await screen.findByRole("region", { name: "근태·휴가" });
    const on = await within(card).findByRole("button", { name: "켜기" });
    await waitFor(() => expect(on).toBeEnabled());
    fireEvent.click(on);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/modules/attendance", { enabled: true }));
    fireEvent.click(await within(await screen.findByRole("region", { name: "근태·휴가" })).findByRole("button", { name: "끄기" }));
    const dlg = await screen.findByRole("alertdialog");
    expect(within(dlg).getByText(/데이터는 지워지지 않으며/)).toBeInTheDocument();
    fireEvent.click(within(dlg).getByRole("button", { name: "끄기" }));
    await waitFor(() => expect(put).toHaveBeenLastCalledWith("/api/admin/modules/attendance", { enabled: false }));
  });

  it("인사 담당 역할 만들기는 기존 역할 API 로 템플릿을 보낸다. 서버 거부 문구는 업무 문구로 보인다", async () => {
    post.mockResolvedValueOnce({});
    mount();
    const card = await screen.findByRole("region", { name: "근태·휴가" });
    fireEvent.click(await within(card).findByRole("button", { name: "「인사 담당」 역할 만들기" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/api/admin/roles", { name: "인사 담당", description: "인사 담당 역할", permissions: ["attendance:hr", "attendance:export", "org:history"] }),
    );
    const { ApiError } = await import("../api/client");
    put.mockRejectedValueOnce(new ApiError(409, "필수 점검 항목을 먼저 마치세요: 2026년 공휴일 등록"));
    fireEvent.click(await within(card).findByRole("button", { name: "켜기" }));
    expect(await screen.findByText(/필수 점검 항목을 먼저 마치세요/)).toBeInTheDocument();
  });
});

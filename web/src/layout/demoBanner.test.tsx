import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { Layout } from "./Layout";

vi.mock("../api/client", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

const get = vi.mocked(api.get);
beforeEach(() => get.mockReset());

function renderLayout() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <AuthProvider initialUser={{ id: 1, name: "구매직원", role: "staff", departmentId: 1 }}>
        <Routes>
          <Route element={<Layout menu={[]} />}>
            <Route path="/" element={<p>본문</p>} />
          </Route>
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

// B38 D5: 설치 때 데모 데이터를 고른 시연 환경이면 모든 화면 위에 배너가 영구 표시된다.
describe("시연 환경 배너", () => {
  it("demoMode 가 켜져 있으면 배너를 보인다", async () => {
    get.mockResolvedValue({ demoMode: true });
    renderLayout();
    expect(await screen.findByText(/시연 환경: 데모 데이터가 들어 있습니다/)).toBeInTheDocument();
  });

  it("꺼져 있거나 설정을 읽지 못하면 아무것도 그리지 않는다", async () => {
    get.mockResolvedValue({ demoMode: false });
    const { unmount } = renderLayout();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/auth/config"));
    expect(screen.queryByText(/시연 환경/)).toBeNull();
    unmount();
    get.mockImplementation((path: string) => (path === "/api/auth/config" ? Promise.reject(new Error("x")) : Promise.resolve({})) as never);
    renderLayout();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/auth/config"));
    expect(screen.queryByText(/시연 환경/)).toBeNull();
    expect(screen.getByText("본문")).toBeInTheDocument();
  });
});

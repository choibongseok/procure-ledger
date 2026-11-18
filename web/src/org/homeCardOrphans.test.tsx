import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ROLES, type Role } from "../auth/permissions";
import { HomePage } from "../layout/HomePage";
import { orgHomeCards } from "./homeCards";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;

function renderHome(role: Role) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "홍길동", role, departmentId: 1 }}>
        <HomePage cards={orgHomeCards} />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
});

describe("관리자 홈 카드 결재·합의자 소멸(B22 7.2)", () => {
  it("제목과 N건, 점검 탭 링크가 홈에 그려진다", async () => {
    get.mockResolvedValue([{ prId: 1 }, { prId: 1 }, { prId: 2 }]);
    renderHome("admin");
    const card = screen.getByLabelText("결재·합의자 소멸");
    expect(card).toHaveTextContent("결재·합의자 소멸");
    expect(await screen.findByText("3건")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "점검 탭 열기" })).toHaveAttribute("href", "/org/manage?tab=check");
  });

  it("0건이면 홈 카드가 없음을 보인다", async () => {
    get.mockResolvedValue([]);
    renderHome("admin");
    expect(await screen.findByText("없음")).toBeInTheDocument();
    expect(screen.queryByText("0건")).toBeNull();
    expect(screen.getByRole("link", { name: "점검 탭 열기" })).toBeInTheDocument();
  });

  it("불러오기에 실패하면 7.2 의 오류 문구가 보인다", async () => {
    get.mockRejectedValue(new Error("x"));
    renderHome("admin");
    expect(await screen.findByText("목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.")).toBeInTheDocument();
  });

  it("admin 외 역할에는 카드가 그려지지 않고 건수 API 도 부르지 않는다", () => {
    for (const role of ROLES.filter((r) => r !== "admin")) {
      const { container, unmount } = renderHome(role);
      expect(screen.queryByLabelText("결재·합의자 소멸"), role).toBeNull();
      expect(container.querySelector('[data-home-card="org-orphans"]'), role).toBeNull();
      unmount();
    }
    expect(get).not.toHaveBeenCalled();
  });
});

import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import type { Permission, Role } from "../auth/permissions";
import { roleHas } from "../auth/permissions";
import { AiHelpSection, llmHomeAside } from "./homeAside";
import { llmModule } from "./routes";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const usage = (limit: object) => ({ day: "2026-11-12", mine: { calls: 2, blocked: 0, tokens: 1200 }, limit });

function renderSection(role: Role, initialPermissions?: Permission[]) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "홍길동", role, departmentId: 1 }} initialPermissions={initialPermissions}>
        <AiHelpSection />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => get.mockReset());

describe("홈 AI 도움 절", () => {
  it("llm:usage_read 권한을 요구하고 등록돼 있다", () => {
    const s = llmHomeAside.find((x) => x.id === "ai-help")!;
    expect(s.title).toBe("AI 도움");
    expect(s.permission).toBe("llm:usage_read");
    expect(s.order).toBe(30);
    expect(llmModule.homeAside).toBe(llmHomeAside);
    expect(roleHas("finance", s.permission)).toBe(true);
    expect(roleHas("staff", s.permission)).toBe(false);
  });

  it("사용량·한도를 보이고 한도 근접이면 배지를 단다", async () => {
    get.mockImplementation((p: string) => Promise.resolve(p === "/api/llm/usage/today" ? usage({ scope: "role", dailyTokenLimit: 20000, usedTokens: 18000, nearLimit: true }) : {}));
    renderSection("finance");
    expect(await screen.findByText("한도 근접")).toBeInTheDocument();
    expect(screen.getByText(/일일 한도 20,000 토큰/)).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("여유가 있으면 배지가 없고, 문서 검증 권한이 있으면 바로가기가 있다", async () => {
    get.mockImplementation(() => Promise.resolve(usage({ scope: "role", dailyTokenLimit: 20000, usedTokens: 100, nearLimit: false })));
    renderSection("finance");
    expect(await screen.findByText(/일일 한도/)).toBeInTheDocument();
    expect(screen.queryByText("한도 근접")).toBeNull();
    expect(screen.getByRole("link", { name: "문서 검증 열기" })).toHaveAttribute("href", "/documents");
  });

  it("절 컴포넌트는 래퍼와 절 제목을 만들지 않는다", async () => {
    get.mockImplementation(() => Promise.resolve(usage({ scope: "role", dailyTokenLimit: 20000, usedTokens: 100, nearLimit: false })));
    const { container } = renderSection("finance");
    expect(await screen.findByText(/일일 한도/)).toBeInTheDocument();
    expect(container.querySelector(".aside-section, .card-flat")).toBeNull();
    expect(screen.queryByRole("heading")).toBeNull();
  });

  it("문서 검증 권한이 없으면 바로가기 링크가 없다", async () => {
    get.mockImplementation(() => Promise.resolve(usage({ scope: "role", dailyTokenLimit: 20000, usedTokens: 100, nearLimit: false })));
    renderSection("auditor", ["llm:usage_read"]);
    expect(await screen.findByText(/일일 한도/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "문서 검증 열기" })).toBeNull();
  });
});

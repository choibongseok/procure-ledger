import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { App } from "./App";
import { AuthProvider } from "./auth/AuthProvider";
import { PERMISSIONS } from "./auth/permissions";
import { visibleMenu } from "./layout/Layout";
import { allMenu, appModules, publicModules } from "./routes";

describe("라우트 모듈", () => {
  const routePaths = [...appModules, ...publicModules].flatMap((m) => m.routes.map((r) => r.path));

  it("경로가 중복되지 않는다", () => {
    expect(new Set(routePaths).size).toBe(routePaths.length);
  });
  it("모든 메뉴 항목은 등록된 라우트를 가리키고 알려진 권한을 선언한다", () => {
    for (const m of allMenu) {
      expect(routePaths).toContain(m.path);
      expect([...PERMISSIONS, "authenticated", "public"]).toContain(m.permission);
    }
  });
  it("메뉴는 권한이 있는 항목만 순서대로 보인다", () => {
    const staffMenu = visibleMenu(allMenu, (p) => p === "purchase:read" || p === "approval:read" || p === "token:manage");
    expect(staffMenu.map((m) => m.label)).toEqual(["구매요청", "결재함", "개인 토큰"]);
  });
  it("같은 group 안에서 order 가 중복되지 않는다(SNB 순서가 정해진다)", () => {
    const seen = new Map<string, string>();
    const dup: string[] = [];
    for (const m of allMenu) {
      const key = `${m.group}:${m.order}`;
      const prev = seen.get(key);
      if (prev) dup.push(`${m.group} order ${m.order}: ${prev} / ${m.label}`);
      else seen.set(key, m.label);
    }
    expect(dup).toEqual([]);
  });
  it("메뉴 이름에 내부 용어를 쓰지 않는다", () => {
    for (const m of allMenu) expect(m.label).not.toMatch(/ledger|rbac|token|audit/i);
  });
});

describe("App", () => {
  it("로그인한 직원에게 감사 로그 메뉴는 보이지 않는다", () => {
    render(
      <MemoryRouter initialEntries={["/purchase-requests"]}>
        <AuthProvider initialUser={{ id: 1, name: "김직원", role: "staff", departmentId: 1 }}>
          <App />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "구매 메뉴" })).toHaveTextContent("구매요청");
    expect(screen.queryByText("감사 로그")).toBeNull();
  });
  it("미로그인은 로그인 화면으로 보낸다", () => {
    render(
      <MemoryRouter initialEntries={["/purchase-requests"]}>
        <AuthProvider initialUser={null}>
          <App />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "로그인" })).toBeInTheDocument();
  });
});

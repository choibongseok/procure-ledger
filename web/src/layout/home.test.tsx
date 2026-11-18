import { render, screen } from "@testing-library/react";
import { useEffect } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { PERMISSIONS, ROLES, roleHas, type Role } from "../auth/permissions";
import { allHomeCards, allMenu, homeModule } from "../routes";
import { HomePage } from "./HomePage";
import { AsideProvider, useAsideSpec } from "./aside";
import type { HomeAsideSection, HomeCard } from "./routeModule";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;

function user(role: Role) {
  return { id: 1, name: "홍길동", role, departmentId: 1 };
}

/** 마운트되면 자기 경로를 호출하는 시험용 카드. */
function probeCard(id: string, permission: HomeCard["permission"], order: number): HomeCard {
  function Body() {
    useEffect(() => {
      void api.get(`/api/probe/${id}`);
    }, []);
    return <p>{id} 내용</p>;
  }
  return { id, title: `${id} 카드`, permission, order, Component: Body };
}

function renderHome(cards: HomeCard[], role: Role) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user(role)}>
        <HomePage cards={cards} />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
  get.mockResolvedValue({});
});

describe("홈 화면 카드", () => {
  const cards = [
    probeCard("approval", "approval:decide", 20),
    probeCard("mine", "purchase:read", 10),
    probeCard("usage", "llm:usage_read", 30),
  ];

  it("권한이 없는 카드는 DOM 에 없고 그 카드의 API 도 호출되지 않는다", () => {
    renderHome(cards, "staff"); // staff: purchase:read 만 있다
    expect(screen.getByLabelText("mine 카드")).toBeInTheDocument();
    expect(screen.queryByLabelText("approval 카드")).toBeNull();
    expect(screen.queryByLabelText("usage 카드")).toBeNull();
    expect(screen.queryByText("approval 내용")).toBeNull();
    const called = get.mock.calls.map((c) => c[0]);
    expect(called).toEqual(["/api/probe/mine"]);
  });

  it("권한이 있으면 order 순서로 그린다", () => {
    renderHome(cards, "finance"); // finance: 세 권한 모두
    const ids = Array.from(document.querySelectorAll("[data-home-card]")).map((e) => e.getAttribute("data-home-card"));
    expect(ids).toEqual(["mine", "approval", "usage"]);
  });

  it("보일 카드가 하나도 없으면 안내 문구를 보인다", () => {
    renderHome([probeCard("usage", "llm:usage_read", 1)], "staff");
    expect(screen.getByText(/지금 표시할 항목이 없습니다/)).toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });

  it("카드 하나가 오류를 내도 나머지는 보인다", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    const broken: HomeCard = {
      id: "broken",
      title: "깨진 카드",
      permission: "authenticated",
      order: 1,
      Component: () => {
        throw new Error("boom");
      },
    };
    renderHome([broken, probeCard("mine", "purchase:read", 2)], "staff");
    expect(screen.getByText("이 항목을 표시하지 못했습니다.")).toBeInTheDocument();
    expect(screen.getByText("mine 내용")).toBeInTheDocument();
    err.mockRestore();
  });
});

/** 우측 패널 자리: Layout 이 하는 것처럼 AsideProvider 가 받은 내용을 그린다. */
function AsideOut() {
  const spec = useAsideSpec();
  return spec ? (
    <aside aria-label={spec.title}>
      <h2>{spec.title}</h2>
      {spec.content}
    </aside>
  ) : (
    <p data-testid="no-aside">패널 없음</p>
  );
}

function probeAside(id: string, permission: HomeAsideSection["permission"], order: number): HomeAsideSection {
  function Body() {
    useEffect(() => {
      void api.get(`/api/probe-aside/${id}`);
    }, []);
    return <p>{id} 절 내용</p>;
  }
  return { id, title: `${id} 절`, permission, order, Component: Body };
}

function renderHomeWithAside(sections: HomeAsideSection[], role: Role) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user(role)}>
        <AsideProvider>
          <HomePage cards={[]} asideSections={sections} />
          <AsideOut />
        </AsideProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("홈 우측 패널 절", () => {
  const sections = [probeAside("todo", "approval:decide", 20), probeAside("mine", "purchase:read", 10), probeAside("usage", "llm:usage_read", 30)];

  it("권한 없는 절은 마운트되지 않고 API 도 호출되지 않는다", () => {
    renderHomeWithAside(sections, "staff"); // staff: purchase:read 만
    expect(screen.getByRole("complementary", { name: "오늘 할 일" })).toBeInTheDocument();
    expect(screen.getByText("mine 절 내용")).toBeInTheDocument();
    expect(screen.queryByText("todo 절 내용")).toBeNull();
    expect(screen.queryByText("usage 절 내용")).toBeNull();
    expect(get.mock.calls.map((c) => c[0])).toEqual(["/api/probe-aside/mine"]);
  });

  it("권한 있는 절은 order 순서로 그린다", () => {
    renderHomeWithAside(sections, "finance");
    const ids = Array.from(document.querySelectorAll("[data-home-aside]")).map((e) => e.getAttribute("data-home-aside"));
    expect(ids).toEqual(["mine", "todo", "usage"]);
  });

  it("권한 있는 절이 하나도 없으면 패널을 올리지 않고 호출도 없다", () => {
    renderHomeWithAside([probeAside("usage", "llm:usage_read", 1)], "staff");
    expect(screen.getByTestId("no-aside")).toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });
});

describe("등록된 홈 카드(모든 영역)", () => {
  it("id 가 중복되지 않고 알려진 권한을 선언하며 제목에 내부 용어가 없다", () => {
    const ids = allHomeCards.map((c) => c.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const c of allHomeCards) {
      expect([...PERMISSIONS, "authenticated", "public"]).toContain(c.permission);
      expect(c.title).toMatch(/[가-힣]/);
      expect(c.title).not.toMatch(/ledger|rbac|token|audit|mcp|llm/i);
    }
  });

  it.each(ROLES)("%s 에게는 권한이 있는 카드 제목만 그려진다", (role) => {
    renderHome(allHomeCards, role);
    const rendered = Array.from(document.querySelectorAll("[data-home-card]")).map((e) => e.getAttribute("data-home-card"));
    const expected = allHomeCards.filter((c) => roleHas(role, c.permission) && !c.module).map((c) => c.id); // 꺼진 업무 모듈의 카드는 그려지지 않는다
    expect([...rendered].sort()).toEqual([...expected].sort());
  });
});

describe("홈이 첫 화면이다", () => {
  it("메뉴 맨 위가 홈이고 / 경로가 홈 라우트다", () => {
    expect(allMenu[0]).toMatchObject({ label: "홈", path: "/" });
    expect(homeModule.routes.map((r) => r.path)).toEqual(["/"]);
  });

  it("로그인 직후(/)에 홈 화면이 열린다", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider initialUser={user("staff")}>
          <App />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "홈" })).toBeInTheDocument();
    expect(screen.getByText(/홍길동님, 안녕하세요/)).toBeInTheDocument();
  });

  it("로그인하지 않은 채 /(홈)을 열면 빈 메뉴 껍데기가 아니라 로그인 화면으로 간다", () => {
    get.mockImplementation((path: string) => (path === "/api/auth/config" ? Promise.resolve({ login: true, devIdp: false }) : Promise.resolve({})));
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider initialUser={null}>
          <App />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "로그인" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "홈" })).toBeNull();
    expect(screen.queryByRole("navigation", { name: "주 메뉴" })).toBeNull();
  });
});

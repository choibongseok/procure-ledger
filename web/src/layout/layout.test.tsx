// 3층 내비(GNB·SNB·본문·우측 패널) 시험. 메뉴는 실제 allMenu 다.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import type { Role } from "../auth/permissions";
import { allMenu } from "../routes";
import { ASIDE_HIDDEN_QUERY, ASIDE_MIN, NARROW_MAX, NARROW_QUERY } from "./breakpoints";
import { useAside } from "./aside";
import { Layout } from "./Layout";
import { PageHead } from "./ui";
import type { MenuItem } from "./routeModule";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});
const get = api.get as unknown as ReturnType<typeof vi.fn>;

const menu: MenuItem[] = allMenu;

function user(role: Role) {
  return { id: 7, name: "홍길동", role, departmentId: 1, unitName: "구매1팀" };
}

function Screen({ aside, head }: { aside?: { title: string; body: string } | null; head?: boolean }) {
  useAside(aside === undefined || aside === null ? null : { title: aside.title, content: <p>{aside.body}</p> });
  if (head) return <PageHead title="화면">{<button type="button">저장</button>}</PageHead>;
  return <h1>화면</h1>;
}
function LocationProbe() {
  const l = useLocation();
  return <p data-testid="loc">{l.pathname + l.search}</p>;
}

function renderAt(path: string, role: Role = "admin", aside?: { title: string; body: string } | null, extra?: ReactNode, head = false) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider initialUser={user(role)}>
        <Routes>
          <Route element={<Layout menu={menu} />}>
            <Route path="*" element={<Screen aside={aside} head={head} />} />
          </Route>
        </Routes>
        {extra}
        <LocationProbe />
      </AuthProvider>
    </MemoryRouter>,
  );
}

function gnbModules(): string[] {
  return within(screen.getByRole("navigation", { name: "모듈" }))
    .getAllByRole("link")
    .map((a) => a.textContent ?? "");
}

/** matchMedia 흉내: 주어진 쿼리 문자열이 listed 에 있으면 일치. */
function mockMedia(matching: string[]) {
  window.matchMedia = ((q: string) => ({
    matches: matching.includes(q),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

// 실행 환경에 따라 jsdom localStorage 가 없을 수 있어 메모리 저장소로 바꿔 끼운다.
const store = new Map<string, string>();
const memoryStorage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
  removeItem: (k: string) => void store.delete(k),
  clear: () => store.clear(),
};

beforeEach(() => {
  get.mockReset();
  get.mockResolvedValue({ unread: 0 });
  store.clear();
  Object.defineProperty(window, "localStorage", { value: memoryStorage, configurable: true });
});
afterEach(() => {
  // @ts-expect-error 시험이 넣은 matchMedia 를 지운다(jsdom 에는 원래 없다)
  delete window.matchMedia;
});

describe("본문으로 건너뛰기", () => {
  it("첫 Tab 대상인 링크가 GNB 앞에 있고 main#main 을 가리킨다", () => {
    const { container, unmount } = renderAt("/purchase-requests", "admin");
    const link = screen.getByRole("link", { name: "본문으로 건너뛰기" });
    expect(link.getAttribute("href")).toBe("#main");
    const main = container.querySelector("main#main");
    expect(main?.getAttribute("tabindex")).toBe("-1");
    const gnb = container.querySelector("header.gnb")!;
    expect(link.compareDocumentPosition(gnb) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    unmount();
  });

  it("링크를 누르면 main 이 포커스를 받는다", () => {
    const { container, unmount } = renderAt("/purchase-requests", "admin");
    fireEvent.click(screen.getByRole("link", { name: "본문으로 건너뛰기" }));
    expect(document.activeElement).toBe(container.querySelector("main#main"));
    unmount();
  });
});

describe("GNB 모듈과 SNB 항목은 권한대로 나온다", () => {
  const FULL = ["홈", "구매", "결재", "재고·입고", "문서·AI", "조직·인사", "재무", "관리"];

  it("관리자·재무: 모듈 8개, 정해진 순서", () => {
    for (const role of ["admin", "finance"] as const) {
      const { unmount } = renderAt("/purchase-requests", role);
      expect(gnbModules(), role).toEqual(FULL);
      unmount();
    }
  });

  it("직원·결재자: 모듈 7개 정확히(관리 없음), 관리 모듈이 DOM 에 없다", () => {
    for (const role of ["staff", "manager"] as const) {
      const { unmount } = renderAt("/purchase-requests", role);
      expect(gnbModules(), role).toEqual(FULL.filter((m) => m !== "관리"));
      expect(screen.queryByRole("link", { name: "관리" })).toBeNull();
      expect(screen.queryByText("감사 로그")).toBeNull();
      unmount();
    }
  });

  it("감사: 관리 모듈이 있고 SNB 에는 감사 로그가 있다", () => {
    renderAt("/audit-logs", "auditor");
    expect(gnbModules()).toEqual(FULL);
    const snb = screen.getByRole("navigation", { name: "관리 메뉴" });
    expect(within(snb).getByRole("link", { name: "감사 로그" })).toBeInTheDocument();
    expect(within(snb).queryByRole("link", { name: "역할 관리" })).toBeNull();
  });

  it("SNB 는 현재 모듈의 항목만 order 순으로 보이고 다른 모듈 항목은 DOM 에 없다", () => {
    renderAt("/org", "admin");
    const snb = screen.getByRole("navigation", { name: "조직·인사 메뉴" });
    expect(within(snb).getAllByRole("link").map((a) => a.textContent)).toEqual(["조직도", "조직 관리", "인사 발령", "조직 일괄 반영"]);
    expect(screen.queryByRole("link", { name: "감사 로그" })).toBeNull();
    // 현재 모듈 강조: GNB 는 aria-current="true", SNB 는 현재 항목 하나만 "page"
    expect(within(screen.getByRole("navigation", { name: "모듈" })).getByRole("link", { name: "조직·인사" })).toHaveAttribute("aria-current", "true");
    expect(within(snb).getAllByRole("link", { current: "page" }).map((a) => a.textContent)).toEqual(["조직도"]);
  });

  it("결재 모듈은 결재함이 맨 위(order 11)", () => {
    renderAt("/approvals", "manager");
    const snb = screen.getByRole("navigation", { name: "결재 메뉴" });
    expect(within(snb).getAllByRole("link")[0]).toHaveTextContent("결재함");
  });

  it("재무 SNB 는 소제목 묶음(전표·지급·자금·카드·세무)이다", () => {
    renderAt("/finance/journals", "admin");
    const snb = screen.getByRole("navigation", { name: "재무 메뉴" });
    const heads = Array.from(snb.querySelectorAll(".snb-group")).map((e) => e.textContent);
    expect(heads).toEqual(["전표", "지급", "자금", "카드", "세무"]);
    const lists = snb.querySelectorAll("ul[aria-labelledby]");
    expect(lists).toHaveLength(5);
    expect(within(lists[0] as HTMLElement).getAllByRole("link").map((a) => a.textContent)).toEqual(["전표 대기", "전표", "계정별 원장", "시산표", "계정과목"]);
  });

  it("group 이 없는 항목은 사용자 메뉴로 보아 SNB 에도 GNB 에도 나오지 않는다", () => {
    const bare = [{ label: "이름 없는 화면", path: "/bare", permission: "authenticated", order: 1 }] as unknown as MenuItem[]; // group 을 빠뜨린 항목(타입 우회)
    render(
      <MemoryRouter initialEntries={["/bare"]}>
        <AuthProvider initialUser={user("admin")}>
          <Routes>
            <Route element={<Layout menu={bare} />}>
              <Route path="*" element={<Screen />} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.queryByRole("link", { name: "이름 없는 화면" })).toBeNull();
  });

  it("모든 메뉴 항목에 group 이 있다(user 로 둔 것은 알림·내 계정·개인 토큰뿐)", () => {
    const missing = allMenu.filter((m) => !m.group).map((m) => m.path);
    expect(missing, "다른 영역의 routes.tsx menu 에 group 이 필요하다").toEqual([]);
  });
});

describe("홈", () => {
  it("SNB 가 없고 data-snb 가 없다", () => {
    const { container } = renderAt("/", "admin");
    expect(container.querySelector(".snb")).toBeNull();
    expect(container.querySelector(".app-body")).not.toHaveAttribute("data-snb");
    expect(within(screen.getByRole("navigation", { name: "모듈" })).getByRole("link", { name: "홈" })).toHaveAttribute("aria-current", "true");
  });

  it("사용자 메뉴에서 내 계정·내 정보 열람 요청·개인 토큰·로그아웃, 알림은 종 아이콘으로", async () => {
    get.mockResolvedValue({ unread: 3 });
    renderAt("/", "admin");
    const bell = await screen.findByRole("link", { name: "알림, 안 읽음 3건" });
    expect(bell).toHaveAttribute("href", "/notifications");
    expect(screen.queryByRole("menu")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /홍길동/ }));
    const m = screen.getByRole("menu", { name: "사용자 메뉴" });
    expect(within(m).getAllByRole("menuitem").map((e) => e.textContent)).toEqual(["내 계정", "내 정보 열람 요청", "개인 토큰", "로그아웃"]);
    expect(screen.getByTestId("user-unit")).toHaveTextContent("관리자 · 구매1팀");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("알림 권한이 없으면 안 읽음 수를 호출하지 않고 종 아이콘도 없다", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider initialUser={user("auditor")} initialPermissions={["org:read"]}>
          <Routes>
            <Route element={<Layout menu={menu} />}>
              <Route path="*" element={<Screen />} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(get).not.toHaveBeenCalledWith("/api/notifications/unread-count");
    expect(screen.queryByRole("link", { name: /^알림/ })).toBeNull();
  });
});

describe("우측 패널", () => {
  it("화면이 useAside(null) 이면 data-aside=none 이고 aside 를 그리지 않는다", () => {
    const { container } = renderAt("/org", "admin", null);
    expect(container.querySelector(".app-body")).toHaveAttribute("data-aside", "none");
    expect(container.querySelector("aside")).toBeNull();
    expect(screen.queryByRole("button", { name: "패널 열기" })).toBeNull();
  });

  it("내용이 있으면 제목·접기 버튼이 있고, 접으면 패널 열기 버튼이 생기며 상태가 사용자별로 기억된다", () => {
    const { container, unmount } = renderAt("/org", "admin", { title: "결재 정보", body: "결재선 내용" });
    const aside = screen.getByRole("complementary", { name: "결재 정보" });
    expect(within(aside).getByRole("heading", { name: "결재 정보" })).toBeInTheDocument();
    expect(within(aside).getByText("결재선 내용")).toBeInTheDocument();
    fireEvent.click(within(aside).getByRole("button", { name: "패널 접기" }));
    expect(container.querySelector(".app-body")).toHaveAttribute("data-aside", "closed");
    expect(container.querySelector("aside")).toBeNull();
    expect(window.localStorage.getItem("procure-ledger.layout.7.aside-closed")).toBe("1");
    unmount();
    // 다시 열면 접힌 채로 시작한다
    const again = renderAt("/org", "admin", { title: "결재 정보", body: "결재선 내용" });
    expect(again.container.querySelector(".app-body")).toHaveAttribute("data-aside", "closed");
    fireEvent.click(screen.getByRole("button", { name: "패널 열기" }));
    expect(screen.getByRole("complementary", { name: "결재 정보" })).toBeInTheDocument();
    expect(window.localStorage.getItem("procure-ledger.layout.7.aside-closed")).toBe("0");
  });

  it("화면을 떠나면 패널이 지워진다", () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/org"]}>
        <AuthProvider initialUser={user("admin")}>
          <Routes>
            <Route element={<Layout menu={menu} />}>
              <Route path="/org" element={<Screen aside={{ title: "조직 정보", body: "x" }} />} />
              <Route path="/org/manage" element={<Screen />} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(container.querySelector("aside")).not.toBeNull();
    fireEvent.click(screen.getByRole("link", { name: "조직 관리" }));
    expect(container.querySelector("aside")).toBeNull();
    expect(container.querySelector(".app-body")).toHaveAttribute("data-aside", "none");
  });

  it("저장소가 막혀 있어도 터지지 않는다", () => {
    const spy = vi.spyOn(memoryStorage, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const set = vi.spyOn(memoryStorage, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    renderAt("/org", "admin", { title: "결재 정보", body: "x" });
    fireEvent.click(screen.getByRole("button", { name: "패널 접기" }));
    expect(screen.getByRole("button", { name: "패널 열기" })).toBeInTheDocument();
    spy.mockRestore();
    set.mockRestore();
  });

  it("SNB 접기도 사용자별로 기억된다", () => {
    const { container } = renderAt("/org", "admin");
    expect(container.querySelector(".app-body")).toHaveAttribute("data-snb", "open");
    fireEvent.click(screen.getByRole("button", { name: "메뉴 접기" }));
    expect(container.querySelector(".app-body")).toHaveAttribute("data-snb", "closed");
    expect(window.localStorage.getItem("procure-ledger.layout.7.snb-closed")).toBe("1");
    expect(screen.getByRole("button", { name: "메뉴 펼치기" })).toHaveAttribute("aria-expanded", "false");
  });
});

describe("1280 미만: 패널은 겹쳐 열린다", () => {
  const MID = ASIDE_HIDDEN_QUERY;
  it("패널 열기로 겹쳐 열고 Esc·바깥 클릭으로 닫는다. 기억하지 않는다", () => {
    mockMedia([MID]);
    const { container } = renderAt("/org", "admin", { title: "결재 정보", body: "x" });
    const body = () => container.querySelector(".app-body")!;
    expect(container.querySelector("aside")).toBeNull();
    expect(body()).toHaveAttribute("data-aside-overlay", "closed");
    fireEvent.click(screen.getByRole("button", { name: "패널 열기" }));
    expect(body()).toHaveAttribute("data-aside-overlay", "open");
    expect(screen.getByRole("complementary", { name: "결재 정보" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(container.querySelector("aside")).toBeNull();
    expect(screen.getByRole("button", { name: "패널 열기" })).toHaveFocus();

    fireEvent.click(screen.getByRole("button", { name: "패널 열기" }));
    expect(container.querySelector("aside")).not.toBeNull();
    fireEvent.mouseDown(screen.getByRole("heading", { name: "화면" }));
    expect(container.querySelector("aside")).toBeNull();

    // 패널 안 클릭은 닫지 않는다
    fireEvent.click(screen.getByRole("button", { name: "패널 열기" }));
    fireEvent.mouseDown(screen.getByText("x"));
    expect(container.querySelector("aside")).not.toBeNull();
    expect(window.localStorage.getItem("procure-ledger.layout.7.aside-closed")).toBeNull();
  });
});

describe("760 이하: GNB 는 햄버거, SNB 는 서랍", () => {
  const NARROW = NARROW_QUERY;
  const MID = ASIDE_HIDDEN_QUERY;
  it("열면 첫 항목으로 포커스, Esc 로 닫고 햄버거로 복귀, 항목을 누르면 닫힌다", () => {
    mockMedia([NARROW, MID]);
    const { container } = renderAt("/org", "admin");
    const body = () => container.querySelector(".app-body")!;
    expect(body()).not.toHaveAttribute("data-snb"); // 서랍 규칙: 접힌 레일 규칙이 서랍 내용을 숨기지 않게
    expect(body()).toHaveAttribute("data-drawer", "closed");
    const burger = screen.getByRole("button", { name: "메뉴 열기" });
    expect(burger).toHaveAttribute("aria-controls", "snb");
    fireEvent.click(burger);
    expect(body()).toHaveAttribute("data-drawer", "open");
    expect(screen.getByRole("button", { name: "메뉴 닫기" })).toHaveAttribute("aria-expanded", "true");
    const snb = screen.getByRole("navigation", { name: "조직·인사 메뉴" });
    expect(within(snb).getByRole("link", { name: "조직도" })).toHaveFocus();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(body()).toHaveAttribute("data-drawer", "closed");
    expect(screen.getByRole("button", { name: "메뉴 열기" })).toHaveFocus();

    fireEvent.click(screen.getByRole("button", { name: "메뉴 열기" }));
    fireEvent.click(within(screen.getByRole("navigation", { name: "조직·인사 메뉴" })).getByRole("link", { name: "인사 발령" }));
    expect(body()).toHaveAttribute("data-drawer", "closed");
  });

  it("서랍의 모듈 토글은 이동 없이 아래 하위 메뉴만 바꾼다", () => {
    mockMedia([NARROW, MID]);
    renderAt("/org", "admin");
    fireEvent.click(screen.getByRole("button", { name: "메뉴 열기" }));
    const snb = screen.getByRole("navigation", { name: "조직·인사 메뉴" });
    const toggles = within(snb).getAllByRole("button", { pressed: false });
    expect(within(snb).getByRole("button", { name: "조직·인사", pressed: true })).toBeInTheDocument();
    expect(toggles.length).toBe(7);
    fireEvent.click(within(snb).getByRole("button", { name: "관리" }));
    const admin = screen.getByRole("navigation", { name: "관리 메뉴" });
    expect(within(admin).getByRole("link", { name: "감사 로그" })).toBeInTheDocument();
    expect(within(admin).queryByRole("link", { name: "조직도" })).toBeNull();
  });

  it("바깥(배경 막)을 누르면 서랍이 닫힌다", () => {
    mockMedia([NARROW, MID]);
    const { container } = renderAt("/org", "admin");
    fireEvent.click(screen.getByRole("button", { name: "메뉴 열기" }));
    act(() => {
      fireEvent.click(container.querySelector(".drawer-backdrop")!);
    });
    expect(container.querySelector(".app-body")).toHaveAttribute("data-drawer", "closed");
  });
});

describe("기준점은 한 곳(breakpoints.ts)과 app.css 가 같은 값이다", () => {
  it("app.css 의 @media max-width 값이 상수(ASIDE_MIN-1, NARROW_MAX)와 같다", async () => {
    // 프론트 타입 설정에 node 타입이 없어 동적 import 로 읽는다(시험은 node 에서 돈다). css?raw 는 vitest 가 css 를 비워 쓸 수 없다.
    // @ts-ignore node 모듈
    const fs: { readFileSync: (p: string, enc: string) => string } = await import("node:fs");
    const css = fs.readFileSync("src/styles/app.css", "utf8");
    const widths = Array.from(css.matchAll(/@media\s*\(max-width:\s*(\d+)px\)/g)).map((m) => Number(m[1]));
    expect([...new Set(widths)].sort((a, c) => a - c)).toEqual([NARROW_MAX, ASIDE_MIN - 1]);
    expect(NARROW_QUERY).toBe(`(max-width: ${NARROW_MAX}px)`);
    expect(ASIDE_HIDDEN_QUERY).toBe(`(max-width: ${ASIDE_MIN - 1}px)`);
  });
});

describe("PageHead: '패널 열기' 는 화면 제목 줄 오른쪽으로 들어간다", () => {
  it("PageHead 가 있으면 .page-head-actions 안, 없으면 본문 위", () => {
    window.localStorage.setItem("procure-ledger.layout.7.aside-closed", "1");
    const a = renderAt("/org", "admin", { title: "결재 정보", body: "x" }, null, true);
    const actions = a.container.querySelector(".page-head-actions")!;
    expect(within(actions as HTMLElement).getByRole("button", { name: "저장" })).toBeInTheDocument();
    expect(within(actions as HTMLElement).getByRole("button", { name: "패널 열기" })).toBeInTheDocument();
    a.unmount();
    const b = renderAt("/org", "admin", { title: "결재 정보", body: "x" });
    expect(b.container.querySelector(".page-head-actions")).toBeNull();
    expect(b.container.querySelector(".app-main > .aside-open")).not.toBeNull();
  });
});

describe("GNB 통합 검색", () => {
  it("Enter 로 /search?q= 로 이동하고 빈 값은 무시한다", () => {
    renderAt("/org");
    const input = screen.getByRole("searchbox", { name: "통합 검색" });
    fireEvent.submit(input.closest("form")!);
    expect(screen.getByTestId("loc")).toHaveTextContent("/org");
    fireEvent.change(input, { target: { value: "   " } });
    fireEvent.submit(input.closest("form")!);
    expect(screen.getByTestId("loc")).toHaveTextContent("/org");
    fireEvent.change(input, { target: { value: "볼트 M8" } });
    fireEvent.submit(input.closest("form")!);
    expect(screen.getByTestId("loc")).toHaveTextContent("/search?q=" + encodeURIComponent("볼트 M8"));
  });

  it("760 이하에서는 검색 단추를 눌러야 입력이 펼쳐진다", () => {
    mockMedia([NARROW_QUERY]);
    renderAt("/org");
    expect(screen.queryByRole("searchbox", { name: "통합 검색" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "검색 열기" }));
    expect(screen.getByRole("searchbox", { name: "통합 검색" })).toHaveFocus();
  });
});

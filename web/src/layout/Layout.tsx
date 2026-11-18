// (web/src/layout). GNB(모듈) / SNB(현재 모듈 하위 메뉴) / 본문 / 우측 패널 3층.
// 메뉴는 모듈이 선언한 항목 중 권한이 있는 것만 보여 준다. 마크업·규칙은 design/handoff/2026-11-12-visual-ui.md 1~3절.
import { useCallback, type FormEvent, useEffect, useId, useMemo, useRef, useState } from "react";
import { Link, matchPath, Navigate, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { createPortal } from "react-dom";
import { AsideProvider, useAsideSpec, usePageHeadSlot } from "./aside";
import { ASIDE_HIDDEN_QUERY, NARROW_QUERY, readFlag, useMedia, writeFlag } from "./breakpoints";
import { IconBell, IconChevronsLeft, IconChevronsRight, IconMenu, IconPanel, IconSearch, IconX } from "./icons";
import type { MenuGroup, MenuItem } from "./routeModule";
import { IconButton, ToastProvider } from "./ui";
import { EntitySwitcher, GroupModeBanner, entityKey } from "./EntitySwitcher";

export function visibleMenu(items: MenuItem[], can: (perm: MenuItem["permission"]) => boolean, multiEntity = true, hasModule: (key: string) => boolean = () => true): MenuItem[] {
  return items.filter((m) => (multiEntity || !m.multiEntityOnly) && (!m.module || hasModule(m.module)) && (m.anyOf ? m.anyOf.some((p) => can(p)) : can(m.permission))).sort((a, b) => a.order - b.order);
}

/** 다른 메뉴 경로가 이 경로의 하위(예: /org 와 /org/manage)이면 이 메뉴는 경로가 정확히 같을 때만 강조한다(메뉴 하나만 강조). */
export function hasNestedMenu(m: MenuItem, all: MenuItem[]): boolean {
  return all.some((o) => o.path !== m.path && o.path.startsWith(m.path.endsWith("/") ? m.path : m.path + "/"));
}

/** GNB 모듈 순서와 이름. 순서는 이 배열이 정하고, 모듈 안 순서는 메뉴 order 가 정한다. */
export const GNB_MODULES: { id: Exclude<MenuGroup, "user">; label: string }[] = [
  { id: "home", label: "홈" },
  { id: "purchase", label: "구매" },
  { id: "approval", label: "결재" },
  { id: "inventory", label: "재고·입고" },
  { id: "docai", label: "문서·AI" },
  { id: "org", label: "조직·인사" },
  { id: "finance", label: "재무" },
  { id: "admin", label: "관리" },
];

/** 항목의 GNB 모듈. 런타임에 group 이 빠진 항목(타입을 우회한 경우)은 사용자 메뉴로 본다. */
export function groupOf(m: MenuItem): MenuGroup {
  return m.group ?? "user";
}

/** 지금 경로가 속한 메뉴 항목: 경로가 가장 길게 맞는 항목. */
export function currentItem(items: MenuItem[], pathname: string): MenuItem | null {
  let best: MenuItem | null = null;
  for (const m of items) {
    const hit = m.path === "/" ? pathname === "/" : !!matchPath({ path: m.path, end: false }, pathname);
    if (hit && (!best || m.path.length > best.path.length)) best = m;
  }
  return best;
}

/** SNB 항목을 소제목(section) 묶음으로 나눈다. 같은 소제목이 이어진 묶음 단위(order 순서 유지). */
export function sectionsOf(items: MenuItem[]): { section?: string; items: MenuItem[] }[] {
  const out: { section?: string; items: MenuItem[] }[] = [];
  for (const m of items) {
    const last = out[out.length - 1];
    if (last && last.section === m.section) last.items.push(m);
    else out.push({ section: m.section, items: [m] });
  }
  return out;
}

function Shell({ menu }: { menu: MenuItem[] }) {
  const { user, can, hasModule, refresh, roleText, currentEntity, entities } = useAuth();
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const aside = useAsideSpec();
  const headSlot = usePageHeadSlot();
  const narrow = useMedia(NARROW_QUERY);
  const asideHidden = useMedia(ASIDE_HIDDEN_QUERY); // 1280 미만: 패널이 숨고 겹쳐 열린다
  const uid = useId();
  const userKey = user ? String(user.id) : "anon";

  const [snbClosed, setSnbClosed] = useState(() => readFlag(`procure-ledger.layout.${userKey}.snb-closed`, false));
  const [asideClosed, setAsideClosed] = useState(() => readFlag(`procure-ledger.layout.${userKey}.aside-closed`, false));
  const [overlay, setOverlay] = useState(false); // 1280 미만에서 겹쳐 연 패널(기억하지 않는다)
  const [drawer, setDrawer] = useState(false);
  const [drawerModule, setDrawerModule] = useState<MenuGroup | null>(null);
  const [userMenu, setUserMenu] = useState(false);
  const [unread, setUnread] = useState(0);
  const [query, setQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false); // 760 이하에서 검색 입력을 펼쳤는가
  const searchInputRef = useRef<HTMLInputElement>(null);

  const hamburgerRef = useRef<HTMLButtonElement>(null);
  const snbRef = useRef<HTMLElement>(null);
  const asideRef = useRef<HTMLElement>(null);
  const asideOpenRef = useRef<HTMLButtonElement>(null);
  const userRef = useRef<HTMLDivElement>(null);

  const items = useMemo(() => visibleMenu(menu, can, (entities?.length ?? 0) > 1, hasModule), [menu, can, hasModule, entities]);
  const current = currentItem(items, pathname);
  const currentGroup: MenuGroup | null = current ? groupOf(current) : null;
  const modules = GNB_MODULES.map((m) => ({ ...m, items: items.filter((i) => groupOf(i) === m.id) })).filter((m) => m.items.length > 0);
  const userLinks = items.filter((i) => groupOf(i) === "user" && i.path !== "/notifications");
  const canNotify = can("notification:read");

  // 서랍은 화면이 넓어지거나 다른 화면으로 가면 닫는다.
  useEffect(() => {
    if (!narrow) setDrawer(false);
  }, [narrow]);
  useEffect(() => {
    setDrawer(false);
    setUserMenu(false);
    setOverlay(false);
  }, [pathname]);
  useEffect(() => {
    if (!asideHidden) setOverlay(false);
  }, [asideHidden]);

  // 알림 종 아이콘의 안 읽음 수. 권한이 없으면 호출하지 않는다.
  useEffect(() => {
    if (!canNotify) return;
    let alive = true;
    void (async () => {
      try {
        const r = await api.get<{ unread: number }>("/api/notifications/unread-count");
        if (alive && typeof r?.unread === "number") setUnread(r.unread);
      } catch {
        // 숫자를 못 가져와도 종 아이콘은 그대로 둔다.
      }
    })();
    return () => {
      alive = false;
    };
  }, [canNotify, pathname]);

  // 서랍: 열면 첫 항목으로 포커스, Esc 로 닫고 햄버거로 포커스 복귀.
  useEffect(() => {
    if (!drawer) return;
    snbRef.current?.querySelector<HTMLElement>("ul:not(.snb-modules) a")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setDrawer(false);
      hamburgerRef.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [drawer]);

  // 겹쳐 연 패널(1280 미만): Esc·바깥 클릭으로 닫는다.
  useEffect(() => {
    if (!overlay) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOverlay(false);
      asideOpenRef.current?.focus();
    };
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (asideRef.current?.contains(t) || asideOpenRef.current?.contains(t)) return;
      setOverlay(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [overlay]);

  // 사용자 메뉴: Esc·바깥 클릭으로 닫는다.
  useEffect(() => {
    if (!userMenu) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setUserMenu(false);
      userRef.current?.querySelector<HTMLElement>(".gnb-user")?.focus();
    };
    const onDown = (e: MouseEvent) => {
      if (!userRef.current?.contains(e.target as Node)) setUserMenu(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [userMenu]);

  const toggleSnb = useCallback(() => {
    setSnbClosed((v) => {
      writeFlag(`procure-ledger.layout.${userKey}.snb-closed`, !v);
      return !v;
    });
  }, [userKey]);
  const collapseAside = () => {
    if (asideHidden) {
      setOverlay(false);
      asideOpenRef.current?.focus();
      return;
    }
    setAsideClosed(true);
    writeFlag(`procure-ledger.layout.${userKey}.aside-closed`, true);
  };
  const openAside = () => {
    if (asideHidden) {
      setOverlay(true);
      return;
    }
    setAsideClosed(false);
    writeFlag(`procure-ledger.layout.${userKey}.aside-closed`, false);
  };

  // GNB 통합 검색: Enter 로 /search?q= 로 간다. 빈 값은 아무 일도 하지 않는다.
  const submitSearch = (e: FormEvent) => {
    e.preventDefault();
    const q = query.trim();
    if (!q) return;
    navigate(`/search?q=${encodeURIComponent(q)}`);
    if (narrow) setSearchOpen(false);
  };
  useEffect(() => {
    if (narrow && searchOpen) searchInputRef.current?.focus();
  }, [narrow, searchOpen]);

  async function logout() {
    try {
      await api.post("/api/auth/logout");
    } finally {
      await refresh(); // 세션이 없어지면 /api/me 가 401 이라 로그인 화면으로 간다
    }
  }

  // SNB 에 보일 모듈: 넓은 화면은 지금 모듈, 서랍은 서랍에서 고른 모듈(기본은 지금 모듈).
  const snbGroup: MenuGroup | null = narrow && drawer ? (drawerModule ?? currentGroup) : currentGroup;
  const snbModule = modules.find((m) => m.id === snbGroup);
  const snbItems = snbModule?.items ?? [];
  // 홈과 사용자 메뉴 화면은 SNB 자리가 없다(좁은 화면 서랍만 모듈 목록을 위해 그린다).
  const showSnb = narrow || (snbModule !== undefined && snbModule.id !== "home");
  const hasAside = aside !== null;
  const asideState = !hasAside ? "none" : asideClosed ? "closed" : "open";
  const renderAside = hasAside && (asideHidden ? overlay : !asideClosed);

  const bodyAttrs: Record<string, string> = {};
  if (showSnb && !narrow) bodyAttrs["data-snb"] = snbClosed ? "closed" : "open";
  if (!showSnb) bodyAttrs["data-layout"] = "no-snb";
  bodyAttrs["data-aside"] = asideState;
  bodyAttrs["data-aside-overlay"] = overlay ? "open" : "closed";
  bodyAttrs["data-drawer"] = drawer ? "open" : "closed";

  const unitText = user?.unitName || user?.departmentName;
  const meta = [roleText || "권한 없음", unitText].filter(Boolean).join(" · ");
  const bellLabel = unread > 0 ? `알림, 안 읽음 ${unread}건` : "알림";

  const asideOpenButton = (
    <button ref={asideOpenRef} type="button" className="btn-secondary btn-sm aside-open" data-in-head={headSlot ? "true" : undefined} aria-expanded={renderAside} aria-controls={`${uid}-aside`} onClick={openAside}>
      <IconPanel />
      패널 열기
    </button>
  );

  return (
    <ToastProvider>
    <div className="app-shell">
      <a
        className="skip-link"
        href="#main"
        onClick={(e) => {
          e.preventDefault();
          document.getElementById("main")?.focus();
        }}
      >
        본문으로 건너뛰기
      </a>
      <DemoBanner />
      <GroupModeBanner />
      <header className="gnb">
        <IconButton
          ref={hamburgerRef}
          className="gnb-menu"
          label={drawer ? "메뉴 닫기" : "메뉴 열기"}
          aria-expanded={drawer}
          aria-controls="snb"
          onClick={() => {
            setDrawerModule(null);
            setDrawer((v) => !v);
          }}
        >
          {drawer ? <IconX /> : <IconMenu />}
        </IconButton>
        <Link className="gnb-brand" to="/">
          ProcureLedger
        </Link>
        <nav className="gnb-modules" aria-label="모듈">
          <ul>
            {modules.map((m) => {
              const active = m.id === currentGroup;
              return (
                <li key={m.id}>
                  <Link to={m.items[0].path} className={active ? "active" : undefined} aria-current={active ? "true" : undefined}>
                    {m.label}
                  </Link>
                </li>
              );
            })}
          </ul>
        </nav>
        <div className="gnb-tools">
          {narrow && !searchOpen ? (
            <IconButton className="btn-icon" label="검색 열기" aria-expanded={false} onClick={() => setSearchOpen(true)}>
              <IconSearch />
            </IconButton>
          ) : (
            <form className="gnb-search" role="search" onSubmit={submitSearch}>
              <IconSearch className="gnb-search-icon" />
              <input
                ref={searchInputRef}
                type="search"
                aria-label="통합 검색"
                placeholder="번호·제목·이름"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape" && narrow) setSearchOpen(false);
                }}
              />
            </form>
          )}
          <EntitySwitcher />
          {canNotify && (
            <Link className="btn btn-icon" to="/notifications" aria-label={bellLabel} title="알림">
              <IconBell />
              {unread > 0 && (
                <span className="gnb-count" aria-hidden="true">
                  {unread > 99 ? "99+" : unread}
                </span>
              )}
            </Link>
          )}
          <div className="gnb-user-wrap" ref={userRef}>
            <button type="button" className="gnb-user" aria-haspopup="menu" aria-expanded={userMenu} aria-controls={`${uid}-user-menu`} onClick={() => setUserMenu((v) => !v)}>
              <span className="gnb-user-name">{user?.name}</span>
              <span className="gnb-user-meta" data-testid="user-unit">
                {meta}
              </span>
            </button>
            {userMenu && (
              <div className="gnb-user-menu" id={`${uid}-user-menu`} role="menu" aria-label="사용자 메뉴">
                {userLinks.map((m) => (
                  <Link key={m.path} to={m.path} role="menuitem" onClick={() => setUserMenu(false)}>
                    {m.label}
                  </Link>
                ))}
                <button type="button" role="menuitem" onClick={logout}>
                  로그아웃
                </button>
              </div>
            )}
          </div>
        </div>
      </header>
      <div className="app-body" {...bodyAttrs}>
        {showSnb && (
          <nav ref={snbRef} className="snb" id="snb" aria-label={`${snbModule?.label ?? "하위"} 메뉴`}>
            <div className="snb-user">
              <strong>{user?.name}</strong>
              {meta}
            </div>
            <ul className="snb-modules">
              {modules.map((m) => (
                <li key={m.id}>
                  <button type="button" className="btn-secondary btn-sm" aria-pressed={m.id === snbGroup} onClick={() => setDrawerModule(m.id)}>
                    {m.label}
                  </button>
                </li>
              ))}
            </ul>
            <div className="snb-head">
              <span className="snb-title">{snbModule?.label}</span>
              {!narrow && (
                <IconButton className="btn-ghost" size="sm" label={snbClosed ? "메뉴 펼치기" : "메뉴 접기"} aria-expanded={!snbClosed} onClick={toggleSnb}>
                  {snbClosed ? <IconChevronsRight /> : <IconChevronsLeft />}
                </IconButton>
              )}
            </div>
            {sectionsOf(snbItems).map((g, i) => (
              <div key={g.section ?? `_${i}`}>
                {g.section && (
                  <p className="snb-group" id={`${uid}-g${i}`}>
                    {g.section}
                  </p>
                )}
                <ul aria-labelledby={g.section ? `${uid}-g${i}` : undefined}>
                  {g.items.map((m) => (
                    <li key={m.path}>
                      <NavLink to={m.path} end={hasNestedMenu(m, snbItems)} onClick={() => setDrawer(false)}>
                        {m.label}
                      </NavLink>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </nav>
        )}
        <div className="drawer-backdrop" onClick={() => setDrawer(false)} />
        <main className="app-main" id="main" tabIndex={-1}>
          {hasAside && (headSlot ? createPortal(asideOpenButton, headSlot) : asideOpenButton)}
          <Outlet key={entityKey(currentEntity)} />
        </main>
        {renderAside && aside && (
          <aside ref={asideRef} className="aside" id={`${uid}-aside`} aria-label={aside.title}>
            <div className="aside-head">
              <h2>{aside.title}</h2>
              <IconButton className="btn-ghost" size="sm" label="패널 접기" aria-expanded onClick={collapseAside}>
                <IconChevronsRight />
              </IconButton>
            </div>
            {aside.content}
          </aside>
        )}
      </div>
    </div>
    </ToastProvider>
  );
}

/** 설치 때 데모 데이터를 고른 시연 환경이면 모든 화면 위에 영구 표시한다(B38 D5). 설정을 읽지 못하면 아무것도 그리지 않는다. */
function DemoBanner() {
  const [demo, setDemo] = useState(false);
  useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const c = await api.get<{ demoMode?: boolean }>("/api/auth/config");
        if (alive) setDemo(c?.demoMode === true);
      } catch {
        /* 배너는 부가 정보라 읽지 못하면 그리지 않는다 */
      }
    })();
    return () => {
      alive = false;
    };
  }, []);
  if (!demo) return null;
  return (
    <p className="notice notice-warn demo-banner" role="status">
      시연 환경: 데모 데이터가 들어 있습니다. 실제 업무에는 쓰지 마세요.
    </p>
  );
}

export function Layout({ menu }: { menu: MenuItem[] }) {
  const { user, loading } = useAuth();
  // 로그인하지 않은 채 레이아웃 안의 어떤 주소(홈 포함)를 열어도 빈 메뉴 껍데기를 보이지 않고 로그인 화면으로 보낸다.
  if (loading) return <p className="auth-box">불러오는 중…</p>;
  if (!user) return <Navigate to="/login" replace />;
  return (
    <AsideProvider>
      <Shell menu={menu} />
    </AsideProvider>
  );
}

// (web/src/layout). 로그인 뒤 첫 화면. 각 모듈이 선언한 카드 중 권한이 있는 것만 그린다.
import { Component, useMemo, type ErrorInfo, type ReactNode } from "react";
import { useAuth } from "../auth/AuthProvider";
import type { PermissionOrOpen } from "../auth/permissions";
import { useAside } from "./aside";
import type { HomeAsideSection, HomeCard } from "./routeModule";
import { EmptyState, Notice, PageHead } from "./ui";

/** 권한이 있는 카드만 order 순으로 돌려준다. 권한 없는 카드는 목록에서 빠지므로 마운트되지 않는다. */
export function visibleCards(cards: HomeCard[], can: (perm: PermissionOrOpen) => boolean, hasModule: (key: string) => boolean = () => true): HomeCard[] {
  return cards.filter((c) => can(c.permission) && (!c.module || hasModule(c.module))).sort((a, b) => a.order - b.order);
}

/** 카드 하나가 오류를 내도 홈 전체가 깨지지 않게 한다. */
class CardBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(_e: Error, _i: ErrorInfo) {
    // 화면에는 일반 문구만 보인다.
  }
  render() {
    return this.state.failed ? <Notice tone="error">이 항목을 표시하지 못했습니다.</Notice> : this.props.children;
  }
}

/** 권한이 있는 우측 패널 절만 order 순으로 돌려준다. */
export function visibleAside(sections: HomeAsideSection[], can: (perm: PermissionOrOpen) => boolean): HomeAsideSection[] {
  return sections.filter((s) => can(s.permission)).sort((a, b) => a.order - b.order);
}

export function HomePage({ cards, asideSections = [] }: { cards: HomeCard[]; asideSections?: HomeAsideSection[] }) {
  const { user, can, hasModule, roleText } = useAuth();
  const shown = visibleCards(cards, can, hasModule);
  const asideShown = visibleAside(asideSections, can);
  const asideKey = asideShown.map((a) => a.id).join("|");
  const asideContent = useMemo(
    () =>
      asideShown.length === 0 ? null : (
        <>
          {asideShown.map(({ id, title, Component: Body }) => (
            <section key={id} className="aside-section" data-home-aside={id}>
              <h3>{title}</h3>
              <CardBoundary>
                <Body />
              </CardBoundary>
            </section>
          ))}
        </>
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [asideKey],
  );
  // 권한 있는 절이 하나도 없으면 패널을 그리지 않는다.
  useAside(asideContent ? { title: "오늘 할 일", content: asideContent } : null);
  return (
    <section>
      <PageHead title="홈" />
      {user && (
        <p>
          {user.name}님, 안녕하세요. ({roleText || "권한 없음"})
        </p>
      )}
      {shown.length === 0 && <EmptyState>지금 표시할 항목이 없습니다. 위쪽 메뉴에서 업무 화면을 선택하세요.</EmptyState>}
      <div className="card-grid">
        {shown.map(({ id, title, Component: Body }) => (
          <section key={id} aria-label={title} data-home-card={id} className="card">
            <h2>{title}</h2>
            <CardBoundary>
              <Body />
            </CardBoundary>
          </section>
        ))}
      </div>
    </section>
  );
}

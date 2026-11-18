// 영역별 화면 모듈의 형식이다.
// 각 영역은 자기 디렉터리의 routes.tsx 한 파일에서 RouteModule 을 export 하고, 화면 추가는 거기서만 한다.
// src/routes.ts(platform)가 모듈들을 모으므로 영역이 서로의 파일을 고칠 일이 없다.
import type { ComponentType } from "react";
import type { RouteObject } from "react-router-dom";
import type { PermissionOrOpen } from "../auth/permissions";

/** GNB 모듈. 순서는 Layout 의 상수 배열이 정한다. */
export type MenuGroup = "home" | "purchase" | "approval" | "inventory" | "docai" | "org" | "finance" | "admin" | "user";

export interface MenuItem {
  /** 화면에 보이는 한국어 이름. 내부 용어(ledger, RBAC 등)를 쓰지 않는다. */
  label: string;
  /** 이동 경로. routes 중 하나와 일치해야 한다. */
  path: string;
  /** 이 권한이 없으면 메뉴에서 숨기고 라우트도 RequirePermission 으로 막는다. */
  permission: PermissionOrOpen;
  /** 있으면 permission 대신 이 중 하나라도 있을 때 보인다(권한이 둘로 갈린 화면, 예: 기초 데이터 가져오기). 라우트 가드는 guardedAny 로 같은 목록을 쓴다. */
  anyOf?: PermissionOrOpen[];
  /** 참이면 고를 수 있는 법인이 둘 이상일 때만 메뉴에 보인다(법인이 하나면 법인 관련 요소를 숨긴다, B44 AC-4425). 라우트는 그대로 열려 있다. */
  multiEntityOnly?: boolean;
  /** 있으면 그 업무 모듈(B50)이 켜져 있을 때만 메뉴에 보인다. 라우트는 서버가 꺼진 모듈을 404 로 막는다. */
  module?: string;
  /** 메뉴 정렬 순서(작을수록 위). */
  order: number;
  /** GNB 모듈. 알림·내 계정·개인 토큰 같은 사용자 메뉴 화면은 "user"(GNB 모듈에도 SNB 에도 나오지 않고 종 아이콘·사용자 메뉴로 간다). */
  group: MenuGroup;
  /** SNB 소제목(지금은 재무만: 전표·지급·자금·카드·세무). 없으면 소제목 없이. */
  section?: string;
}

/**
 * 홈 화면 카드(B8). 각 영역의 화면 모듈이 RouteModule.homeCards 로 선언한다.
 * 홈은 permission 이 없는 카드를 DOM 에 그리지 않고 Component 를 마운트하지도 않는다.
 * 그래서 카드가 데이터를 불러오는 호출(useEffect 안)은 권한이 있을 때만 일어난다.
 * 카드가 쓰는 API 는 서버 쪽에서도 같은 권한을 선언해야 한다(화면 숨김은 강제가 아니다).
 */
export interface HomeCard {
  /** 카드 식별자(모듈 안에서 유일, 예 "approval-pending"). 화면에는 보이지 않는다. */
  id: string;
  /** 카드 제목. 한국어, 내부 용어를 쓰지 않는다. */
  title: string;
  /** 이 권한이 없으면 카드를 그리지 않는다. */
  permission: PermissionOrOpen;
  /** 있으면 그 업무 모듈(B50)이 켜져 있을 때만 카드를 그린다. */
  module?: string;
  /** 정렬 순서(작을수록 위). */
  order: number;
  /** 카드 본문. 제목 틀은 홈이 그린다. */
  Component: ComponentType;
}

/**
 * 홈 우측 패널 절. 각 영역 모듈이 RouteModule.homeAside 로 선언한다.
 * 홈은 권한 있는 절만 order 순으로 모아 패널("오늘 할 일")을 그린다. 권한 없는 절은 마운트하지 않는다.
 * 절 래퍼(<section class="aside-section">)와 제목(<h3>{title}</h3>)은 HomePage 가 그린다.
 * 절 컴포넌트는 래퍼·제목을 만들지 않고 본문만 그린다(제목이 두 번 나온다).
 */
export interface HomeAsideSection {
  id: string;
  title: string;
  permission: PermissionOrOpen;
  order: number;
  Component: ComponentType;
}

export interface RouteModule {
  /** 모듈 이름(영역 디렉터리 이름). */
  name: string;
  /** 레이아웃 안(auth 모듈은 밖)에 그려질 라우트. element 는 RequirePermission 으로 감싼다. */
  routes: RouteObject[];
  menu: MenuItem[];
  /** 홈 화면에 올릴 카드(없으면 생략). */
  homeCards?: HomeCard[];
  /** 홈 우측 패널에 올릴 절(없으면 생략). */
  homeAside?: HomeAsideSection[];
}

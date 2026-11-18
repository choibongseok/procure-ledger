// 레이아웃 기준점은 이 파일과 web/src/styles/app.css 의 @media 두 곳에만 있다.
// 미디어 쿼리는 CSS 변수를 못 쓰므로 값을 바꾸면 app.css 의 아래 두 @media 도 함께 고친다.
//   NARROW_MAX = 760   ↔ app.css `@media (max-width: 760px)`   (GNB 햄버거·SNB 서랍)
//   ASIDE_MIN  = 1280  ↔ app.css `@media (max-width: 1279px)`  (우측 패널 숨김·겹쳐 열기)
import { useEffect, useState } from "react";

export const NARROW_MAX = 760;
export const ASIDE_MIN = 1280;

export const NARROW_QUERY = `(max-width: ${NARROW_MAX}px)`;
export const ASIDE_HIDDEN_QUERY = `(max-width: ${ASIDE_MIN - 1}px)`;

function matches(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(query).matches : false;
}

/** 미디어 쿼리 일치 여부. matchMedia 가 없는 환경(일부 시험)에서는 false(넓은 화면)로 본다. */
export function useMedia(query: string): boolean {
  const [on, setOn] = useState(() => matches(query));
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mql = window.matchMedia(query);
    const onChange = () => setOn(mql.matches);
    onChange();
    mql.addEventListener?.("change", onChange);
    return () => mql.removeEventListener?.("change", onChange);
  }, [query]);
  return on;
}

/** 사용자별 접힘 상태 보관. 저장소가 막혀 있으면 기본값을 쓰고 저장은 조용히 건너뛴다. */
export function readFlag(key: string, fallback: boolean): boolean {
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}

export function writeFlag(key: string, value: boolean): void {
  try {
    window.localStorage.setItem(key, value ? "1" : "0");
  } catch {
    // 저장소가 막혀 있어도 화면은 그대로 동작한다.
  }
}

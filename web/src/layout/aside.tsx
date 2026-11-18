// 우측 패널 슬롯(디자인 인계 visual-ui.md 3절).
// 화면이 useAside({title, content}) 를 부르면 Layout 의 패널에 올라가고, 언마운트되면 지워진다.
// useAside(null) 은 "이 화면은 패널 없음"(data-aside="none"). Provider 밖(테스트)에서는 아무 일도 하지 않는다.
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

export interface AsideSpec {
  title: string;
  content: ReactNode;
}

// 값(spec)과 설정 함수를 컨텍스트 둘로 나눈다. 화면(useAside)은 설정 함수만 구독하므로
// 패널 내용이 바뀌어도 화면이 다시 그려지지 않는다(화면이 내용을 만들고 → 올리고 → 다시 그리는 되먹임을 막는다).
const AsideSpecContext = createContext<AsideSpec | null>(null);
const AsideSetContext = createContext<((spec: AsideSpec | null) => void) | null>(null);

// PageHead 의 .page-head-actions 요소(마운트 지점). 화면에 PageHead 가 있으면 Layout 이 '패널 열기' 단추를 여기로 옮긴다.
const HeadSlotContext = createContext<HTMLElement | null>(null);
const HeadSlotSetContext = createContext<((el: HTMLElement | null) => void) | null>(null);

export function AsideProvider({ children }: { children: ReactNode }) {
  const [spec, setSpec] = useState<AsideSpec | null>(null);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  return (
    <AsideSetContext.Provider value={setSpec}>
      <AsideSpecContext.Provider value={spec}>
        <HeadSlotSetContext.Provider value={setSlot}>
          <HeadSlotContext.Provider value={slot}>{children}</HeadSlotContext.Provider>
        </HeadSlotSetContext.Provider>
      </AsideSpecContext.Provider>
    </AsideSetContext.Provider>
  );
}

/** Layout 이 읽는다: 화면 제목 줄의 단추 자리(없으면 null). */
export function usePageHeadSlot(): HTMLElement | null {
  return useContext(HeadSlotContext);
}

/** PageHead 가 쓴다: 자기 단추 자리를 등록하는 ref 콜백. Provider 밖이면 아무 일도 하지 않는다. */
export function useRegisterPageHeadSlot(): (el: HTMLElement | null) => void {
  const set = useContext(HeadSlotSetContext);
  return (el) => set?.(el);
}

/** Layout 이 패널을 그릴 때 읽는다. Provider 밖이면 null. */
export function useAsideSpec(): AsideSpec | null {
  return useContext(AsideSpecContext);
}

/**
 * 화면이 우측 패널 내용을 올린다. 제목·내용(참조)이 바뀔 때 갱신하고, 언마운트하면 지운다.
 * 내용이 매 렌더 새 객체면 그때마다 올리게 되니, 무거운 내용은 호출하는 쪽에서 useMemo 로 고정한다.
 * Provider 밖(시험)에서는 아무 일도 하지 않는다.
 */
export function useAside(spec: AsideSpec | null): void {
  const setSpec = useContext(AsideSetContext);
  const title = spec?.title;
  const content = spec?.content;
  const present = spec !== null;
  useEffect(() => {
    setSpec?.(present ? { title: title ?? "", content } : null);
  }, [setSpec, present, title, content]);
  useEffect(() => () => setSpec?.(null), [setSpec]);
}

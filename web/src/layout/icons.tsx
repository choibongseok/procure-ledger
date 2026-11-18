// 공용 아이콘 한 벌. 16px stroke SVG(currentColor, 두께는 app.css 의 .icon 이 정한다).
// 경로는 design/system/project/components/*/preview.html 의 svg 를 그대로 옮겼다.
// 예외: menu·panel 은 디자인 미리보기에 없어 같은 격자(24, 선 둥근 끝)로 새로 그렸다.
// 장식이라 aria-hidden 이다. 뜻은 곁의 글자나 IconButton 의 label 이 전한다.
import type { ReactNode } from "react";

function icon(children: ReactNode) {
  return function Icon({ className }: { className?: string }) {
    return (
      <svg className={className ? `icon ${className}` : "icon"} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        {children}
      </svg>
    );
  };
}

export const IconSearch = icon(
  <>
    <circle cx="11" cy="11" r="6.5" />
    <path d="m20 20-4.2-4.2" />
  </>,
);
export const IconBell = icon(
  <>
    <path d="M6 9.5a6 6 0 0 1 12 0c0 6 2.5 7.5 2.5 7.5h-17S6 15.5 6 9.5Z" />
    <path d="M10 20.5a2.2 2.2 0 0 0 4 0" />
  </>,
);
export const IconMenu = icon(<path d="M4 7h16M4 12h16M4 17h16" />);
export const IconX = icon(<path d="M6 6l12 12M18 6 6 18" />);
export const IconChevronsLeft = icon(
  <>
    <path d="m11 17-5-5 5-5" />
    <path d="m18 17-5-5 5-5" />
  </>,
);
export const IconChevronsRight = icon(
  <>
    <path d="m13 17 5-5-5-5" />
    <path d="m6 17 5-5-5-5" />
  </>,
);
export const IconPanel = icon(
  <>
    <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
    <path d="M14.5 4.5v15" />
  </>,
);
export const IconPencil = icon(
  <>
    <path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16v4Z" />
    <path d="m13.5 6.5 4 4" />
  </>,
);
export const IconRefresh = icon(
  <>
    <path d="M20 11a8 8 0 0 0-14.5-4.5L4 8" />
    <path d="M4 4v4h4" />
    <path d="M4 13a8 8 0 0 0 14.5 4.5L20 16" />
    <path d="M20 20v-4h-4" />
  </>,
);
export const IconTrash = icon(
  <>
    <path d="M4 7h16" />
    <path d="M9 7V4.5h6V7" />
    <path d="M6.5 7l1 13h9l1-13" />
  </>,
);
export const IconPaperclip = icon(<path d="M20 11.5 12 19.5a5 5 0 0 1-7-7l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7L9.7 17.2a1.7 1.7 0 0 1-2.4-2.4L15 7" />);
export const IconPlus = icon(<path d="M12 5v14M5 12h14" />);
export const IconCheckCircle = icon(
  <>
    <circle cx="12" cy="12" r="9" />
    <path d="m8.5 12.5 2.5 2.5 4.5-5" />
  </>,
);
export const IconXCircle = icon(
  <>
    <circle cx="12" cy="12" r="9" />
    <path d="m9 9 6 6M15 9l-6 6" />
  </>,
);
export const IconAlert = icon(
  <>
    <path d="M12 3.5 2.5 20h19L12 3.5Z" />
    <path d="M12 10v4.5" />
    <path d="M12 17.5h.01" />
  </>,
);
export const IconInfo = icon(
  <>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 11v5.5" />
    <path d="M12 7.5h.01" />
  </>,
);
export const IconInbox = icon(
  <>
    <path d="M3 13h5l1.5 3h5L16 13h5" />
    <path d="M5.5 5h13L21 13v6H3v-6l2.5-8Z" />
  </>,
);

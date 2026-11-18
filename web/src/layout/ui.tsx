// 공통 화면 부품. 사용법과 색 규칙은 web/src/styles/app.css 머리 주석에 있다.
import { createContext, forwardRef, useCallback, useContext, useEffect, useId, useRef, useState, type ButtonHTMLAttributes, type ComponentType, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useRegisterPageHeadSlot } from "./aside";
import { IconAlert, IconCheckCircle, IconInbox, IconInfo, IconX, IconXCircle } from "./icons";

/**
 * 화면 제목 줄: 왼쪽 h1, 오른쪽 .page-head-actions(화면 단추 children + 접힘·1280 미만일 때 Layout 이 옮겨 오는 '패널 열기').
 * 화면은 h1 을 직접 쓰지 말고 이 부품을 쓴다.
 */
export function PageHead({ title, children }: { title: ReactNode; children?: ReactNode }) {
  const register = useRegisterPageHeadSlot();
  return (
    <div className="page-head">
      <h1>{title}</h1>
      <div className="page-head-actions">
        {children}
        <span className="page-head-slot" ref={register} />
      </div>
    </div>
  );
}

export type Tone = "ok" | "warn" | "bad" | "info" | "muted";

/** 상태 배지. tone 은 뜻(ok 정상·warn 조치 필요·bad 거부/오류·info 진행·muted 중립)이고 글자는 한국어 업무 용어로 쓴다. */
export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

const NOTICE_ICON: Record<"error" | "ok" | "warn" | "info", ComponentType<{ className?: string }>> = {
  error: IconXCircle,
  warn: IconAlert,
  info: IconInfo,
  ok: IconCheckCircle,
};

/** 알림 문구. error 는 role=alert, 나머지는 role=status 다. 맨 앞에 tone 별 장식 아이콘이 붙는다(icon={false} 로 끈다). */
export function Notice({
  tone = "info",
  children,
  testId,
  role,
  icon = true,
}: {
  tone?: "error" | "ok" | "warn" | "info";
  children: ReactNode;
  testId?: string;
  /** 기본은 error 면 alert, 아니면 status. 화면에 알림이 둘 이상 겹칠 때만 바꾼다. */
  role?: "alert" | "status";
  /** 앞 아이콘. 좁은 자리(표 안 등)에서만 끈다. */
  icon?: boolean;
}) {
  const Icon = NOTICE_ICON[tone];
  return (
    <p className={`notice notice-${tone}`} role={role ?? (tone === "error" ? "alert" : "status")} data-testid={testId}>
      {icon && <Icon />}
      <span>{children}</span>
    </p>
  );
}

/** 입력칸 하나의 오류 문구. 입력칸에 aria-invalid="true" 와 aria-describedby={id} 를 함께 준다. 무엇이 안 됐는지 + 할 일을 쓴다. */
export function FieldError({ id, children }: { id: string; children: ReactNode }) {
  return (
    <span id={id} className="field-error">
      <IconXCircle />
      <span>{children}</span>
    </span>
  );
}

/** 목록이 비었을 때. 첫 줄은 무엇이 비었는지, 필요하면 hint 로 다음 행동을 알린다. 앞에 받은편지함 아이콘이 붙는다. */
export function EmptyState({ children, hint, actions }: { children: ReactNode; hint?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="empty">
      <IconInbox />
      <div>
        <p>{children}</p>
        {hint && <p className="hint">{hint}</p>}
        {actions && <div className="empty-actions">{actions}</div>}
      </div>
    </div>
  );
}

/**
 * 표를 감싸 좁은 화면에서 가로 스크롤이 이 영역 안에서만 생기게 한다.
 * density: default 는 행 40·머리행 36, compact 는 행 32(행이 많은 감사 로그 등).
 */
export function TableWrap({ children, density = "default", busy }: { children: ReactNode; density?: "default" | "compact"; busy?: boolean }) {
  return (
    <div className={density === "compact" ? "table-wrap compact" : "table-wrap"} aria-busy={busy ? true : undefined}>
      {children}
    </div>
  );
}

/**
 * 쪽 이동. 이전·다음 단추와 "n–m / 전체 N건". 한 쪽(total <= limit)이면 그리지 않는다.
 * offset 은 0부터, onChange 는 새 offset 을 받는다.
 */
export function Pagination({
  offset,
  limit,
  total,
  onChange,
}: {
  offset: number;
  limit: number;
  total: number;
  onChange: (offset: number) => void;
}) {
  if (total <= limit) return null;
  const from = Math.min(offset + 1, total);
  const to = Math.min(offset + limit, total);
  return (
    <nav className="pagination" aria-label="쪽 이동">
      <button type="button" className="btn-secondary" disabled={offset <= 0} onClick={() => onChange(Math.max(0, offset - limit))}>
        이전
      </button>
      <span className="pagination-range" aria-live="polite">
        {from.toLocaleString("ko-KR")}–{to.toLocaleString("ko-KR")} / 전체 {total.toLocaleString("ko-KR")}건
      </span>
      <button type="button" className="btn-secondary" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
        다음
      </button>
    </nav>
  );
}

/**
 * 불러오는 중 자리 표시 행. 표 tbody 안에 둔다(행 높이는 --table-row-h 를 그대로 지킨다).
 * 쓰는 법: <TableWrap busy={loading}><table>…<tbody>{loading ? <LoadingRows cols={5} /> : rows}</tbody></table></TableWrap>
 */
export function LoadingRows({ cols, rows = 4 }: { cols: number; rows?: number }) {
  return (
    <>
      <tr>
        <td colSpan={cols} className="sr-only" role="status">
          불러오는 중
        </td>
      </tr>
      {Array.from({ length: rows }, (_, r) => (
        <tr key={r} className="loading-row" aria-hidden="true">
          {Array.from({ length: cols }, (_, c) => (
            <td key={c}>
              <span className="loading-bar" />
            </td>
          ))}
        </tr>
      ))}
    </>
  );
}

/** 단계 표시줄. current 는 1부터, 끝난 단계는 "완료", 지금 단계는 aria-current. busyLabel 이 있으면 처리 중 상태를 덧붙인다. */
export function Stepper({ steps, current, busyLabel }: { steps: readonly string[]; current: number; busyLabel?: string }) {
  return (
    <ol className="chip-row stepper" aria-label="진행 단계">
      {steps.map((label, i) => {
        const n = i + 1;
        const tone: Tone = n < current ? "ok" : n === current ? "info" : "muted";
        return (
          <li key={label} aria-current={n === current ? "step" : undefined}>
            <Badge tone={tone}>
              {n}. {label}
              {n < current ? " 완료" : ""}
            </Badge>
          </li>
        );
      })}
      {busyLabel && (
        <li role="status">
          <Badge tone="warn">{busyLabel}</Badge>
        </li>
      )}
    </ol>
  );
}

/**
 * 아이콘 전용 버튼. 글자가 없으므로 label(aria-label)이 필수이고 마우스를 올리면 같은 글이 title 로 보인다.
 * 아이콘은 children 으로 넘기고 장식이므로 스크린리더가 읽지 않게 가린다.
 */
export const IconButton = forwardRef<
  HTMLButtonElement,
  {
    label: string;
    children: ReactNode;
    /** md 36px, sm 28px(btn-sm). */
    size?: "md" | "sm";
  } & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label" | "children">
>(function IconButton({ label, children, className, size = "md", ...rest }, ref) {
  const cls = ["btn-icon", className ?? "btn-secondary", size === "sm" ? "btn-sm" : ""].filter(Boolean).join(" ");
  return (
    <button type="button" {...rest} ref={ref} className={cls} aria-label={label} title={label}>
      <span aria-hidden="true">{children}</span>
    </button>
  );
});

const FOCUSABLE = 'button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])';

/**
 * 공용 확인 대화상자(위험 동작용). 제목 + 결과 설명(무엇이 어떻게 되는지) + 위험 버튼 + 취소.
 * - 열리면 취소 버튼에 포커스가 간다(실수로 Enter 를 눌러도 위험 동작이 실행되지 않게). 닫히면 열기 전 포커스로 돌아간다.
 * - Esc 와 바깥 영역 클릭은 취소다. Tab 은 대화상자 안에서만 돈다.
 * - 열려 있는 동안(busy) 버튼은 꺼진다. 호출하는 쪽이 실패 문구를 따로 보인다.
 * - 처리 중(busy)에는 Esc·바깥 클릭도 닫지 않는다.
 */
export function ConfirmDialog({
  title,
  children,
  confirmLabel,
  cancelLabel = "취소",
  onConfirm,
  onCancel,
  busy = false,
  variant = "danger",
}: {
  title: string;
  /** 결과 설명. 돌이킬 수 없으면 그렇다고 쓴다. */
  children: ReactNode;
  /** 위험 버튼 글자. "확인" 대신 동작 이름(예: "폐기")을 쓴다. */
  confirmLabel: string;
  cancelLabel?: string;
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
  /** 파괴 동작(폐기·삭제·반려)이 아니면 "primary" 를 쓴다(확정 단추가 기본 주 버튼). */
  variant?: "danger" | "primary";
}) {
  const id = useId();
  const boxRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const cancelFn = useRef(onCancel);
  cancelFn.current = onCancel;
  const busyRef = useRef(busy);
  busyRef.current = busy;

  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        if (!busyRef.current) cancelFn.current();
        return;
      }
      if (e.key !== "Tab") return;
      const items = Array.from(boxRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || !boxRef.current?.contains(active))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !boxRef.current?.contains(active))) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      before?.focus?.();
    };
  }, []);

  return (
    <div
      className="dialog-backdrop"
      onMouseDown={(e) => e.target === e.currentTarget && variant !== "danger" && !busyRef.current && onCancel()}
    >
      <div
        ref={boxRef}
        className="dialog"
        role={variant === "danger" ? "alertdialog" : "dialog"}
        aria-modal="true"
        aria-busy={variant === "danger" && busy ? true : undefined}
        aria-labelledby={`${id}-t`}
        aria-describedby={`${id}-d`}
      >
        <h2 id={`${id}-t`}>{title}</h2>
        <div id={`${id}-d`} className="dialog-body">
          {children}
        </div>
        <div className="dialog-actions">
          <button ref={cancelRef} type="button" className="btn-secondary" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </button>
          <button type="button" className={variant === "danger" ? "btn-danger" : undefined} onClick={onConfirm} disabled={busy}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

/** 경로. 상세·하위 화면 맨 위에만 둔다. 마지막 항목은 현재 화면(링크 없음, aria-current="page"). */
export function Breadcrumbs({ items }: { items: readonly { label: string; to?: string }[] }) {
  return (
    <nav aria-label="경로" className="breadcrumbs">
      <ol>
        {items.map((it, i) => {
          const last = i === items.length - 1;
          return (
            <li key={`${i}-${it.label}`}>
              {last || !it.to ? <span aria-current={last ? "page" : undefined}>{it.label}</span> : <Link to={it.to}>{it.label}</Link>}
              {!last && (
                <span className="breadcrumbs-sep" aria-hidden="true">
                  ›
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

const TOAST_MS = 4000;
const ToastContext = createContext<((message: string) => void) | null>(null);

/** 저장·발송 성공을 알리는 짧은 알림. 한 번에 하나, 4초 뒤 자동으로 닫힌다. 오류에는 쓰지 않는다(본문 Notice). */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<{ id: number; message: string } | null>(null);
  const seq = useRef(0);
  const show = useCallback((message: string) => {
    seq.current += 1;
    setToast({ id: seq.current, message });
  }, []);
  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), TOAST_MS);
    return () => window.clearTimeout(t);
  }, [toast]);
  return (
    <ToastContext.Provider value={show}>
      {children}
      <div className="toast-region" role="status" aria-live="polite">
        {toast && (
          <div className="toast" key={toast.id}>
            <span>{toast.message}</span>
            <button type="button" className="toast-close" aria-label="알림 닫기" onClick={() => setToast(null)}>
              <IconX />
            </button>
          </div>
        )}
      </div>
    </ToastContext.Provider>
  );
}

/** 성공 알림을 띄우는 함수. ToastProvider 밖(부품 단독 시험 등)에서는 아무 일도 하지 않는다. */
export function useToast(): (message: string) => void {
  return useContext(ToastContext) ?? noopToast;
}
const noopToast = () => {};

export type SortOrder = "asc" | "desc";

/** 정렬 가능한 열 머리. 정렬 중인 열만 aria-sort 와 진한 화살표를 가진다. */
export function SortHeader({
  label,
  field,
  sort,
  order,
  onSort,
  align,
}: {
  label: string;
  field: string;
  sort: string;
  order: SortOrder;
  onSort: (field: string) => void;
  /** "num" 이면 숫자 열처럼 머리글과 단추를 오른쪽에 맞춘다. */
  align?: "num";
}) {
  const active = sort === field;
  const aria = active ? (order === "asc" ? "ascending" : "descending") : "none";
  return (
    <th className={align === "num" ? "num" : undefined} aria-sort={aria}>
      <button type="button" className="th-sort" data-active={active ? "true" : undefined} onClick={() => onSort(field)}>
        {label}
        <span className="th-sort-arrow" aria-hidden="true">
          {active ? (order === "asc" ? "▲" : "▼") : "↕"}
        </span>
      </button>
    </th>
  );
}

/** 적용된 조건 칩 줄. 조건이 없으면 아무것도 그리지 않는다. */
export function FilterChips({
  chips,
  onClearAll,
  hideClearAll,
}: {
  chips: readonly { label: string; onClear: () => void }[];
  onClearAll: () => void;
  /** 같은 화면의 빈 상태가 이미 "조건 지우기"를 보일 때 칩 줄의 전체 지우기를 숨긴다(칩별 해제는 유지). */
  hideClearAll?: boolean;
}) {
  if (chips.length === 0) return null;
  return (
    <div className="filter-chips">
      {chips.map((c) => (
        <span key={c.label} className="filter-chip">
          {c.label}
          <button type="button" aria-label={`조건 해제: ${c.label}`} onClick={c.onClear}>
            <IconX />
          </button>
        </span>
      ))}
      {!hideClearAll && (
        <button type="button" className="btn-ghost btn-sm" onClick={onClearAll}>
          조건 지우기
        </button>
      )}
    </div>
  );
}

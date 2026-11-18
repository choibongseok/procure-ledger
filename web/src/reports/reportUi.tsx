// 업무 보고서(B28) 공용 조각: 탭, 시간 표기, 오류 문구, 집행률 막대.
import { NavLink } from "react-router-dom";
import { ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { businessMessage } from "../purchase/useLoad";
import "./reports.css";

export const LOAD_FAILED = "보고서를 불러오지 못했습니다. 기간을 확인하고 다시 조회하세요.";

/** 조회 실패 문구: 서버가 기간 오류(400)를 업무 문구로 돌려주면 그대로, 그 밖에는 5.1 의 조회 실패 문구. */
export function reportError(e: unknown): string {
  if (e instanceof ApiError && e.status === 400 && e.message) return businessMessage(e.message);
  return LOAD_FAILED;
}

/** 조회 약속을 보고서 문구의 ApiError 로 바꾼다(useLoad 의 errorText 가 그대로 화면에 낸다). */
export function wrapReport<T>(p: Promise<T>): Promise<T> {
  return p.catch((e) => {
    throw new ApiError(0, reportError(e));
  });
}

/** 분 → "1일 3시간 20분". 0분이면 "0분". */
export function formatMinutes(total: number): string {
  const m = Math.max(0, Math.round(total));
  const d = Math.floor(m / 1440);
  const h = Math.floor((m % 1440) / 60);
  const mm = m % 60;
  const parts: string[] = [];
  if (d) parts.push(`${d}일`);
  if (h) parts.push(`${h}시간`);
  if (mm || parts.length === 0) parts.push(`${mm}분`);
  return parts.join(" ");
}

export const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);
export const kstDaysAgo = (n: number) => new Date(Date.now() + 9 * 3600 * 1000 - n * 86400000).toISOString().slice(0, 10);

/** 날짜 두 개의 기간 검사(5.1 문구). 문제가 없으면 빈 문자열. */
export function dayRangeProblem(from: string, to: string): string {
  if (!from || !to) return "기간을 입력하세요.";
  if (to < from) return "종료일은 시작일 이후여야 합니다.";
  const f = new Date(from);
  const limit = new Date(f);
  limit.setMonth(limit.getMonth() + 12);
  if (new Date(to) > limit) return "조회 기간은 최대 12개월입니다.";
  return "";
}

/** 월(YYYY-MM) 두 개의 기간 검사. */
export function monthRangeProblem(from: string, to: string): string {
  if (!from || !to) return "기간을 입력하세요.";
  if (to < from) return "종료일은 시작일 이후여야 합니다.";
  const n = (Number(to.slice(0, 4)) * 12 + Number(to.slice(5, 7))) - (Number(from.slice(0, 4)) * 12 + Number(from.slice(5, 7))) + 1;
  if (n > 12) return "조회 기간은 최대 12개월입니다.";
  return "";
}

/** 보고서 탭. 거래처 탭은 거래처 평가 권한이 있을 때만 보인다(탭 단위 권한). */
export function ReportTabs() {
  const { can } = useAuth();
  const tabs = [
    { to: "/reports/purchase", label: "구매 실적", show: true },
    { to: "/reports/vendors", label: "거래처별 실적·평가", show: can("vendor:rating_read") },
    { to: "/reports/approvals", label: "결재 소요 시간", show: true },
  ];
  return (
    <nav className="report-tabs" aria-label="보고서 종류">
      {tabs
        .filter((t) => t.show)
        .map((t) => (
          <NavLink key={t.to} to={t.to} className={({ isActive }) => (isActive ? "report-tab active" : "report-tab")}>
            {t.label}
          </NavLink>
        ))}
    </nav>
  );
}

/** 숫자와 함께 보이는 막대. 색만으로 뜻을 전하지 않는다(숫자 글자가 같이 있다). */
export function PctBar({ pct, tone }: { pct: number | null; tone: "ok" | "warn" | "bad" | "muted" }) {
  if (pct === null) return <span>—</span>;
  return (
    <span className="pct-bar">
      <span className="pct-track" aria-hidden="true">
        <span className={`pct-fill ${tone}`} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
      </span>
      <span className="pct-num">{pct.toFixed(1)}%</span>
    </span>
  );
}

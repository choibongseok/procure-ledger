// 홈 화면의 "오늘 AI 사용량" 카드. 로그인한 누구나 본인 범위를 보고, 전체 합계는 서버가 사용량 조회 권한이 있을 때만 준다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import type { HomeCard } from "../layout/routeModule";

interface Counts {
  calls: number;
  blocked: number;
  tokens: number;
}

export interface TodayUsage {
  day: string;
  mine: Counts;
  all?: Counts;
  limit?: { scope: string; dailyTokenLimit: number; usedTokens?: number; nearLimit: boolean };
}

const SCOPE_LABEL: Record<string, string> = { user: "내", department: "부서", role: "역할" };
const n = (v: number) => v.toLocaleString("ko-KR");

export function TodayUsageCard() {
  const { can } = useAuth();
  const [data, setData] = useState<TodayUsage | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    api
      .get<TodayUsage>("/api/llm/usage/today")
      .then((d) => alive && (d?.mine ? setData(d) : setFailed(true)))
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, []);

  if (failed) return <p role="alert">사용량을 불러오지 못했습니다.</p>;
  if (!data) return <p>불러오는 중…</p>;
  const lim = data.limit;
  const used = lim?.usedTokens;
  const pct = lim && used !== undefined && lim.dailyTokenLimit > 0 ? Math.min(100, Math.round((used / lim.dailyTokenLimit) * 100)) : null;
  return (
    <div>
      <p>
        내 호출 <strong>{n(data.mine.calls)}</strong>회 · 토큰 {n(data.mine.tokens)}
        {data.mine.blocked > 0 && <> · 막힌 호출 {n(data.mine.blocked)}회</>}
      </p>
      {data.all && (
        <p>
          전체 호출 <strong>{n(data.all.calls)}</strong>회 · 토큰 {n(data.all.tokens)}
          {data.all.blocked > 0 && <> · 막힌 호출 {n(data.all.blocked)}회</>}
        </p>
      )}
      {lim ? (
        <p>
          {SCOPE_LABEL[lim.scope] ?? ""} 일일 한도 {n(lim.dailyTokenLimit)} 토큰
          {used !== undefined && <> 중 {n(used)} 사용{pct !== null && <> ({pct}%)</>}</>}
          {lim.nearLimit && <strong role="status"> · 한도에 가까워졌습니다</strong>}
        </p>
      ) : (
        <p>적용된 일일 한도가 없습니다.</p>
      )}
      {can("llm:usage_read") && <Link to="/llm-usage">자세히 보기</Link>}
    </div>
  );
}

export const llmHomeCards: HomeCard[] = [
  { id: "ai-usage-today", title: "오늘 AI 사용량", permission: "authenticated", order: 50, Component: TodayUsageCard },
];

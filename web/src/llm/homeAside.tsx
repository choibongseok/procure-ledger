// 홈 우측 패널 "AI 도움" 절: 오늘 사용량·한도와 문서 검증 바로가기.
// 바깥 래퍼와 절 제목은 HomePage 가 그린다(절 컴포넌트는 내용만). 사용량 호출은 홈 카드 "오늘 AI 사용량"과 같은 API 를 쓴다. 절은 llm:usage_read 권한이 있을 때만 마운트된다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import type { HomeAsideSection } from "../layout/routeModule";
import { Badge } from "../layout/ui";
import type { TodayUsage } from "./homeCards";

const n = (v: number) => v.toLocaleString("ko-KR");

export function AiHelpSection() {
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

  const lim = data?.limit;
  const used = lim?.usedTokens;
  const overLimit = !!lim && used !== undefined && lim.dailyTokenLimit > 0 && used >= lim.dailyTokenLimit;
  return (
    <>
      {failed && <p role="alert">사용량을 불러오지 못했습니다.</p>}
      {!failed && !data && <p>불러오는 중…</p>}
      {data && (
        <>
          <p>
            오늘 호출 <strong>{n(data.mine.calls)}</strong>회 · 토큰 {n(data.mine.tokens)}
          </p>
          {lim ? (
            <p>
              일일 한도 {n(lim.dailyTokenLimit)} 토큰{used !== undefined && <> 중 {n(used)} 사용</>}{" "}
              {overLimit ? <Badge tone="bad">한도 초과</Badge> : lim.nearLimit ? <Badge tone="warn">한도 근접</Badge> : null}
            </p>
          ) : (
            <p>적용된 일일 한도가 없습니다.</p>
          )}
        </>
      )}
      {can("document:read") && <Link to="/documents">문서 검증 열기</Link>}
    </>
  );
}

export const llmHomeAside: HomeAsideSection[] = [
  { id: "ai-help", title: "AI 도움", permission: "llm:usage_read", order: 30, Component: AiHelpSection },
];

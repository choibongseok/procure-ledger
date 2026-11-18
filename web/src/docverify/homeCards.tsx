// 홈 화면의 "판정 확정 대기" 카드(재무). 권한이 없으면 홈이 마운트하지 않으므로 API 도 호출되지 않는다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { HomeCard } from "../layout/routeModule";
import { OUTCOME_LABEL, type Outcome } from "./api";

export interface PendingItem {
  id: number;
  poId: number;
  outcome: Outcome;
  supplier: string;
  totalAmount: number;
  createdAt: string;
}

interface PendingSummary {
  total: number;
  items: PendingItem[];
}

export function PendingConfirmCard() {
  const [data, setData] = useState<PendingSummary | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    api
      .get<PendingSummary>("/api/documents/pending-confirm?limit=5")
      .then((d) => alive && setData({ total: d?.total ?? 0, items: d?.items ?? [] }))
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, []);

  if (failed) return <p role="alert">확정 대기 현황을 불러오지 못했습니다.</p>;
  if (!data) return <p>불러오는 중…</p>;
  if (data.total === 0) return <p>확정을 기다리는 판정이 없습니다.</p>;
  return (
    <div>
      <p>
        확정 대기 <strong>{data.total}</strong>건
      </p>
      <ul>
        {data.items.map((it) => (
          <li key={it.id}>
            {it.supplier || "공급자 미상"} · 발주 {it.poId}번 · {it.totalAmount.toLocaleString("ko-KR")}원 · AI 판정 {OUTCOME_LABEL[it.outcome] ?? "확인 필요"}
          </li>
        ))}
      </ul>
      <Link to="/documents">문서 검증에서 확정하기</Link>
    </div>
  );
}

export const docverifyHomeCards: HomeCard[] = [
  { id: "doc-pending-confirm", title: "판정 확정 대기", permission: "document:confirm", order: 40, Component: PendingConfirmCard },
];

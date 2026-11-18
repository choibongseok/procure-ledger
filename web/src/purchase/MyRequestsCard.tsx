// 홈 카드 "내 요청 진행": 내가 올린 구매요청의 상태별 건수와 결재자 없음 표시. purchase:read 권한이 있을 때만 그려진다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { PR_STATUS_LABEL, label } from "./labels";
import { useLoad } from "./useLoad";

interface MySummary {
  total: number;
  byStatus: Record<string, number>;
  noApprover: number;
}

const ORDER = ["draft", "submitted", "in_review", "rejected", "approved", "ordered"] as const;

export function MyRequestsCard() {
  const { data, error, loading } = useLoad(() => api.get<MySummary>("/api/purchase-requests/my-summary"), []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  if (data.total === 0) return <p>올린 구매요청이 없습니다.</p>;
  return (
    <div>
      <ul>
        {ORDER.filter((s) => (data.byStatus[s] ?? 0) > 0).map((s) => (
          <li key={s}>
            {label(PR_STATUS_LABEL, s)} {data.byStatus[s].toLocaleString("ko-KR")}건
          </li>
        ))}
      </ul>
      {data.noApprover > 0 && (
        <Notice tone="warn" role="alert">
          결재자 없음 {data.noApprover}건: 결재를 맡을 사람이 없어 멈춰 있습니다. <Link to="/purchase-requests">요청 목록</Link>에서 회수하세요.
        </Notice>
      )}
    </div>
  );
}

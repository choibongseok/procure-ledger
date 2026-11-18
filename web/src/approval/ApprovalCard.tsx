// 홈 카드 "결재 대기": 내 결재함 건수와 상위 몇 건. 이 카드는 approval:read 권한이 있을 때만 그려진다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { won } from "../purchase/labels";
import type { PurchaseRequest } from "../purchase/types";
import { useLoad } from "../purchase/useLoad";

const TOP = 3;

export function ApprovalCard() {
  const { data, error, loading } = useLoad(() => api.get<PurchaseRequest[]>("/api/approvals/pending"), []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  if (data.length === 0) return <p>결재할 건이 없습니다.</p>;
  return (
    <div>
      <p>
        내 결재함 <strong>{data.length.toLocaleString("ko-KR")}건</strong>
      </p>
      <ul>
        {data.slice(0, TOP).map((pr) => (
          <li key={pr.id}>
            <Link to={`/approvals/${pr.id}`}>{pr.title}</Link> · {pr.requesterName} · {won(pr.totalAmount)}
          </li>
        ))}
      </ul>
      {data.length > TOP && <Link to="/approvals">결재함에서 {data.length - TOP}건 더 보기</Link>}
    </div>
  );
}

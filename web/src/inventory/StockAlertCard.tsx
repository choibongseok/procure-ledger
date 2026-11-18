// 홈 카드 "재고 경보": 안전재고 미달 품목 수와 유효기한 임박 로트 수. inventory:read 권한이 있을 때만 그려진다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import type { ExpiringLot, ItemStock } from "../purchase/types";
import { useLoad } from "../purchase/useLoad";

const EXPIRY_DAYS = 30;

export function StockAlertCard() {
  const { data, error, loading } = useLoad(async () => {
    const [low, soon] = await Promise.all([
      api.get<ItemStock[]>("/api/stock/below-safety"),
      api.get<ExpiringLot[]>(`/api/stock/expiring?days=${EXPIRY_DAYS}`),
    ]);
    return { low: low.length, soon: soon.length };
  }, []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  if (data.low === 0 && data.soon === 0) return <p>재고 경보가 없습니다.</p>;
  return (
    <div>
      <ul>
        <li>안전재고 미달 {data.low.toLocaleString("ko-KR")}품목</li>
        <li>
          유효기한 {EXPIRY_DAYS}일 이내 {data.soon.toLocaleString("ko-KR")}로트
        </li>
      </ul>
      <Link to="/inventory">재고 화면에서 보기</Link>
    </div>
  );
}

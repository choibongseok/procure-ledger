// 홈 카드 "알림": 안 읽은 알림 건수와 최근 3건. notification:read 권한이 있을 때만 그려진다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import type { AppNotification } from "../purchase/types";
import { useLoad } from "../purchase/useLoad";

const TOP = 3;

export function NotificationCard() {
  const { data, error, loading } = useLoad(async () => {
    const [count, list] = await Promise.all([
      api.get<{ unread: number }>("/api/notifications/unread-count"),
      api.get<AppNotification[]>(`/api/notifications?unread=1&limit=${TOP}`),
    ]);
    return { unread: count.unread, list };
  }, []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  if (data.unread === 0) return <p>새 알림이 없습니다.</p>;
  return (
    <div>
      <p>
        안 읽은 알림 <strong>{data.unread.toLocaleString("ko-KR")}건</strong>
      </p>
      <ul>
        {data.list.map((n) => (
          <li key={n.id}>{n.title}</li>
        ))}
      </ul>
      <Link to="/notifications">알림 전체 보기</Link>
    </div>
  );
}

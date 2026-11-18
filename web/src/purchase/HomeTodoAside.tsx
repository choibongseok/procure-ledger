// 홈 우측 패널 "오늘 할 일" 절(권한은 notification:read, 할 일 소절은 purchase:create 일 때만): 반려된 내 요청·임시저장 중인 요청 건수(목록 링크)와 최근 알림 3건.
// 할 일 소절은 반려·임시저장이 0건이어도 소절 안에 "할 일이 없습니다." 한 줄을 둔다(알림만 있을 때도 같다).
// 둘 다 비면 "오늘 할 일이 없습니다" 한 줄만 보인다. 알림은 notification:read 권한이 있을 때만 불러온다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Notice } from "../layout/ui";
import type { AppNotification } from "./types";
import { useLoad } from "./useLoad";

interface MySummary {
  total: number;
  byStatus: Record<string, number>;
}

const RECENT = 3;

export function HomeTodoAside() {
  const { can } = useAuth();
  const canNotify = can("notification:read");
  const canTodo = can("purchase:create");
  const { data, error, loading } = useLoad(async () => {
    const [sum, list] = await Promise.all([
      canTodo ? api.get<MySummary>("/api/purchase-requests/my-summary") : Promise.resolve<MySummary>({ total: 0, byStatus: {} }),
      canNotify ? api.get<AppNotification[]>(`/api/notifications?limit=${RECENT}`) : Promise.resolve([] as AppNotification[]),
    ]);
    return { rejected: sum.byStatus?.rejected ?? 0, draft: sum.byStatus?.draft ?? 0, list };
  }, [canNotify, canTodo]);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  const todo = data.rejected > 0 || data.draft > 0;
  if (!todo && data.list.length === 0) return <p>오늘 할 일이 없습니다.</p>;
  return (
    <>
      {canTodo && (
        <section>
          <h4>할 일</h4>
          {!todo && <p>할 일이 없습니다.</p>}
          {todo && (
          <ul>
            {data.rejected > 0 && (
              <li>
                <Link to="/purchase-requests?status=rejected">반려된 내 요청 {data.rejected.toLocaleString("ko-KR")}건</Link>
              </li>
            )}
            {data.draft > 0 && (
              <li>
                <Link to="/purchase-requests?status=draft">임시저장 중인 요청 {data.draft.toLocaleString("ko-KR")}건</Link>
              </li>
            )}
          </ul>
          )}
        </section>
      )}
      {data.list.length > 0 && (
        <section>
          <h4>알림</h4>
          <ul>
            {data.list.map((n) => (
              <li key={n.id}>{n.title}</li>
            ))}
          </ul>
          <Link to="/notifications">알림 전체 보기</Link>
        </section>
      )}
    </>
  );
}

// 내 알림 화면(B21 3.9·7.8): 앱 안 알림 목록, 안 읽음만 보기, 읽음 처리. 메일·메신저 발송은 없다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import type { AppNotification } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";

export function NotificationsPage() {
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [error, setError] = useState("");
  const { data, error: loadError, loading, reload } = useLoad(
    () => api.get<AppNotification[]>(`/api/notifications?limit=100${unreadOnly ? "&unread=1" : ""}`),
    [unreadOnly],
  );

  async function markRead(id: number) {
    setError("");
    try {
      await api.post(`/api/notifications/${id}/read`);
      await reload();
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function markAll() {
    setError("");
    try {
      await api.post("/api/notifications/read-all");
      await reload();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <section>
      <h1>알림</h1>
      <div className="form-row">
        <label>
          <input type="checkbox" checked={unreadOnly} onChange={(e) => setUnreadOnly(e.target.checked)} /> 안 읽음만
        </label>
        <button type="button" className="btn-secondary" onClick={() => void markAll()}>
          모두 읽음
        </button>{" "}
        <Link to="/notifications/settings">내 알림 설정</Link>
      </div>
      {(loadError || error) && <Notice tone="error">{error || "알림을 불러오지 못했습니다. 잠시 뒤 다시 시도해 주세요."}</Notice>}
      {loading && !data && <p>불러오는 중…</p>}
      {data && data.length === 0 && <EmptyState>{unreadOnly ? "안 읽은 알림이 없습니다." : "알림이 없습니다."}</EmptyState>}
      {data && data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">시각</th>
                <th>알림</th>
                <th className="nowrap">상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((n) => (
                <tr key={n.id}>
                  <td className="nowrap">{dateTime(n.createdAt)}</td>
                  <td>
                    <strong>{n.title}</strong>
                    <br />
                    {n.link ? (
                      <Link to={n.link} onClick={() => void markRead(n.id)}>
                        {n.body}
                      </Link>
                    ) : (
                      n.body
                    )}
                  </td>
                  <td className="nowrap">{n.readAt ? "읽음" : <Badge tone="info">안 읽음</Badge>}</td>
                  <td className="nowrap">
                    {!n.readAt && (
                      <button type="button" className="btn-secondary btn-sm" onClick={() => void markRead(n.id)}>
                        읽음
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

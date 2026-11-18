// 알림 발송 기록(B36 명세 4.7): 조건·표·실패 건 다시 보내기. notification:manage 또는 audit:read.
// 메시지 본문·이메일 주소·웹훅 주소는 서버가 주지 않고 화면에도 없다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, Breadcrumbs, EmptyState, LoadingRows, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Delivery {
  id: number;
  kind: string;
  kindName: string;
  channel: string;
  channelName: string;
  recipient: string;
  status: string;
  attempts: number;
  reason?: string;
  nextTryAt?: string;
  createdAt: string;
  sentAt?: string;
  canResend: boolean;
  groupedCount?: number;
}

const STATUS_LABEL: Record<string, string> = { queued: "대기", held: "보류", sending: "발송 중", sent: "발송됨", failed: "실패", skipped: "건너뜀" };
const STATUS_TONE: Record<string, Tone> = { queued: "info", held: "muted", sending: "info", sent: "ok", failed: "bad", skipped: "muted" };

/** 알림 종류의 한국어 이름(서버 Kinds 표와 같다). 조건 선택지에 쓴다. */
export const KIND_OPTIONS: [string, string][] = [
  ["approval_pending", "결재 요청"], ["approval_rejected", "반려"], ["approval_done", "결재 완료"], ["agreement_objected", "합의 이견"], ["document_cc", "참조 문서"],
  ["receipt_arrived", "수신 문서 도착"], ["receipt_returned", "수신 반송"], ["receipt_done", "수신 처리 완료"], ["submission_cancelled", "상신 취소"], ["approval_nudge", "결재 독촉"],
  ["approval_reassigned", "결재자 변경"], ["absence_assigned", "대결자 지정"], ["absence_started", "대결 기간 시작"], ["absence_ended", "부재 종료"], ["absence_revoked", "부재 해제"],
  ["card_reminder", "카드 소명 기한 안내"], ["card_overdue_head", "카드 소명 지연"], ["card_returned", "카드 소명 반려"], ["card_reopened", "카드 소명 보완 필요"],
  ["payment_due", "지급 예정"], ["month_close_check", "월 마감 점검"],
];

function when(s?: string): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "-";
}

export function DeliveryLogPage() {
  const { can } = useAuth();
  const manage = can("notification:manage");
  const [status, setStatus] = useState("");
  const [kind, setKind] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const base = manage ? "/api/notify/deliveries" : "/api/audit/notify-deliveries";
  const list = useLoad(() => {
    const q = new URLSearchParams();
    if (status) q.set("status", status);
    if (kind) q.set("kind", kind);
    if (from) q.set("from", from);
    if (to) q.set("to", to);
    const s = q.toString();
    return api.get<{ deliveries: Delivery[] }>(`${base}${s ? `?${s}` : ""}`).then((r) => r.deliveries ?? []);
  }, [base, status, kind, from, to]);
  const rows = list.data ?? [];

  async function resend(d: Delivery) {
    setMsg(null);
    try {
      await api.post(`/api/notify/deliveries/${d.id}/resend`, {});
      setMsg({ tone: "ok", text: `${d.kindName} 메일을 다시 보내도록 큐에 올렸습니다.` });
      await list.reload();
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }

  return (
    <section>
      {manage && <Breadcrumbs items={[{ label: "알림 채널", to: "/admin/notifications" }, { label: "알림 발송 기록" }]} />}
      <h1>알림 발송 기록</h1>
      <p className="hint">메시지 본문·이메일 주소는 기록에 남기지 않습니다. 실패 건은 한 번만 다시 보낼 수 있습니다.</p>
      <div className="form-row">
        <label>
          상태
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">전체 상태</option>
            {Object.entries(STATUS_LABEL).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          알림 종류
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">전체 종류</option>
            {KIND_OPTIONS.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          시작일
          <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label>
          종료일
          <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
      </div>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.data && rows.length === 0 && <EmptyState hint="업무 알림이 생기면 메일 발송 건이 이곳에 쌓입니다.">조건에 맞는 발송 기록이 없습니다.</EmptyState>}
      {(rows.length > 0 || (list.loading && !list.data)) && (
        <TableWrap busy={list.loading && !list.data}>
          <table>
            <thead>
              <tr>
                <th>시각</th>
                <th>종류</th>
                <th>채널</th>
                <th>받는 사람</th>
                <th>상태</th>
                <th className="num">시도</th>
                <th>사유·다음 시도</th>
                {manage && <th>조치</th>}
              </tr>
            </thead>
            <tbody>
              {list.loading && !list.data && <LoadingRows cols={manage ? 8 : 7} />}
              {rows.map((d) => (
                <tr key={d.id}>
                  <td className="nowrap">{when(d.createdAt)}</td>
                  <td>
                    {d.kindName}
                    {d.groupedCount ? <span className="hint"> (한 통 {d.groupedCount}건)</span> : null}
                  </td>
                  <td>{d.channelName}</td>
                  <td>{d.recipient || "-"}</td>
                  <td>
                    <Badge tone={STATUS_TONE[d.status] ?? "muted"}>{STATUS_LABEL[d.status] ?? "알 수 없음"}</Badge>
                  </td>
                  <td className="num">{d.attempts}</td>
                  <td>
                    {d.reason}
                    {d.nextTryAt ? <div className="hint">다음 시도 {when(d.nextTryAt)}</div> : null}
                    {d.status === "sent" ? <span className="hint">{when(d.sentAt)}</span> : null}
                  </td>
                  {manage && (
                    <td>
                      {d.canResend ? (
                        <button type="button" className="btn-secondary btn-sm" onClick={() => void resend(d)}>
                          다시 보내기
                        </button>
                      ) : null}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

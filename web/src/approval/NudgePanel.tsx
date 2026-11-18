// 기안자의 결재 독촉 영역(B29 ④): 지연된 단계(대기 24시간 초과)에 독촉 알림을 보내고, 독촉 기록을 보인다. 문서 상세(구매요청·양식 문서)가 같이 쓴다.
import { useState } from "react";
import { api } from "../api/client";
import { ConfirmDialog, Notice } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";

interface Status {
  delayed: boolean;
  stepTitle: string;
  waitingMinutes: number;
  noApprover: boolean;
  holiday?: boolean;
  doneToday: string | null;
  records: { at: string; day: string; stepTitle: string; recipients: number; recipientNames: string[] }[];
}

/** 분 → "1일 3시간"(분은 1시간 미만일 때만). */
export function waitingText(m: number): string {
  const d = Math.floor(m / 1440);
  const h = Math.floor((m % 1440) / 60);
  if (d > 0) return `${d}일 ${h}시간`;
  if (h > 0) return `${h}시간`;
  return `${m}분`;
}

/** 기안자 본인의 진행 중 문서에서만 쓴다(그렇지 않으면 그리지 않는 것은 호출한 쪽이 정한다). 조회가 실패하면 영역을 그리지 않는다. */
export function NudgePanel({ docId, onSent }: { docId: number; onSent?: () => void }) {
  const st = useLoad(() => api.get<Status>(`/api/nudges?docId=${docId}`), [docId]);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const d = st.data;
  if (!d || st.error || !Array.isArray(d.records)) return null; // 조회 실패·예상 밖 응답이면 영역을 그리지 않는다
  if (!d.delayed && d.records.length === 0) return null;

  async function send() {
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/approvals/${docId}/nudge`);
      setConfirming(false);
      setDone("독촉 알림을 보냈습니다. 같은 단계는 내일 다시 독촉할 수 있습니다.");
      await st.reload();
      onSent?.();
    } catch (e) {
      setConfirming(false);
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-label="결재 독촉">
      <h3>결재 독촉</h3>
      {d.delayed && (
        <>
          <p>
            「{d.stepTitle}」 단계는 대기 {waitingText(d.waitingMinutes)}째입니다.
          </p>
          {d.noApprover ? (
            <p className="hint">결재자가 없어 독촉할 수 없습니다. 관리자에게 결재자 변경을 요청하세요.</p>
          ) : (
            <>
              <button type="button" disabled={busy || !!d.doneToday} onClick={() => setConfirming(true)}>
                독촉하기
              </button>
              {d.doneToday && <p className="hint">오늘은 이미 독촉했습니다({dateTime(d.doneToday)}).</p>}
              {d.holiday && <p className="hint">오늘은 휴일(주말·공휴일)입니다. 독촉은 보낼 수 있지만 결재자는 다음 영업일에 확인할 가능성이 높습니다.</p>}
            </>
          )}
        </>
      )}
      {done && <Notice tone="ok">{done}</Notice>}
      {error && <Notice tone="error">{error}</Notice>}
      {d.records.length > 0 && (
        <>
          <h4>독촉 기록</h4>
          <ul>
            {d.records.map((r, i) => (
              <li key={i}>
                {dateTime(r.at)} · {r.stepTitle} · 받은 사람 {r.recipientNames.length > 0 ? r.recipientNames.join(", ") : `${r.recipients}명`}
              </li>
            ))}
          </ul>
        </>
      )}
      {confirming && (
        <ConfirmDialog title="독촉 알림을 보낼까요?" confirmLabel="독촉 알림 보내기" busy={busy} onConfirm={() => void send()} onCancel={() => setConfirming(false)}>
          이 단계의 현재 결재자에게 독촉 알림을 보냅니다. 같은 단계는 하루에 한 번만 보낼 수 있습니다.
        </ConfirmDialog>
      )}
    </section>
  );
}

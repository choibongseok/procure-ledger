// 전표 대기(B31 3.3.8): 자동 전표를 만들지 못한 증빙 확정·지출 결의서 승인을 사유와 함께 보고, 다시 만들기·수동 입력 연결·전표 불필요로 처리한다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import type { QueueItem } from "./types";

const STATUS_TONE: Record<string, "muted" | "info" | "ok" | "bad"> = { pending: "info", created: "ok", manual: "ok", skipped: "muted" };

export function QueuePage() {
  const { can } = useAuth();
  const write = can("ledger:write");
  const [status, setStatus] = useState("pending");
  const [tick, setTick] = useState(0);
  const list = useLoad(() => api.get<QueueItem[]>(`/api/finance/journal-queue${status ? `?status=${status}` : ""}`), [status, tick]);
  const [busy, setBusy] = useState(0);
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [skip, setSkip] = useState<{ id: number; note: string } | null>(null);
  const [manual, setManual] = useState<{ id: number; no: string } | null>(null);
  const [dates, setDates] = useState<Record<number, string>>({});

  async function run(id: number, fn: () => Promise<QueueItem>, done: (q: QueueItem) => string) {
    setBusy(id);
    setMsg(null);
    try {
      const q = await fn();
      setMsg({ tone: q.status === "pending" ? "error" : "ok", text: done(q) });
      setSkip(null);
      setManual(null);
      setTick((t) => t + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(0);
    }
  }

  const rows = list.data ?? [];
  return (
    <section>
      <h1>전표 대기</h1>
      <p className="hint">증빙 판정 확정이나 지출 결의서 승인에서 자동 전표를 만들지 못한 건입니다. 확정·승인 자체는 이미 끝났습니다.</p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="jq-status">처리 상태</label>
          <select id="jq-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="pending">처리 전</option>
            <option value="created">전표 생성</option>
            <option value="manual">수동 입력</option>
            <option value="skipped">전표 불필요</option>
            <option value="">전체</option>
          </select>
        </div>
      </div>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>전표 대기 건이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">출처</th>
                <th>사유</th>
                <th className="nowrap">발주</th>
                <th>거래처</th>
                <th className="num nowrap">금액 후보</th>
                <th className="nowrap">상태</th>
                <th>처리</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((q) => (
                <tr key={q.id}>
                  <td data-label="출처" className="nowrap">{q.sourceLabel}</td>
                  <td data-label="사유">{q.reasonText}</td>
                  <td data-label="발주" className="nowrap">{q.poNo || "—"}</td>
                  <td data-label="거래처">{q.vendorName || "—"}</td>
                  <td data-label="금액 후보" className="num nowrap">{q.amount != null ? won(q.amount) : "—"}</td>
                  <td data-label="상태" className="nowrap">
                    <Badge tone={STATUS_TONE[q.status]}>{q.statusLabel}</Badge>
                  </td>
                  <td data-label="처리">
                    {q.status === "pending" && write && (
                      <div className="form-row">
                        {q.reason === "pre_adoption" && (
                          <input type="date" aria-label={`${q.sourceLabel} 전기일`} value={dates[q.id] ?? ""} onChange={(e) => setDates({ ...dates, [q.id]: e.target.value })} />
                        )}
                        {q.reason !== "mismatch_confirmed" && q.reason !== "hold_confirmed" && (
                          <button
                            type="button"
                            disabled={busy === q.id}
                            onClick={() =>
                              void run(q.id, () => (q.reason === "pre_adoption" ? api.post<QueueItem>(`/api/finance/journal-queue/${q.id}/retry`, { postingDate: dates[q.id] ?? "" }) : api.post<QueueItem>(`/api/finance/journal-queue/${q.id}/retry`)), (r) => (r.status === "pending" ? `다시 만들지 못했습니다: ${r.reasonText}` : `전표 ${r.journalNo} 을(를) 만들었습니다.`))
                            }
                          >
                            다시 만들기
                          </button>
                        )}
                        <button type="button" className="btn-secondary" disabled={busy === q.id} onClick={() => setManual({ id: q.id, no: "" })}>
                          수동 입력 연결
                        </button>
                        <button type="button" className="btn-secondary" disabled={busy === q.id} onClick={() => setSkip({ id: q.id, note: "" })}>
                          전표 불필요
                        </button>
                      </div>
                    )}
                    {q.status !== "pending" && (
                      <span>
                        {q.journalNo ? <Link to={`/finance/journals/${q.journalNo}`}>{q.journalNo}</Link> : q.note || "—"}
                        {q.handledAt ? ` · ${q.handledBy} ${dateTime(q.handledAt)}` : ""}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {manual && (
        <div className="form-row">
          <div className="field">
            <label htmlFor="jq-manual">연결할 전표 번호(직접 입력한 전표)</label>
            <input id="jq-manual" value={manual.no} placeholder="JV-2026-00001" onChange={(e) => setManual({ ...manual, no: e.target.value })} />
          </div>
          <button type="button" disabled={busy === manual.id || manual.no.trim() === ""} onClick={() => void run(manual.id, () => api.post<QueueItem>(`/api/finance/journal-queue/${manual.id}/manual`, { journalNo: manual.no }), (r) => `전표 ${r.journalNo} 에 연결했습니다.`)}>
            연결
          </button>
          <button type="button" className="btn-secondary" onClick={() => setManual(null)}>
            닫기
          </button>
        </div>
      )}
      {skip && (
        <div className="form-row">
          <div className="field">
            <label htmlFor="jq-skip">전표가 필요 없는 사유(필수)</label>
            <input id="jq-skip" value={skip.note} maxLength={200} onChange={(e) => setSkip({ ...skip, note: e.target.value })} />
          </div>
          <button type="button" disabled={busy === skip.id || skip.note.trim() === ""} onClick={() => void run(skip.id, () => api.post<QueueItem>(`/api/finance/journal-queue/${skip.id}/skip`, { note: skip.note }), () => "전표 불필요로 처리했습니다.")}>
            처리
          </button>
          <button type="button" className="btn-secondary" onClick={() => setSkip(null)}>
            닫기
          </button>
        </div>
      )}
    </section>
  );
}

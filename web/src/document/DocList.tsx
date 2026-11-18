// 전자결재 문서함 7종(B23 단계 2): 기안함·결재 대기함·결재 진행함·완료함·참조함·수신함·반려함.
// 안 읽은 문서는 점과 굵은 글씨로 보이고, 결재 대기함에서는 여러 건을 골라 한꺼번에 승인한다. 수신함에서는 접수·담당자 지정·처리 완료·반송을 한다.
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { PermissionGate } from "../auth/PermissionGate";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { PrStatusBadge } from "../purchase/badges";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { ReceiptActions } from "./ReceiptActions";
import type { InboxBox, InboxCounts, InboxItem } from "./types";
import { BOX_HELP, BOX_LABELS, BOXES, receiptStatusLabel } from "./types";

/** 일괄 승인 실패 문구용 이름: "문서 번호 제목"(제목은 30자까지). 목록에 없으면 내부 번호로 떨어진다. */
function docLabel(items: InboxItem[], docId: number): string {
  const it = items.find((x) => x.id === docId);
  if (!it) return `#${docId}`;
  const t = Array.from(it.title);
  return `${it.no} ${t.length > 30 ? t.slice(0, 30).join("") + "…" : it.title}`.trim();
}

function linkOf(it: InboxItem): string {
  return it.formKind === "purchase" ? `/purchase-requests/${it.id}` : `/docs/${it.id}`;
}

export function DocList() {
  const [params, setParams] = useSearchParams();
  const box = (BOXES.includes(params.get("box") as InboxBox) ? params.get("box") : "pending") as InboxBox;
  const [tab, setTab] = useState("");
  const counts = useLoad(() => api.get<InboxCounts>("/api/inbox/counts"), []);
  const list = useLoad(() => api.get<InboxItem[]>(`/api/inbox/${box}${box === "receive" && tab ? `?tab=${tab}` : ""}`), [box, tab]);
  const [picked, setPicked] = useState<Record<number, boolean>>({});
  const [comment, setComment] = useState("");
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const chosen = Object.keys(picked).filter((k) => picked[Number(k)]).map(Number);
  const reloadAll = () => {
    void counts.reload();
    void list.reload();
  };
  async function batch() {
    setMsg("");
    setErr("");
    setBusy(true);
    try {
      const r = await api.post<{ results: { docId: number; ok: boolean; message: string }[] }>("/api/approvals/batch", { docIds: chosen, comment });
      const ok = r.results.filter((x) => x.ok).length;
      const failed = r.results.filter((x) => !x.ok);
      setMsg(`${ok}건을 승인했습니다.` + (failed.length ? ` ${failed.length}건은 처리하지 못했습니다: ` + failed.map((f) => `${docLabel(list.data ?? [], f.docId)}: ${f.message}`).join(" / ") : ""));
      setPicked({});
      setComment("");
      reloadAll();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const go = (b: InboxBox) => {
    setParams({ box: b });
    setTab("");
    setPicked({});
    setMsg("");
    setErr("");
  };
  const rows = list.data ?? [];
  return (
    <section>
      <h1>전자결재 문서함</h1>
      <p>
        <PermissionGate permission="draft:create">
          <Link to="/docs/new">새 결재 문서 작성</Link>
          {" · "}
        </PermissionGate>
        <Link to="/docs/search">문서 검색</Link>
      </p>
      <div className="form-row" role="tablist" aria-label="문서함">
        {BOXES.map((b) => {
          const c = counts.data?.[b];
          return (
            <button key={b} type="button" role="tab" aria-selected={box === b} className={box === b ? "" : "btn-secondary"} onClick={() => go(b)}>
              {BOX_LABELS[b]}
              {c && c.total > 0 ? ` ${c.total}` : ""}
              {c && c.unread > 0 ? ` (안 읽음 ${c.unread})` : ""}
            </button>
          );
        })}
      </div>
      <p className="muted">{BOX_HELP[box]}</p>
      {box === "receive" && (
        <div className="form-row" role="group" aria-label="수신 상태">
          {[
            ["", "전체"],
            ["waiting", "접수 대기"],
            ["received", "접수·처리 중"],
            ["done", "처리 완료"],
            ["returned", "반송된 건"],
          ].map(([k, label]) => (
            <button key={k} type="button" className={tab === k ? "" : "btn-secondary"} aria-pressed={tab === k} onClick={() => setTab(k)}>
              {label}
            </button>
          ))}
        </div>
      )}
      <ExportButtons path="/api/inbox/export" params={{ box, tab: box === "receive" ? tab : "" }} disabled={list.loading || rows.length === 0} />
      {msg && <Notice tone="ok">{msg}</Notice>}
      {(err || list.error || counts.error) && <Notice tone="error">{err || list.error || counts.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {!list.loading && rows.length === 0 && <EmptyState>{BOX_LABELS[box]}에 문서가 없습니다.</EmptyState>}
      {box === "pending" && rows.length > 0 && (
        <PermissionGate permission="approval:agree">
          <div className="form-row">
            <label>
              일괄 승인 의견(선택)
              <input value={comment} maxLength={1000} onChange={(e) => setComment(e.target.value)} />
            </label>
            <button type="button" disabled={busy || chosen.length === 0} onClick={() => void batch()}>
              선택한 {chosen.length}건 일괄 승인
            </button>
          </div>
        </PermissionGate>
      )}
      {rows.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                {box === "pending" && <th className="nowrap">선택</th>}
                <th className="nowrap">문서 번호</th>
                <th>제목</th>
                <th className="nowrap">양식</th>
                <th className="nowrap">기안자</th>
                <th>소속</th>
                <th className="nowrap">상태</th>
                {box === "receive" && <th className="nowrap">수신 처리</th>}
                <th className="nowrap">최근 변경</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={`${d.id}-${d.receiptId}`}>
                  {box === "pending" && (
                    <td className="nowrap">
                      <input type="checkbox" aria-label={`${d.title} 선택`} checked={!!picked[d.id]} onChange={(e) => setPicked({ ...picked, [d.id]: e.target.checked })} />
                    </td>
                  )}
                  <td className="nowrap">{d.no.startsWith("TMP-") ? "임시저장" : d.no}</td>
                  <td>
                    {d.unread && (
                      <span aria-label="안 읽음" title="안 읽음">
                        ●{" "}
                      </span>
                    )}
                    {d.urgent && <Badge tone="bad">긴급</Badge>}{" "}
                    <Link to={linkOf(d)} style={d.unread ? { fontWeight: 700 } : undefined}>
                      {d.title}
                    </Link>
                    {d.formKind === "purchase" && d.amount > 0 && <span className="muted"> · {won(d.amount)}</span>}
                  </td>
                  <td className="nowrap">{d.formName}</td>
                  <td className="nowrap">{d.requesterName}</td>
                  <td>{d.unitPath || "—"}</td>
                  <td className="nowrap">
                    {d.disposed ? <Badge tone="muted">폐기됨</Badge> : <PrStatusBadge status={d.status} />}
                  </td>
                  {box === "receive" && (
                    <td>
                      <div>
                        {d.receiptUnit} · {receiptStatusLabel(d.receiptStatus)}
                      </div>
                      <ReceiptActions receiptId={d.receiptId} status={d.receiptStatus} onChanged={reloadAll} />
                    </td>
                  )}
                  <td className="nowrap">{dateTime(d.updatedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

// 정보주체 열람 요청(B41 명세 7절).
// 직원 화면(MyPrivacyRequestsPage): "내 정보를 누가 봤는지" 요청과 결과. 관리자 화면(PrivacyRequestsAdminPage): privacy:request_manage 가 승인·반려.
// 결과에는 열람자의 이름이 없고 부서와 역할만 보인다. 본인 요청은 본인이 처리할 수 없다(서버가 거부).
import { Fragment, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, ConfirmDialog, EmptyState, FieldError, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { CATEGORY_LABEL, FIELD_LABEL } from "./PrivacyLogPage";

interface RequestRow {
  id: number;
  userId: number;
  userName?: string;
  status: "requested" | "approved" | "rejected" | "expired";
  periodFrom: string;
  periodTo: string;
  reason: string;
  requestedAt: string;
  decidedAt?: string;
  decisionNote?: string;
  expiresAt?: string;
}

interface ResultRow {
  at: string;
  viewerUnit: string;
  viewerRole: string;
  category: string;
  screen: string;
  fields: string;
  isExport: boolean;
  summary: boolean;
}

const STATUS: Record<RequestRow["status"], { text: string; tone: Tone }> = {
  requested: { text: "처리 대기", tone: "info" },
  approved: { text: "결과 있음", tone: "ok" },
  rejected: { text: "반려", tone: "bad" },
  expired: { text: "보관 기간 지남", tone: "muted" },
};

const fmt = (s: string) => new Date(s).toLocaleString("ko-KR");

function fieldsText(raw: string): string {
  try {
    return (JSON.parse(raw) as string[]).map((f) => FIELD_LABEL[f] ?? f).join(", ") || "-";
  } catch {
    return "-";
  }
}

function today(): string {
  const d = new Date(Date.now() + 9 * 3600 * 1000);
  return d.toISOString().slice(0, 10);
}

function Result({ id }: { id: number }) {
  const res = useLoad(() => api.get<ResultRow[]>(`/api/privacy/requests/${id}/result`), [id]);
  if (res.error) return <Notice tone="error">{res.error}</Notice>;
  if (!res.data) return <p className="hint">불러오는 중…</p>;
  if (res.data.length === 0) return <EmptyState>이 기간에 다른 사람이 내 정보를 본 기록이 없습니다.</EmptyState>;
  return (
    <TableWrap>
      <table className="stack-table" aria-label="내 정보 열람 결과">
        <thead>
          <tr>
            <th>시각</th>
            <th>열람자 부서</th>
            <th>열람자 역할</th>
            <th>종류</th>
            <th>화면</th>
            <th>항목</th>
          </tr>
        </thead>
        <tbody>
          {res.data.map((r, i) =>
            r.summary ? (
              <tr key={i}>
                <td colSpan={6}>{r.screen}</td>
              </tr>
            ) : (
              <tr key={i}>
                <td data-label="시각" className="nowrap">
                  {fmt(r.at)}
                </td>
                <td data-label="열람자 부서">{r.viewerUnit || "-"}</td>
                <td data-label="열람자 역할">{r.viewerRole || "-"}</td>
                <td data-label="종류">
                  {CATEGORY_LABEL[r.category] ?? r.category}
                  {r.isExport && <Badge tone="info">내보내기</Badge>}
                </td>
                <td data-label="화면">{r.screen}</td>
                <td data-label="항목">{fieldsText(r.fields)}</td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    </TableWrap>
  );
}

export function MyPrivacyRequestsPage() {
  const list = useLoad(() => api.get<RequestRow[]>("/api/privacy/requests"), []);
  const [from, setFrom] = useState(() => new Date(Date.now() + 9 * 3600 * 1000 - 30 * 86400 * 1000).toISOString().slice(0, 10));
  const [to, setTo] = useState(today);
  const [reason, setReason] = useState("");
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<number | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setNote(null);
    try {
      await api.post("/api/privacy/requests", { from, to, reason });
      setNote({ tone: "ok", text: "요청을 접수했습니다. 관리자가 확인하면 알림으로 알려 드립니다." });
      setReason("");
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>내 정보 열람 요청</h1>
      <p>
        다른 사람이 내 연락처·계좌·사번 같은 원문을 언제 어느 화면에서 봤는지 요청할 수 있습니다. 관리자가 승인하면 결과를 30일 동안 볼 수 있고, 열람자는 이름 없이 부서와 역할로만 표시됩니다.
      </p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <form onSubmit={submit} aria-label="열람 요청">
        <div className="field">
          <label htmlFor="req-from">시작 날짜</label>
          <input id="req-from" type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="req-to">종료 날짜</label>
          <input id="req-to" type="date" value={to} min={from} max={today()} onChange={(e) => setTo(e.target.value)} required />
          <span className="hint">기간은 최대 1년까지 요청할 수 있습니다.</span>
        </div>
        <div className="field">
          <label htmlFor="req-reason">사유(선택)</label>
          <input id="req-reason" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <button type="submit" disabled={busy}>
          요청하기
        </button>
      </form>
      <h2>내 요청</h2>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.data && list.data.length === 0 && <EmptyState>아직 요청한 내역이 없습니다.</EmptyState>}
      {list.data && list.data.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="내 열람 요청">
            <thead>
              <tr>
                <th>요청 시각</th>
                <th>기간</th>
                <th>상태</th>
                <th>처리 메모</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.data.map((r) => (
                <Fragment key={r.id}>
                  <tr>
                    <td data-label="요청 시각" className="nowrap">
                      {fmt(r.requestedAt)}
                    </td>
                    <td data-label="기간">
                      {r.periodFrom} ~ {r.periodTo}
                    </td>
                    <td data-label="상태">
                      <Badge tone={STATUS[r.status].tone}>{STATUS[r.status].text}</Badge>
                      {r.status === "approved" && r.expiresAt && <span className="hint"> {fmt(r.expiresAt)} 까지</span>}
                    </td>
                    <td data-label="처리 메모">{r.decisionNote || "-"}</td>
                    <td>
                      {r.status === "approved" && (
                        <button type="button" className="btn-secondary btn-sm" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? null : r.id)}>
                          {open === r.id ? "결과 접기" : "결과 보기"}
                        </button>
                      )}
                    </td>
                  </tr>
                  {open === r.id && (
                    <tr>
                      <td colSpan={5}>
                        <Result id={r.id} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

export function PrivacyRequestsAdminPage() {
  const list = useLoad(() => api.get<RequestRow[]>("/api/admin/privacy/requests"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [rejecting, setRejecting] = useState<RequestRow | null>(null);
  const [memo, setMemo] = useState("");
  const [memoErr, setMemoErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function decide(r: RequestRow, approve: boolean) {
    if (!approve && !memo.trim()) {
      setMemoErr("반려 사유를 입력하세요. 요청자가 결과 화면에서 사유를 봅니다.");
      return;
    }
    setMemoErr("");
    setBusy(true);
    setNote(null);
    try {
      await api.post(`/api/admin/privacy/requests/${r.id}/${approve ? "approve" : "reject"}`, { note: approve ? "" : memo });
      setNote({ tone: "ok", text: approve ? "승인했습니다. 요청자에게 결과 알림을 보냈습니다." : "반려했습니다." });
      setRejecting(null);
      setMemo("");
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
      setRejecting(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>정보주체 열람 요청</h1>
      <p>직원이 본인 정보의 열람 기록을 요청한 건입니다. 승인하면 요청 기간의 기록이 열람자 이름 없이 부서·역할로 요약되어 요청자에게 30일 동안 보입니다. 반려할 때는 사유가 필요합니다. 본인이 올린 요청은 본인이 처리할 수 없고, 처리할 다른 관리자가 없으면 감사가 대신 처리합니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.data && list.data.length === 0 && <EmptyState>접수된 요청이 없습니다.</EmptyState>}
      {list.data && list.data.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="열람 요청 목록">
            <thead>
              <tr>
                <th>요청 시각</th>
                <th>요청자</th>
                <th>기간</th>
                <th>사유</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.data.map((r) => (
                <tr key={r.id}>
                  <td data-label="요청 시각" className="nowrap">
                    {fmt(r.requestedAt)}
                  </td>
                  <td data-label="요청자">{r.userName || `사용자 ${r.userId}`}</td>
                  <td data-label="기간">
                    {r.periodFrom} ~ {r.periodTo}
                  </td>
                  <td data-label="사유">{r.reason || "-"}</td>
                  <td data-label="상태">
                    <Badge tone={STATUS[r.status].tone}>{STATUS[r.status].text}</Badge>
                  </td>
                  <td>
                    {r.status === "requested" && (
                      <>
                        <button type="button" className="btn-sm" disabled={busy} onClick={() => void decide(r, true)}>
                          승인
                        </button>{" "}
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setRejecting(r)}>
                          반려
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {rejecting && (
        <ConfirmDialog title="열람 요청 반려" confirmLabel="반려" busy={busy} onConfirm={() => void decide(rejecting, false)} onCancel={() => { setRejecting(null); setMemoErr(""); }}>
          <p>{rejecting.userName || "요청자"} 님의 요청을 반려합니다. 요청자가 볼 수 있는 반려 사유를 적으세요(필수, 200자 이하).</p>
          <div className="field">
            <label htmlFor="reject-memo">반려 사유</label>
            <input id="reject-memo" maxLength={200} value={memo} aria-invalid={memoErr ? true : undefined} aria-describedby={memoErr ? "reject-memo-err" : undefined} onChange={(e) => setMemo(e.target.value)} />
            {memoErr && <FieldError id="reject-memo-err">{memoErr}</FieldError>}
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

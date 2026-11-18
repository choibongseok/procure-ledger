// 견적 비교(B25 ②): 견적서 올리기(AI 가 값을 읽음) → 값 확인·수정 → 거래처별 비교 → 거래처 선택과 사유.
// 최저가·사유 필수 판정은 서버가 한다(화면은 결과만 보인다). 상신한 요청은 읽기 전용이다.
import { useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { api, apiFetch } from "../api/client";
import { Badge, Breadcrumbs, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "./labels";
import type { Quote, QuoteCompare, Vendor } from "./types";
import { errorText, useLoad } from "./useLoad";

interface LineForm {
  name: string;
  quantity: string;
  unitPrice: string;
}

function vendorLabel(q: Quote): string {
  return q.vendorName || q.supplier || "거래처 미정";
}

function leadText(q: Quote): string {
  if (q.leadDays !== null) return `발주 후 ${q.leadDays}일`;
  return q.leadText || "—";
}

function EditForm({ q, vendors, onDone, onCancel }: { q: Quote; vendors: Vendor[]; onDone: () => void; onCancel: () => void }) {
  const [vendorId, setVendorId] = useState(q.vendorId ? String(q.vendorId) : "");
  const [lines, setLines] = useState<LineForm[]>(
    q.lines.length > 0 ? q.lines.map((l) => ({ name: l.name, quantity: String(l.quantity), unitPrice: String(l.unitPrice) })) : [{ name: "", quantity: "1", unitPrice: "0" }],
  );
  const [docTotal, setDocTotal] = useState(q.docTotal === null ? "" : String(q.docTotal));
  const [leadDays, setLeadDays] = useState(q.leadDays === null ? "" : String(q.leadDays));
  const [lead, setLead] = useState(q.leadText);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (i: number, patch: Partial<LineForm>) => setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l)));

  async function save() {
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i];
      if (!l.name.trim()) return setErr(`${i + 1}번째 줄의 품목 이름을 입력하세요.`);
      if (!Number.isInteger(Number(l.quantity)) || Number(l.quantity) <= 0) return setErr(`${i + 1}번째 줄의 수량은 1 이상의 정수여야 합니다.`);
      if (!Number.isInteger(Number(l.unitPrice)) || Number(l.unitPrice) < 0) return setErr(`${i + 1}번째 줄의 단가가 올바르지 않습니다.`);
    }
    if (docTotal.trim() !== "" && (!Number.isInteger(Number(docTotal)) || Number(docTotal) < 0)) return setErr("문서 합계는 0 이상의 정수여야 합니다.");
    if (leadDays.trim() !== "" && (!Number.isInteger(Number(leadDays)) || Number(leadDays) < 0)) return setErr("납기 일수는 0 이상의 정수여야 합니다.");
    setBusy(true);
    setErr("");
    try {
      await api.put(`/api/quotes/${q.id}`, {
        vendorId: vendorId ? Number(vendorId) : 0,
        lines: lines.map((l) => ({ name: l.name, quantity: Number(l.quantity), unitPrice: Number(l.unitPrice) })),
        docTotal: docTotal.trim() === "" ? null : Number(docTotal),
        leadDays: leadDays.trim() === "" ? null : Number(leadDays),
        leadText: lead,
      });
      onDone();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-label={`${vendorLabel(q)} 견적 값 입력`}>
      <h3>견적 값 입력·수정 — {q.filename}</h3>
      <div className="field">
        <label htmlFor={`qv-${q.id}`}>거래처</label>
        <select id={`qv-${q.id}`} aria-label="견적 거래처" value={vendorId} onChange={(e) => setVendorId(e.target.value)}>
          <option value="">거래처를 고르세요</option>
          {vendors.map((v) => (
            <option key={v.id} value={v.id}>
              {v.name}
            </option>
          ))}
        </select>
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>품목</th>
              <th className="nowrap">수량</th>
              <th className="nowrap">단가(원)</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lines.map((l, i) => (
              <tr key={i}>
                <td>
                  <input aria-label={`견적 ${i + 1}번째 줄 품목`} value={l.name} onChange={(e) => set(i, { name: e.target.value })} maxLength={100} />
                </td>
                <td>
                  <input aria-label={`견적 ${i + 1}번째 줄 수량`} inputMode="numeric" value={l.quantity} onChange={(e) => set(i, { quantity: e.target.value })} />
                </td>
                <td>
                  <input aria-label={`견적 ${i + 1}번째 줄 단가`} inputMode="numeric" value={l.unitPrice} onChange={(e) => set(i, { unitPrice: e.target.value })} />
                </td>
                <td className="nowrap">
                  {lines.length > 1 && (
                    <button type="button" className="btn-danger btn-sm" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>
                      줄 삭제
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <p>
        <button type="button" className="btn-secondary" onClick={() => setLines((ls) => [...ls, { name: "", quantity: "1", unitPrice: "0" }])}>
          품목 줄 추가
        </button>
      </p>
      <div className="form-row">
        <div className="field">
          <label htmlFor={`qd-${q.id}`}>문서에 적힌 합계 (선택)</label>
          <input id={`qd-${q.id}`} inputMode="numeric" value={docTotal} onChange={(e) => setDocTotal(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor={`ql-${q.id}`}>납기 (발주 후 일수, 선택)</label>
          <input id={`ql-${q.id}`} inputMode="numeric" value={leadDays} onChange={(e) => setLeadDays(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor={`qt-${q.id}`}>납기 문구 (선택)</label>
          <input id={`qt-${q.id}`} value={lead} onChange={(e) => setLead(e.target.value)} maxLength={100} />
        </div>
      </div>
      <p className="hint">합계는 품목 줄의 금액 합으로 계산됩니다. 값을 고치면 "수정됨"으로 표시됩니다.</p>
      {err && <Notice tone="error">{err}</Notice>}
      <div className="form-row">
        <button type="button" disabled={busy} onClick={() => void save()}>
          값 저장
        </button>
        <button type="button" className="btn-secondary" disabled={busy} onClick={onCancel}>
          취소
        </button>
      </div>
    </section>
  );
}

export function QuotePage() {
  const { id } = useParams();
  const cmp = useLoad(() => api.get<QuoteCompare>(`/api/purchase-requests/${id}/quotes`), [id]);
  const vendors = useLoad(() => api.get<Vendor[]>("/api/vendors"), []);
  const [editing, setEditing] = useState(0);
  // 입력 중인 사유(null 이면 서버에 저장된 값을 보인다). 서버 값을 effect 로 복사하면 지연된 effect 가 사용자의 입력을 덮어쓸 수 있어 파생값으로 둔다.
  const [draft, setDraft] = useState<string | null>(null);
  const reason = draft ?? cmp.data?.reason ?? "";
  const setReason = setDraft;
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);


  if (cmp.error) return <Notice tone="error">{cmp.error}</Notice>;
  if (!cmp.data) return <p>불러오는 중…</p>;
  const c = cmp.data;
  const filled = c.quotes.filter((q) => q.filled).length;
  const selected = c.quotes.find((q) => q.selected);

  async function run(fn: () => Promise<unknown>, ok?: string) {
    setErr("");
    setMsg("");
    setBusy(true);
    try {
      await fn();
      if (ok) setMsg(ok);
      await cmp.reload();
      setDraft(null);
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function upload(files: FileList | null) {
    if (!files || files.length === 0) return;
    const f = files[0];
    await run(async () => {
      const fd = new FormData();
      fd.append("file", f);
      const q = await apiFetch<Quote>(`/api/purchase-requests/${id}/quotes`, { method: "POST", body: fd });
      setMsg(
        q.status === "unreadable"
          ? `${q.filename} 파일은 글자를 읽을 수 없어 값을 채우지 못했습니다. 금액과 납기를 직접 입력하세요.`
          : `${q.filename} 파일에서 값을 읽었습니다. 원본과 다른 곳이 없는지 확인하세요.`,
      );
      if (q.status === "unreadable") setEditing(q.id);
    });
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: "구매요청 상세", to: `/purchase-requests/${id}` }, { label: "견적 비교" }]} />
      <h1>견적 비교</h1>
      <p className="hint">AI 가 견적서에서 읽은 값입니다. 원본과 다르면 직접 고쳐 주세요. 최종 선택은 요청자가 합니다. 금액은 공급가액(부가세 별도)입니다.</p>
      {c.editable ? (
        <div className="field">
          <label htmlFor="quote-file">견적서 파일 (PDF 또는 텍스트)</label>
          <input
            id="quote-file"
            ref={fileInput}
            aria-label="견적서 파일 선택"
            type="file"
            accept=".pdf,.txt,application/pdf,text/plain"
            disabled={busy}
            onChange={(e) => {
              void upload(e.target.files);
              e.target.value = "";
            }}
          />
          <span className="hint">한 요청에 10건까지 올릴 수 있고, 같은 거래처의 견적서를 다시 올리면 교체됩니다.</span>
        </div>
      ) : (
        <Notice tone="info">상신한 요청이라 견적서와 선택은 읽기 전용입니다.</Notice>
      )}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {err && <Notice tone="error">{err}</Notice>}

      {c.quotes.length === 0 && <EmptyState>첨부된 견적서가 없습니다.</EmptyState>}
      {c.quotes.length === 1 && <Notice tone="info">견적서가 한 건뿐이라 비교할 거래처가 없습니다. 견적서를 더 올리면 비교 표가 나옵니다.</Notice>}
      {c.quotes.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>거래처</th>
                <th>품목 줄</th>
                <th className="num nowrap">합계</th>
                <th className="nowrap">납기</th>
                <th className="nowrap">비교</th>
                <th className="nowrap">상태</th>
                {c.editable && <th />}
              </tr>
            </thead>
            <tbody>
              {c.quotes.map((q) => (
                <tr key={q.id} aria-label={`${vendorLabel(q)} 견적`}>
                  <td>
                    {vendorLabel(q)}
                    {!q.vendorId && <span className="hint"> (거래처 미연결)</span>}
                    <div className="hint">{q.filename}</div>
                  </td>
                  <td>
                    {q.lines.length === 0 ? (
                      "—"
                    ) : (
                      <ul>
                        {q.lines.map((l, i) => (
                          <li key={i}>
                            {l.name} {l.quantity.toLocaleString("ko-KR")} × {won(l.unitPrice)}
                          </li>
                        ))}
                      </ul>
                    )}
                  </td>
                  <td className="num nowrap">{q.filled ? won(q.total) : "—"}</td>
                  <td className="nowrap">{leadText(q)}</td>
                  <td className="nowrap">
                    {filled >= 2 && q.filled && (q.lowest ? <Badge tone="ok">최저가</Badge> : <span>최저가보다 +{won(q.overLowest)}</span>)}
                  </td>
                  <td className="nowrap">
                    {q.selected && <Badge tone="info">선택됨</Badge>}{" "}
                    {q.status === "unreadable" && !q.edited ? <Badge tone="warn">읽지 못함</Badge> : q.edited ? <Badge tone="muted">수정됨</Badge> : <Badge tone="muted">AI 읽음</Badge>}
                  </td>
                  {c.editable && (
                    <td className="nowrap">
                      <button
                        type="button"
                        className="btn-sm"
                        disabled={busy || q.selected || !q.filled || !q.vendorId}
                        title={!q.vendorId ? "거래처를 먼저 고르세요" : !q.filled ? "금액을 먼저 입력하세요" : undefined}
                        onClick={() => void run(() => api.put(`/api/purchase-requests/${id}/quote-selection`, { quoteId: q.id, reason }))}
                      >
                        {q.selected ? "선택됨" : "이 거래처 선택"}
                      </button>{" "}
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setEditing(editing === q.id ? 0 : q.id)}>
                        값 입력·수정
                      </button>{" "}
                      <button
                        type="button"
                        className="btn-danger btn-sm"
                        disabled={busy}
                        onClick={() => void run(() => api.del(`/api/quotes/${q.id}`), `${q.filename} 파일을 지웠습니다.`)}
                      >
                        삭제
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      {c.quotes.map((q) => (
        <div key={q.id}>
          {q.status === "unreadable" && !q.edited && (
            <Notice tone="warn">
              {q.filename} 파일은 글자를 읽을 수 없어 값을 채우지 못했습니다. 금액과 납기를 직접 입력하세요.
            </Notice>
          )}
          {q.warnings.map((w) => (
            <Notice key={w} tone="warn">
              {vendorLabel(q)}: {w}
            </Notice>
          ))}
          {c.editable && editing === q.id && (
            <EditForm
              q={q}
              vendors={vendors.data ?? []}
              onCancel={() => setEditing(0)}
              onDone={() => {
                setEditing(0);
                setMsg("견적 값을 저장했습니다.");
                void cmp.reload();
              }}
            />
          )}
        </div>
      ))}

      {selected && c.totalMismatch && (
        <Notice tone="warn">
          선택한 거래처의 견적 합계({won(selected.total)})와 요청 금액({won(c.prTotal)})이 다릅니다. 요청 금액을 견적에 맞추거나 차이 이유를 사유에 적어 주세요. 상신은 막지 않습니다.
        </Notice>
      )}
      {selected && c.reasonRequired && (
        <section className="card" aria-label="선택 사유">
          <h2>선택 사유</h2>
          <Notice tone="warn">{c.note ? `${c.note} ` : ""}최저가가 아닌 거래처를 선택했습니다. 상신하려면 선택 사유가 필요합니다.</Notice>
          {c.editable ? (
            <>
              <div className="field">
                <label htmlFor="quote-reason">선택 사유 (500자 이내)</label>
                <textarea id="quote-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
              </div>
              <button type="button" disabled={busy} onClick={() => void run(() => api.put(`/api/purchase-requests/${id}/quote-selection`, { quoteId: selected.id, reason }), "선택 사유를 저장했습니다.")}>
                사유 저장
              </button>
            </>
          ) : (
            <p>{c.reason || "—"}</p>
          )}
        </section>
      )}
      {selected && !c.reasonRequired && c.editable && (
        <p>
          <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(() => api.put(`/api/purchase-requests/${id}/quote-selection`, { quoteId: 0, reason: "" }), "선택을 해제했습니다.")}>
            선택 해제
          </button>
        </p>
      )}
    </section>
  );
}

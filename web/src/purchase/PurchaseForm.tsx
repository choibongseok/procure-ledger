// 구매요청 작성·수정(임시저장 또는 저장 후 상신).
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Breadcrumbs, FieldError, Notice, TableWrap } from "../layout/ui";
import { EditPanel } from "../document/LineEditor";
import type { LineEdit } from "../document/types";
import { won } from "./labels";
import { BudgetCheckCard } from "./BudgetCheckCard";
import { LinePreviewCard, type LineReady } from "./LinePreviewCard";
import { PolicyHints } from "./PolicyHints";
import type { Item, PurchaseRequest, Vendor } from "./types";
import { errorText, useLoad } from "./useLoad";

interface LineForm {
  itemId: string;
  quantity: string;
  unitPrice: string;
}

const emptyLine = (): LineForm => ({ itemId: "", quantity: "1", unitPrice: "0" });

export function lineAmount(l: LineForm): number {
  const q = Number(l.quantity);
  const p = Number(l.unitPrice);
  return Number.isFinite(q) && Number.isFinite(p) ? q * p : 0;
}

/** 칸 하나의 입력 오류. id 는 오류를 붙일 입력칸의 id 이고 오류 문구는 `${id}-err` 로 이어진다. */
export interface FieldProblem {
  id: string;
  message: string;
}

/** 입력 검사. 서버가 최종 판정하지만 화면에서도 먼저 알려 준다. 화면 순서대로 모든 오류를 돌려주고, 문제가 없으면 빈 배열. */
export function validateFields(title: string, vendorId: string, lines: LineForm[], forSubmit: boolean): FieldProblem[] {
  const out: FieldProblem[] = [];
  if (!title.trim()) out.push({ id: "pr-title", message: "제목을 입력하세요." });
  if (forSubmit && !vendorId) out.push({ id: "pr-vendor", message: "상신하려면 거래처를 선택하세요." });
  if (lines.length === 0) out.push({ id: "pr-add-line", message: "품목을 한 줄 이상 입력하세요." });
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    if (!l.itemId) out.push({ id: `pr-line-${i}-item`, message: `${i + 1}번째 줄의 품목을 선택하세요.` });
    if (!Number.isInteger(Number(l.quantity)) || Number(l.quantity) <= 0) out.push({ id: `pr-line-${i}-qty`, message: `${i + 1}번째 줄의 수량은 1 이상의 정수여야 합니다.` });
    if (!Number.isInteger(Number(l.unitPrice)) || Number(l.unitPrice) < 0) out.push({ id: `pr-line-${i}-price`, message: `${i + 1}번째 줄의 단가가 올바르지 않습니다.` });
  }
  return out;
}

/** 첫 오류 문구만(없으면 빈 문자열). 서버 검사와 문구를 맞춘 단순 판정에 쓴다. */
export function validateForm(title: string, vendorId: string, lines: LineForm[], forSubmit: boolean): string {
  return validateFields(title, vendorId, lines, forSubmit)[0]?.message ?? "";
}

export function PurchaseForm() {
  const { id } = useParams();
  const editing = id !== undefined;
  const nav = useNavigate();
  const { can } = useAuth();
  const items = useLoad(() => api.get<Item[]>("/api/items?limit=200"), []);
  const vendors = useLoad(() => api.get<Vendor[]>("/api/vendors"), []);
  const existing = useLoad(() => (editing ? api.get<PurchaseRequest>(`/api/purchase-requests/${id}`) : Promise.resolve(null)), [id]);

  const [title, setTitle] = useState("");
  const [reason, setReason] = useState("");
  const [dueDate, setDueDate] = useState("");
  const [place, setPlace] = useState("본사 창고");
  const [vendorId, setVendorId] = useState("");
  const [lines, setLines] = useState<LineForm[]>([emptyLine()]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // 저장·상신이 막힌 적이 있으면 그때의 종류(false 저장, true 상신)를 기억하고, 오류는 입력이 바뀔 때마다 다시 계산해 고친 칸에서 사라진다.
  const [attempt, setAttempt] = useState<boolean | null>(null);
  const [repeatNote, setRepeatNote] = useState("");
  const lastSig = useRef("");
  const [lineReady, setLineReady] = useState<LineReady>(null);
  const [edits, setEdits] = useState<LineEdit[]>([]);
  const [editNotes, setEditNotes] = useState<string[]>([]);

  useEffect(() => {
    const pr = existing.data;
    if (!pr) return;
    setTitle(pr.title);
    setReason(pr.reason);
    setDueDate(pr.dueDate ?? "");
    setPlace(pr.deliveryPlace || "본사 창고");
    setVendorId(pr.vendorId ? String(pr.vendorId) : "");
    setLines(pr.lines.map((l) => ({ itemId: String(l.itemId), quantity: String(l.quantity), unitPrice: String(l.unitPrice) })));
    setEdits((pr.draftEdits ?? []).map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group, name: e.name }) as LineEdit));
    setEditNotes(pr.editNotes ?? []);
  }, [existing.data]);

  const total = lines.reduce((s, l) => s + lineAmount(l), 0);
  const lineItemNames = [...new Set(lines.map((l) => items.data?.find((it) => String(it.id) === l.itemId)?.name ?? "").filter(Boolean))];
  const setLine = (i: number, patch: Partial<LineForm>) => setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l)));

  const problems = attempt === null ? [] : validateFields(title, vendorId, lines, attempt);
  const problemOf = (id: string) => problems.find((p) => p.id === id)?.message;
  /** 오류가 있는 입력칸에 붙일 속성. */
  const bad = (id: string) => (problemOf(id) ? { "aria-invalid": true as const, "aria-describedby": `${id}-err` } : {});
  const errorOf = (id: string) => {
    const m = problemOf(id);
    return m ? <FieldError id={`${id}-err`}>{m}</FieldError> : null;
  };

  async function save(thenSubmit: boolean) {
    const found = validateFields(title, vendorId, lines, thenSubmit);
    if (found.length > 0) {
      const sig = found.map((p) => `${p.id}:${p.message}`).join("|");
      // 같은 오류로 다시 눌렀을 때만 건수를 한 번 알린다(처음에는 첫 오류 칸의 문구가 읽힌다).
      setRepeatNote(sig === lastSig.current ? `오류 ${found.length}건이 남아 있습니다.` : "");
      lastSig.current = sig;
      setAttempt(thenSubmit);
      setError("");
      document.getElementById(found[0].id)?.focus();
      return;
    }
    lastSig.current = "";
    setRepeatNote("");
    setAttempt(null);
    setBusy(true);
    setError("");
    try {
      const body = {
        vendorId: vendorId ? Number(vendorId) : 0,
        title,
        reason,
        dueDate,
        deliveryPlace: place,
        lines: lines.map((l) => ({ itemId: Number(l.itemId), quantity: Number(l.quantity), unitPrice: Number(l.unitPrice) })),
        edits: edits.map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group })),
      };
      const saved = editing
        ? await api.put<PurchaseRequest>(`/api/purchase-requests/${id}`, body)
        : await api.post<PurchaseRequest>("/api/purchase-requests", body);
      if (thenSubmit) await api.post<PurchaseRequest>(`/api/purchase-requests/${saved.id}/submit`);
      nav(`/purchase-requests/${saved.id}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: editing ? "구매요청 수정" : "새 구매요청" }]} />
      <h1>{editing ? "구매요청 수정" : "새 구매요청"}</h1>
      {(items.error || vendors.error || existing.error) && <Notice tone="error">{items.error || vendors.error || existing.error}</Notice>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save(false);
        }}
      >
        <div className="field">
          <label htmlFor="pr-title">제목</label>
          <input id="pr-title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={200} {...bad("pr-title")} />
          {errorOf("pr-title")}
        </div>
        <div className="field">
          <label htmlFor="pr-vendor">거래처</label>
          <select id="pr-vendor" value={vendorId} onChange={(e) => setVendorId(e.target.value)} {...bad("pr-vendor")}>
            <option value="">선택하세요</option>
            {vendors.data?.map((v) => (
              <option key={v.id} value={v.id}>
                {v.name}
              </option>
            ))}
          </select>
          {errorOf("pr-vendor")}
        </div>
        <div className="field">
          <label htmlFor="pr-reason">구매 사유</label>
          <textarea id="pr-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="pr-due">희망 납기 (선택)</label>
          <input id="pr-due" type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} />
          <span className="hint">비워 두면 납기를 정하지 않습니다. 납기를 넘긴 입고는 거래처 평가에 반영됩니다.</span>
        </div>
        <div className="field">
          <label htmlFor="pr-place">납품처</label>
          <input id="pr-place" value={place} onChange={(e) => setPlace(e.target.value)} maxLength={100} />
        </div>
        <TableWrap>
        <table>
          <thead>
            <tr>
              <th>품목</th>
              <th className="nowrap">수량</th>
              <th className="nowrap">단가(원)</th>
              <th className="num nowrap">금액</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lines.map((l, i) => (
              <tr key={i}>
                <td>
                  <select id={`pr-line-${i}-item`} aria-label={`${i + 1}번째 줄 품목`} value={l.itemId} onChange={(e) => setLine(i, { itemId: e.target.value })} {...bad(`pr-line-${i}-item`)}>
                    <option value="">선택하세요</option>
                    {items.data?.map((it) => (
                      <option key={it.id} value={it.id}>
                        {it.name} ({it.sku})
                      </option>
                    ))}
                  </select>
                  {errorOf(`pr-line-${i}-item`)}
                </td>
                <td>
                  <input id={`pr-line-${i}-qty`} aria-label={`${i + 1}번째 줄 수량`} inputMode="numeric" value={l.quantity} onChange={(e) => setLine(i, { quantity: e.target.value })} {...bad(`pr-line-${i}-qty`)} />
                  {errorOf(`pr-line-${i}-qty`)}
                </td>
                <td>
                  <input id={`pr-line-${i}-price`} aria-label={`${i + 1}번째 줄 단가`} inputMode="numeric" value={l.unitPrice} onChange={(e) => setLine(i, { unitPrice: e.target.value })} {...bad(`pr-line-${i}-price`)} />
                  {errorOf(`pr-line-${i}-price`)}
                </td>
                <td className="num nowrap">{won(lineAmount(l))}</td>
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
          <button id="pr-add-line" type="button" className="btn-secondary" onClick={() => setLines((ls) => [...ls, emptyLine()])} {...bad("pr-add-line")}>
            품목 줄 추가
          </button>
          {errorOf("pr-add-line")}
        </p>
        <p>
          <strong>합계 {won(total)}</strong> — 금액에 따라 결재선이 정해집니다.
        </p>
        <section className="card" aria-label="견적 비교">
          <h2>견적 비교</h2>
          {editing ? (
            <p>
              <Link to={`/purchase-requests/${id}/quotes`}>견적 비교·견적서 올리기</Link>
              <span className="hint"> 견적서를 올리면 AI 가 값을 읽고, 거래처를 고르면 이 요청의 거래처가 바뀝니다. 이 화면에서 고친 내용은 먼저 저장한 뒤 이동하세요.</span>
            </p>
          ) : (
            <p className="hint">임시저장하면 견적서를 올려 거래처별로 비교할 수 있습니다. 견적서 없이도 상신할 수 있습니다.</p>
          )}
        </section>
        <BudgetCheckCard amount={total} />
        <LinePreviewCard amount={total} onReady={setLineReady} />
        <section className="card" aria-label="참조·수신">
          <h2>참조·수신</h2>
          <p className="hint">구매 품의서의 결재선은 금액 구간으로 정해지며 바꿀 수 없습니다. 참조(열람만)와 수신(결재 뒤 받는 조직)은 더할 수 있습니다.</p>
          {editNotes.length > 0 && <Notice tone="warn">{editNotes.join(" ")}</Notice>}
          <EditPanel formCode="PUR" mode="cc_receive" edits={edits} onEdits={setEdits} />
        </section>
        {can("policy:search") && <PolicyHints amount={total} items={lineItemNames} />}
        {error && <Notice tone="error">{error}</Notice>}
        <p role="status" className="hint">
          {problems.length > 0 ? repeatNote : ""}
        </p>
        <div className="form-row">
          <button type="submit" className="btn-secondary" disabled={busy}>
            임시저장
          </button>
          <button type="button" disabled={busy || lineReady === false} onClick={() => void save(true)}>
            저장 후 상신
          </button>
        </div>
      </form>
    </section>
  );
}

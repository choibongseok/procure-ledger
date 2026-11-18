// 문서 통합 검색(B29 ②): 제목·번호·기안자·본문·필드 값을 부분 일치로 찾는다. 결과는 내가 열람할 수 있는 문서뿐이다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, Pagination, TableWrap } from "../layout/ui";
import { PrStatusBadge } from "../purchase/badges";
import { dateTime } from "../purchase/labels";
import type { PurchaseRequest } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import type { FormSummary } from "./types";

interface Row {
  id: number;
  no: string;
  title: string;
  formCode: string;
  formName: string;
  formKind: "purchase" | "general";
  requesterName: string;
  unitPath: string;
  status: PurchaseRequest["status"];
  disposed: boolean;
  createdAt: string;
  updatedAt: string;
  matched: string;
}
interface Result {
  rows: Row[];
  total: number;
  pageSize: number;
}

const STATUS_CHOICES: [string, string][] = [
  ["draft", "임시저장"],
  ["in_review", "결재 중"],
  ["done", "완료"],
  ["rejected", "반려"],
  ["disposed", "폐기됨"],
];

const emptyDraft = { q: "", from: "", to: "", formCode: "", requester: "", statuses: [] as string[] };

export function DocSearchPage() {
  const forms = useLoad(() => api.get<FormSummary[]>("/api/forms"), []);
  const [draft, setDraft] = useState(emptyDraft);
  const [result, setResult] = useState<Result | null>(null);
  const [offset, setOffset] = useState(0);
  const [applied, setApplied] = useState<typeof emptyDraft | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function run(cond: typeof emptyDraft, off: number) {
    const q = new URLSearchParams();
    if (cond.q.trim()) q.set("q", cond.q.trim());
    if (cond.from) q.set("from", cond.from);
    if (cond.to) q.set("to", cond.to);
    if (cond.formCode) q.set("formCode", cond.formCode);
    if (cond.requester.trim()) q.set("requester", cond.requester.trim());
    if (cond.statuses.length) q.set("status", cond.statuses.join(","));
    if (off) q.set("offset", String(off));
    if ([...q.keys()].filter((k) => k !== "offset").length === 0) {
      setResult(null);
      return setError("검색어나 조건을 하나 이상 입력하세요.");
    }
    if (cond.q.trim() && cond.q.trim().length < 2) {
      setResult(null);
      return setError("검색어는 2자 이상 입력하세요.");
    }
    if (cond.from && cond.to && cond.to < cond.from) {
      setResult(null);
      return setError("종료일은 시작일 이후여야 합니다.");
    }
    setBusy(true);
    setError("");
    try {
      setResult(await api.get<Result>(`/api/docs/search?${q.toString()}`));
      setApplied(cond);
      setOffset(off);
    } catch (e) {
      setResult(null);
      setError(errorText(e) || "문서를 검색하지 못했습니다. 잠시 후 다시 시도하세요.");
    } finally {
      setBusy(false);
    }
  }

  function toggle(s: string) {
    setDraft((d) => ({ ...d, statuses: d.statuses.includes(s) ? d.statuses.filter((x) => x !== s) : [...d.statuses, s] }));
  }

  const rows = result?.rows ?? [];
  const total = result?.total ?? 0;
  const size = result?.pageSize ?? 50;
  return (
    <section>
      <h1>문서 검색</h1>
      <p className="hint">
        내가 볼 수 있는 문서만 검색됩니다. 제목·문서 번호·기안자·본문·필드 값에서 찾으며 개인정보가 담긴 필드와 첨부 파일의 내용은 찾지 않습니다. <Link to="/docs">문서함으로 돌아가기</Link>
      </p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="ds-q">검색어</label>
          <input id="ds-q" value={draft.q} maxLength={100} onChange={(e) => setDraft({ ...draft, q: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="ds-from">기안일 시작</label>
          <input id="ds-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="ds-to">기안일 종료</label>
          <input id="ds-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="ds-form">양식</label>
          <select id="ds-form" value={draft.formCode} onChange={(e) => setDraft({ ...draft, formCode: e.target.value })}>
            <option value="">전체</option>
            {(forms.data ?? []).map((f) => (
              <option key={f.code} value={f.code}>
                {f.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="ds-requester">기안자</label>
          <input id="ds-requester" value={draft.requester} maxLength={50} onChange={(e) => setDraft({ ...draft, requester: e.target.value })} />
        </div>
      </div>
      <div className="form-row" role="group" aria-label="상태">
        {STATUS_CHOICES.map(([k, v]) => (
          <label key={k}>
            <input type="checkbox" checked={draft.statuses.includes(k)} onChange={() => toggle(k)} /> {v}
          </label>
        ))}
        <button type="button" disabled={busy} onClick={() => void run(draft, 0)}>
          검색
        </button>
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      {busy && <p>불러오는 중…</p>}
      {result && rows.length === 0 && <EmptyState hint="검색어를 줄이거나 기간·상태 조건을 넓혀 보세요. 볼 수 있는 문서만 검색됩니다.">조건에 맞는 문서가 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <>
          {total <= size && <p>검색 결과 {total}건</p>}
          <TableWrap busy={busy}>
            <table className="stack-table">
              <thead>
                <tr>
                  <th className="nowrap">문서 번호</th>
                  <th>제목</th>
                  <th className="nowrap">양식</th>
                  <th className="nowrap">기안자</th>
                  <th>소속</th>
                  <th className="nowrap">상태</th>
                  <th className="nowrap">기안일</th>
                  <th className="nowrap">최근 변경</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id}>
                    <td data-label="문서 번호" className="nowrap">{r.no.startsWith("TMP-") ? "임시저장" : r.no}</td>
                    <td data-label="제목">
                      <Link to={r.formKind === "purchase" ? `/purchase-requests/${r.id}` : `/docs/${r.id}`}>{r.title}</Link>
                      {r.matched && <div className="hint">{r.matched}에서 일치</div>}
                    </td>
                    <td data-label="양식" className="nowrap">{r.formName}</td>
                    <td data-label="기안자" className="nowrap">{r.requesterName}</td>
                    <td data-label="소속">{r.unitPath || "—"}</td>
                    <td data-label="상태" className="nowrap">{r.disposed ? <Badge tone="muted">폐기됨</Badge> : <PrStatusBadge status={r.status} />}</td>
                    <td data-label="기안일" className="nowrap">{dateTime(r.createdAt)}</td>
                    <td data-label="최근 변경" className="nowrap">{dateTime(r.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <Pagination offset={offset} limit={size} total={total} onChange={(o) => applied && !busy && void run(applied, o)} />
        </>
      )}
    </section>
  );
}

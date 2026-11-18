// 구매요청 목록.
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { useAside } from "../layout/aside";
import { ExportButtons } from "../layout/ExportButtons";
import { EmptyState, FilterChips, LoadingRows, Notice, PageHead, Pagination, SortHeader, TableWrap } from "../layout/ui";
import type { SortOrder } from "../layout/ui";
import { RequestSummaryPanel } from "./asidePanels";
import { PrStatusBadge } from "./badges";
import { BudgetOverBadge } from "./BudgetCheckCard";
import { PR_STATUS_LABEL, dateTime, label, won } from "./labels";
import type { PRStatus, PurchaseRequest } from "./types";
import { useLoad } from "./useLoad";

const PAGE_SIZE = 50;
const STATUSES = Object.keys(PR_STATUS_LABEL) as PRStatus[];

interface Applied {
  from: string;
  to: string;
  q: string;
}

export function PurchaseList() {
  const [search] = useSearchParams();
  const [status, setStatus] = useState(() => {
    const s = search.get("status") ?? "";
    return (STATUSES as string[]).includes(s) ? s : "";
  });
  const [draft, setDraft] = useState<Applied>({ from: "", to: "", q: "" });
  const [applied, setApplied] = useState<Applied>({ from: "", to: "", q: "" });
  // 정렬: 기본은 작성일 내림차순. 열 머리를 누르면 그 열의 내림차순부터, 같은 열을 다시 누르면 방향이 바뀐다.
  const [sort, setSort] = useState("created_at");
  const [order, setOrder] = useState<SortOrder>("desc");
  const onSort = (field: string) => {
    if (field === sort) setOrder(order === "asc" ? "desc" : "asc");
    else {
      setSort(field);
      setOrder("desc");
    }
  };
  const { can } = useAuth();
  const params = { status, from: applied.from, to: applied.to, q: applied.q };
  const qs = new URLSearchParams(Object.entries({ ...params, sort, order }).filter(([, v]) => v) as [string, string][]).toString();
  // 조건이 바뀌면 첫 쪽부터 다시 본다(조건 키가 다른 위치는 0 으로 읽는다).
  const [pos, setPos] = useState({ qs, offset: 0 });
  const offset = pos.qs === qs ? pos.offset : 0;
  const setOffset = (o: number) => setPos({ qs, offset: o });
  const pageQs = new URLSearchParams(qs);
  pageQs.set("limit", String(PAGE_SIZE));
  if (offset > 0) pageQs.set("offset", String(offset));
  const { data: page, error, loading } = useLoad(() => api.getPage<PurchaseRequest>(`/api/purchase-requests?${pageQs.toString()}`), [pageQs.toString()]);
  const data = page?.items ?? null;
  const total = page?.total ?? 0;
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const selected = data?.find((pr) => pr.id === selectedId) ?? null;
  const clearAll = () => {
    setStatus("");
    setDraft({ from: "", to: "", q: "" });
    setApplied({ from: "", to: "", q: "" });
  };
  const chips = [
    ...(status ? [{ label: `상태: ${label(PR_STATUS_LABEL, status)}`, onClear: () => setStatus("") }] : []),
    ...(applied.from ? [{ label: `작성일 시작: ${applied.from}`, onClear: () => { setDraft({ ...draft, from: "" }); setApplied({ ...applied, from: "" }); } }] : []),
    ...(applied.to ? [{ label: `작성일 종료: ${applied.to}`, onClear: () => { setDraft({ ...draft, to: "" }); setApplied({ ...applied, to: "" }); } }] : []),
    ...(applied.q ? [{ label: `제목: ${applied.q}`, onClear: () => { setDraft({ ...draft, q: "" }); setApplied({ ...applied, q: "" }); } }] : []),
  ];
  const emptyNoCond = !!data && data.length === 0 && !loading && chips.length === 0;
  const canCreate = can("purchase:create");
  const content = useMemo(() => (selected ? <RequestSummaryPanel pr={selected} /> : null), [selected]);
  useAside(content ? { title: "선택한 요청", content } : null);
  return (
    <section>
      <PageHead title="구매요청" />
      <div className="toolbar">
        <div className="field">
          <label htmlFor="pr-status">상태</label>
          <select id="pr-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">전체</option>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {label(PR_STATUS_LABEL, s)}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="pr-from">작성일 시작</label>
          <input id="pr-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="pr-to">작성일 종료</label>
          <input id="pr-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="pr-q">제목 검색</label>
          <input id="pr-q" value={draft.q} maxLength={100} onChange={(e) => setDraft({ ...draft, q: e.target.value })} />
        </div>
        <button type="button" className="btn-secondary" onClick={() => setApplied(draft)}>
          조회
        </button>
        <div className="toolbar-end">
          <ExportButtons path="/api/purchase-requests/export" params={params} disabled={loading || !data || data.length === 0} />
          {canCreate && !(emptyNoCond) && (
            <Link className="btn" to="/purchase-requests/new">
              새 구매요청 작성
            </Link>
          )}
        </div>
      </div>
      <FilterChips chips={chips} onClearAll={clearAll} hideClearAll={!!data && data.length === 0 && !loading && chips.length > 0} />
      {error && <Notice tone="error">{error}</Notice>}
      {emptyNoCond && (
        <EmptyState
          hint="구매요청을 작성하면 여기에 목록으로 나타납니다."
          actions={
            canCreate ? (
              <Link className="btn" to="/purchase-requests/new">
                새 구매요청 작성
              </Link>
            ) : undefined
          }
        >
          아직 구매요청이 없습니다.
        </EmptyState>
      )}
      {data && data.length === 0 && !loading && chips.length > 0 && (
        <EmptyState
          hint="조건을 바꾸거나 지워 보세요."
          actions={
            <button type="button" className="btn-secondary" onClick={clearAll}>
              조건 지우기
            </button>
          }
        >
          조건에 맞는 구매요청이 없습니다.
        </EmptyState>
      )}
      {((data && data.length > 0) || (loading && !data)) && (
        <TableWrap busy={loading}>
          <table>
            <thead>
              <tr>
                <th className="nowrap">선택</th>
                <th className="nowrap">번호</th>
                <th>제목</th>
                <th className="nowrap">요청자</th>
                <SortHeader label="금액" align="num" field="total_amount" sort={sort} order={order} onSort={onSort} />
                <th className="nowrap">상태</th>
                <SortHeader label="작성일" field="created_at" sort={sort} order={order} onSort={onSort} />
              </tr>
            </thead>
            <tbody>
              {loading && !data ? <LoadingRows cols={7} /> : (data ?? []).map((pr) => (
                <tr key={pr.id} aria-selected={pr.id === selectedId ? "true" : undefined}>
                  <td className="nowrap">
                    <input
                      type="radio"
                      name="pr-select"
                      aria-label={`${pr.no} 선택`}
                      checked={pr.id === selectedId}
                      onChange={() => setSelectedId(pr.id)}
                    />
                  </td>
                  <td className="nowrap">{pr.no}</td>
                  <td>
                    <Link to={`/purchase-requests/${pr.id}`}>{pr.title}</Link>
                  </td>
                  <td className="nowrap">{pr.requesterName}</td>
                  <td className="num nowrap">{won(pr.totalAmount)}</td>
                  <td className="nowrap">
                    <PrStatusBadge status={pr.status} /> <BudgetOverBadge budget={pr.budget} />
                    {pr.noApprover ? " (결재자 없음)" : ""}
                  </td>
                  <td className="nowrap">{dateTime(pr.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <Pagination offset={offset} limit={PAGE_SIZE} total={total} onChange={setOffset} />
    </section>
  );
}

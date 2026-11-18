// 월별 재고 현황(B27 ④): 마감된 달은 저장된 월말 스냅샷, 그 밖의 달은 마감 전 잠정 값. 품목·창고별 기초·입고·출고·이동·조정·기말과 전월 대비.
// 수량은 재고 조회 권한 전체, 금액 열은 재고 평가 조회 권한이 있는 역할에게만 서버가 내려준다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";
import type { Warehouse } from "./stockTypes";

interface Row {
  itemId: number;
  sku: string;
  itemName: string;
  warehouseId: number;
  warehouseName: string;
  warehouseActive: boolean;
  entityId?: number;
  entityName?: string;
  openQty: number;
  inQty: number;
  outQty: number;
  transferQty: number;
  adjustQty: number;
  closeQty: number;
  openAmount?: number;
  inAmount?: number;
  outAmount?: number;
  closeAmount?: number;
  avgCost?: number;
  delta?: number | null;
  deltaPct?: number | null;
}

interface EntityTotal {
  entityId: number;
  entityName: string;
  closeQty: number;
  closeAmount?: number;
}

interface Report {
  /** 전체 법인 보기: 줄이 법인별이고 단가는 합산하지 않는다. */
  allEntities?: boolean;
  entityTotals?: EntityTotal[];
  /** 법인을 나누기 전에 마감한 달의 스냅샷에서 제외한 줄 수. */
  excludedLines?: number;
  month: string;
  provisional: boolean;
  version: number;
  hasAmounts: boolean;
  prevClosed: boolean;
  rows: Row[];
  total: Record<string, number | null>;
}

const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : v.toLocaleString("ko-KR"));
const signed = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${v > 0 ? "+" : ""}${v.toLocaleString("ko-KR")}`);

/** "2026-11" → "2026년 11월". */
export function monthLabel(m: string): string {
  const [y, mo] = m.split("-");
  return y && mo ? `${y}년 ${Number(mo)}월` : m;
}

/** 이번 달(KST)부터 거꾸로 12개월. */
function recentMonths(): string[] {
  const base = new Date(Date.now() + 9 * 3600 * 1000);
  return Array.from({ length: 12 }, (_, i) => {
    const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth() - i, 1));
    return d.toISOString().slice(0, 7);
  });
}

export function MonthStockPage() {
  const { can, entities, currentEntity } = useAuth();
  const multi = (entities?.length ?? 0) > 1;
  const entityName = multi && !currentEntity?.all ? currentEntity?.name : undefined;
  const months = recentMonths();
  const [month, setMonth] = useState(months[1]);
  const [wh, setWh] = useState("");
  const [q, setQ] = useState("");
  const [applied, setApplied] = useState({ month: months[1], wh: "", q: "" });
  const whs = useLoad(() => api.get<Warehouse[]>("/api/warehouses"), []);
  const rep = useLoad(
    () => api.get<Report>(`/api/inventory/month-stock?month=${applied.month}${applied.wh ? `&warehouseId=${applied.wh}` : ""}${applied.q ? `&q=${encodeURIComponent(applied.q)}` : ""}`),
    [applied],
  );
  const valued = can("inventory:valuation_read");

  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>월별 재고 현황</h2>
      <p className="hint">
        마감한 달은 마감 때 저장한 월말 재고이고, 마감 전인 달은 지금 기록으로 계산한 잠정 값입니다. 금액은 이동평균 단가로 계산한 공급가액입니다.
      </p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="ms-month">월</label>
          <select id="ms-month" value={month} onChange={(e) => setMonth(e.target.value)}>
            {months.map((m) => (
              <option key={m} value={m}>
                {monthLabel(m)}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="ms-wh">창고</label>
          <select id="ms-wh" value={wh} onChange={(e) => setWh(e.target.value)}>
            <option value="">전체 창고</option>
            {whs.data?.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="ms-q">품목 검색</label>
          <input id="ms-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="코드나 이름" />
        </div>
      </div>
      <button type="button" onClick={() => setApplied({ month, wh, q })}>
        조회
      </button>{" "}
      <ExportButtons
        path={valued ? "/api/inventory/month-stock/export-valued" : "/api/inventory/month-stock/export"}
        params={{ month: applied.month, warehouseId: applied.wh, q: applied.q }}
        disabled={!rep.data || rep.data.rows.length === 0}
      />
      {rep.error && <Notice tone="error">{rep.error || errorText(rep.error)}</Notice>}
      {rep.data && (
        <p>
          <strong>{monthLabel(rep.data.month)}</strong>{" "}
          {rep.data.provisional ? <Badge tone="warn">마감 전 잠정 값입니다</Badge> : <Badge tone="ok">마감됨{rep.data.version > 1 ? ` (${rep.data.version}차)` : ""}</Badge>}
        </p>
      )}
      {rep.data && rep.data.hasAmounts && !rep.data.prevClosed && <Notice tone="info">전월이 마감되지 않아 비교할 수 없습니다.</Notice>}
      {entityName && <p className="hint">{entityName} 법인의 재고입니다.</p>}
      {rep.data?.allEntities && <Notice tone="info">전체 법인 보기에서는 법인마다 단가가 달라 단가를 합산하지 않습니다. 법인별로 나누어 보입니다.</Notice>}
      {rep.data && (rep.data.excludedLines ?? 0) > 0 && <Notice tone="info">이 달 스냅샷은 법인을 나누기 전에 마감되어 일부 줄을 제외했습니다.</Notice>}
      {rep.data && rep.data.rows.length === 0 && (
        <EmptyState>
          {entityName ? `${entityName} 법인에는 이 달에 재고 기록이 없습니다.` : "이 조건에 맞는 재고 기록이 없습니다. 마감된 달이 없으면 월 마감 화면에서 지난 달을 마감하세요."}
        </EmptyState>
      )}
      {rep.data && rep.data.rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                {rep.data.allEntities && <th>법인</th>}
                <th>품목</th>
                <th>창고</th>
                <th className="num nowrap">기초</th>
                <th className="num nowrap">입고</th>
                <th className="num nowrap">출고</th>
                <th className="num nowrap">이동</th>
                <th className="num nowrap">조정</th>
                <th className="num nowrap">기말</th>
                {rep.data.hasAmounts && !rep.data.allEntities && <th className="num nowrap">평균 단가</th>}
                {rep.data.hasAmounts && <th className="num nowrap">기말 금액</th>}
                {rep.data.hasAmounts && rep.data.prevClosed && <th className="num nowrap">전월 대비</th>}
              </tr>
            </thead>
            <tbody>
              {rep.data.rows.map((r) => (
                <tr key={`${r.entityId ?? 0}-${r.itemId}-${r.warehouseId}`}>
                  {rep.data!.allEntities && <td data-label="법인">{r.entityName}</td>}
                  <td data-label="품목">
                    {r.itemName}
                    <div className="hint">{r.sku}</div>
                  </td>
                  <td data-label="창고">
                    {r.warehouseName}
                    {!r.warehouseActive && <span className="hint">(중지)</span>}
                  </td>
                  <td data-label="기초" className="num nowrap">{n(r.openQty)}</td>
                  <td data-label="입고" className="num nowrap">{n(r.inQty)}</td>
                  <td data-label="출고" className="num nowrap">{n(r.outQty)}</td>
                  <td data-label="이동" className="num nowrap">{signed(r.transferQty)}</td>
                  <td data-label="조정" className="num nowrap">{signed(r.adjustQty)}</td>
                  <td data-label="기말" className="num nowrap">{n(r.closeQty)}</td>
                  {rep.data!.hasAmounts && !rep.data!.allEntities && <td data-label="평균 단가" className="num nowrap">{won(r.avgCost ?? 0)}</td>}
                  {rep.data!.hasAmounts && <td data-label="기말 금액" className="num nowrap">{won(r.closeAmount ?? 0)}</td>}
                  {rep.data!.hasAmounts && rep.data!.prevClosed && (
                    <td data-label="전월 대비" className="num nowrap">
                      {r.delta === null || r.delta === undefined ? "—" : `${signed(r.delta)}원${r.deltaPct === null || r.deltaPct === undefined ? "" : ` (${signed(r.deltaPct)}%)`}`}
                    </td>
                  )}
                </tr>
              ))}
              <tr>
                <th>합계</th>
                {rep.data.allEntities && <td />}
                <td />
                <td className="num nowrap">{n(rep.data.total.openQty)}</td>
                <td className="num nowrap">{n(rep.data.total.inQty)}</td>
                <td className="num nowrap">{n(rep.data.total.outQty)}</td>
                <td className="num nowrap">{signed(rep.data.total.transferQty)}</td>
                <td className="num nowrap">{signed(rep.data.total.adjustQty)}</td>
                <td className="num nowrap">{n(rep.data.total.closeQty)}</td>
                {rep.data.hasAmounts && !rep.data.allEntities && <td />}
                {rep.data.hasAmounts && <td className="num nowrap">{won(rep.data.total.closeAmount ?? 0)}</td>}
                {rep.data.hasAmounts && rep.data.prevClosed && <td className="num nowrap">{signed((rep.data.total.closeAmount ?? 0) - (rep.data.total.prevAmount ?? 0))}원</td>}
              </tr>
            </tbody>
          </table>
        </TableWrap>
      )}
      {rep.data?.allEntities && rep.data.entityTotals && (
        <>
          <h3>법인별 소계</h3>
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th>법인</th>
                  <th className="num nowrap">기말 수량</th>
                  {rep.data.hasAmounts && <th className="num nowrap">기말 금액</th>}
                </tr>
              </thead>
              <tbody>
                {rep.data.entityTotals.map((e) => (
                  <tr key={e.entityId}>
                    <td data-label="법인">{e.entityName}</td>
                    <td data-label="기말 수량" className="num nowrap">{n(e.closeQty)}</td>
                    {rep.data!.hasAmounts && <td data-label="기말 금액" className="num nowrap">{won(e.closeAmount ?? 0)}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
      {rep.data && rep.data.hasAmounts && rep.data.total.roundDiff !== null && rep.data.total.roundDiff !== undefined && rep.data.total.roundDiff !== 0 && (
        <p className="hint">반올림 차이 {signed(rep.data.total.roundDiff)}원: 품목 잔액 금액과 창고별 기말 금액의 합이 정수 원 반올림 때문에 이만큼 다릅니다.</p>
      )}
    </section>
  );
}

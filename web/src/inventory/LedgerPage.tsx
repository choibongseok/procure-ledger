// 수불부 화면(B24 ④): 품목별 기초·입고·출고·기말. 읽기 전용이라 재고 조회 권한이 있는 모든 역할이 본다(처리자·금액 없음).
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import type { Item } from "../purchase/types";
import { errorText } from "../purchase/useLoad";
import { useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";
import type { LedgerPage as Page, Warehouse } from "./stockTypes";

// 서버는 한국 시간(KST) 날짜로 판정하므로 화면의 오늘도 KST 로 맞춘다(UTC 로 하면 새벽에 하루 전 날짜가 된다).
const today = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);
const monthStart = () => today().slice(0, 8) + "01";
const n = (v: number) => v.toLocaleString("ko-KR");

export function LedgerPage() {
  const whs = useLoad(() => api.get<Warehouse[]>("/api/warehouses"), []);
  const [q, setQ] = useState("");
  const items = useLoad(
    () =>
      q.trim()
        ? api.get<Item[]>(`/api/items?q=${encodeURIComponent(q)}&limit=20`)
        : Promise.resolve([] as Item[]),
    [q],
  );
  const [item, setItem] = useState("");
  const [wh, setWh] = useState("");
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Page | null>(null);
  const [err, setErr] = useState("");
  const [applied, setApplied] = useState<Record<string, string>>({});
  const { can } = useAuth();
  // B27: 단가·금액 열은 재고 평가 조회 권한이 있는 역할에게만 그린다(서버도 같은 권한으로 값을 거른다).
  const showValue = can("inventory:valuation_read") && !!data && data.rows.some((r) => r.amount !== undefined);

  async function search(p = 1) {
    setErr("");
    if (!item) {
      setData(null);
      setErr("품목을 고르세요. 수불부는 품목마다 봅니다.");
      return;
    }
    if (to < from) {
      setData(null);
      setErr("종료일은 시작일 이후여야 합니다.");
      return;
    }
    try {
      const qs = `itemId=${item}&from=${from}&to=${to}&page=${p}${wh ? `&warehouseId=${wh}` : ""}`;
      setData(await api.get<Page>(`/api/stock-ledger?${qs}`));
      setApplied({ itemId: item, from, to, warehouseId: wh });
      setPage(p);
    } catch (e) {
      setData(null);
      setErr(errorText(e));
    }
  }
  const pages = data ? Math.max(1, Math.ceil(data.total / 50)) : 1;
  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>수불부</h2>
      {err && <Notice tone="error">{err}</Notice>}
      <div className="form-row">
        <label>
          품목 찾기
          <input
            value={q}
            placeholder="품목 이름 또는 코드"
            onChange={(e) => setQ(e.target.value)}
          />
        </label>
        <label>
          품목
          <select
            aria-label="수불부 품목 선택"
            value={item}
            onChange={(e) => setItem(e.target.value)}
          >
            <option value="">선택하세요</option>
            {(items.data ?? []).map((i) => (
              <option key={i.id} value={i.id}>
                {i.name} ({i.sku})
              </option>
            ))}
          </select>
        </label>
        <label>
          창고
          <select
            aria-label="수불부 창고 선택"
            value={wh}
            onChange={(e) => setWh(e.target.value)}
          >
            <option value="">전체 창고</option>
            {(whs.data ?? []).map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          시작일
          <input
            type="date"
            lang="ko-KR"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </label>
        <label>
          종료일
          <input
            type="date"
            lang="ko-KR"
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </label>
        <button type="button" onClick={() => void search(1)}>
          조회
        </button>
      </div>
      {data && (
        <>
          <ExportButtons
            path={can("inventory:valuation_read") ? "/api/stock-ledger/export-valued" : "/api/stock-ledger/export"}
            params={applied}
          />
          <p>
            기초 {n(data.opening)} + 입고 {n(data.totalIn)} − 출고{" "}
            {n(data.totalOut)} = <strong>기말 {n(data.closing)}</strong>
            {wh === "" && to === today()
              ? " · 현재 재고 화면의 합계와 같습니다."
              : ""}
          </p>
          {wh !== "" && (
            <p className="hint">
              창고를 하나로 고르면 그 창고의 기록만 보입니다. 이동은 반대편
              창고의 줄이 보이지 않는 것이 정상입니다.
            </p>
          )}
          {data.rows.length === 0 ? (
            <EmptyState>
              이 기간에는 입고·출고 기록이 없습니다. 기초 수량 {n(data.opening)}
              , 기말 수량 {n(data.closing)} 입니다.
            </EmptyState>
          ) : (
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th className="nowrap">일자</th>
                    <th className="nowrap">구분</th>
                    <th>창고</th>
                    <th>로트</th>
                    <th className="nowrap">전표 번호</th>
                    <th className="num nowrap">입고</th>
                    <th className="num nowrap">출고</th>
                    <th className="num nowrap">잔량</th>
                    {showValue && <th className="num nowrap">단가</th>}
                    {showValue && <th className="num nowrap">금액</th>}
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td className="nowrap">기초</td>
                    <td colSpan={4} className="hint">
                      ({from} 시작)
                    </td>
                    <td />
                    <td />
                    <td className="num nowrap">{n(data.opening)}</td>
                    {showValue && <td colSpan={2} />}
                  </tr>
                  {data.rows.map((r, i) => (
                    <tr key={i}>
                      <td className="nowrap">{r.date}</td>
                      <td className="nowrap">{r.kindLabel}</td>
                      <td>{r.warehouse}</td>
                      <td>{r.lotNo || "-"}</td>
                      <td className="nowrap">{r.docNo || "-"}</td>
                      <td className="num nowrap">{r.in ? n(r.in) : ""}</td>
                      <td className="num nowrap">{r.out ? n(r.out) : ""}</td>
                      <td className="num nowrap">{n(r.balance)}</td>
                      {showValue && <td className="num nowrap">{r.unitCost === undefined ? "—" : won(r.unitCost)}</td>}
                      {showValue && <td className="num nowrap">{r.amount === undefined ? "—" : won(r.amount)}</td>}
                    </tr>
                  ))}
                  <tr>
                    <th>기말</th>
                    <td colSpan={4} className="hint">
                      ({to} 기준)
                    </td>
                    <td className="num nowrap">{n(data.totalIn)}</td>
                    <td className="num nowrap">{n(data.totalOut)}</td>
                    <td className="num nowrap">{n(data.closing)}</td>
                    {showValue && <td colSpan={2} />}
                  </tr>
                </tbody>
              </table>
            </TableWrap>
          )}
          {pages > 1 && (
            <p>
              <button
                type="button"
                className="btn-secondary"
                disabled={page <= 1}
                onClick={() => void search(page - 1)}
              >
                이전 쪽
              </button>{" "}
              {page} / {pages}쪽{" "}
              <button
                type="button"
                className="btn-secondary"
                disabled={page >= pages}
                onClick={() => void search(page + 1)}
              >
                다음 쪽
              </button>
            </p>
          )}
        </>
      )}
    </section>
  );
}

// 재고 화면: 품목 검색·재고 조회, 안전재고 미달, 유효기한 임박, (관리 권한) 품목·거래처 등록.
import { ExportButtons } from "../layout/ExportButtons";
import { useState } from "react";
import { api } from "../api/client";
import { PermissionGate } from "../auth/PermissionGate";
import { Badge, EmptyState, FilterChips, Notice, SortHeader, TableWrap, useToast } from "../layout/ui";
import type { SortOrder } from "../layout/ui";
import { useAuth } from "../auth/AuthProvider";
import { won } from "../purchase/labels";
import type { ExpiringLot, Item, ItemStock } from "../purchase/types";
import { ApiError } from "../api/client";
import { businessMessage, useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";
import { VendorLinks } from "./VendorLinks";

function StockDetail({ itemId }: { itemId: number }) {
  const { entities, currentEntity } = useAuth();
  const entityName = (entities?.length ?? 0) > 1 && !currentEntity?.all ? currentEntity?.name : undefined;
  const [wh, setWh] = useState("");
  const whs = useLoad(() => api.get<{ id: number; name: string; active: boolean }[]>("/api/warehouses"), []);
  const { data, error } = useLoad(() => api.get<ItemStock>(`/api/items/${itemId}/stock${wh ? `?warehouseId=${wh}` : ""}`), [itemId, wh]);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (!data) return <p>불러오는 중…</p>;
  const rows = data.warehouseLots ?? [];
  const showWh = (whs.data ?? []).length > 1;
  return (
    <div className="card">
      <h3>
        {data.item.name} ({data.item.sku})
      </h3>
      {showWh && (
        <label>
          창고
          <select aria-label="재고 창고 선택" value={wh} onChange={(e) => setWh(e.target.value)}>
            <option value="">전체 창고</option>
            {(whs.data ?? []).map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
                {w.active ? "" : " (사용 중지)"}
              </option>
            ))}
          </select>
        </label>
      )}
      <p>
        {wh ? "이 창고의 재고" : "현재 재고"} <strong>{data.onHand.toLocaleString("ko-KR")}</strong> {data.item.unit} / 안전재고 {data.item.safetyStock.toLocaleString("ko-KR")}{" "}
        {data.belowSafety && (
          <span role="note">
            <Badge tone="warn">안전재고 미달</Badge>
          </span>
        )}
      </p>
      {!wh && data.avgCost !== undefined && data.stockAmount !== undefined && (
        <p className="hint">
          {entityName && `${entityName} 법인 `}평균 단가 {won(data.avgCost)} · 재고 금액 {won(data.stockAmount)}(이동평균, 공급가액 기준)
        </p>
      )}
      {!wh && (data.valuations ?? []).length > 0 && (
        <>
          <p className="hint">전체 법인 보기에서는 법인마다 단가가 달라 단가를 합산하지 않습니다. 법인별로 나누어 보입니다.</p>
          <p className="hint">
            {(data.valuations ?? []).map((v) => `${v.entityName}: 평균 단가 ${won(v.avgCost)} · 재고 금액 ${won(v.stockAmount)}`).join(" / ")}
          </p>
        </>
      )}
      {!wh && (data.byWarehouse ?? []).length > 1 && (
        <p className="hint">창고별: {(data.byWarehouse ?? []).map((w) => `${w.warehouseName} ${w.onHand.toLocaleString("ko-KR")}`).join(" · ")}</p>
      )}
      {(rows.some((r) => r.lotId !== 0) || data.lots.length > 0) && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                {showWh && <th>창고</th>}
                <th>로트</th>
                <th className="nowrap">유효기한</th>
                <th className="num nowrap">수량</th>
              </tr>
            </thead>
            <tbody>
              {rows.length > 0
                ? rows
                    .filter((r) => r.lotId !== 0)
                    .map((r) => (
                      <tr key={`${r.warehouseId}-${r.lotId}`}>
                        {showWh && <td>{r.warehouseName}</td>}
                        <td>{r.lotNo}</td>
                        <td className="nowrap">{r.expiryDate || "-"}</td>
                        <td className="num nowrap">{r.quantity.toLocaleString("ko-KR")}</td>
                      </tr>
                    ))
                : data.lots.map((l) => (
                    <tr key={l.lotId}>
                      {showWh && <td>-</td>}
                      <td>{l.lotNo}</td>
                      <td className="nowrap">{l.expiryDate || "-"}</td>
                      <td className="num nowrap">{l.quantity.toLocaleString("ko-KR")}</td>
                    </tr>
                  ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </div>
  );
}

function expiryText(days: number): string {
  if (days < 0) return `${-days}일 지남`;
  if (days === 0) return "오늘 만료";
  return `${days}일 남음`;
}

/** 품목·거래처 등록 실패를 업무 말로 바꾼다(서버 문구를 그대로 보이지 않는다). */
function masterErrorText(e: unknown, kind: "품목" | "거래처"): string {
  if (!(e instanceof ApiError)) return "등록하지 못했습니다. 잠시 후 다시 시도해 주세요.";
  const msg = businessMessage(e.message);
  if (msg.includes("이미 등록된")) return kind === "품목" ? "이미 등록된 품목 코드입니다. 다른 코드를 입력하세요." : "이미 등록된 거래처입니다. 거래처 목록을 확인하세요.";
  if (msg.startsWith("품목 코드와 이름")) return "품목 코드와 이름을 입력하세요.";
  if (msg.startsWith("거래처 이름")) return "거래처 이름을 입력하세요.";
  if (msg.startsWith("안전재고는")) return "안전재고는 0 이상의 숫자로 입력하세요.";
  if (e.status === 403) return `${kind}을 등록할 권한이 없습니다. 재고 담당자나 관리자에게 요청하세요.`;
  if (e.status >= 500) return "등록하지 못했습니다. 잠시 후 다시 시도해 주세요.";
  return `${kind}을 등록하지 못했습니다. ${msg}`;
}

function MasterForms({ onCreated }: { onCreated: () => void }) {
  const [item, setItem] = useState({
    sku: "",
    name: "",
    unit: "EA",
    safetyStock: "0",
    trackLot: false,
  });
  const [vendor, setVendor] = useState({
    name: "",
    businessNo: "",
    accountNo: "",
    phone: "",
  });
  const [err, setErr] = useState("");
  const toast = useToast();

  async function run(fn: () => Promise<unknown>, done: string, kind: "품목" | "거래처") {
    setErr("");
    try {
      await fn();
      toast(done);
      onCreated();
    } catch (e) {
      setErr(masterErrorText(e, kind));
    }
  }

  return (
    <div>
      <h2>품목·거래처 등록</h2>
      <div className="card-grid">
        <form
          className="card"
          onSubmit={(e) => {
            e.preventDefault();
            void run(
              () =>
                api.post("/api/items", {
                  ...item,
                  safetyStock: Number(item.safetyStock),
                }),
              "품목을 등록했습니다.",
              "품목",
            );
          }}
        >
          <h3>품목</h3>
          <div className="field">
            <label htmlFor="item-sku">품목 코드</label>
            <input
              id="item-sku"
              value={item.sku}
              onChange={(e) => setItem({ ...item, sku: e.target.value })}
            />
          </div>
          <div className="field">
            <label htmlFor="item-name">이름</label>
            <input
              id="item-name"
              value={item.name}
              onChange={(e) => setItem({ ...item, name: e.target.value })}
            />
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="item-unit">단위</label>
              <input
                id="item-unit"
                value={item.unit}
                onChange={(e) => setItem({ ...item, unit: e.target.value })}
              />
            </div>
            <div className="field">
              <label htmlFor="item-safety">안전재고</label>
              <input
                id="item-safety"
                value={item.safetyStock}
                onChange={(e) =>
                  setItem({ ...item, safetyStock: e.target.value })
                }
              />
            </div>
          </div>
          <div className="field">
            <label>
              <input
                type="checkbox"
                checked={item.trackLot}
                onChange={(e) =>
                  setItem({ ...item, trackLot: e.target.checked })
                }
              />{" "}
              로트·유효기한 관리
            </label>
          </div>
          <button type="submit">품목 등록</button>
        </form>
        <form
          className="card"
          onSubmit={(e) => {
            e.preventDefault();
            void run(
              () => api.post("/api/vendors", vendor),
              "거래처를 등록했습니다.",
              "거래처",
            );
          }}
        >
          <h3>거래처</h3>
          <div className="field">
            <label htmlFor="vendor-name">이름</label>
            <input
              id="vendor-name"
              value={vendor.name}
              onChange={(e) => setVendor({ ...vendor, name: e.target.value })}
            />
          </div>
          <div className="field">
            <label htmlFor="vendor-bizno">사업자번호</label>
            <input
              id="vendor-bizno"
              value={vendor.businessNo}
              onChange={(e) =>
                setVendor({ ...vendor, businessNo: e.target.value })
              }
            />
          </div>
          <div className="field">
            <label htmlFor="vendor-account">계좌</label>
            <input
              id="vendor-account"
              value={vendor.accountNo}
              onChange={(e) =>
                setVendor({ ...vendor, accountNo: e.target.value })
              }
            />
          </div>
          <div className="field">
            <label htmlFor="vendor-phone">연락처</label>
            <input
              id="vendor-phone"
              value={vendor.phone}
              onChange={(e) => setVendor({ ...vendor, phone: e.target.value })}
            />
          </div>
          <button type="submit">거래처 등록</button>
        </form>
      </div>
      {err && <Notice tone="error">{err}</Notice>}
      <VendorLinks />
    </div>
  );
}

export function StockPage() {
  const [q, setQ] = useState("");
  const [days, setDays] = useState(30);
  const [belowSafety, setBelowSafety] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);
  const { can } = useAuth();
  const [belowSort, setBelowSort] = useState({ sort: "name", order: "asc" as SortOrder });
  const [expSort, setExpSort] = useState({ sort: "expiry_date", order: "asc" as SortOrder });
  // 같은 열을 다시 누르면 방향이 바뀌고, 다른 열을 누르면 그 열의 오름차순부터 본다.
  const nextSort = (cur: { sort: string; order: SortOrder }, field: string) => ({ sort: field, order: (cur.sort === field && cur.order === "asc" ? "desc" : "asc") as SortOrder });
  const items = useLoad(
    () => api.get<Item[]>(`/api/items?q=${encodeURIComponent(q)}&limit=50`),
    [q],
  );
  const below = useLoad(
    () => api.get<ItemStock[]>(`/api/stock/below-safety?sort=${belowSort.sort}&order=${belowSort.order}`),
    [belowSort.sort, belowSort.order],
  );
  const expiring = useLoad(
    () => api.get<ExpiringLot[]>(`/api/stock/expiring?days=${days}&sort=${expSort.sort}&order=${expSort.order}`),
    [days, expSort.sort, expSort.order],
  );

  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />

      <h2>품목 조회</h2>
      <div className="toolbar">
        <div className="field">
          <label htmlFor="stock-q">품목 검색</label>
          <input
            id="stock-q"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="이름 또는 품목 코드"
          />
        </div>
        <label>
          <input type="checkbox" checked={belowSafety} onChange={(e) => setBelowSafety(e.target.checked)} /> 안전재고 미달 품목만 내보내기
        </label>
        <div className="toolbar-end">
          <ExportButtons path="/api/stock/export" params={{ belowSafety }} />
        </div>
      </div>
      <FilterChips chips={q ? [{ label: `검색: ${q}`, onClear: () => setQ("") }] : []} onClearAll={() => setQ("")} hideClearAll={!!items.data && items.data.length === 0 && !!q} />
      {items.error && <Notice tone="error">{items.error}</Notice>}
      {items.data && items.data.length === 0 && !q && (
        <EmptyState
          hint="품목을 등록하면 여기에서 재고를 조회할 수 있습니다."
          actions={
            can("inventory:manage") ? (
              <button type="button" onClick={() => document.getElementById("item-sku")?.focus()}>
                품목 등록
              </button>
            ) : undefined
          }
        >
          아직 등록된 품목이 없습니다.
        </EmptyState>
      )}
      {items.data && items.data.length === 0 && q && (
        <EmptyState
          hint="조건을 바꾸거나 지워 보세요."
          actions={
            <button type="button" className="btn-secondary" onClick={() => setQ("")}>
              조건 지우기
            </button>
          }
        >
          조건에 맞는 품목이 없습니다.
        </EmptyState>
      )}
      {items.data && items.data.length > 0 && (
        <ul>
          {items.data.map((it) => (
            <li key={it.id}>
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={() => setSelected(it.id)}
              >
                {it.name} ({it.sku})
              </button>
            </li>
          ))}
        </ul>
      )}
      {selected !== null && <StockDetail itemId={selected} />}

      <h2>안전재고 미달 품목</h2>
      {below.error && <Notice tone="error">{below.error}</Notice>}
      {below.data && below.data.length === 0 && (
        <EmptyState>안전재고에 못 미치는 품목이 없습니다.</EmptyState>
      )}
      {below.data && below.data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <SortHeader label="품목" field="name" sort={belowSort.sort} order={belowSort.order} onSort={(f) => setBelowSort(nextSort(belowSort, f))} />
                <SortHeader label="현재 재고" align="num" field="on_hand" sort={belowSort.sort} order={belowSort.order} onSort={(f) => setBelowSort(nextSort(belowSort, f))} />
                <th className="num nowrap">안전재고</th>
                <th className="num nowrap">부족 수량</th>
              </tr>
            </thead>
            <tbody>
              {below.data.map((s) => (
                <tr key={s.item.id}>
                  <td>{s.item.name}</td>
                  <td className="num nowrap">
                    {s.onHand.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    {s.item.safetyStock.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    {(s.item.safetyStock - s.onHand).toLocaleString("ko-KR")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      <h2>유효기한 임박 로트</h2>
      <div className="field">
        <label htmlFor="stock-days">기준</label>
        <select
          id="stock-days"
          value={days}
          onChange={(e) => setDays(Number(e.target.value))}
        >
          {[7, 14, 30, 60, 90].map((d) => (
            <option key={d} value={d}>
              {d}일 이내
            </option>
          ))}
        </select>
      </div>
      {expiring.error && <Notice tone="error">{expiring.error}</Notice>}
      {expiring.data && expiring.data.length === 0 && (
        <EmptyState>유효기한이 임박한 로트가 없습니다.</EmptyState>
      )}
      {expiring.data && expiring.data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <SortHeader label="품목" field="name" sort={expSort.sort} order={expSort.order} onSort={(f) => setExpSort(nextSort(expSort, f))} />
                <th>로트</th>
                <SortHeader label="유효기한" field="expiry_date" sort={expSort.sort} order={expSort.order} onSort={(f) => setExpSort(nextSort(expSort, f))} />
                <th className="nowrap">남은 기간</th>
                <th className="num nowrap">수량</th>
              </tr>
            </thead>
            <tbody>
              {expiring.data.map((e) => (
                <tr key={e.lot.lotId}>
                  <td>{e.item.name}</td>
                  <td>{e.lot.lotNo}</td>
                  <td className="nowrap">{e.lot.expiryDate}</td>
                  <td className="nowrap">
                    <Badge tone={e.daysToExpire <= 7 ? "bad" : "warn"}>
                      {expiryText(e.daysToExpire)}
                    </Badge>
                  </td>
                  <td className="num nowrap">
                    {e.lot.quantity.toLocaleString("ko-KR")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      <PermissionGate permission="inventory:manage">
        <MasterForms
          onCreated={() => {
            void items.reload();
            void below.reload();
          }}
        />
      </PermissionGate>
    </section>
  );
}

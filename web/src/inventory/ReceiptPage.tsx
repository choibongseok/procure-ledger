// 입고 화면: 발주를 고르고 줄마다 입고 수량·로트·유효기한을 넣는다(부분 입고 허용, 잔량 초과 불가).
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, LoadingRows, Notice, Pagination, TableWrap, useToast } from "../layout/ui";
import { PoStatusBadge } from "../purchase/badges";
import { won } from "../purchase/labels";
import type { Item, PurchaseOrder } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import { ReceiptHistory } from "./ReceiptHistory";

interface LineInput {
  qty: string;
  lotNo: string;
  expiry: string;
}

/** 입력 검사. 문제가 없으면 빈 문자열. remaining 은 줄별 잔량. */
export function validateReceipt(
  po: PurchaseOrder,
  inputs: Record<number, LineInput>,
  trackLot: (itemId: number) => boolean,
): string {
  let any = false;
  for (const l of po.lines) {
    const inp = inputs[l.id];
    const qty = Number(inp?.qty ?? "0");
    if (!inp || inp.qty.trim() === "" || qty === 0) continue;
    any = true;
    if (!Number.isInteger(qty) || qty < 0)
      return `${l.itemName}: 입고 수량은 1 이상의 정수여야 합니다.`;
    const remaining = l.quantity - l.receivedQty;
    if (qty > remaining)
      return `${l.itemName}: 남은 수량(${remaining})보다 많이 입고할 수 없습니다.`;
    if (trackLot(l.itemId) && !inp.lotNo.trim())
      return `${l.itemName}: 로트 번호를 입력하세요.`;
    if (inp.expiry && !/^\d{4}-\d{2}-\d{2}$/.test(inp.expiry))
      return `${l.itemName}: 유효기한은 연-월-일(예 2026-12-18) 형식으로 입력하세요.`;
  }
  return any ? "" : "입고할 수량을 한 줄 이상 입력하세요.";
}

function ReceiptForm({
  po,
  items,
  onStart,
  onDone,
}: {
  po: PurchaseOrder;
  items: Map<number, Item>;
  onStart: () => void;
  onDone: () => void;
}) {
  const [inputs, setInputs] = useState<Record<number, LineInput>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const whs = useLoad(
    () =>
      api.get<{ id: number; name: string; active: boolean }[]>(
        "/api/warehouses",
      ),
    [],
  );
  const [wh, setWh] = useState("");
  const trackLot = (id: number) => items.get(id)?.trackLot ?? false;
  const set = (id: number, patch: Partial<LineInput>) =>
    setInputs((cur) => {
      const base: LineInput = cur[id] ?? { qty: "", lotNo: "", expiry: "" };
      return { ...cur, [id]: { ...base, ...patch } };
    });

  async function submit() {
    const problem = validateReceipt(po, inputs, trackLot);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError("");
    onStart();
    try {
      const lines = po.lines
        .filter((l) => Number(inputs[l.id]?.qty) > 0)
        .map((l) => ({
          poLineId: l.id,
          itemId: l.itemId,
          quantity: Number(inputs[l.id].qty),
          lotNo: inputs[l.id].lotNo.trim(),
          expiryDate: inputs[l.id].expiry,
        }));
      await api.post("/api/receipts", {
        poId: po.id,
        lines,
        ...(wh ? { warehouseId: Number(wh) } : {}),
      });
      setInputs({});
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h3>
        {po.no} 입고 처리 <PoStatusBadge status={po.status} />
        {po.intercompany && <> <Badge tone="info">법인 간 거래({po.intercompany})</Badge></>}
      </h3>
      <label>
        입고 창고
        <select value={wh} onChange={(e) => setWh(e.target.value)}>
          <option value="">기본 창고</option>
          {(whs.data ?? [])
            .filter((w) => w.active)
            .map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
        </select>
      </label>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>품목</th>
              <th className="num nowrap">발주 수량</th>
              <th className="num nowrap">기입고</th>
              <th className="num nowrap">남은 수량</th>
              <th className="nowrap">이번 입고</th>
              <th className="nowrap">로트 번호</th>
              <th className="nowrap">유효기한</th>
            </tr>
          </thead>
          <tbody>
            {po.lines.map((l) => {
              const remaining = l.quantity - l.receivedQty;
              const lot = trackLot(l.itemId);
              return (
                <tr key={l.id}>
                  <td>{l.itemName}</td>
                  <td className="num nowrap">
                    {l.quantity.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    {l.receivedQty.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    {remaining.toLocaleString("ko-KR")}
                  </td>
                  <td>
                    <input
                      aria-label={`${l.itemName} 입고 수량`}
                      inputMode="numeric"
                      disabled={remaining === 0}
                      value={inputs[l.id]?.qty ?? ""}
                      onChange={(e) => set(l.id, { qty: e.target.value })}
                      size={6}
                    />
                  </td>
                  <td>
                    <input
                      aria-label={`${l.itemName} 로트 번호`}
                      disabled={!lot || remaining === 0}
                      placeholder={lot ? "필수" : "해당 없음"}
                      value={inputs[l.id]?.lotNo ?? ""}
                      onChange={(e) => set(l.id, { lotNo: e.target.value })}
                      size={10}
                    />
                  </td>
                  <td>
                    <input
                      aria-label={`${l.itemName} 유효기한`}
                      disabled={!lot || remaining === 0}
                      type="date"
                      value={inputs[l.id]?.expiry ?? ""}
                      onChange={(e) => set(l.id, { expiry: e.target.value })}
                    />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      <p>발주 금액 {won(po.totalAmount)}</p>
      {error && <Notice tone="error">{error}</Notice>}
      <button type="button" disabled={busy} onClick={() => void submit()}>
        입고 처리
      </button>
    </div>
  );
}

const PAGE_SIZE = 50;

export function ReceiptPage() {
  // 구매요청 상세의 "입고 화면으로" 링크가 ?po=발주ID 로 해당 발주를 미리 골라 준다.
  const [params] = useSearchParams();
  const preset = Number(params.get("po"));
  const [selected, setSelected] = useState<number | null>(Number.isInteger(preset) && preset > 0 ? preset : null);
  const [historyVersion, setHistoryVersion] = useState(0);
  // 성공 문구는 입력 칸(발주마다 따로 열림) 밖, 이 화면에 둔다: 마지막 잔량을 입고하면 그 발주가 목록에서 사라지며 입력 칸이 닫히는데,
  // 문구가 입력 칸 안에 있으면 처리 결과를 알리는 순간 함께 지워져 사용자(와 화면 시험)가 처리됐는지 알 수 없다.
  const toast = useToast();
  const items = useLoad(() => api.get<Item[]>("/api/items?limit=200"), []);
  // 입고가 남은 발주(발주 상태 open·partial)를 한 번에 쪽으로 나눠 부른다. 서버 순서(최신 발주 먼저)를 그대로 쓴다.
  const [offset, setOffset] = useState(0);
  const pageRes = useLoad(() => api.getPage<PurchaseOrder>(`/api/purchase-orders?status=open,partial&limit=${PAGE_SIZE}&offset=${offset}`), [offset]);
  const open = { data: pageRes.data?.items ?? null, loading: pageRes.loading, error: pageRes.error, reload: pageRes.reload };
  const total = pageRes.data?.total ?? 0;
  // 마지막 쪽의 발주를 모두 입고해 쪽이 비면 앞 쪽으로 돌아간다.
  useEffect(() => {
    if (pageRes.data && pageRes.data.items.length === 0 && offset > 0) setOffset(Math.max(0, offset - PAGE_SIZE));
  }, [pageRes.data, offset]);
  const itemMap = new Map((items.data ?? []).map((i) => [i.id, i]));
  const po = open.data?.find((p) => p.id === selected) ?? null;

  return (
    <section>
      <h1>입고</h1>
      <p>
        입고가 남은 발주입니다. 일부만 먼저 입고할 수 있고, 발주 수량을 넘는
        입고는 처리되지 않습니다.
      </p>
      <ExportButtons path="/api/purchase-orders/export" disabled={open.loading} />
      {(open.error || items.error) && (
        <Notice tone="error">{open.error || items.error}</Notice>
      )}
      {open.data && open.data.length === 0 && (
        <EmptyState>입고할 발주가 없습니다.</EmptyState>
      )}
      {((open.data && open.data.length > 0) || (open.loading && !open.data)) && (
        <TableWrap busy={open.loading}>
          <table>
            <thead>
              <tr>
                <th className="nowrap">발주 번호</th>
                <th className="nowrap">상태</th>
                <th className="num nowrap">금액</th>
              </tr>
            </thead>
            <tbody>
              {!open.data ? <LoadingRows cols={3} /> : open.data.map((p) => (
                <tr key={p.id}>
                  <td className="nowrap">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => {
                        setSelected(p.id);
                      }}
                    >
                      {p.no}
                    </button>
                  </td>
                  <td className="nowrap">
                    <PoStatusBadge status={p.status} />
                    {p.intercompany && <> <Badge tone="info">법인 간 거래({p.intercompany})</Badge></>}
                  </td>
                  <td className="num nowrap">{won(p.totalAmount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <Pagination offset={offset} limit={PAGE_SIZE} total={total} onChange={setOffset} />
      {po && (
        <ReceiptForm
          key={po.id}
          po={po}
          items={itemMap}
          onStart={() => undefined}
          onDone={() => {
            toast("입고를 처리했습니다.");
            void open.reload();
            setHistoryVersion((v) => v + 1); // 입고 처리 뒤 입고 이력 표도 다시 읽는다
          }}
        />
      )}
      <ReceiptHistory version={historyVersion} />
    </section>
  );
}

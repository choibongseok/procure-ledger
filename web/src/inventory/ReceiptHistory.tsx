// 입고 이력(B28): 최근 입고 줄과 내보내기. 입고 처리 화면 아래에 둔다.
import { useState } from "react";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import { useLoad } from "../purchase/useLoad";

interface HistoryRow {
  receiptNo: string;
  poNo: string;
  itemName: string;
  lotNo: string;
  expiryDate: string;
  quantity: number;
  warehouseName: string;
  receivedAt: string;
}

// version 이 바뀌면(입고 처리 직후) 같은 조건으로 다시 읽는다.
export function ReceiptHistory({ version = 0 }: { version?: number }) {
  const [draft, setDraft] = useState({ from: "", to: "" });
  const [applied, setApplied] = useState({ from: "", to: "" });
  const qs = new URLSearchParams(Object.entries(applied).filter(([, v]) => v)).toString();
  const list = useLoad(() => api.get<{ rows: HistoryRow[]; more: boolean }>(`/api/receipt-history${qs ? `?${qs}` : ""}`), [qs, version]);
  const rows = list.data?.rows ?? [];
  return (
    <div>
      <h2>입고 이력</h2>
      <div className="form-row">
        <div className="field">
          <label htmlFor="rh-from">입고일 시작</label>
          <input id="rh-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="rh-to">입고일 종료</label>
          <input id="rh-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <button type="button" className="btn-secondary" onClick={() => setApplied(draft)}>
          조회
        </button>
      </div>
      <ExportButtons path="/api/receipts/export" params={applied} disabled={list.loading || rows.length === 0} />
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>이 기간에 입고한 기록이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">입고 번호</th>
                <th className="nowrap">발주 번호</th>
                <th>품목</th>
                <th>로트 번호</th>
                <th className="nowrap">유효기한</th>
                <th className="num nowrap">수량</th>
                <th>창고</th>
                <th className="nowrap">입고일</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={`${r.receiptNo}-${i}`}>
                  <td className="nowrap">{r.receiptNo}</td>
                  <td className="nowrap">{r.poNo}</td>
                  <td>{r.itemName}</td>
                  <td>{r.lotNo || "—"}</td>
                  <td className="nowrap">{r.expiryDate || "—"}</td>
                  <td className="num nowrap">{r.quantity.toLocaleString("ko-KR")}</td>
                  <td>{r.warehouseName}</td>
                  <td className="nowrap">{dateTime(r.receivedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {list.data?.more && <p className="hint">최근 100줄만 보입니다. 전체는 내보내기로 받으세요.</p>}
    </div>
  );
}

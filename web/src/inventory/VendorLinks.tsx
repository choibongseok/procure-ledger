// 거래처의 연결 법인(B44 명세 15.1 5항): 거래처가 그룹의 다른 법인이면 그 법인을 연결해 둔다. 법인이 둘 이상일 때만 보인다.
// 사업자번호가 다른 법인과 같으면 그 법인을 제안한다(자동으로 연결하지 않는다). 사업자번호·계좌는 이 화면에 나오지 않는다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, LoadingRows, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface LinkRow {
  vendorId: number;
  name: string;
  linkedEntityId: number;
  linkedEntityName: string;
  suggestedEntityId: number;
  suggestedEntityName: string;
}

export function VendorLinks() {
  const { entities = [], currentEntity = null, can } = useAuth();
  const manage = can("inventory:manage");
  const res = useLoad(() => (entities.length >= 2 ? api.get<{ rows: LinkRow[] }>("/api/vendors/links") : Promise.resolve({ rows: [] as LinkRow[] })), [entities.length]);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  if (entities.length < 2) return null;
  const others = entities.filter((e) => e.id !== currentEntity?.id);
  const rows = res.data?.rows ?? [];

  async function set(row: LinkRow, entityId: number) {
    setBusy(true);
    setNote(null);
    try {
      await api.put(`/api/vendors/${row.vendorId}/link`, { entityId });
      setNote({ tone: "ok", text: entityId ? `${row.name} 을(를) 그룹 법인으로 연결했습니다.` : `${row.name} 의 연결을 해제했습니다.` });
      await res.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-labelledby="vlink-h">
      <h2 id="vlink-h">거래처의 연결 법인</h2>
      <p className="hint">거래처가 그룹의 다른 법인이면 연결해 두세요. 법인 간 거래를 알아보는 데 쓰입니다. 사업자번호가 다른 법인과 같으면 연결을 제안합니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {res.error && <Notice tone="error">{res.error}</Notice>}
      <TableWrap busy={res.loading}>
        <table className="stack-table" aria-label="거래처 연결 법인">
          <thead>
            <tr>
              <th>거래처</th>
              <th>연결 법인</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {res.loading && !res.data ? <LoadingRows cols={3} /> : null}
            {rows.map((r) => (
              <tr key={r.vendorId}>
                <td data-label="거래처">
                  {r.name} {r.suggestedEntityId > 0 && r.linkedEntityId === 0 && <Badge tone="info">{r.suggestedEntityName} 과(와) 사업자번호가 같습니다</Badge>}
                </td>
                <td data-label="연결 법인">
                  <select aria-label={`${r.name} 연결 법인`} value={r.linkedEntityId} disabled={!manage || busy} onChange={(e) => void set(r, Number(e.target.value))}>
                    <option value={0}>연결 없음</option>
                    {others.map((e) => (
                      <option key={e.id} value={e.id}>
                        {e.name}
                      </option>
                    ))}
                  </select>
                </td>
                <td>
                  {manage && r.suggestedEntityId > 0 && r.linkedEntityId !== r.suggestedEntityId && (
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void set(r, r.suggestedEntityId)} aria-label={`${r.name} 을(를) ${r.suggestedEntityName} 으로 연결`}>
                      제안대로 연결
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {res.data && rows.length === 0 && <EmptyState>등록된 거래처가 없습니다.</EmptyState>}
    </section>
  );
}

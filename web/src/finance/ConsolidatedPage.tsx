// 연결 재무 집계(B44 3단계, 11.2): 법인별 시산표·재무 요약·내부 거래 참고. 합산만 하고 상계·조정 분개는 없다.
import { useState } from "react";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { useLoad } from "../purchase/useLoad";

interface ConsEntity {
  id: number;
  code: string;
  name: string;
  closed: boolean;
  closedMonths: number;
  months: number;
}
interface ConsReport {
  from: string;
  to: string;
  entities: ConsEntity[];
  trial: { code: string; name: string; kind: string; amounts: number[]; total: number }[];
  summary: { kind: string; label: string; amounts: number[]; total: number }[];
  intercompany: {
    fromId: number;
    toId: number;
    fromName: string;
    toName: string;
    lines: { code: string; name: string; debit: number; credit: number }[];
    payable: number;
    receivable: number;
    gap: number;
    counterpartRecorded: boolean;
  }[];
  partialScope: boolean;
  pending: number;
}

const KIND_LABEL: Record<string, string> = { asset: "자산", liability: "부채", equity: "자본", revenue: "수익", expense: "비용" };
const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

function ClosedBadge({ e }: { e: ConsEntity }) {
  return e.closed ? <Badge tone="ok">마감</Badge> : <Badge tone="warn">마감 전</Badge>;
}

export function ConsolidatedPage() {
  const today = kstToday();
  const [draft, setDraft] = useState({ unit: "month", month: today.slice(0, 7), year: Number(today.slice(0, 4)), quarter: Math.floor((Number(today.slice(5, 7)) - 1) / 3) + 1 });
  const [applied, setApplied] = useState(draft);
  const params: Record<string, string> =
    applied.unit === "month"
      ? { unit: "month", month: applied.month }
      : applied.unit === "quarter"
        ? { unit: "quarter", year: String(applied.year), quarter: String(applied.quarter) }
        : { unit: "year", year: String(applied.year) };
  const qs = new URLSearchParams(params).toString();
  const rep = useLoad(() => api.get<ConsReport>(`/api/finance/consolidated?${qs}`), [qs]);
  const r = rep.data;
  return (
    <section>
      <h1>연결 재무 집계</h1>
      <p className="hint">
        법인별 시산표와 재무 요약을 한 화면에서 봅니다. 승인된 전표만 전기일 기준으로 더하며, 자산·부채·자본은 기간 끝까지의 잔액, 수익·비용은 기간 안의 발생액입니다. 내부 거래를 상계하거나 조정 분개를 만들지는 않습니다.
      </p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="cs-unit">집계 단위</label>
          <select id="cs-unit" value={draft.unit} onChange={(e) => setDraft({ ...draft, unit: e.target.value })}>
            <option value="month">월</option>
            <option value="quarter">분기</option>
            <option value="year">연</option>
          </select>
        </div>
        {draft.unit === "month" && (
          <div className="field">
            <label htmlFor="cs-month">월</label>
            <input id="cs-month" type="month" value={draft.month} onChange={(e) => setDraft({ ...draft, month: e.target.value })} />
          </div>
        )}
        {draft.unit !== "month" && (
          <div className="field">
            <label htmlFor="cs-year">연도</label>
            <input id="cs-year" type="number" value={draft.year} onChange={(e) => setDraft({ ...draft, year: Number(e.target.value) })} />
          </div>
        )}
        {draft.unit === "quarter" && (
          <div className="field">
            <label htmlFor="cs-q">분기</label>
            <select id="cs-q" value={draft.quarter} onChange={(e) => setDraft({ ...draft, quarter: Number(e.target.value) })}>
              <option value={1}>1분기(1~3월)</option>
              <option value={2}>2분기(4~6월)</option>
              <option value={3}>3분기(7~9월)</option>
              <option value={4}>4분기(10~12월)</option>
            </select>
          </div>
        )}
        <button type="button" onClick={() => setApplied(draft)}>
          조회
        </button>
      </div>
      {rep.error && <Notice tone="error">{rep.error}</Notice>}
      {rep.loading && <p>불러오는 중…</p>}
      {r && (
        <>
          <p>
            기간 {r.from} ~ {r.to}
          </p>
          {r.partialScope && <Notice tone="info">내가 볼 수 있는 법인만 합산했습니다. 일부 법인은 합계에 들어 있지 않습니다.</Notice>}
          {r.pending > 0 && <Notice tone="info">승인 대기 전표 {r.pending}건은 집계에서 뺐습니다.</Notice>}
          {r.entities.some((e) => !e.closed) && <Notice tone="warn">월 마감 전인 법인이 있습니다. 해당 법인 열은 이후 전표가 더해지면 값이 바뀔 수 있습니다.</Notice>}

          <h2>법인별 시산표</h2>
          <ExportButtons path="/api/finance/consolidated/export/trial" params={params} disabled={r.trial.length === 0} />
          {r.trial.length === 0 && <EmptyState>기간에 승인된 전표가 없습니다.</EmptyState>}
          {r.trial.length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="법인별 시산표">
                <thead>
                  <tr>
                    <th className="nowrap">계정 코드</th>
                    <th>계정</th>
                    <th className="nowrap">분류</th>
                    {r.entities.map((e) => (
                      <th key={e.id} className="num nowrap">
                        {e.name} <ClosedBadge e={e} />
                      </th>
                    ))}
                    <th className="num nowrap">합계</th>
                  </tr>
                </thead>
                <tbody>
                  {r.trial.map((a) => (
                    <tr key={a.code}>
                      <td data-label="계정 코드" className="nowrap">{a.code}</td>
                      <td data-label="계정">{a.name}</td>
                      <td data-label="분류" className="nowrap">{KIND_LABEL[a.kind] ?? a.kind}</td>
                      {r.entities.map((e, i) => (
                        <td key={e.id} data-label={e.name} className="num nowrap">{won(a.amounts[i] ?? 0)}</td>
                      ))}
                      <td data-label="합계" className="num nowrap">{won(a.total)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}

          <h2>재무 요약</h2>
          <TableWrap>
            <table className="stack-table" aria-label="재무 요약">
              <thead>
                <tr>
                  <th>분류</th>
                  {r.entities.map((e) => (
                    <th key={e.id} className="num nowrap">{e.name}</th>
                  ))}
                  <th className="num nowrap">합계</th>
                </tr>
              </thead>
              <tbody>
                {r.summary.map((s) => (
                  <tr key={s.kind}>
                    <td data-label="분류">{s.label}</td>
                    {r.entities.map((e, i) => (
                      <td key={e.id} data-label={e.name} className="num nowrap">{won(s.amounts[i] ?? 0)}</td>
                    ))}
                    <td data-label="합계" className="num nowrap">{won(s.total)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>

          <h2>내부 거래 참고</h2>
          <Notice tone="info">참고: 내부 거래가 합계에 포함되어 있습니다. 이 표의 금액은 합계에 반영하거나 상계하지 않았고, 양쪽 금액 차이는 보여 드리기만 합니다.</Notice>
          <ExportButtons path="/api/finance/consolidated/export/intercompany" params={params} disabled={r.intercompany.length === 0} />
          {r.intercompany.length === 0 && <EmptyState>기간에 연결 거래처와의 거래가 없습니다.</EmptyState>}
          {r.intercompany.map((p) => (
            <div key={`${p.fromId}-${p.toId}`}>
              <h3>
                {p.fromName} → {p.toName}
              </h3>
              <TableWrap>
                <table className="stack-table" aria-label={`내부 거래 ${p.fromName} → ${p.toName}`}>
                  <thead>
                    <tr>
                      <th className="nowrap">계정 코드</th>
                      <th>계정</th>
                      <th className="num nowrap">차변</th>
                      <th className="num nowrap">대변</th>
                    </tr>
                  </thead>
                  <tbody>
                    {p.lines.map((l) => (
                      <tr key={l.code}>
                        <td data-label="계정 코드" className="nowrap">{l.code}</td>
                        <td data-label="계정">{l.name}</td>
                        <td data-label="차변" className="num nowrap">{won(l.debit)}</td>
                        <td data-label="대변" className="num nowrap">{won(l.credit)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <p className="hint">
                {p.fromName}의 채무 {won(p.payable)} · {p.toName}이(가) 기록한 채권 {won(p.receivable)} · 차이 {won(p.gap)}
                {!p.counterpartRecorded && " (상대 법인에는 이쪽을 거래처로 기록한 전표가 없습니다)"}
              </p>
            </div>
          ))}
        </>
      )}
    </section>
  );
}

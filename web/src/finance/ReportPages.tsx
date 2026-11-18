// 매입 채무·계정별 원장·시산표(B31 3.5·3.6). 승인된 전표만 센다. 금액은 쉼표와 "원", 잔액은 계정의 정상 방향 부호다.
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import type { Account, LedgerReport, PayableItem, PayableReport, TrialReport } from "./types";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);
const monthStart = () => `${kstToday().slice(0, 8)}01`;

// ---- 매입 채무 ----

export function PayablesPage() {
  const { can } = useAuth();
  const manage = can("account:manage");
  const [asOf, setAsOf] = useState(kstToday());
  const [applied, setApplied] = useState(asOf);
  const [tick, setTick] = useState(0);
  const rep = useLoad(() => api.get<PayableReport>(`/api/finance/payables?asOf=${applied}`), [applied, tick]);
  const [open, setOpen] = useState(0);
  const items = useLoad(() => (open ? api.get<PayableItem[]>(`/api/finance/payables/${open}?asOf=${applied}`) : Promise.resolve([] as PayableItem[])), [open, applied, tick]);
  const [days, setDays] = useState<Record<number, string>>({});
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);

  async function saveDays(vendorId: number) {
    setMsg(null);
    try {
      await api.put(`/api/finance/vendors/${vendorId}/payment-days`, { days: Number(days[vendorId]) });
      setMsg({ tone: "ok", text: "결제 조건을 바꿨습니다. 이미 만들어진 채무의 만기는 그대로이고 새 매입 전표부터 적용됩니다." });
      setDays((d) => ({ ...d, [vendorId]: "" }));
      setTick((t) => t + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }

  const r = rep.data;
  return (
    <section>
      <h1>매입 채무</h1>
      <p className="hint">승인된 매입 전표에서 생긴 외상매입금입니다. 만기는 거래일에 거래처 결제 조건(기본 30일)을 더해 정합니다.</p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="pay-asof">기준일</label>
          <input id="pay-asof" type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
        </div>
        <button type="button" onClick={() => setApplied(asOf || kstToday())}>
          조회
        </button>
      </div>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {rep.error && <Notice tone="error">{rep.error}</Notice>}
      {rep.loading && <p>불러오는 중…</p>}
      {r && r.rows.length === 0 && <EmptyState>미지급 채무가 없습니다.</EmptyState>}
      {r && r.rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>거래처</th>
                <th className="num nowrap">건수</th>
                <th className="num nowrap">미지급 잔액</th>
                <th className="num nowrap">지급 예정</th>
                <th className="nowrap">가장 이른 만기</th>
                <th className="num nowrap">만기 경과 금액</th>
                <th className="nowrap">결제 조건</th>
              </tr>
            </thead>
            <tbody>
              {r.rows.map((x) => (
                <tr key={x.vendorId}>
                  <td data-label="거래처">
                    <button type="button" className="btn-secondary" onClick={() => setOpen(open === x.vendorId ? 0 : x.vendorId)}>
                      {x.vendorName}
                    </button>
                    {x.intercompany && <> <Badge tone="info">법인 간 거래({x.intercompany})</Badge></>}
                  </td>
                  <td data-label="건수" className="num nowrap">{x.count}</td>
                  <td data-label="미지급 잔액" className="num nowrap">{won(x.outstanding)}</td>
                  <td data-label="지급 예정" className="num nowrap">{won(x.reserved)}</td>
                  <td data-label="가장 이른 만기" className="nowrap">{x.earliestDue}</td>
                  <td data-label="만기 경과 금액" className="num nowrap">{x.overdueAmount > 0 ? <Badge tone="bad">{won(x.overdueAmount)}</Badge> : won(0)}</td>
                  <td data-label="결제 조건" className="nowrap">
                    {x.paymentDays}일
                    {manage && (
                      <>
                        {" "}
                        <input
                          aria-label={`${x.vendorName} 결제 조건(일)`}
                          type="number"
                          min={0}
                          max={365}
                          style={{ width: 70 }}
                          value={days[x.vendorId] ?? ""}
                          onChange={(e) => setDays({ ...days, [x.vendorId]: e.target.value })}
                        />{" "}
                        <button type="button" className="btn-secondary" disabled={!days[x.vendorId]} onClick={() => void saveDays(x.vendorId)}>
                          변경
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {r && (
        <p>
          합계 {won(r.total)} ·{" "}
          {r.matches ? <Badge tone="ok">계정별 원장 외상매입금 잔액과 일치</Badge> : <Badge tone="bad">계정별 원장 잔액 {won(r.ledgerBalance)}과 다릅니다. 관리자에게 알리세요</Badge>}
        </p>
      )}
      {open > 0 && (items.data ?? []).length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="채무 항목">
            <thead>
              <tr>
                <th className="nowrap">전표 번호</th>
                <th className="nowrap">발생일</th>
                <th className="nowrap">만기일</th>
                <th className="num nowrap">금액</th>
                <th className="num nowrap">지급 누계</th>
                <th className="num nowrap">미지급</th>
                <th className="num nowrap">경과일</th>
              </tr>
            </thead>
            <tbody>
              {(items.data ?? []).map((it) => (
                <tr key={it.id}>
                  <td data-label="전표 번호" className="nowrap">
                    <Link to={`/finance/journals/${it.journalNo}`}>{it.journalNo}</Link>
                  </td>
                  <td data-label="발생일" className="nowrap">{it.tradeDate}</td>
                  <td data-label="만기일" className="nowrap">{it.dueDate}</td>
                  <td data-label="금액" className="num nowrap">{won(it.amount)}</td>
                  <td data-label="지급 누계" className="num nowrap">{won(it.paid)}</td>
                  <td data-label="미지급" className="num nowrap">{won(it.outstanding)}</td>
                  <td data-label="경과일" className="num nowrap">{it.overdueDays > 0 ? `${it.overdueDays}일` : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

// ---- 계정별 원장 ----

export function LedgerPage() {
  const [sp, setSp] = useSearchParams();
  const accounts = useLoad(() => api.get<Account[]>("/api/finance/accounts?all=1"), []);
  const [draft, setDraft] = useState({ account: sp.get("account") ?? "", from: sp.get("from") ?? monthStart(), to: sp.get("to") ?? kstToday() });
  const q = new URLSearchParams(Object.entries({ account: sp.get("account") ?? "", from: sp.get("from") ?? "", to: sp.get("to") ?? "" }).filter(([, v]) => v) as [string, string][]);
  const rep = useLoad(() => (sp.get("account") ? api.get<LedgerReport>(`/api/finance/ledger?${q.toString()}`) : Promise.resolve(null)), [q.toString()]);
  const l = rep.data;
  return (
    <section>
      <h1>계정별 원장</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="lg-acc">계정과목</label>
          <select id="lg-acc" value={draft.account} onChange={(e) => setDraft({ ...draft, account: e.target.value })}>
            <option value="">계정을 고르세요</option>
            {(accounts.data ?? []).map((a) => (
              <option key={a.code} value={a.code}>
                {a.code} {a.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="lg-from">시작일</label>
          <input id="lg-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="lg-to">종료일</label>
          <input id="lg-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <button type="button" disabled={!draft.account} onClick={() => setSp(Object.fromEntries(Object.entries(draft).filter(([, v]) => v)))}>
          조회
        </button>
        <ExportButtons path="/api/finance/ledger/export" params={{ account: sp.get("account") ?? "", from: sp.get("from") ?? "", to: sp.get("to") ?? "" }} disabled={!l} />
      </div>
      {!sp.get("account") && <EmptyState>계정과목을 고르고 조회하세요.</EmptyState>}
      {rep.error && <Notice tone="error">{rep.error}</Notice>}
      {l && (
        <>
          {l.excludedCount > 0 && <Notice tone="info">승인 대기 {l.excludedCount}건은 제외했습니다. 승인된 전표만 원장에 들어갑니다.</Notice>}
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th className="nowrap">전기일</th>
                  <th className="nowrap">전표 번호</th>
                  <th>적요</th>
                  <th>상대 계정</th>
                  <th className="num nowrap">차변</th>
                  <th className="num nowrap">대변</th>
                  <th className="num nowrap">잔액</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td data-label="전기일" className="nowrap">{l.from}</td>
                  <td data-label="전표 번호">이월 잔액</td>
                  <td data-label="적요">—</td>
                  <td data-label="상대 계정">—</td>
                  <td data-label="차변" className="num">—</td>
                  <td data-label="대변" className="num">—</td>
                  <td data-label="잔액" className="num nowrap">{won(l.opening)}</td>
                </tr>
                {l.rows.map((x, i) => (
                  <tr key={`${x.journalNo}-${i}`}>
                    <td data-label="전기일" className="nowrap">{x.postingDate}</td>
                    <td data-label="전표 번호" className="nowrap">
                      <Link to={`/finance/journals/${x.journalNo}`}>{x.journalNo}</Link>
                    </td>
                    <td data-label="적요">{x.memo}</td>
                    <td data-label="상대 계정">{x.counter}</td>
                    <td data-label="차변" className="num nowrap">{x.debit ? won(x.debit) : "—"}</td>
                    <td data-label="대변" className="num nowrap">{x.credit ? won(x.credit) : "—"}</td>
                    <td data-label="잔액" className="num nowrap">{won(x.balance)}</td>
                  </tr>
                ))}
                <tr>
                  <td data-label="전기일">—</td>
                  <td data-label="전표 번호">합계</td>
                  <td data-label="적요">—</td>
                  <td data-label="상대 계정">—</td>
                  <td data-label="차변" className="num nowrap">{won(l.totalDebit)}</td>
                  <td data-label="대변" className="num nowrap">{won(l.totalCredit)}</td>
                  <td data-label="잔액" className="num nowrap">{won(l.closing)}</td>
                </tr>
              </tbody>
            </table>
          </TableWrap>
          {l.rows.length === 0 && <p className="hint">이 기간에는 승인된 전표가 없습니다.</p>}
        </>
      )}
    </section>
  );
}

// ---- 시산표 ----

export function TrialBalancePage() {
  // 시산표는 회계연도 단위로 보는 표라 기본 시작일을 서버가 이번 회계연도 시작일로 정한다(시작일이 비면 서버 기본).
  const [draft, setDraft] = useState({ from: "", to: kstToday(), includeZero: false });
  const [applied, setApplied] = useState(draft);
  const qs = `from=${applied.from}&to=${applied.to}${applied.includeZero ? "&includeZero=1" : ""}`;
  const rep = useLoad(() => api.get<TrialReport>(`/api/finance/trial-balance?${qs}`), [qs]);
  const t = rep.data;
  type FyRange = { thisFrom?: string; thisTo?: string; lastFrom?: string; lastTo?: string };
  const fy = useLoad<FyRange>(() => api.get<FyRange>("/api/company/fiscal").catch(() => ({}) as FyRange), []);
  const fyd: FyRange = fy.data ?? {};
  const pick = (from: string, to: string) => {
    const next = { ...draft, from, to };
    setDraft(next);
    setApplied(next);
  };
  const shownFrom = draft.from || t?.from || "";
  return (
    <section>
      <h1>시산표</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="tb-from">시작일</label>
          <input id="tb-from" type="date" value={shownFrom} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="tb-to">종료일</label>
          <input id="tb-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <label>
          <input type="checkbox" checked={draft.includeZero} onChange={(e) => setDraft({ ...draft, includeZero: e.target.checked })} /> 0 잔액 계정 포함
        </label>
        <button type="button" onClick={() => setApplied({ ...draft, from: draft.from })}>
          조회
        </button>
        {fyd.thisFrom && fyd.thisTo && (
          <button type="button" className="btn-secondary" onClick={() => pick(fyd.thisFrom as string, fyd.thisTo as string < kstToday() ? (fyd.thisTo as string) : kstToday())}>
            이번 회계연도
          </button>
        )}
        {fyd.lastFrom && fyd.lastTo && (
          <button type="button" className="btn-secondary" onClick={() => pick(fyd.lastFrom as string, fyd.lastTo as string)}>
            지난 회계연도
          </button>
        )}
        <ExportButtons path="/api/finance/trial-balance/export" params={{ from: applied.from, to: applied.to, includeZero: applied.includeZero ? "1" : "" }} disabled={!t} />
      </div>
      {rep.error && <Notice tone="error">{rep.error}</Notice>}
      {rep.loading && <p>불러오는 중…</p>}
      {t && (
        <>
          {t.excludedCount > 0 && <Notice tone="info">승인 대기 {t.excludedCount}건은 제외했습니다. 승인된 전표만 시산표에 들어갑니다.</Notice>}
          {t.rows.length === 0 && <EmptyState>이 달에는 승인된 전표가 없습니다.</EmptyState>}
          {t.rows.length > 0 && (
            <TableWrap>
              <table className="stack-table">
                <thead>
                  <tr>
                    <th className="nowrap">계정 코드</th>
                    <th>계정</th>
                    <th className="nowrap">구분</th>
                    <th className="num nowrap">기초 잔액</th>
                    <th className="num nowrap">차변</th>
                    <th className="num nowrap">대변</th>
                    <th className="num nowrap">기말 잔액</th>
                  </tr>
                </thead>
                <tbody>
                  {t.rows.map((x) => (
                    <tr key={x.code}>
                      <td data-label="계정 코드" className="nowrap">
                        <Link to={`/finance/ledger?account=${x.code}&from=${t.from}&to=${t.to}`}>{x.code}</Link>
                      </td>
                      <td data-label="계정">{x.name}</td>
                      <td data-label="구분" className="nowrap">{x.kindLabel}</td>
                      <td data-label="기초 잔액" className="num nowrap">{won(x.opening)}</td>
                      <td data-label="차변" className="num nowrap">{won(x.debit)}</td>
                      <td data-label="대변" className="num nowrap">{won(x.credit)}</td>
                      <td data-label="기말 잔액" className="num nowrap">{won(x.closing)}</td>
                    </tr>
                  ))}
                  <tr>
                    <td data-label="계정 코드">—</td>
                    <td data-label="계정">합계</td>
                    <td data-label="구분">—</td>
                    <td data-label="기초 잔액" className="num">—</td>
                    <td data-label="차변" className="num nowrap">{won(t.totalDebit)}</td>
                    <td data-label="대변" className="num nowrap">{won(t.totalCredit)}</td>
                    <td data-label="기말 잔액" className="num">—</td>
                  </tr>
                </tbody>
              </table>
            </TableWrap>
          )}
          <p>
            {t.balanced ? <Badge tone="ok">차변·대변 합계 일치</Badge> : <Badge tone="bad">차대가 일치하지 않습니다. 관리자에게 알리세요</Badge>}
          </p>
        </>
      )}
    </section>
  );
}

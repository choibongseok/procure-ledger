// 자금 계좌(입금 기록 포함)·자금 일보·지급 일정표(B32 3.2). 계좌번호는 끝 4자리만 보이고 원문 보기는 매번 감사에 남는다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import type { Account } from "./types";
import type { BankAccount, Calendar, FundDaily } from "./payTypes";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);
const monthStart = () => `${kstToday().slice(0, 8)}01`;

export function BankAccountsPage() {
  const { can } = useAuth();
  const manage = can("fund:manage");
  const [tick, setTick] = useState(0);
  const banks = useLoad(() => api.get<BankAccount[]>("/api/finance/bank-accounts?all=1"), [tick]);
  const accounts = useLoad(() => api.get<Account[]>("/api/finance/accounts"), []);
  const [form, setForm] = useState({ alias: "", bankName: "", accountNo: "", ledgerCode: "1020", openingBalance: "0", openingDate: kstToday() });
  const [dep, setDep] = useState({ bankAccountId: 0, date: kstToday(), amount: "", counterCode: "4910", memo: "" });
  const [revealed, setRevealed] = useState<Record<number, string>>({});
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);

  async function run(fn: () => Promise<string>) {
    setMsg(null);
    try {
      setMsg({ tone: "ok", text: await fn() });
      setTick((t) => t + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }
  async function reveal(id: number) {
    setMsg(null);
    try {
      const r = await api.post<{ accountNo: string }>(`/api/finance/bank-accounts/${id}/reveal?screen=account`);
      setRevealed((m) => ({ ...m, [id]: r.accountNo }));
      window.setTimeout(() => setRevealed((m) => ({ ...m, [id]: "" })), 30000);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }

  const rows = banks.data ?? [];
  return (
    <section>
      <h1>자금 계좌</h1>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {banks.error && <Notice tone="error">{banks.error}</Notice>}
      {banks.data && rows.length === 0 && <EmptyState>등록된 자금 계좌가 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>별칭</th>
                <th>은행</th>
                <th>계좌번호</th>
                <th>연결 계정</th>
                <th className="num nowrap">오늘 잔액</th>
                <th className="nowrap">상태</th>
                <th>관리</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((b) => (
                <tr key={b.id}>
                  <td data-label="별칭">{b.alias}</td>
                  <td data-label="은행">{b.bankName}</td>
                  <td data-label="계좌번호" className="nowrap">
                    {revealed[b.id] || b.accountNo}{" "}
                    {can("mask:bypass") && !revealed[b.id] && (
                      <button type="button" className="btn-secondary" onClick={() => void reveal(b.id)}>
                        원문 보기
                      </button>
                    )}
                  </td>
                  <td data-label="연결 계정">
                    {b.ledgerCode} {b.ledgerName}
                  </td>
                  <td data-label="오늘 잔액" className="num nowrap">{won(b.balance)}</td>
                  <td data-label="상태" className="nowrap">
                    <Badge tone={b.active ? "ok" : "muted"}>{b.active ? "사용 중" : "중지"}</Badge>
                  </td>
                  <td data-label="관리">
                    {manage && (
                      <button
                        type="button"
                        className="btn-secondary"
                        onClick={() => void run(async () => (await api.post(`/api/finance/bank-accounts/${b.id}/active`, { active: !b.active }), b.active ? "계좌를 중지했습니다." : "계좌를 다시 쓰게 했습니다."))}
                      >
                        {b.active ? "사용 중지" : "사용 재개"}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {manage && (
        <>
          <h2>계좌 추가</h2>
          <p className="hint">기초 잔액과 기준일은 입력한 뒤 바꿀 수 없습니다. 정정은 입금·출금 기록으로 합니다.</p>
          <div className="form-row">
            <div className="field">
              <label htmlFor="ba-alias">별칭</label>
              <input id="ba-alias" maxLength={40} value={form.alias} onChange={(e) => setForm({ ...form, alias: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ba-bank">은행 이름</label>
              <input id="ba-bank" maxLength={40} value={form.bankName} onChange={(e) => setForm({ ...form, bankName: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ba-no">계좌번호</label>
              <input id="ba-no" autoComplete="off" value={form.accountNo} onChange={(e) => setForm({ ...form, accountNo: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ba-code">연결 계정</label>
              <select id="ba-code" value={form.ledgerCode} onChange={(e) => setForm({ ...form, ledgerCode: e.target.value })}>
                <option value="1020">1020 보통예금</option>
                <option value="1010">1010 현금</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="ba-open">기초 잔액(원)</label>
              <input id="ba-open" type="number" min={0} value={form.openingBalance} onChange={(e) => setForm({ ...form, openingBalance: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ba-date">기초 기준일</label>
              <input id="ba-date" type="date" value={form.openingDate} onChange={(e) => setForm({ ...form, openingDate: e.target.value })} />
            </div>
            <button
              type="button"
              onClick={() =>
                void run(async () => {
                  await api.post("/api/finance/bank-accounts", { ...form, openingBalance: Number(form.openingBalance) });
                  setForm({ ...form, alias: "", bankName: "", accountNo: "" });
                  return "계좌를 추가했습니다.";
                })
              }
            >
              계좌 추가
            </button>
          </div>
        </>
      )}
      {can("ledger:write") && can("fund:manage") && rows.some((b) => b.active) && (
        <>
          <h2>입금 기록</h2>
          <p className="hint">입금을 기록하면 임시 입금 전표가 만들어집니다. 전표가 승인되어야 잔액에 들어갑니다.</p>
          <div className="form-row">
            <div className="field">
              <label htmlFor="dp-bank">입금 계좌</label>
              <select id="dp-bank" value={dep.bankAccountId} onChange={(e) => setDep({ ...dep, bankAccountId: Number(e.target.value) })}>
                <option value={0}>계좌를 고르세요</option>
                {rows
                  .filter((b) => b.active)
                  .map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.alias}
                    </option>
                  ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="dp-date">입금일</label>
              <input id="dp-date" type="date" max={kstToday()} value={dep.date} onChange={(e) => setDep({ ...dep, date: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="dp-amount">금액(원)</label>
              <input id="dp-amount" type="number" min={1} value={dep.amount} onChange={(e) => setDep({ ...dep, amount: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="dp-counter">상대 계정</label>
              <select id="dp-counter" value={dep.counterCode} onChange={(e) => setDep({ ...dep, counterCode: e.target.value })}>
                {(accounts.data ?? [])
                  .filter((a) => a.active && (a.kind === "revenue" || a.kind === "liability" || a.kind === "asset") && a.code !== "1020" && a.code !== "1010")
                  .map((a) => (
                    <option key={a.code} value={a.code}>
                      {a.code} {a.name}
                    </option>
                  ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="dp-memo">적요</label>
              <input id="dp-memo" maxLength={200} value={dep.memo} onChange={(e) => setDep({ ...dep, memo: e.target.value })} />
            </div>
            <button
              type="button"
              disabled={!dep.bankAccountId || !dep.amount || !dep.memo}
              onClick={() =>
                void run(async () => {
                  const j = await api.post<{ no: string }>("/api/finance/deposits", { ...dep, amount: Number(dep.amount) });
                  setDep({ ...dep, amount: "", memo: "" });
                  return `입금을 기록했습니다. 임시 전표 ${j.no} 을(를) 승인 요청하세요.`;
                })
              }
            >
              입금 기록
            </button>
          </div>
        </>
      )}
    </section>
  );
}

export function FundDailyPage() {
  const banks = useLoad(() => api.get<BankAccount[]>("/api/finance/bank-accounts?all=1"), []);
  const [draft, setDraft] = useState({ from: monthStart(), to: kstToday(), bankAccountId: 0 });
  const [applied, setApplied] = useState(draft);
  const qs = `from=${applied.from}&to=${applied.to}${applied.bankAccountId ? `&bankAccountId=${applied.bankAccountId}` : ""}`;
  const rep = useLoad(() => api.get<FundDaily>(`/api/finance/fund-daily?${qs}`), [qs]);
  const d = rep.data;
  return (
    <section>
      <h1>자금 일보</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="fd-from">시작일</label>
          <input id="fd-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="fd-to">종료일</label>
          <input id="fd-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="fd-bank">계좌</label>
          <select id="fd-bank" value={draft.bankAccountId} onChange={(e) => setDraft({ ...draft, bankAccountId: Number(e.target.value) })}>
            <option value={0}>전체</option>
            {(banks.data ?? []).map((b) => (
              <option key={b.id} value={b.id}>
                {b.alias}
              </option>
            ))}
          </select>
        </div>
        <button type="button" onClick={() => setApplied(draft)}>
          조회
        </button>
        <ExportButtons path="/api/finance/fund-daily/export" params={{ from: applied.from, to: applied.to, bankAccountId: applied.bankAccountId || "" }} disabled={!d} />
      </div>
      {rep.error && <Notice tone="error">{rep.error}</Notice>}
      {rep.loading && <p>불러오는 중…</p>}
      {d && (
        <>
          {d.pendingCount > 0 && <Notice tone="info">전표 승인 전 입금이 있는 날이 {d.pendingCount}일 있습니다. 승인 전 입금은 표시만 하고 잔액에 넣지 않았습니다.</Notice>}
          <p>
            이월 잔액 {won(d.opening)} · 기간 입금 {won(d.totalDeposit)} · 기간 출금 {won(d.totalWithdraw)} · 기말 잔액 {won(d.closing)}
          </p>
          {d.rows.length === 0 && <EmptyState>이 기간에는 입출금이 없습니다.</EmptyState>}
          {d.rows.length > 0 && (
            <TableWrap>
              <table className="stack-table">
                <thead>
                  <tr>
                    <th className="nowrap">일자</th>
                    <th>계좌</th>
                    <th className="num nowrap">기초</th>
                    <th className="num nowrap">입금</th>
                    <th className="num nowrap">출금</th>
                    <th className="num nowrap">기말</th>
                    <th className="num nowrap">승인 전 입금</th>
                  </tr>
                </thead>
                <tbody>
                  {d.rows.map((x) => (
                    <tr key={`${x.date}-${x.bankAccountId}`}>
                      <td data-label="일자" className="nowrap">{x.date}</td>
                      <td data-label="계좌">{x.bankAlias}</td>
                      <td data-label="기초" className="num nowrap">{won(x.opening)}</td>
                      <td data-label="입금" className="num nowrap">{won(x.deposit)}</td>
                      <td data-label="출금" className="num nowrap">{won(x.withdraw)}</td>
                      <td data-label="기말" className="num nowrap">{won(x.closing)}</td>
                      <td data-label="승인 전 입금" className="num nowrap">{x.pendingDeposit ? won(x.pendingDeposit) : "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
          {d.movements.length > 0 && (
            <>
              <h2>입출금 내역</h2>
              <TableWrap>
                <table className="stack-table" aria-label="입출금 내역">
                  <thead>
                    <tr>
                      <th className="nowrap">일자</th>
                      <th>계좌</th>
                      <th className="nowrap">구분</th>
                      <th className="num nowrap">금액</th>
                      <th>지급 건·거래처</th>
                      <th>적요</th>
                    </tr>
                  </thead>
                  <tbody>
                    {d.movements.map((m, i) => (
                      <tr key={i}>
                        <td data-label="일자" className="nowrap">{m.date}</td>
                        <td data-label="계좌">{m.bankAlias}</td>
                        <td data-label="구분" className="nowrap">
                          {m.kindLabel}
                          {m.pending && <> <Badge tone="warn">전표 승인 전</Badge></>}
                        </td>
                        <td data-label="금액" className="num nowrap">{won(m.amount)}</td>
                        <td data-label="지급 건·거래처">
                          {m.paymentNo ? <Link to={`/finance/payments/${m.paymentNo}`}>{m.paymentNo}</Link> : "—"} {m.vendorName}
                        </td>
                        <td data-label="적요">{m.memo}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </>
          )}
          <p>{d.matches ? <Badge tone="ok">총계정원장 보통예금 잔액과 일치</Badge> : <Badge tone="bad">총계정원장 보통예금 잔액 {won(d.ledgerBalance)}과 다릅니다(승인 전 입금 전표·기초 잔액 기준일을 확인하세요)</Badge>}</p>
        </>
      )}
    </section>
  );
}

const KIND_LABEL: Record<string, string> = { due: "만기 도래", payment: "지급", requested: "승인 요청 중" };

export function PaymentCalendarPage() {
  const [mode, setMode] = useState<"week" | "month">("week");
  const [from, setFrom] = useState(kstToday());
  const to = (() => {
    const t = new Date(`${from}T00:00:00Z`);
    t.setUTCDate(t.getUTCDate() + (mode === "week" ? 6 : 30));
    return t.toISOString().slice(0, 10);
  })();
  const cal = useLoad(() => api.get<Calendar>(`/api/finance/payment-calendar?from=${from}&to=${to}`), [from, to]);
  const c = cal.data;
  const byDate: Record<string, Calendar["entries"]> = {};
  for (const e of c?.entries ?? []) (byDate[e.date] ??= []).push(e);
  const dates = Object.keys(byDate).sort();
  return (
    <section>
      <h1>지급 일정표</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="pc-from">시작일</label>
          <input id="pc-from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="pc-mode">보기</label>
          <select id="pc-mode" value={mode} onChange={(e) => setMode(e.target.value as "week" | "month")}>
            <option value="week">주간(7일)</option>
            <option value="month">월간(31일)</option>
          </select>
        </div>
      </div>
      {cal.error && <Notice tone="error">{cal.error}</Notice>}
      {c && c.overdueTotal > 0 && <Notice tone="error">만기가 지난 미지급 채무가 {won(c.overdueTotal)} 있습니다.</Notice>}
      {c && c.shortageWarn && (
        <Notice tone="info">
          이 기간 지급 예정({won(c.scheduledTotal)})이 현재 잔액({won(c.balanceTotal)})보다 많습니다.
        </Notice>
      )}
      {c && dates.length === 0 && <EmptyState>이 기간에는 지급 예정이 없습니다.</EmptyState>}
      {dates.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">날짜</th>
                <th className="nowrap">구분</th>
                <th>거래처</th>
                <th className="num nowrap">금액</th>
                <th>지급 건</th>
              </tr>
            </thead>
            <tbody>
              {dates.flatMap((dt) =>
                byDate[dt].map((e, i) => (
                  <tr key={`${dt}-${i}`}>
                    <td data-label="날짜" className="nowrap">{dt}</td>
                    <td data-label="구분" className="nowrap">
                      {KIND_LABEL[e.kind] ?? e.kind}
                      {e.statusLabel && <> <Badge tone={e.status === "paid" ? "ok" : "info"}>{e.statusLabel}</Badge></>}
                      {e.delayed && <> <Badge tone="bad">지연</Badge></>}
                      {e.nextBusinessDay && <div className="hint">휴일: 다음 영업일({e.nextBusinessDay.slice(5).replace("-", "/")})에 지급</div>}
                    </td>
                    <td data-label="거래처">{e.vendorName}</td>
                    <td data-label="금액" className="num nowrap">{won(e.amount)}</td>
                    <td data-label="지급 건">{e.paymentNo ? <Link to={`/finance/payments/${e.paymentNo}`}>{e.paymentNo}</Link> : "—"}</td>
                  </tr>
                )),
              )}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

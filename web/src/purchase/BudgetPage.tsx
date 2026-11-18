// 예산 현황(B25 ①): 부서·월 예산 표와 사용률 배지, 재무의 예산 등록·수정, 수정 이력.
// 잔액 = 월 예산 − 사용액(발주까지 끝난 건) − 결재 진행 중(상신·결재 중). 모든 금액은 공급가액(부가세 별도)이다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap, useToast, type Tone } from "../layout/ui";
import { monthText } from "./BudgetCheckCard";
import { dateTime, won } from "./labels";
import type { BudgetRevision, BudgetRow } from "./types";
import { errorText, useLoad } from "./useLoad";

const BADGE: Record<BudgetRow["badge"], { text: string; tone: Tone }> = {
  ok: { text: "여유", tone: "ok" },
  near: { text: "한도 근접", tone: "warn" },
  over: { text: "예산 초과", tone: "bad" },
};

const thisMonth = () => {
  const d = new Date(Date.now() + 9 * 3600 * 1000); // KST
  return d.toISOString().slice(0, 7);
};

/** 이번 달부터 12개월(지난 달은 고칠 수 없다). */
function editableMonths(): string[] {
  const base = new Date(`${thisMonth()}-01T00:00:00Z`);
  return Array.from({ length: 12 }, (_, i) => {
    const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth() + i, 1));
    return d.toISOString().slice(0, 7);
  });
}

interface Dept {
  id: number;
  name: string;
}

/** 연·월 두 칸 선택(브라우저 기본 월 입력은 영문으로 보여서 쓰지 않는다). */
function MonthPicker({ idPrefix, value, onChange }: { idPrefix: string; value: string; onChange: (m: string) => void }) {
  const [y, m] = value.split("-");
  const cur = Number(thisMonth().slice(0, 4));
  const years = Array.from({ length: 6 }, (_, i) => cur - 3 + i);
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    <span style={{ display: "flex", gap: "0.5rem" }}>
      <select id={`${idPrefix}-year`} style={{ width: "auto" }} aria-label="조회 연도" value={y} onChange={(e) => onChange(`${e.target.value}-${m}`)}>
        {years.map((yy) => (
          <option key={yy} value={yy}>
            {yy}년
          </option>
        ))}
      </select>
      <select id={`${idPrefix}-month`} style={{ width: "auto" }} aria-label="조회 월" value={m} onChange={(e) => onChange(`${y}-${e.target.value}`)}>
        {Array.from({ length: 12 }, (_, i) => (
          <option key={i} value={pad(i + 1)}>
            {i + 1}월
          </option>
        ))}
      </select>
    </span>
  );
}

function EditForm({ month, onSaved }: { month: string; onSaved: (month: string) => void }) {
  const depts = useLoad(() => api.get<Dept[]>("/api/stock-issues/departments"), []);
  const [dept, setDept] = useState("");
  const [applyMonth, setApplyMonth] = useState(month < thisMonth() ? thisMonth() : month);
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState("");
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  async function save() {
    setErr("");
    if (!dept) return setErr("부서를 선택하세요.");
    const digits = amount.replace(/[,\s]/g, "");
    if (!/^\d+$/.test(digits)) return setErr("월 예산은 0 이상의 숫자로 입력하세요. 쉼표(,)는 넣어도 됩니다.");
    if (!reason.trim()) return setErr("예산을 바꾸는 사유를 입력하세요.");
    setBusy(true);
    try {
      await api.put("/api/budgets", { departmentId: Number(dept), month: applyMonth, amount: Number(digits), reason });
      toast(`${monthText(applyMonth)} 예산을 저장했습니다.`);
      setAmount("");
      setReason("");
      onSaved(applyMonth);
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-label="예산 등록·수정">
      <h2>예산 등록·수정</h2>
      <p className="hint">이번 달 이후의 월 예산만 등록·수정할 수 있고, 지우는 기능은 없습니다. 0원으로 두면 그 달의 모든 구매요청이 예산 초과로 판정됩니다. 이미 결재 중인 건은 다시 판정하지 않습니다.</p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="bd-dept">부서</label>
          <select id="bd-dept" value={dept} onChange={(e) => setDept(e.target.value)}>
            <option value="">선택하세요</option>
            {depts.data?.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="bd-month">적용 월</label>
          <select id="bd-month" value={applyMonth} onChange={(e) => setApplyMonth(e.target.value)}>
            {editableMonths().map((m) => (
              <option key={m} value={m}>
                {monthText(m)}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="bd-amount">월 예산 (원)</label>
          <input id="bd-amount" inputMode="numeric" value={amount} onChange={(e) => setAmount(e.target.value)} />
        </div>
      </div>
      <div className="field">
        <label htmlFor="bd-reason">변경 사유</label>
        <input id="bd-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} />
      </div>
      {err && <Notice tone="error">{err}</Notice>}
      <button type="button" disabled={busy} onClick={() => void save()}>
        저장
      </button>
    </section>
  );
}

export function BudgetPage() {
  const { can } = useAuth();
  const [month, setMonth] = useState(thisMonth());
  const rows = useLoad(() => api.get<BudgetRow[]>(`/api/budgets?month=${month}`), [month]);
  const revs = useLoad(() => api.get<BudgetRevision[]>(`/api/budgets/revisions?month=${month}`), [month]);

  return (
    <section>
      <h1>예산 현황</h1>
      <p className="hint">잔액은 월 예산에서 사용액과 결재 진행 중 금액을 뺀 값입니다. 예산 월은 상신한 날이 속한 달이고, 모든 금액은 공급가액(부가세 별도)입니다.</p>
      <div className="field">
        <label htmlFor="bd-view-year">조회 연·월</label>
        <MonthPicker idPrefix="bd-view" value={month} onChange={setMonth} />
      </div>
      <ExportButtons path="/api/budgets/export" params={{ month }} disabled={rows.loading || !rows.data || rows.data.length === 0} />
      {rows.error && <Notice tone="error">{rows.error}</Notice>}
      {rows.data && rows.data.length === 0 && <EmptyState hint="재무 담당자가 아래에서 부서별 월 예산을 등록합니다.">{monthText(month)}에 등록된 예산이 없습니다.</EmptyState>}
      {rows.data && rows.data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>부서</th>
                <th className="num nowrap">월 예산</th>
                <th className="num nowrap">사용액</th>
                <th className="num nowrap">결재 진행 중</th>
                <th className="num nowrap">잔액</th>
                <th className="num nowrap">사용률</th>
                <th className="nowrap">상태</th>
              </tr>
            </thead>
            <tbody>
              {rows.data.map((r) => (
                <tr key={r.departmentId}>
                  <td>{r.departmentName}</td>
                  <td className="num nowrap">{won(r.amount)}</td>
                  <td className="num nowrap">{won(r.used)}</td>
                  <td className="num nowrap">{won(r.inProgress)}</td>
                  <td className="num nowrap">{won(r.balance)}</td>
                  <td className="num nowrap">{r.usagePct < 0 ? "—" : `${r.usagePct}%`}</td>
                  <td className="nowrap">
                    <Badge tone={BADGE[r.badge].tone}>{BADGE[r.badge].text}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {can("budget:manage") && <EditForm month={month} onSaved={(m) => (m === month ? (void rows.reload(), void revs.reload()) : setMonth(m))} />}
      <h2>수정 이력</h2>
      {revs.error && <Notice tone="error">{revs.error}</Notice>}
      {revs.data && revs.data.length === 0 && <EmptyState>{monthText(month)} 예산의 수정 이력이 없습니다.</EmptyState>}
      {revs.data && revs.data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">일시</th>
                <th>부서</th>
                <th className="num nowrap">이전 금액</th>
                <th className="num nowrap">변경 금액</th>
                <th>사유</th>
                <th className="nowrap">처리자</th>
              </tr>
            </thead>
            <tbody>
              {revs.data.map((r, i) => (
                <tr key={i}>
                  <td className="nowrap">{dateTime(r.at)}</td>
                  <td>{r.departmentName}</td>
                  <td className="num nowrap">{r.oldAmount === undefined ? "신규 등록" : won(r.oldAmount)}</td>
                  <td className="num nowrap">{won(r.newAmount)}</td>
                  <td>{r.reason}</td>
                  <td className="nowrap">{r.actorName}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

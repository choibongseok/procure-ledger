// 구매 실적 보고서(B28 7.1): 부서 × 예산 월의 사용액·결재 진행 중·예산 대비 집행률. 계산은 서버가 한다.
import { useState } from "react";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { won } from "../purchase/labels";
import { useLoad } from "../purchase/useLoad";
import { PctBar, ReportTabs, kstToday, monthRangeProblem, wrapReport } from "./reportUi";

interface Row {
  departmentId: number;
  departmentName: string;
  month: string;
  budget: number | null;
  orders: number;
  used: number;
  inProgress: number;
  balance: number | null;
  executionPct: number | null;
  forecastPct: number | null;
  badge: "ok" | "near" | "over" | "none";
}
interface Report {
  from?: string;
  to?: string;
  rows: Row[];
  totals: { orders: number; used: number; inProgress: number; budget: number; budgetedUsed: number; executionPct: number | null; unbudgetedUsed: number };
}
interface Options {
  departments: { id: number; name: string }[];
  forms: { code: string; name: string }[];
}

const BADGE: Record<Row["badge"], { text: string; tone: Tone }> = {
  ok: { text: "여유", tone: "ok" },
  near: { text: "한도 근접", tone: "warn" },
  over: { text: "예산 초과", tone: "bad" },
  none: { text: "예산 없음", tone: "muted" },
};

const thisMonth = () => kstToday().slice(0, 7);

export function PurchaseReportPage() {
  const [draft, setDraft] = useState({ from: "", to: thisMonth(), departmentId: "" });
  const [applied, setApplied] = useState(draft);
  const [problem, setProblem] = useState("");
  const options = useLoad(() => api.get<Options>("/api/reports/options"), []);
  const qs = new URLSearchParams({ from: applied.from, to: applied.to, ...(applied.departmentId ? { departmentId: applied.departmentId } : {}) }).toString();
  const report = useLoad(() => wrapReport(api.get<Report>(`/api/reports/purchase?${qs}`)), [qs]);
  const rows = report.data?.rows ?? [];
  const t = report.data?.totals;

  function search() {
    const from = draft.from || report.data?.from || ""; // 비어 있으면 서버가 정한 기본 시작 월(이번 회계연도 시작 월)을 쓴다
    const p = monthRangeProblem(from, draft.to);
    setProblem(p);
    if (!p) setApplied({ ...draft, from });
  }

  return (
    <section>
      <h1>보고서</h1>
      <ReportTabs />
      <details className="report-basis">
        <summary>계산 기준</summary>
        <p className="hint">
          행은 부서 × 예산 월입니다. 예산 월은 구매요청을 상신한 날이 속한 달이고, 모든 금액은 공급가액(부가세 별도)입니다. 사용액은 발주가 만들어진(결재 완료) 구매요청의 합, 결재 진행 중은 상신~결재 중인 요청의 합이며
          임시저장·반려·회수한 건은 빠집니다. 집행률은 사용액 ÷ 월 예산, 소진 예상은 (사용액 + 결재 진행 중) ÷ 월 예산입니다. 상태는 소진 예상이 80% 미만이면 여유, 100%까지는 한도 근접, 넘으면 예산 초과입니다.
          예산이 등록되지 않은 부서·월은 집행률이 비고 “예산 없음”으로 보입니다. 내가 열람할 수 있는 구매요청만 집계하므로 사람에 따라 숫자가 다를 수 있습니다.
        </p>
      </details>
      <div className="form-row">
        <div className="field">
          <label htmlFor="rp-from">시작 월</label>
          <input id="rp-from" type="month" value={draft.from || report.data?.from || ""} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="rp-to">종료 월</label>
          <input id="rp-to" type="month" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="rp-dept">부서</label>
          <select id="rp-dept" value={draft.departmentId} onChange={(e) => setDraft({ ...draft, departmentId: e.target.value })}>
            <option value="">전체</option>
            {(options.data?.departments ?? []).map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </div>
        <button type="button" onClick={search}>
          조회
        </button>
      </div>
      <ExportButtons path="/api/reports/purchase/export" params={{ from: applied.from, to: applied.to, departmentId: applied.departmentId }} disabled={report.loading || rows.length === 0} />
      {problem && <Notice tone="error">{problem}</Notice>}
      {report.error && <Notice tone="error">{report.error}</Notice>}
      {report.loading && <p>불러오는 중…</p>}
      {report.data && rows.length === 0 && <EmptyState>이 기간에 집계할 구매가 없습니다. 기간이나 부서를 바꿔 다시 조회하세요.</EmptyState>}
      {report.data && rows.length > 0 && t && (
        <>
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th>부서</th>
                  <th className="nowrap">월</th>
                  <th className="num nowrap">월 예산</th>
                  <th className="num nowrap">구매 건수</th>
                  <th className="num nowrap">사용액</th>
                  <th className="num nowrap">결재 진행 중</th>
                  <th className="num nowrap">잔액</th>
                  <th className="nowrap">집행률</th>
                  <th className="num nowrap">소진 예상</th>
                  <th className="nowrap">상태</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={`${r.departmentId}-${r.month}`}>
                    <td data-label="부서">{r.departmentName}</td>
                    <td data-label="월" className="nowrap">{r.month}</td>
                    <td data-label="월 예산" className="num nowrap">{r.budget === null ? "" : won(r.budget)}</td>
                    <td data-label="구매 건수" className="num nowrap">{r.orders}</td>
                    <td data-label="사용액" className="num nowrap">{won(r.used)}</td>
                    <td data-label="결재 진행 중" className="num nowrap">{won(r.inProgress)}</td>
                    <td data-label="잔액" className="num nowrap">{r.balance === null ? "" : won(r.balance)}</td>
                    <td data-label="집행률" className="nowrap">
                      <PctBar pct={r.executionPct} tone={r.badge === "over" ? "bad" : r.badge === "near" ? "warn" : "ok"} />
                    </td>
                    <td data-label="소진 예상" className="num nowrap">{r.forecastPct === null ? "" : `${r.forecastPct.toFixed(1)}%`}</td>
                    <td data-label="상태" className="nowrap">
                      <Badge tone={BADGE[r.badge].tone}>{BADGE[r.badge].text}</Badge>
                    </td>
                  </tr>
                ))}
                <tr className="report-total">
                  <td data-label="부서">합계</td>
                  <td data-label="월" />
                  <td data-label="월 예산" className="num nowrap">{won(t.budget)}</td>
                  <td data-label="구매 건수" className="num nowrap">{t.orders}</td>
                  <td data-label="사용액" className="num nowrap">{won(t.used)}</td>
                  <td data-label="결재 진행 중" className="num nowrap">{won(t.inProgress)}</td>
                  <td data-label="잔액" />
                  <td data-label="집행률" className="nowrap">
                    <PctBar pct={t.executionPct} tone="ok" />
                  </td>
                  <td data-label="소진 예상" />
                  <td data-label="상태" />
                </tr>
              </tbody>
            </table>
          </TableWrap>
          {t.unbudgetedUsed > 0 && <p className="hint">예산이 등록되지 않은 부서·월의 사용액 {won(t.unbudgetedUsed)}은 집행률에서 제외했습니다.</p>}
        </>
      )}
    </section>
  );
}

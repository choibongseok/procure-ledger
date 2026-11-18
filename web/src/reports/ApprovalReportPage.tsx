// 결재 소요 시간 보고서(B28 7.3): 양식·단계별 평균·중앙값·최대와 지금 지연되는 건. 주말·공휴일을 빼지 않은 달력 시간이다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import { useLoad } from "../purchase/useLoad";
import { ReportTabs, dayRangeProblem, formatMinutes, kstDaysAgo, kstToday, wrapReport } from "./reportUi";

interface Stat {
  formCode: string;
  formName: string;
  stepTitle: string;
  count: number;
  avgMinutes: number;
  medMinutes: number;
  maxMinutes: number;
  rejected: number;
  objected: number;
  formTotal: boolean;
}
interface Delayed {
  docId: number;
  docNo: string;
  title: string;
  formName: string;
  stepTitle: string;
  approverName: string;
  noApprover: boolean;
  elapsedMinutes: number;
  stepStart: string;
  link: string;
}
interface Report {
  stats: Stat[];
  delayed: Delayed[];
  excluded: number;
}
interface Options {
  departments: { id: number; name: string }[];
  forms: { code: string; name: string }[];
}

export function ApprovalReportPage() {
  const [draft, setDraft] = useState({ from: kstDaysAgo(30), to: kstToday(), formCode: "", departmentId: "" });
  const [applied, setApplied] = useState(draft);
  const [problem, setProblem] = useState("");
  const options = useLoad(() => api.get<Options>("/api/reports/options"), []);
  const params = { from: applied.from, to: applied.to, formCode: applied.formCode, departmentId: applied.departmentId };
  const qs = new URLSearchParams(Object.entries(params).filter(([, v]) => v) as [string, string][]).toString();
  const report = useLoad(() => wrapReport(api.get<Report>(`/api/reports/approvals?${qs}`)), [qs]);
  const stats = report.data?.stats ?? [];
  const delayed = report.data?.delayed ?? [];

  function search() {
    const p = dayRangeProblem(draft.from, draft.to);
    setProblem(p);
    if (!p) setApplied(draft);
  }

  return (
    <section>
      <h1>보고서</h1>
      <ReportTabs />
      <details className="report-basis">
        <summary>계산 기준</summary>
        <p className="hint">
          주말·공휴일을 빼지 않은 실제 경과 시간입니다. 단계 시작은 그 단계가 내 차례가 된 때(첫 단계는 상신 시각, 이후 단계는 직전 단계의 처리 시각)이고 소요는 처리 시각까지입니다. 병렬 합의는 같은 시작 시각을 씁니다.
          재상신하면 새 차수가 새 상신 시각에서 시작합니다. 전결로 건너뛴 단계, 취소·회수, 결재자가 없어 멈춘 시간은 단계 소요에 넣지 않습니다. 전체 소요는 상신부터 결재 완료까지입니다.
          대결·위임·결재자 재지정은 소요를 바꾸지 않습니다. 기간은 단계가 처리된 날 기준이고, 내가 열람할 수 있는 문서만 집계합니다.
        </p>
      </details>
      <div className="form-row">
        <div className="field">
          <label htmlFor="ar-from">시작일</label>
          <input id="ar-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="ar-to">종료일</label>
          <input id="ar-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="ar-form">양식</label>
          <select id="ar-form" value={draft.formCode} onChange={(e) => setDraft({ ...draft, formCode: e.target.value })}>
            <option value="">전체</option>
            {(options.data?.forms ?? []).map((f) => (
              <option key={f.code} value={f.code}>
                {f.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="ar-dept">기안자 부서</label>
          <select id="ar-dept" value={draft.departmentId} onChange={(e) => setDraft({ ...draft, departmentId: e.target.value })}>
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
      <ExportButtons path="/api/reports/approvals/export" params={params} disabled={report.loading || stats.length === 0} />
      {problem && <Notice tone="error">{problem}</Notice>}
      {report.error && <Notice tone="error">{report.error}</Notice>}
      {report.loading && <p>불러오는 중…</p>}
      {report.data && stats.length === 0 && <EmptyState>이 기간에 처리된 결재가 없습니다.</EmptyState>}
      {stats.length > 0 && (
        <>
          <h2>양식·단계별 소요</h2>
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th>양식</th>
                  <th>단계</th>
                  <th className="num nowrap">처리 건수</th>
                  <th className="num nowrap">평균 소요</th>
                  <th className="num nowrap">중앙값 소요</th>
                  <th className="num nowrap">최대 소요</th>
                  <th className="num nowrap">반려</th>
                  <th className="num nowrap">이견</th>
                </tr>
              </thead>
              <tbody>
                {stats.map((s) => (
                  <tr key={`${s.formCode}-${s.stepTitle}`} className={s.formTotal ? "report-total" : undefined}>
                    <td data-label="양식">{s.formName}</td>
                    <td data-label="단계">{s.formTotal ? "전체(상신~결재 완료)" : s.stepTitle}</td>
                    <td data-label="처리 건수" className="num nowrap">{s.count}</td>
                    <td data-label="평균 소요" className="num nowrap">{formatMinutes(s.avgMinutes)}</td>
                    <td data-label="중앙값 소요" className="num nowrap">{formatMinutes(s.medMinutes)}</td>
                    <td data-label="최대 소요" className="num nowrap">{formatMinutes(s.maxMinutes)}</td>
                    <td data-label="반려" className="num nowrap">{s.formTotal ? "—" : s.rejected}</td>
                    <td data-label="이견" className="num nowrap">{s.formTotal ? "—" : s.objected}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          {report.data && report.data.excluded > 0 && <p className="hint">시작 시각을 알 수 없어 집계에서 제외한 건 {report.data.excluded}건</p>}
        </>
      )}
      <h2>지연 상위 건</h2>
      <p className="hint">지금 기준입니다. 위의 기간과 관계없이 진행 중인 문서의 현재 단계가 시작된 뒤 지난 시간이 긴 순서로 20건까지 보입니다.</p>
      {report.data && delayed.length === 0 && <EmptyState>지금 진행 중인 결재가 없습니다.</EmptyState>}
      {delayed.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">문서 번호</th>
                <th>제목</th>
                <th>양식</th>
                <th className="nowrap">현재 단계</th>
                <th className="nowrap">현재 결재자</th>
                <th className="num nowrap">경과 시간</th>
                <th className="nowrap">단계 시작</th>
              </tr>
            </thead>
            <tbody>
              {delayed.map((d) => (
                <tr key={d.docId}>
                  <td data-label="문서 번호" className="nowrap">
                    <Link to={d.link === "purchase" ? `/purchase-requests/${d.docId}` : `/docs/${d.docId}`}>{d.docNo.startsWith("TMP-") ? "번호 없음" : d.docNo}</Link>
                  </td>
                  <td data-label="제목">{d.title}</td>
                  <td data-label="양식">{d.formName}</td>
                  <td data-label="현재 단계" className="nowrap">{d.stepTitle}</td>
                  <td data-label="현재 결재자" className="nowrap">{d.noApprover ? "결재자 없음" : d.approverName}</td>
                  <td data-label="경과 시간" className="num nowrap">{formatMinutes(d.elapsedMinutes)}</td>
                  <td data-label="단계 시작" className="nowrap">{dateTime(d.stepStart)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

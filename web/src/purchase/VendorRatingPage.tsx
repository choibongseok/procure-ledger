// 거래처 평가(B25 ④): 납기 준수율과 증빙 판정 불일치 비율을 기간으로 조회한다(재무·관리자·감사).
// 점수는 조회 때 계산하고 구매 흐름에는 영향이 없다. 납기를 넘긴 미입고는 점수에 넣지 않고 건수로 따로 보인다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { ReportTabs } from "../reports/reportUi";
import { Badge, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText } from "./useLoad";

interface Rating {
  vendorId: number;
  vendorName: string;
  orders: number;
  onTime: number;
  withDue: number;
  noDue: number;
  onTimePct: number;
  onTimeBadge: RBadge;
  verdicts: number;
  mismatches: number;
  mismatchPct: number;
  mismatchBadge: RBadge;
  lateOpen: number;
  purchaseOrders?: number; // 보고서 탭에서만
  orderAmount?: number;
}
type RBadge = "good" | "warn" | "bad" | "small" | "none";
interface Late {
  poNo: string;
  dueDate: string;
  receivedOn: string;
  daysLate: number;
}
interface Detail extends Rating {
  late: Late[];
  openLate: Late[];
  mismatched: { verdictId: number; documentId: number; poNo: string; confirmedOn: string }[];
}

const TEXT: Record<RBadge, { text: string; tone: Tone }> = {
  good: { text: "양호", tone: "ok" },
  warn: { text: "주의", tone: "warn" },
  bad: { text: "나쁨", tone: "bad" },
  small: { text: "표본 적음", tone: "muted" },
  none: { text: "—", tone: "muted" },
};

function ratingBadge(kind: "ontime" | "mismatch", b: RBadge) {
  if (b === "none") return <span>—</span>;
  const t = b === "bad" ? (kind === "ontime" ? "지연 많음" : "불일치 많음") : TEXT[b].text;
  return <Badge tone={TEXT[b].tone}>{t}</Badge>;
}

const LOAD_FAILED = "거래처 평가를 불러오지 못했습니다. 기간을 확인하고 다시 조회하세요.";
const day = (d: Date) => new Date(d.getTime() + 9 * 3600 * 1000).toISOString().slice(0, 10);

/** report 가 참이면 보고서 탭 "거래처별 실적·평가"다: 같은 계산·같은 표에 발주 건수·발주 금액 열을 더한다(B28 5.0). */
export function VendorRatingPage({ report = false }: { report?: boolean } = {}) {
  const [to, setTo] = useState(day(new Date()));
  const [from, setFrom] = useState(day(new Date(Date.now() - 90 * 86400000)));
  const [rows, setRows] = useState<Rating[] | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [applied, setApplied] = useState({ from: "", to: "" });

  // 보고서 탭은 다른 탭처럼 처음 열 때 기본 기간으로 바로 조회한다(조회 전 빈 화면 방지).
  useEffect(() => {
    if (report) void search();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function search() {
    setErr("");
    setDetail(null);
    if (from > to) {
      setRows(null);
      return setErr("종료일은 시작일 이후여야 합니다.");
    }
    setBusy(true);
    try {
      setRows(await api.get<Rating[]>(`${report ? "/api/reports/vendors" : "/api/vendors/rating"}?from=${from}&to=${to}`));
      setApplied({ from, to });
    } catch (e) {
      setRows(null);
      setErr(LOAD_FAILED);
    } finally {
      setBusy(false);
    }
  }

  async function open(id: number) {
    setErr("");
    try {
      setDetail(await api.get<Detail>(`/api/vendors/${id}/rating?from=${from}&to=${to}`));
    } catch (e) {
      setErr(errorText(e));
    }
  }

  return (
    <section>
      <h1>{report ? "보고서" : "거래처 평가"}</h1>
      {report ? <ReportTabs /> : (
        <p className="hint">
          이 화면은 보고서의 <Link to="/reports/vendors">거래처별 실적·평가</Link> 탭에서도 볼 수 있고, 거기에는 발주 건수와 발주 금액이 더 있습니다.
        </p>
      )}
      <p className="hint">
        납기 준수율은 입고 화면의 입고 기록에서, 불일치 비율은 문서 검증의 판정에서 계산합니다. 이 기간에 입고가 끝난 발주와 재무가 확정한 판정만 셉니다. 확정 전 판정은 제외합니다.
        납기 당일 입고는 납기 내로 봅니다. 증빙 판정이 0건인 거래처는 불일치 열에 &apos;-&apos; 로 표시하고 비율은 계산하지 않습니다. 분모가 3건 미만이면 “표본 적음”으로 보입니다. 납기 준수율이 90% 이상이면 양호, 70% 이상이면 주의, 그 아래면 지연 많음이고, 불일치 비율이 10% 이하면 양호입니다.
        입고가 끝난 발주가 없어도 납기를 넘긴 미입고 발주가 있는 거래처는 표에 나오며, 이때 점수는 “—”입니다.
      </p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="vr-from">시작일</label>
          <input id="vr-from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="vr-to">종료일</label>
          <input id="vr-to" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
      </div>
      <button type="button" disabled={busy} onClick={() => void search()}>
        조회
      </button>
      {report && <ExportButtons path="/api/reports/vendors/export" params={applied} disabled={busy || !rows || rows.length === 0} />}
      {err && <Notice tone="error">{err}</Notice>}
      {rows && rows.length === 0 && <EmptyState>이 기간에 발주한 거래처가 없습니다.</EmptyState>}
      {rows && rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>거래처</th>
                {report && <th className="num nowrap">발주 건수</th>}
                {report && <th className="num nowrap">발주 금액</th>}
                <th className="num nowrap">발주(입고 완료)</th>
                <th className="num nowrap">납기 내 입고</th>
                <th className="num nowrap">납기 준수율</th>
                <th className="nowrap">납기 평가</th>
                <th className="num nowrap">증빙 판정</th>
                <th className="num nowrap">불일치</th>
                <th className="num nowrap">불일치 비율</th>
                <th className="nowrap">판정 평가</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.vendorId}>
                  <td data-label="거래처">
                    <button type="button" className="btn-link" onClick={() => void open(r.vendorId)}>
                      {r.vendorName}
                    </button>
                    {r.noDue > 0 && <div className="hint">납기 미지정 {r.noDue}건 제외</div>}
                    {r.lateOpen > 0 && <div className="hint">납기를 넘긴 미입고 {r.lateOpen}건</div>}
                  </td>
                  {report && <td data-label="발주 건수" className="num nowrap">{r.purchaseOrders ?? 0}</td>}
                  {report && <td data-label="발주 금액" className="num nowrap">{(r.orderAmount ?? 0).toLocaleString("ko-KR")}원</td>}
                  <td data-label="발주(입고 완료)" className="num nowrap">{r.orders}</td>
                  <td data-label="납기 내 입고" className="num nowrap">
                    {r.onTime}/{r.withDue}
                  </td>
                  <td data-label="납기 준수율" className="num nowrap">{r.onTimePct < 0 ? "—" : `${r.onTimePct}%`}</td>
                  <td data-label="납기 평가" className="nowrap">{ratingBadge("ontime", r.onTimeBadge)}</td>
                  <td data-label="증빙 판정" className="num nowrap">{r.verdicts}</td>
                  <td data-label="불일치" className="num nowrap">{r.verdicts === 0 ? "-" : r.mismatches}</td>
                  <td data-label="불일치 비율" className="num nowrap">{r.verdicts === 0 || r.mismatchPct < 0 ? "-" : `${r.mismatchPct}%`}</td>
                  <td data-label="판정 평가" className="nowrap">{ratingBadge("mismatch", r.mismatchBadge)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {detail && (
        <section className="card" aria-label={`${detail.vendorName} 상세`}>
          <h2>{detail.vendorName}</h2>
          <h3>납기를 넘겨 입고된 발주</h3>
          {detail.late.length === 0 ? (
            <p>이 기간에 납기를 넘겨 입고된 발주가 없습니다.</p>
          ) : (
            <ul>
              {detail.late.map((l) => (
                <li key={l.poNo}>
                  {l.poNo}: 납기 {l.dueDate}, 입고 {l.receivedOn} ({l.daysLate}일 늦음)
                </li>
              ))}
            </ul>
          )}
          <h3>납기를 넘긴 미입고</h3>
          {detail.openLate.length === 0 ? (
            <p>납기를 넘긴 미입고 발주가 없습니다.</p>
          ) : (
            <ul>
              {detail.openLate.map((l) => (
                <li key={l.poNo}>
                  {l.poNo}: 납기 {l.dueDate}, 아직 입고되지 않음 ({l.daysLate}일 지남)
                </li>
              ))}
            </ul>
          )}
          <h3>불일치로 확정된 증빙 판정</h3>
          {detail.mismatched.length === 0 ? (
            <p>이 기간에 불일치로 확정된 판정이 없습니다.</p>
          ) : (
            <ul>
              {detail.mismatched.map((m) => (
                <li key={m.verdictId}>
                  {m.poNo}: 확정일 {m.confirmedOn} · <Link to="/documents">문서 검증 화면에서 확인</Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
    </section>
  );
}

// 부가세 매입 집계(B32 3.4): 승인된 전표만, 거래일(공급 시기) 기준. 신고 서식 전송은 하지 않고 엑셀·CSV 내보내기까지다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { useLoad } from "../purchase/useLoad";
import { hasSiteChoice, useSites } from "./useSites";

interface VatReport {
  from: string;
  to: string;
  vendors: { vendorId: number; vendorName: string; businessNo: string; count: number; supply: number; vat: number }[];
  unreceived: { journalNo: string; vendorName: string; supply: number; vat: number; tradeDate: string }[];
  unlinked?: { invoiceNo: string; approvalNo: string; supplier: string; issueDate: string; supply: number; vat: number; state: string }[];
  diffs?: { invoiceNo: string; journalNos: string; issueDate: string; invoiceVat: number; journalVat: number; diff: number }[];
  cards: { kind: string; reason: string; reasonLabel: string; count: number; supply: number; vat: number; total: number }[];
  vendorVat: number;
  cardDeductVat: number;
  deductibleVat: number;
  unreceivedVat: number;
  ledgerVat: number;
  difference: number;
  matches: boolean;
  hints: string[];
  excludedCount: number;
  /** 사업장이 둘 이상인 법인에서만 온다(B44 14.2). */
  sites?: { siteId: number; siteNo: string; name: string; kind: string; vatMode: string; count: number; vat: number }[];
  siteId?: number;
  siteName?: string;
  siteScoped?: boolean;
  consolidatedIn?: string[];
}

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

export function VatPurchasePage() {
  const today = kstToday();
  const [draft, setDraft] = useState({ unit: "month", month: today.slice(0, 7), year: Number(today.slice(0, 4)), quarter: Math.floor((Number(today.slice(5, 7)) - 1) / 3) + 1, from: `${today.slice(0, 4)}-01-01`, to: today, siteId: 0 });
  const [applied, setApplied] = useState(draft);
  const params: Record<string, string> =
    applied.unit === "month" ? { unit: "month", month: applied.month } : applied.unit === "quarter" ? { unit: "quarter", year: String(applied.year), quarter: String(applied.quarter) } : { unit: "custom", from: applied.from, to: applied.to };
  const siteChoices = useSites();
  if (applied.siteId) params.siteId = String(applied.siteId);
  const qs = new URLSearchParams(params).toString();
  const rep = useLoad(() => api.get<VatReport>(`/api/finance/vat-purchase?${qs}`), [qs]);
  const r = rep.data;
  return (
    <section>
      <h1>부가세 매입 집계</h1>
      <p className="hint">승인된 전표만 세며 기준은 거래일(공급 시기)입니다. 장부 전기일과 달이 다를 수 있습니다. 신고 서식은 전송하지 않고 엑셀·CSV 로 내려받습니다.</p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="vp-unit">집계 단위</label>
          <select id="vp-unit" value={draft.unit} onChange={(e) => setDraft({ ...draft, unit: e.target.value })}>
            <option value="month">월</option>
            <option value="quarter">분기</option>
            <option value="custom">사용자 지정(12개월까지)</option>
          </select>
        </div>
        {draft.unit === "month" && (
          <div className="field">
            <label htmlFor="vp-month">월</label>
            <input id="vp-month" type="month" value={draft.month} onChange={(e) => setDraft({ ...draft, month: e.target.value })} />
          </div>
        )}
        {draft.unit === "quarter" && (
          <>
            <div className="field">
              <label htmlFor="vp-year">연도</label>
              <input id="vp-year" type="number" value={draft.year} onChange={(e) => setDraft({ ...draft, year: Number(e.target.value) })} />
            </div>
            <div className="field">
              <label htmlFor="vp-q">분기</label>
              <select id="vp-q" value={draft.quarter} onChange={(e) => setDraft({ ...draft, quarter: Number(e.target.value) })}>
                <option value={1}>1분기(1~3월)</option>
                <option value={2}>2분기(4~6월)</option>
                <option value={3}>3분기(7~9월)</option>
                <option value={4}>4분기(10~12월)</option>
              </select>
            </div>
          </>
        )}
        {draft.unit === "custom" && (
          <>
            <div className="field">
              <label htmlFor="vp-from">시작일</label>
              <input id="vp-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="vp-to">종료일</label>
              <input id="vp-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
            </div>
          </>
        )}
        {hasSiteChoice(siteChoices) && (
          <div className="field">
            <label htmlFor="vp-site">사업장</label>
            <select id="vp-site" value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: Number(e.target.value) })}>
              <option value={0}>법인 전체</option>
              {siteChoices.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                  {s.kind === "head" ? "(주사업장)" : s.vatMode === "consolidated" ? "(총괄납부)" : ""}
                </option>
              ))}
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
          {r.excludedCount > 0 && <Notice tone="info">승인 대기 {r.excludedCount}건은 제외했습니다. 승인된 전표만 집계합니다.</Notice>}
          <p>
            기간 {r.from} ~ {r.to}
            {r.siteScoped && r.siteName && ` · 사업장 ${r.siteName}`}
          </p>
          {r.consolidatedIn && r.consolidatedIn.length > 0 && (
            <Notice tone="info">
              <Badge tone="info">총괄납부 합산</Badge> {r.consolidatedIn.join(", ")}의 매입이 주사업장 집계에 합산되어 있습니다.
            </Notice>
          )}
          {r.siteScoped && <Notice tone="info">사업장 단위 집계입니다. 수신했지만 연결되지 않은 세금계산서는 전표가 없어 사업장을 알 수 없으므로 법인 전체 조회에서만 보입니다.</Notice>}
          {r.sites && r.sites.length > 1 && (
            <details>
              <summary>사업장별 내역</summary>
              <TableWrap>
                <table className="stack-table" aria-label="사업장별 매입 부가세">
                  <thead>
                    <tr>
                      <th>사업장</th>
                      <th className="nowrap">신고 방식</th>
                      <th className="num nowrap">매입 전표</th>
                      <th className="num nowrap">매입 부가세</th>
                    </tr>
                  </thead>
                  <tbody>
                    {r.sites.map((s) => (
                      <tr key={s.siteId}>
                        <td data-label="사업장">{s.name}</td>
                        <td data-label="신고 방식" className="nowrap">
                          {s.kind === "head" ? "주사업장" : s.vatMode === "consolidated" ? <Badge tone="info">총괄납부 합산</Badge> : "별도 신고"}
                        </td>
                        <td data-label="매입 전표" className="num nowrap">{s.count}건</td>
                        <td data-label="매입 부가세" className="num nowrap">{won(s.vat)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </details>
          )}

          <h2>매입처별 세금계산서</h2>
          <ExportButtons path="/api/finance/vat-purchase/export/vendors" params={params} disabled={r.vendors.length === 0} />
          {r.vendors.length === 0 && <EmptyState>세금계산서로 확정된 매입이 없습니다.</EmptyState>}
          {r.vendors.length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="매입처별 세금계산서">
                <thead>
                  <tr>
                    <th>거래처</th>
                    <th className="nowrap">사업자등록번호</th>
                    <th className="num nowrap">건수</th>
                    <th className="num nowrap">공급가액</th>
                    <th className="num nowrap">부가세</th>
                  </tr>
                </thead>
                <tbody>
                  {r.vendors.map((v) => (
                    <tr key={v.vendorId}>
                      <td data-label="거래처">{v.vendorName}</td>
                      <td data-label="사업자등록번호" className="nowrap">{v.businessNo}</td>
                      <td data-label="건수" className="num nowrap">{v.count}</td>
                      <td data-label="공급가액" className="num nowrap">{won(v.supply)}</td>
                      <td data-label="부가세" className="num nowrap">{won(v.vat)}</td>
                    </tr>
                  ))}
                  <tr>
                    <td data-label="거래처">합계</td>
                    <td data-label="사업자등록번호">—</td>
                    <td data-label="건수" className="num">—</td>
                    <td data-label="공급가액" className="num">—</td>
                    <td data-label="부가세" className="num nowrap">{won(r.vendorVat)}</td>
                  </tr>
                </tbody>
              </table>
            </TableWrap>
          )}

          <h2>세금계산서 미수취</h2>
          <p className="hint">거래명세서만 받은 매입입니다. 세금계산서를 받으면 공제됩니다. 아래 금액은 매입세액 공제 합계에 넣지 않았습니다.</p>
          <ExportButtons path="/api/finance/vat-purchase/export/unreceived" params={params} disabled={r.unreceived.length === 0} />
          {r.unreceived.length === 0 && <EmptyState>세금계산서를 받지 못한 매입이 없습니다.</EmptyState>}
          {r.unreceived.length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="세금계산서 미수취">
                <thead>
                  <tr>
                    <th className="nowrap">전표 번호</th>
                    <th>거래처</th>
                    <th className="nowrap">거래일</th>
                    <th className="num nowrap">공급가액</th>
                    <th className="num nowrap">부가세</th>
                  </tr>
                </thead>
                <tbody>
                  {r.unreceived.map((u) => (
                    <tr key={u.journalNo}>
                      <td data-label="전표 번호" className="nowrap">
                        <Link to={`/finance/journals/${u.journalNo}`}>{u.journalNo}</Link>
                      </td>
                      <td data-label="거래처">{u.vendorName}</td>
                      <td data-label="거래일" className="nowrap">{u.tradeDate}</td>
                      <td data-label="공급가액" className="num nowrap">{won(u.supply)}</td>
                      <td data-label="부가세" className="num nowrap">{won(u.vat)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}

          <h2>연결되지 않은 수신 세금계산서</h2>
          <p className="hint">
            받았지만 입고·매입 전표와 연결하지 않은 세금계산서입니다. 연결을 확정하면 위 &quot;매입처별 세금계산서&quot;에 들어갑니다. 아래 금액은 매입세액 공제 합계에 넣지 않았습니다.{" "}
            <Link to="/finance/tax-matches">세금계산서 대조</Link>
          </p>
          <ExportButtons path="/api/finance/vat-purchase/export/unlinked" params={params} disabled={(r.unlinked ?? []).length === 0} />
          {(r.unlinked ?? []).length === 0 && <EmptyState>연결되지 않은 수신 세금계산서가 없습니다.</EmptyState>}
          {(r.unlinked ?? []).length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="연결되지 않은 수신 세금계산서">
                <thead>
                  <tr>
                    <th className="nowrap">세금계산서</th>
                    <th>공급자</th>
                    <th className="nowrap">작성일</th>
                    <th className="num nowrap">공급가액</th>
                    <th className="num nowrap">세액</th>
                    <th>대조 상태</th>
                  </tr>
                </thead>
                <tbody>
                  {(r.unlinked ?? []).map((u) => (
                    <tr key={u.invoiceNo}>
                      <td data-label="세금계산서" className="nowrap">
                        <Link to="/finance/tax-matches">{u.invoiceNo}</Link>
                      </td>
                      <td data-label="공급자">{u.supplier}</td>
                      <td data-label="작성일" className="nowrap">{u.issueDate}</td>
                      <td data-label="공급가액" className="num nowrap">{won(u.supply)}</td>
                      <td data-label="세액" className="num nowrap">{won(u.vat)}</td>
                      <td data-label="대조 상태">{u.state}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}

          {(r.diffs ?? []).length > 0 && (
            <>
              <h2>세금계산서와 전표의 세액 차이</h2>
              <p className="hint">연결된 세금계산서의 세액과 매입 전표의 부가세가 다른 건입니다. 집계는 전표 금액으로 했습니다. 어느 쪽이 맞는지 확인하세요.</p>
              <ExportButtons path="/api/finance/vat-purchase/export/diff" params={params} />
              <TableWrap>
                <table className="stack-table" aria-label="세금계산서와 전표의 세액 차이">
                  <thead>
                    <tr>
                      <th className="nowrap">세금계산서</th>
                      <th className="nowrap">작성일</th>
                      <th>연결된 전표</th>
                      <th className="num nowrap">세금계산서 세액</th>
                      <th className="num nowrap">전표 부가세</th>
                      <th className="num nowrap">차이</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(r.diffs ?? []).map((d) => (
                      <tr key={d.invoiceNo}>
                        <td data-label="세금계산서" className="nowrap">{d.invoiceNo}</td>
                        <td data-label="작성일" className="nowrap">{d.issueDate}</td>
                        <td data-label="연결된 전표">{d.journalNos}</td>
                        <td data-label="세금계산서 세액" className="num nowrap">{won(d.invoiceVat)}</td>
                        <td data-label="전표 부가세" className="num nowrap">{won(d.journalVat)}</td>
                        <td data-label="차이" className="num nowrap">{won(d.diff)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </>
          )}

          <h2>카드 매입</h2>
          <ExportButtons path="/api/finance/vat-purchase/export/cards" params={params} disabled={r.cards.length === 0} />
          {r.cards.length === 0 && <EmptyState>승인된 카드 비용 전표가 없습니다.</EmptyState>}
          {r.cards.length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="카드 매입">
                <thead>
                  <tr>
                    <th>구분</th>
                    <th className="num nowrap">건수</th>
                    <th className="num nowrap">공급가액</th>
                    <th className="num nowrap">부가세</th>
                    <th className="num nowrap">합계</th>
                  </tr>
                </thead>
                <tbody>
                  {r.cards.map((c, i) => (
                    <tr key={i}>
                      <td data-label="구분">{c.kind === "deductible" ? "공제" : `불공제(${c.reasonLabel || "사유 없음"})`}</td>
                      <td data-label="건수" className="num nowrap">{c.count}</td>
                      <td data-label="공급가액" className="num nowrap">{c.kind === "deductible" ? won(c.supply) : "—"}</td>
                      <td data-label="부가세" className="num nowrap">{c.kind === "deductible" ? won(c.vat) : "—"}</td>
                      <td data-label="합계" className="num nowrap">{won(c.total)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}

          <h2>합계와 검산</h2>
          <p>
            매입세액 공제 합계 {won(r.deductibleVat)} (세금계산서 {won(r.vendorVat)} + 카드 공제 {won(r.cardDeductVat)})
          </p>
          <p>
            부가세대급금(1310) 기간 차변 합계 {won(r.ledgerVat)} ·{" "}
            {r.matches ? <Badge tone="ok">매입세액 공제 합계와 일치</Badge> : <Badge tone="warn">차이 {won(r.difference)}</Badge>}
          </p>
          {r.hints.map((h) => (
            <Notice key={h} tone="info">
              {h}
            </Notice>
          ))}
        </>
      )}
    </section>
  );
}

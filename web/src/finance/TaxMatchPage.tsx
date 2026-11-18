// 수신 세금계산서 3자 대조(B43 2단계): 발주·입고와 받은 세금계산서를 코드가 맞춰 제안하고, 확정은 사람이 한다.
// 일치 판정은 공급가액이 입고 금액 합과 정확히 같을 때만이고, 세액·귀속 월 차이는 경고로만 알려 준다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";

export interface TaxMatchReceipt {
  receiptId: number;
  poId: number;
  poNo: string;
  receivedAt: string;
  supply: number;
  journalNo: string;
}

export interface TaxMatch {
  id: number;
  invoiceId: number;
  invoiceNo: string;
  approvalNo: string;
  issueDate: string;
  supplierName: string;
  supply: number;
  tax: number;
  total: number;
  status: "proposed" | "confirmed" | "dismissed" | "unlinked";
  statusName: string;
  result: "match" | "ambiguous" | "amount_diff" | "vendor_unknown" | "no_receipt";
  resultName: string;
  diff: { diff?: number; alternatives?: number; candidates_sum?: number };
  warnings: string[];
  receipts: TaxMatchReceipt[];
  proposedAt: string;
  decidedAt: string;
  comment: string;
  journalNote?: string;
  confirmable: boolean;
  bulk: boolean;
}

export interface TaxSyncStatus {
  lastRunAt: string;
  lastDone: string;
  result: string;
  resultName: string;
  counts: { inserted: number; same: number; conflict: number; invalid: number; not_ours: number };
  remaining: boolean;
}

const RESULT_TONE: Record<TaxMatch["result"], Tone> = { match: "ok", ambiguous: "warn", amount_diff: "bad", vendor_unknown: "muted", no_receipt: "muted" };
const STATUS_CHOICES: [string, string][] = [
  ["proposed", "제안 대기"],
  ["confirmed", "연결됨"],
  ["dismissed", "기각"],
  ["unlinked", "해제"],
];
const RESULT_HELP: Record<TaxMatch["result"], string> = {
  match: "입고 금액 합이 공급가액과 같습니다.",
  ambiguous: "같은 금액을 만드는 입고 조합이 여럿입니다. 가장 오래된 입고부터 제안했으니 맞는지 확인하세요.",
  amount_diff: "입고 금액 합이 공급가액과 다릅니다. 거래처에 확인하거나 입고를 먼저 등록하세요.",
  vendor_unknown: "사업자번호가 같은 거래처가 없습니다. 거래처를 등록한 뒤 다시 대조하세요.",
  no_receipt: "연결할 입고가 없습니다. 입고가 등록되면 다시 대조합니다.",
};

type Dialog = { kind: "dismiss" | "unlink" | "confirm"; m: TaxMatch } | null;

export function TaxMatchPage() {
  const { can } = useAuth();
  const [status, setStatus] = useState("proposed");
  const [result, setResult] = useState("");
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [dialog, setDialog] = useState<Dialog>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");
  const qs = new URLSearchParams({ limit: "100" });
  if (status) qs.set("status", status);
  if (result) qs.set("result", result);
  const list = useLoad(() => api.get<{ rows: TaxMatch[]; total: number }>(`/api/finance/tax-matches?${qs.toString()}`), [status, result]);
  const sync = useLoad(() => api.get<TaxSyncStatus>("/api/finance/tax-sync"), []);
  const rows = list.data?.rows ?? [];
  const write = can("taxinvoice:write");
  const bulkIds = rows.filter((r) => r.bulk && picked.has(r.id)).map((r) => r.id);

  async function run(fn: () => Promise<unknown>, done: string) {
    setBusy(true);
    setErr("");
    setMsg("");
    try {
      await fn();
      setMsg(done);
      setDialog(null);
      setReason("");
      setPicked(new Set());
      await Promise.all([list.reload(), sync.reload()]);
    } catch (e) {
      setErr(errorText(e));
      setDialog(null);
    } finally {
      setBusy(false);
    }
  }

  const open = (kind: "dismiss" | "unlink" | "confirm", m: TaxMatch) => {
    setReason("");
    setDialog({ kind, m });
  };

  const toggle = (id: number) =>
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <section>
      <h1>세금계산서 대조</h1>
      <p className="hint">받은 세금계산서를 발주·입고와 맞춰 봅니다. 시스템이 제안만 하고, 연결은 담당자가 확인해 확정합니다. 확정하면 부가세 매입 집계의 &quot;세금계산서 수취&quot;로 들어갑니다.</p>
      {sync.data && (
        <p className="hint">
          수신 동기화: {sync.data.lastRunAt ? `${dateTime(sync.data.lastRunAt)} · ${sync.data.resultName}` : "아직 실행하지 않았습니다."}
          {sync.data.lastRunAt && ` (새로 ${sync.data.counts.inserted}건, 이미 있음 ${sync.data.counts.same}건, 내용이 달라 기록만 함 ${sync.data.counts.conflict}건)`}
          {sync.data.remaining && " 한 번에 가져올 수 있는 양을 넘어 다음 실행에서 이어서 가져옵니다."}
        </p>
      )}
      <div className="form-row">
        <div className="field">
          <label htmlFor="tm-status">진행 상태</label>
          <select
            id="tm-status"
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              setPicked(new Set());
            }}
          >
            <option value="">전체</option>
            {STATUS_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="tm-result">대조 결과</label>
          <select id="tm-result" value={result} onChange={(e) => setResult(e.target.value)}>
            <option value="">전체</option>
            <option value="match">일치</option>
            <option value="ambiguous">후보가 여럿</option>
            <option value="amount_diff">금액 불일치</option>
            <option value="vendor_unknown">거래처 미등록</option>
            <option value="no_receipt">입고 없음</option>
          </select>
        </div>
        {write && (
          <>
            <button type="button" className="btn" disabled={busy} onClick={() => void run(() => api.post("/api/finance/tax-matches/propose", {}), "대조 제안을 다시 만들었습니다.")}>
              대조 다시 만들기
            </button>
            <button type="button" className="btn" disabled={busy} onClick={() => void run(() => api.post("/api/finance/tax-sync/run", {}), "수신 세금계산서를 가져왔습니다.")}>
              수신 세금계산서 가져오기
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || bulkIds.length === 0}
              onClick={() => void run(() => api.post("/api/finance/tax-matches/confirm-bulk", { ids: bulkIds }), `${bulkIds.length}건을 확정했습니다.`)}
            >
              선택한 일치 건 확정{bulkIds.length > 0 ? ` (${bulkIds.length})` : ""}
            </button>
          </>
        )}
      </div>
      {msg && <Notice tone="ok">{msg}</Notice>}
      {(err || list.error) && <Notice tone="error">{err || list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>조건에 맞는 대조가 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                {write && <th className="nowrap">선택</th>}
                <th className="nowrap">세금계산서</th>
                <th className="nowrap">작성일</th>
                <th>공급자</th>
                <th className="num nowrap">공급가액</th>
                <th className="num nowrap">세액</th>
                <th>연결할 입고</th>
                <th className="nowrap">결과</th>
                <th>확인할 점</th>
                <th className="nowrap">처리</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((m) => (
                <tr key={m.id}>
                  {write && (
                    <td data-label="선택" className="nowrap">
                      {m.bulk ? <input type="checkbox" aria-label={`${m.invoiceNo} 선택`} checked={picked.has(m.id)} onChange={() => toggle(m.id)} /> : null}
                    </td>
                  )}
                  <td data-label="세금계산서" className="nowrap">
                    <Link to={`/finance/tax-invoices/${m.invoiceId}`}>{m.invoiceNo}</Link>
                  </td>
                  <td data-label="작성일" className="nowrap">{m.issueDate}</td>
                  <td data-label="공급자">{m.supplierName}</td>
                  <td data-label="공급가액" className="num nowrap">{won(m.supply)}</td>
                  <td data-label="세액" className="num nowrap">{won(m.tax)}</td>
                  <td data-label="연결할 입고">
                    {m.receipts.length === 0 ? (
                      "-"
                    ) : (
                      <ul className="plain-list">
                        {m.receipts.map((r) => (
                          <li key={r.receiptId}>
                            {r.poNo} · {r.receivedAt} · {won(r.supply)}
                            {r.journalNo ? ` · 전표 ${r.journalNo}` : ""}
                          </li>
                        ))}
                      </ul>
                    )}
                  </td>
                  <td data-label="결과" className="nowrap">
                    <Badge tone={RESULT_TONE[m.result]}>{m.resultName}</Badge>
                    {m.status !== "proposed" && <div className="hint">{m.statusName}</div>}
                  </td>
                  <td data-label="확인할 점">
                    <div>{RESULT_HELP[m.result]}</div>
                    {m.result === "amount_diff" && m.diff.diff !== undefined && m.diff.diff !== 0 && (
                      <div>
                        입고 합이 공급가액보다 {won(Math.abs(m.diff.diff))} {m.diff.diff > 0 ? "많습니다" : "적습니다"}.
                      </div>
                    )}
                    {m.result === "ambiguous" && (m.diff.alternatives ?? 0) > 0 && <div>다른 조합이 {m.diff.alternatives}가지 더 있습니다.</div>}
                    {m.warnings.map((w) => (
                      <div key={w}>
                        <Badge tone="warn">확인</Badge> {w}
                      </div>
                    ))}
                    {m.journalNote && <div><Badge tone="warn">전표</Badge> {m.journalNote}</div>}
                    {m.comment && <div className="hint">의견: {m.comment}</div>}
                  </td>
                  <td data-label="처리" className="nowrap">
                    {write && m.status === "proposed" && m.confirmable && (
                      <button type="button" className="btn btn-sm" disabled={busy} onClick={() => open("confirm", m)}>
                        확정
                      </button>
                    )}
                    {write && m.status === "proposed" && (
                      <button type="button" className="btn btn-sm" disabled={busy} onClick={() => open("dismiss", m)}>
                        기각
                      </button>
                    )}
                    {write && m.status === "confirmed" && (
                      <button type="button" className="btn btn-sm" disabled={busy} onClick={() => open("unlink", m)}>
                        연결 해제
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {dialog?.kind === "confirm" && (
        <ConfirmDialog
          title="입고와 연결할까요?"
          confirmLabel="확정"
          variant="primary"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void run(() => api.post(`/api/finance/tax-matches/${dialog.m.id}/confirm`, { comment: reason }), "연결을 확정했습니다.")}
        >
          {dialog.m.invoiceNo} 을(를) 입고 {dialog.m.receipts.length}건({won(dialog.m.supply)})과 연결합니다. 해제는 나중에도 할 수 있습니다.
          <div className="field">
            <label htmlFor="tm-comment">의견(선택)</label>
            <input id="tm-comment" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
      {(dialog?.kind === "dismiss" || dialog?.kind === "unlink") && (
        <ConfirmDialog
          title={dialog.kind === "dismiss" ? "이 제안을 기각할까요?" : "연결을 해제할까요?"}
          confirmLabel={dialog.kind === "dismiss" ? "기각" : "연결 해제"}
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void run(() => api.post(`/api/finance/tax-matches/${dialog.m.id}/${dialog.kind}`, { reason }), dialog.kind === "dismiss" ? "제안을 기각했습니다." : "연결을 해제했습니다.")}
        >
          {dialog.kind === "dismiss" ? "같은 입고 조합은 다시 제안하지 않습니다." : "해제하면 부가세 매입 집계에서 미수취·미연결로 돌아갑니다."}
          <div className="field">
            <label htmlFor="tm-reason">사유(필수)</label>
            <input id="tm-reason" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

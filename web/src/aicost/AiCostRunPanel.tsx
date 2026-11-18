//(B49). 월 배부 확정·해제·전표 패널과 AI 비용 예산 화면.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api, apiDownload } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { businessMessage } from "../purchase/useLoad";
import { won } from "./AiCostPages";

export interface RunLine {
  departmentId: number;
  departmentName: string;
  headcount: number;
  calls: number;
  directKrw: number;
  sharedKrw: number;
  totalKrw: number;
}
export interface ReleaseInfo {
  id: number;
  status: "pending" | "confirmed" | "rejected" | "canceled" | "expired";
  reason: string;
  requestedBy: number;
  requestedByName: string;
  requestedAt: string;
}
export interface RunInfo {
  id: number;
  month: string;
  entityId: number;
  ruleVersion: number;
  status: "confirmed" | "released";
  totalKrw: number;
  directKrw: number;
  sharedKrw: number;
  journalNo: string;
  journalStatus: string;
  confirmedByName: string;
  confirmedAt: string;
  releasedAt?: string;
  lines: RunLine[];
  release?: ReleaseInfo;
  groupShareKrw?: number;
  counterpartyName?: string;
}

const JOURNAL_STATUS: Record<string, string> = { draft: "임시", requested: "승인 요청 중", approved: "승인", canceled: "취소" };

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string };
      if (j.error) return businessMessage(j.error);
    } catch {
      /* 그대로 */
    }
    return businessMessage(e.message);
  }
  return "요청을 처리하지 못했습니다.";
}

function stamp(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString("ko-KR");
}

/** 월·법인의 확정 상태와 동작(확정·해제 요청·확인·반려·전표 다시 만들기)이다. canConfirm 은 미리보기에 막는 문제가 없을 때 true. */
export function RunPanel({ month, entity, ruleVersion, canConfirm, onChanged }: { month: string; entity?: string; ruleVersion?: string; canConfirm: boolean; onChanged: () => void }) {
  const { can, user } = useAuth();
  const allocate = can("cost:allocate");
  const [run, setRun] = useState<RunInfo | null | undefined>(undefined);
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");
  const [dialog, setDialog] = useState<"confirm" | "release-confirm" | "">("");
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");

  const load = useCallback(async () => {
    setError("");
    const q = new URLSearchParams({ month });
    if (entity) q.set("entity", entity);
    try {
      const r = await api.get<{ run: RunInfo | null }>(`/api/admin/ai-cost/run?${q.toString()}`);
      setRun(r.run);
    } catch (e) {
      setError(errorText(e));
    }
  }, [month, entity]);
  useEffect(() => {
    setMsg("");
    void load();
  }, [load]);

  async function act(path: string, body: unknown, done: string) {
    setBusy(true);
    setError("");
    setMsg("");
    try {
      await api.post(path, body);
      setMsg(done);
      setDialog("");
      setReason("");
      await load();
      onChanged();
    } catch (e) {
      setDialog("");
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const confirmed = run && run.status === "confirmed";
  const rel = run?.release;
  const pending = rel && rel.status === "pending";
  const mine = pending && user ? rel.requestedBy === user.id : false;

  return (
    <section aria-label="월 확정">
      <h2>월 확정과 비용 전표</h2>
      {error && <Notice tone="error">{error}</Notice>}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {run === undefined ? (
        <p>불러오는 중…</p>
      ) : confirmed ? (
        <>
          <p>
            <strong>확정됨</strong> · {stamp(run.confirmedAt)} · {run.confirmedByName} · 규칙 버전 {run.ruleVersion} · 합계 {won(run.totalKrw)}
          </p>
          {(run.groupShareKrw ?? 0) > 0 && (
            <p>
              법인 간 공용 비용 {won(run.groupShareKrw ?? 0)} 포함
              {run.counterpartyName ? ` (지급 법인: ${run.counterpartyName}, 표시만 하며 상계·연결 조정은 하지 않습니다)` : ""}
            </p>
          )}
          <p>
            비용 전표:{" "}
            {run.journalNo ? (
              <>
                <Link to={`/finance/journals/${encodeURIComponent(run.journalNo)}`}>{run.journalNo}</Link> ({JOURNAL_STATUS[run.journalStatus] ?? "상태 모름"})
              </>
            ) : (
              "없음(금액이 0 원이라 만들지 않았습니다)"
            )}
          </p>
          <Notice tone="info">확정된 달은 환율·청구액을 바꾸거나 다시 계산할 수 없습니다. 틀렸다면 확정 해제를 요청하세요. 해제는 다른 재무 담당이 확인해야 합니다.</Notice>
          {allocate && run.journalNo && run.journalStatus === "canceled" && (
            <button type="button" onClick={() => void act(`/api/admin/ai-cost/runs/${run.id}/journal`, {}, "비용 전표 초안을 다시 만들었습니다.")}>
              전표 다시 만들기
            </button>
          )}
          {pending ? (
            <div>
              <Notice tone="warn">
                {rel.requestedByName} 님이 확정 해제를 요청했습니다(사유: {rel.reason}). 다른 재무 담당의 확인을 기다립니다.
              </Notice>
              {allocate && !mine && (
                <button type="button" onClick={() => setDialog("release-confirm")}>
                  해제 확인
                </button>
              )}{" "}
              {allocate && (
                <button type="button" onClick={() => void act(`/api/admin/ai-cost/runs/${run.id}/release-reject`, {}, mine ? "해제 요청을 취소했습니다." : "해제 요청을 반려했습니다.")}>
                  {mine ? "요청 취소" : "반려"}
                </button>
              )}
              {mine && <p>해제는 요청한 사람이 확인할 수 없습니다.</p>}
            </div>
          ) : (
            allocate && (
              <form
                aria-label="해제 요청"
                onSubmit={(e: FormEvent) => {
                  e.preventDefault();
                  void act(`/api/admin/ai-cost/runs/${run.id}/release-request`, { reason }, "해제를 요청했습니다. 다른 재무 담당이 확인하면 해제됩니다.");
                }}
              >
                {rel && rel.status === "expired" && <Notice tone="info">이전 해제 요청은 7일이 지나 만료되었습니다.</Notice>}
                {rel && rel.status === "rejected" && <Notice tone="info">이전 해제 요청은 반려되었습니다.</Notice>}
                <label>
                  해제 사유 <input value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
                </label>{" "}
                <button type="submit" disabled={busy || reason.trim() === ""}>
                  해제 요청
                </button>
              </form>
            )
          )}
        </>
      ) : (
        <>
          {run && run.status === "released" && <Notice tone="info">이 달의 이전 확정은 해제되었습니다({run.releasedAt ? stamp(run.releasedAt) : ""}). 다시 확정할 수 있습니다.</Notice>}
          {allocate ? (
            <button type="button" disabled={!canConfirm} onClick={() => setDialog("confirm")}>
              이 달 배부 확정
            </button>
          ) : (
            <p>확정은 재무 담당이 합니다.</p>
          )}
          {allocate && !canConfirm && <p>확정할 수 없는 상태입니다. 위 안내를 먼저 해결하세요.</p>}
        </>
      )}
      <p>
        <button
          type="button"
          onClick={() => {
            const q = new URLSearchParams({ month });
            if (entity) q.set("entity", entity);
            apiDownload(`/api/admin/ai-cost/export?${q.toString()}`).catch((e) => setError(errorText(e)));
          }}
        >
          CSV 내려받기
        </button>
      </p>
      {dialog === "confirm" && (
        <ConfirmDialog
          title={`${month} 배부를 확정합니다`}
          confirmLabel="확정"
          variant="primary"
          busy={busy}
          onCancel={() => setDialog("")}
          onConfirm={() => void act("/api/admin/ai-cost/runs", { month, entity: entity ? Number(entity) : 0, version: ruleVersion ? Number(ruleVersion) : 0 }, "배부를 확정하고 비용 전표 초안을 만들었습니다.")}
        >
          확정하면 부서별 비용 전표 초안이 만들어지고, 이 달의 환율·청구액은 바꿀 수 없게 됩니다. 되돌리려면 다른 재무 담당의 확인이 필요합니다.
        </ConfirmDialog>
      )}
      {dialog === "release-confirm" && run && (
        <ConfirmDialog
          title={`${run.month} 확정을 해제합니다`}
          confirmLabel="해제"
          busy={busy}
          onCancel={() => setDialog("")}
          onConfirm={() => void act(`/api/admin/ai-cost/runs/${run.id}/release-confirm`, {}, "확정을 해제하고 임시 전표를 취소했습니다.")}
        >
          연결된 임시 전표가 취소되고 이 달을 다시 계산할 수 있게 됩니다. 승인된 전표가 있으면 해제되지 않습니다.
        </ConfirmDialog>
      )}
    </section>
  );
}

export interface BudgetRow {
  entityId: number;
  departmentId: number;
  departmentName: string;
  amountKrw: number | null;
  usedKrw: number;
  pct: number;
  fxEstimated: boolean;
}

function thisMonth(now: Date = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
}

export function AiCostBudgetsPage() {
  const { can } = useAuth();
  const allocate = can("cost:allocate");
  const [month, setMonth] = useState(thisMonth());
  const [rows, setRows] = useState<BudgetRow[] | null>(null);
  const [edit, setEdit] = useState<Record<number, string>>({});
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");

  const load = useCallback(async () => {
    setError("");
    try {
      const r = await api.get<{ budgets: BudgetRow[] }>(`/api/admin/ai-cost/budgets?month=${encodeURIComponent(month)}`);
      setRows(r.budgets);
    } catch (e) {
      setError(errorText(e));
    }
  }, [month]);
  useEffect(() => {
    void load();
  }, [load]);

  async function save(row: BudgetRow) {
    setMsg("");
    const raw = (edit[row.departmentId] ?? "").replace(/,/g, "").trim();
    const n = Number(raw);
    if (raw === "" || !Number.isInteger(n) || n < 0) {
      setError("예산은 0 이상의 정수(원)로 입력하세요.");
      return;
    }
    try {
      await api.put("/api/admin/ai-cost/budgets", { month, departmentId: row.departmentId, amountKrw: n });
      setMsg(`${row.departmentName} 예산을 저장했습니다.`);
      setEdit((s) => ({ ...s, [row.departmentId]: "" }));
      await load();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <div>
      <h1>AI 비용 예산</h1>
      <p>
        부서별 월 AI 비용 예산입니다. 구매 예산과는 별개이며, 사용액은 부서가 직접 쓴 비용(공용 비용 몫 제외)입니다. 80%와 100%에 닿으면 부서장과 재무 담당에게 알립니다.{" "}
        <Link to="/admin/ai-cost">집계</Link>
      </p>
      <form aria-label="조회 월" className="form-row" onSubmit={(e) => { e.preventDefault(); void load(); }}>
        <label>
          월 <input type="month" value={month} onChange={(e) => setMonth(e.target.value)} required />
        </label>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {rows === null ? (
        <p>불러오는 중…</p>
      ) : rows.length === 0 ? (
        <EmptyState>예산을 정할 부서가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="부서별 AI 비용 예산">
            <thead>
              <tr>
                <th>부서</th>
                <th className="num nowrap">예산</th>
                <th className="num nowrap">사용(직접)</th>
                <th className="num nowrap">사용률</th>
                {allocate && <th>예산 입력(원)</th>}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.departmentId}>
                  <td>{r.departmentName}</td>
                  <td className="num">{r.amountKrw === null ? "예산 없음" : won(r.amountKrw)}</td>
                  <td className="num">
                    {won(r.usedKrw)}
                    {r.fxEstimated ? " (환율 어림)" : ""}
                  </td>
                  <td className="num">{r.amountKrw === null || r.amountKrw === 0 ? "-" : `${r.pct}%`}</td>
                  {allocate && (
                    <td>
                      <input aria-label={`${r.departmentName} 예산`} inputMode="numeric" style={{ width: 120 }} value={edit[r.departmentId] ?? ""} onChange={(e) => setEdit((s) => ({ ...s, [r.departmentId]: e.target.value }))} />{" "}
                      <button type="button" onClick={() => void save(r)}>
                        저장
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </div>
  );
}

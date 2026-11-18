// 월 마감 화면(B27 ②): 월 목록과 마감 전 점검(차단·경고), 재무의 월 마감. 마감된 달은 그 달 날짜로 출고·실사를 만들 수 없다.
// 마감 해제는 전자결재 양식(월 마감 해제 요청서, 재무팀장 결재)으로만 한다: 가장 최근에 마감한 달의 "마감 해제 요청"에서 사유를 적어 상신한다.
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, useToast, type Tone } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";

interface MonthRow {
  month: string;
  state: "open" | "closed" | "release_pending";
  version: number;
  closedAt?: string;
  closedBy: string;
  closeAmount?: number;
  closable: boolean;
  releasable: boolean;
}

interface PrecheckItem {
  title: string;
  detail: string;
  docId: number;
}

interface Precheck {
  month: string;
  closable: boolean;
  reason: string;
  blockers: PrecheckItem[];
  warnings: PrecheckItem[];
}

const STATE: Record<MonthRow["state"], { text: string; tone: Tone }> = {
  open: { text: "열림", tone: "muted" },
  closed: { text: "마감됨", tone: "ok" },
  release_pending: { text: "해제 결재 중", tone: "warn" },
};

/** "2026-11" → "2026년 11월". */
export function monthText(m: string): string {
  const [y, mo] = m.split("-");
  return y && mo ? `${y}년 ${Number(mo)}월` : m;
}

export function MonthClosePage() {
  const { can, entities, currentEntity } = useAuth();
  const multi = (entities?.length ?? 0) > 1;
  const allMode = multi && currentEntity?.all === true; // 전체 법인 보기는 읽기 전용: 마감·해제는 법인 하나를 골라야 한다
  const entityName = multi && !allMode ? currentEntity?.name : undefined;
  const months = useLoad(() => api.get<MonthRow[]>("/api/inventory/months"), []);
  const [selected, setSelected] = useState("");
  const [pre, setPre] = useState<Precheck | null>(null);
  const [err, setErr] = useState("");
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [releasing, setReleasing] = useState("");
  const [reason, setReason] = useState("");
  const nav = useNavigate();

  async function check(m: string) {
    setErr("");
    setSelected(m);
    try {
      setPre(await api.get<Precheck>(`/api/inventory/months/${m}/precheck`));
    } catch (e) {
      setPre(null);
      setErr(errorText(e));
    }
  }

  async function close() {
    setBusy(true);
    setErr("");
    try {
      await api.post(`/api/inventory/months/${selected}/close`);
      setConfirm(false);
      toast(`${monthText(selected)}을 마감했습니다. 그 달 날짜로는 출고·실사를 만들거나 고칠 수 없습니다.`);
      await months.reload();
      await check(selected);
    } catch (e) {
      setConfirm(false);
      setErr(errorText(e));
      await check(selected);
    } finally {
      setBusy(false);
    }
  }

  async function requestRelease() {
    if (reason.trim().length < 10) {
      setErr("마감 해제 사유를 10자 이상 입력하세요.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await api.post<{ docId: number }>(`/api/inventory/months/${releasing}/release-request`, { reason });
      setReleasing("");
      setReason("");
      nav(`/docs/${r.docId}`);
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const row = months.data?.find((r) => r.month === selected);
  const showAmount = months.data?.some((r) => r.closeAmount !== undefined) ?? false;

  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>월 마감</h2>
      <p className="hint">
        월을 마감하면 그 달 날짜의 출고·실사 전표를 만들거나 고칠 수 없고, 품목·창고별 월말 재고가 저장됩니다. 마감은 지난 달부터 순서대로 하고, 해제는 전자결재(재무팀장)로만 합니다.
      </p>
      {entityName && <p className="hint">{entityName} 법인의 월 마감입니다.</p>}
      {allMode && <Notice tone="info">법인 하나를 고르세요. 월 마감과 해제 요청은 법인 하나씩 합니다.</Notice>}
      {months.error && <Notice tone="error">{months.error}</Notice>}
      {err && <Notice tone="error">{err}</Notice>}
      {months.data && months.data.length === 0 && (
        <EmptyState>{entityName ? `${entityName} 법인은 마감할 재고 기록이 없습니다.` : "아직 마감한 달이 없습니다. 이번 달이 끝나면 지난 달을 마감할 수 있습니다."}</EmptyState>
      )}
      {months.data && months.data.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>월</th>
                <th className="nowrap">상태</th>
                <th className="nowrap">마감 일시</th>
                <th className="nowrap">마감한 사람</th>
                {showAmount && <th className="num nowrap">기말 재고 금액</th>}
                <th />
              </tr>
            </thead>
            <tbody>
              {months.data.map((r) => (
                <tr key={r.month} aria-current={r.month === selected ? "true" : undefined}>
                  <td data-label="월">{monthText(r.month)}</td>
                  <td data-label="상태" className="nowrap">
                    <Badge tone={STATE[r.state].tone}>{STATE[r.state].text}</Badge>
                    {r.version > 1 && <span className="hint"> {r.version}차 마감</span>}
                  </td>
                  <td data-label="마감 일시" className="nowrap">{r.closedAt ? dateTime(r.closedAt) : "—"}</td>
                  <td data-label="마감한 사람" className="nowrap">{r.closedBy || "—"}</td>
                  {showAmount && (
                    <td data-label="기말 재고 금액" className="num nowrap">
                      {r.closeAmount === undefined ? "—" : won(r.closeAmount)}
                    </td>
                  )}
                  <td className="nowrap">
                    <button type="button" className="btn-secondary btn-sm" aria-label={`${monthText(r.month)} 점검`} onClick={() => void check(r.month)}>
                      {r.state === "open" ? "마감 전 점검" : "점검 보기"}
                    </button>{" "}
                    {can("inventory:close") && !allMode && r.releasable && (
                      <button type="button" className="btn-secondary btn-sm" aria-label={`${monthText(r.month)} 마감 해제 요청`} onClick={() => { setErr(""); setReleasing(r.month); }}>
                        마감 해제 요청
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      {selected && pre && (
        <section className="card" aria-label={`${monthText(selected)} 마감 전 점검`}>
          <h3>{monthText(selected)} 마감 전 점검</h3>
          {entityName && <p className="hint">이 점검은 {entityName} 법인의 기록만 봅니다.</p>}
          {pre.blockers.length === 0 && pre.warnings.length === 0 && <p>점검할 항목이 없습니다. 마감할 수 있습니다.</p>}
          {pre.blockers.length > 0 && (
            <>
              <h4>
                <Badge tone="bad">차단</Badge> 마감하기 전에 처리해야 합니다
              </h4>
              <ul>
                {pre.blockers.map((b, i) => (
                  <li key={i}>
                    <strong>{b.title}</strong>: {b.detail}
                  </li>
                ))}
              </ul>
            </>
          )}
          {pre.warnings.length > 0 && (
            <>
              <h4>
                <Badge tone="warn">경고</Badge> 확인만 하면 됩니다
              </h4>
              <ul>
                {pre.warnings.map((w, i) => (
                  <li key={i}>
                    <strong>{w.title}</strong>: {w.detail}
                  </li>
                ))}
              </ul>
            </>
          )}
          {row?.state === "open" && !pre.closable && pre.reason && <Notice tone="warn">{pre.reason}</Notice>}
          <div className="form-row">
            <button type="button" className="btn-secondary" onClick={() => void check(selected)}>
              점검 다시 하기
            </button>
            {can("inventory:close") && !allMode && row?.state === "open" && (
              <button type="button" disabled={!pre.closable || busy} onClick={() => setConfirm(true)}>
                이 달 마감
              </button>
            )}
          </div>
        </section>
      )}
      {releasing && (
        <ConfirmDialog title="마감 해제 요청" confirmLabel="요청서 상신" variant="primary" busy={busy} onConfirm={() => void requestRelease()} onCancel={() => setReleasing("")}>
          <p>{monthText(releasing)} 마감을 풀려면 월 마감 해제 요청서를 재무팀장에게 올려 결재를 받아야 합니다. 승인되기 전에는 마감이 그대로 유지됩니다.</p>
          <div className="field">
            <label htmlFor="release-reason">해제 사유 (10자 이상)</label>
            <textarea id="release-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
          </div>
          {err && <Notice tone="error">{err}</Notice>}
        </ConfirmDialog>
      )}
      {confirm && (
        <ConfirmDialog title="월 마감" confirmLabel="마감" variant="primary" busy={busy} onConfirm={() => void close()} onCancel={() => setConfirm(false)}>
          <p>{monthText(selected)}을 마감합니다. 마감하면 그 달 날짜로는 출고·실사를 만들거나 고칠 수 없습니다.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

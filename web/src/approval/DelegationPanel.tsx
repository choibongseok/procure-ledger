// 부재·대결 설정(B29 ①): 기간·사유·대결자를 정해 부재를 등록하고 해제한다. 내가 맡긴 부재·위임과 받은 대결을 보인다.
import { useState } from "react";
import { api } from "../api/client";
import { Badge, Breadcrumbs, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import type { DelegateCandidate, Delegation, DelegationList } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";

const STATUS_TONE: Record<string, Tone> = { "진행 중": "ok", 예정: "info", 종료: "muted", 해제됨: "muted" };
/** 해제됨·예정·진행 중·종료 중 하나. 영문 코드는 보이지 않는다. */
export function delegationStatus(d: Delegation, now: number = Date.now()): string {
  if (d.revokedAt) return "해제됨";
  if (now < new Date(d.from).getTime()) return "예정";
  if (now >= new Date(d.to).getTime()) return "종료";
  return "진행 중";
}

/** 후보 라벨: "이름 (직책, 조직)". 직책·조직은 조직도에서 온다. */
function candidateText(c: DelegateCandidate): string {
  const detail = [c.title, c.unitName].filter(Boolean).join(", ");
  const base = detail ? `${c.name} (${detail})` : c.name;
  return `${base} · ${c.away ? "부재 중" : "대결 가능"}`;
}

/** datetime-local 값(브라우저 현지 시각)을 서버가 받는 시각 문자열로 바꾼다. 잘못된 값이면 "". */
export function toServerTime(local: string): string {
  const t = new Date(local);
  return local === "" || Number.isNaN(t.getTime()) ? "" : t.toISOString();
}

const REASONS: [string, string][] = [
  ["vacation", "휴가"],
  ["trip", "출장"],
  ["training", "교육"],
  ["other", "기타"],
];
const REASON_LABEL: Record<string, string> = Object.fromEntries(REASONS);

/** 부재·대결 설정 화면(/approvals/absence). */
export function AbsencePage() {
  return (
    <section>
      <Breadcrumbs items={[{ label: "결재함", to: "/approvals" }, { label: "부재·대결 설정" }]} />
      <h1>부재·대결 설정</h1>
      <DelegationPanel />
    </section>
  );
}

export function DelegationPanel() {
  const list = useLoad(() => api.get<DelegationList>("/api/approvals/delegations"), []);
  const cands = useLoad(() => api.get<DelegateCandidate[]>("/api/approvals/delegations/candidates"), []);
  const [delegateId, setDelegateId] = useState("");
  const [reason, setReason] = useState("");
  const [note, setNote] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function create() {
    const f = toServerTime(from);
    const t = toServerTime(to);
    if (!reason) return setError("부재 사유를 고르세요.");
    if (!delegateId) return setError("대결자를 고르세요.");
    if (!f || !t) return setError("부재 기간(시작·종료)을 입력하세요.");
    if (new Date(t) <= new Date(f)) return setError("부재 기간이 올바르지 않습니다. 시작은 지금 이후, 종료는 시작 이후로 정하세요.");
    setBusy(true);
    setError("");
    try {
      await api.post("/api/approvals/delegations", { delegateId: Number(delegateId), from: f, to: t, kind: "absence", reason, note: note.trim() });
      setDelegateId("");
      setReason("");
      setNote("");
      setFrom("");
      setTo("");
      await list.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: number) {
    setBusy(true);
    setError("");
    try {
      await api.del(`/api/approvals/delegations/${id}`);
      await list.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const given = list.data?.given ?? [];
  const received = list.data?.received ?? [];
  return (
    <section className="card" aria-label="부재·대결 설정">
      <h2>부재 등록</h2>
      <p>자리를 비우는 동안 같은 직책 이상의 조직장을 대결자로 정하면 그 기간에 대결자도 내 결재를 처리할 수 있습니다. 나도 부재 중에 직접 결재할 수 있고, 먼저 처리한 사람이 끝냅니다. 기간이 끝나거나 해제하면 바로 효력이 없어집니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      <div className="form-row">
        <div className="field">
          <label htmlFor="deleg-reason">부재 사유</label>
          <select id="deleg-reason" value={reason} onChange={(e) => setReason(e.target.value)}>
            <option value="">선택</option>
            {REASONS.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="deleg-to">대결자</label>
          <select id="deleg-to" value={delegateId} onChange={(e) => setDelegateId(e.target.value)}>
            <option value="">선택</option>
            {(cands.data ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {candidateText(c)}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="deleg-from">부재 시작</label>
          <input id="deleg-from" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="deleg-until">부재 종료</label>
          <input id="deleg-until" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="deleg-note">메모(선택)</label>
          <input id="deleg-note" value={note} maxLength={100} onChange={(e) => setNote(e.target.value)} />
        </div>
        <button type="button" disabled={busy} onClick={() => void create()}>
          부재 등록
        </button>
      </div>

      <h3>내가 맡긴 부재·위임</h3>
      {given.length === 0 ? (
        <EmptyState hint="자리를 비울 때 대결자를 정해 두면 결재가 멈추지 않습니다.">등록한 부재가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>대결자</th>
                <th className="nowrap">구분</th>
                <th className="nowrap">기간</th>
                <th className="nowrap">상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {given.map((d) => {
                const status = delegationStatus(d);
                return (
                  <tr key={d.id}>
                    <td>{d.delegateName}</td>
                    <td className="nowrap">{d.kind === "absence" ? `부재 · ${REASON_LABEL[d.reason ?? ""] ?? "기타"}` : "위임"}{d.note ? ` · ${d.note}` : ""}</td>
                    <td className="nowrap">
                      {dateTime(d.from)} ~ {dateTime(d.to)}
                    </td>
                    <td className="nowrap">
                      <Badge tone={STATUS_TONE[status] ?? "muted"}>{status}</Badge>
                    </td>
                    <td className="nowrap">
                      {status !== "해제됨" && status !== "종료" && (
                        <button type="button" className="btn-danger btn-sm" disabled={busy} onClick={() => void revoke(d.id)}>
                          해제
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
      )}

      <h3>내가 받은 대결</h3>
      {received.length === 0 ? (
        <EmptyState>받은 대결이 없습니다.</EmptyState>
      ) : (
        <ul>
          {received.map((d) => (
            <li key={d.id}>
              {d.delegatorName} 님{d.kind === "absence" ? ` · 부재(${REASON_LABEL[d.reason ?? ""] ?? "기타"})` : ""} · {dateTime(d.from)} ~ {dateTime(d.to)} · {delegationStatus(d)}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

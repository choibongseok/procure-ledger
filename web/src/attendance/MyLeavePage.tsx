// (web/src/attendance). 내 휴가(B50 13.1): 잔여, 휴가 신청(실시간 계산·결재선은 결재 화면과 같다), 내 신청과 취소, 잔여 원장.
// 사유는 민감한 정보일 수 있어 안내 문구를 둔다(병명·진단 내용은 적지 않는다). 모듈이 꺼져 있으면 서버가 404 라 안내만 보인다.
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, PageHead, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { fmtMinutes, PART_LABEL } from "./leaveFormat";

interface LeaveType {
  code: string;
  name: string;
  unit: string;
  balance: string;
  proofRequired: boolean;
}
interface BalanceLine {
  balance: string;
  label: string;
  minutes: number;
  reserved: number;
  expiringMinutes: number;
  expiringOn: string;
}
interface Balance {
  workdayMinutes: number;
  lines: BalanceLine[];
}
interface Preview {
  minutes: number;
  days: { date: string; minutes: number; skipped: string }[];
  balance: string;
  balanceLabel: string;
  balanceBefore: number;
  balanceAfter: number;
  workdayMinutes: number;
}
interface LeaveReq {
  id: number;
  docId: number;
  docNo: string;
  leaveName: string;
  startDate: string;
  endDate: string;
  part: string;
  startTime: string;
  endTime: string;
  minutes: number;
  status: string;
  statusLabel: string;
  hasReason: boolean;
  /** 승인 때 부재 설정을 만들지 못한 이유 등 안내(사유가 아니다). */
  note: string;
  hasAbsence: boolean;
}
interface Candidate {
  id: number;
  name: string;
  title: string;
  unitName: string;
  away: boolean;
}
interface LedgerLine {
  id: number;
  balanceLabel: string;
  kindLabel: string;
  minutes: number;
  reason: string;
  expiresOn: string;
  createdAt: string;
}

const STATUS_TONE: Record<string, "info" | "ok" | "bad" | "muted" | "warn"> = { pending: "info", approved: "ok", rejected: "bad", recalled: "muted", canceled: "muted" };
const today = () => new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });

export function MyLeavePage() {
  const toast = useToast();
  const { can } = useAuth();
  const canDelegate = can("approval:delegate");
  const candidates = useLoad(() => (canDelegate ? api.get<Candidate[]>("/api/approvals/delegations/candidates") : Promise.resolve([] as Candidate[])), [canDelegate]);
  const types = useLoad(() => api.get<{ types: LeaveType[] }>("/api/attendance/leave/types"), []);
  const balance = useLoad(() => api.get<Balance>("/api/attendance/leave/balance"), []);
  const mine = useLoad(() => api.get<{ requests: LeaveReq[] }>("/api/attendance/leave/requests"), []);
  const [showLedger, setShowLedger] = useState(false);
  const ledger = useLoad(() => (showLedger ? api.get<{ lines: LedgerLine[] }>("/api/attendance/leave/ledger?limit=50") : Promise.resolve(null)), [showLedger]);
  const [code, setCode] = useState("annual");
  const [part, setPart] = useState("full");
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(today());
  const [startTime, setStartTime] = useState("09:00");
  const [endTime, setEndTime] = useState("11:00");
  const [reason, setReason] = useState("");
  const [delegateId, setDelegateId] = useState(0);
  const [proof, setProof] = useState<{ id: number; name: string } | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [cancelFor, setCancelFor] = useState<LeaveReq | null>(null);

  const body = () => ({ leaveCode: code, startDate: from, endDate: part === "full" ? to : from, part, startTime: part === "hours" ? startTime : "", endTime: part === "hours" ? endTime : "", reason, delegateUserId: delegateId, proofAttachmentId: proof?.id ?? 0 });

  async function uploadProof(files: FileList | null) {
    if (!files || files.length === 0) return;
    setError("");
    try {
      const fd = new FormData();
      fd.append("file", files[0]);
      const r = await apiFetch<{ id: number; filename: string }>("/api/approval-docs/attachments", { method: "POST", body: fd });
      setProof({ id: r.id, name: r.filename });
      setPreview(null);
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function doPreview() {
    setError("");
    setPreview(null);
    try {
      setPreview(await api.post<Preview>("/api/attendance/leave/preview", body()));
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.post("/api/attendance/leave/requests", body());
      toast("휴가를 신청했습니다. 결재가 끝나면 알려 드립니다.");
      setPreview(null);
      setReason("");
      setProof(null);
      setDelegateId(0);
      await Promise.all([balance.reload(), mine.reload()]);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function cancel(r: LeaveReq) {
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/attendance/leave/requests/${r.id}/cancel`, { reason: "" });
      toast(r.status === "pending" ? "신청을 취소했습니다." : "휴가를 취소했습니다. 잔여가 복원되었습니다.");
      setCancelFor(null);
      await Promise.all([balance.reload(), mine.reload()]);
    } catch (err) {
      setError(errorText(err));
      setCancelFor(null);
    } finally {
      setBusy(false);
    }
  }

  if (types.error) return <Notice tone="error">{types.error}</Notice>;
  const wd = balance.data?.workdayMinutes ?? 480;
  const selected = types.data?.types.find((t) => t.code === code);
  return (
    <section>
      <PageHead title="내 휴가" />
      <div className="card-grid" aria-label="내 잔여">
        {(balance.data?.lines ?? []).map((l) => (
          <div key={l.balance} className="card" role="group" aria-label={`${l.label} 잔여`}>
            <h2>{l.label}</h2>
            <p className="card-figure">{fmtMinutes(l.minutes, wd)}</p>
            <p className="hint">
              {l.reserved > 0 && <>결재 대기 {fmtMinutes(l.reserved, wd)} 포함 · </>}
              {l.expiringMinutes > 0 ? `${l.expiringOn} 까지 ${fmtMinutes(l.expiringMinutes, wd)} 소멸 예정` : "30일 안에 소멸하는 연차 없음"}
            </p>
          </div>
        ))}
      </div>
      <h2>휴가 신청</h2>
      {error && <Notice tone="error">{error}</Notice>}
      <form onSubmit={submit} aria-label="휴가 신청 양식">
        <div className="field">
          <label htmlFor="lv-type">휴가 종류</label>
          <select id="lv-type" value={code} onChange={(e) => { setCode(e.target.value); setPreview(null); }}>
            {(types.data?.types ?? []).map((t) => (
              <option key={t.code} value={t.code}>
                {t.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="lv-proof">증빙 첨부{selected?.proofRequired ? " *" : "(선택)"}</label>
          <input id="lv-proof" type="file" onChange={(e) => void uploadProof(e.target.files)} />
          {proof && <span className="hint">첨부됨: {proof.name}</span>}
          <span className="hint">{selected?.proofRequired ? "이 휴가는 증빙 첨부가 필요합니다. " : ""}증빙은 신청자·직속 조직장·인사 담당만 내려받을 수 있고 열람 기록이 남습니다.</span>
        </div>
        {canDelegate && (candidates.data ?? []).length > 0 && (
          <div className="field">
            <label htmlFor="lv-delegate">대결자(선택)</label>
            <select id="lv-delegate" value={delegateId} onChange={(e) => { setDelegateId(Number(e.target.value)); setPreview(null); }}>
              <option value={0}>지정하지 않음</option>
              {(candidates.data ?? []).filter((c) => !c.away).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} ({c.unitName ? `${c.unitName} ` : ""}{c.title})
                </option>
              ))}
            </select>
            <span className="hint">승인되면 휴가 기간이 부재로 등록되고, 대결자가 결재를 대신합니다. 휴가를 취소하면 부재도 해제됩니다.</span>
          </div>
        )}
        <div className="field">
          <label htmlFor="lv-part">구간</label>
          <select id="lv-part" value={part} onChange={(e) => { setPart(e.target.value); setPreview(null); }}>
            {Object.entries(PART_LABEL).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="lv-from">{part === "full" ? "시작일" : "날짜"}</label>
          <input id="lv-from" type="date" value={from} onChange={(e) => { setFrom(e.target.value); if (part !== "full" || to < e.target.value) setTo(e.target.value); setPreview(null); }} required />
        </div>
        {part === "full" && (
          <div className="field">
            <label htmlFor="lv-to">종료일</label>
            <input id="lv-to" type="date" value={to} min={from} onChange={(e) => { setTo(e.target.value); setPreview(null); }} required />
          </div>
        )}
        {part === "hours" && (
          <>
            <div className="field">
              <label htmlFor="lv-st">시작 시각</label>
              <input id="lv-st" type="time" value={startTime} onChange={(e) => { setStartTime(e.target.value); setPreview(null); }} required />
            </div>
            <div className="field">
              <label htmlFor="lv-et">종료 시각</label>
              <input id="lv-et" type="time" value={endTime} onChange={(e) => { setEndTime(e.target.value); setPreview(null); }} required />
            </div>
          </>
        )}
        <div className="field">
          <label htmlFor="lv-reason">사유(선택)</label>
          <textarea id="lv-reason" value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
          <span className="hint">병명·진단 내용은 적지 마세요. 사유는 본인·직속 조직장·인사 담당만 볼 수 있고 열람 기록이 남습니다. 지난 날짜의 휴가는 사유가 필요합니다.</span>
        </div>
        <div className="actions">
          <button type="button" className="btn-secondary" disabled={busy} onClick={() => void doPreview()}>
            계산해 보기
          </button>
          <button type="submit" disabled={busy}>
            신청
          </button>
        </div>
      </form>
      {preview && (
        <Notice tone="info" role="status">
          <div>
            사용 분량 <strong>{fmtMinutes(preview.minutes, preview.workdayMinutes)}</strong>
            {preview.balance && (
              <>
                {" "}
                · {preview.balanceLabel} 잔여 {fmtMinutes(preview.balanceBefore, preview.workdayMinutes)} → {fmtMinutes(preview.balanceAfter, preview.workdayMinutes)}
              </>
            )}
          </div>
          {preview.days.filter((d) => d.skipped).length > 0 && <div className="hint">세지 않은 날: {preview.days.filter((d) => d.skipped).map((d) => `${d.date}(${d.skipped})`).join(", ")}</div>}
        </Notice>
      )}
      <h2>내 신청</h2>
      {mine.data && mine.data.requests.length === 0 && <EmptyState>신청한 휴가가 없습니다.</EmptyState>}
      {mine.data && mine.data.requests.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="내 휴가 신청">
            <thead>
              <tr>
                <th>기간</th>
                <th>종류</th>
                <th>분량</th>
                <th>상태</th>
                <th>결재 문서</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {mine.data.requests.map((r) => (
                <tr key={r.id}>
                  <td data-label="기간">
                    {r.startDate === r.endDate ? r.startDate : `${r.startDate} ~ ${r.endDate}`}
                    {r.part !== "full" && <div className="hint">{r.part === "hours" ? `${r.startTime}~${r.endTime}` : PART_LABEL[r.part]}</div>}
                  </td>
                  <td data-label="종류">{r.leaveName}</td>
                  <td data-label="분량" className="nowrap">{fmtMinutes(r.minutes, wd)}</td>
                  <td data-label="상태">
                    <Badge tone={STATUS_TONE[r.status] ?? "muted"}>{r.statusLabel}</Badge>
                    {r.note && <div className="hint">{r.note}</div>}
                    {r.hasAbsence && <div className="hint">부재(대결) 설정됨</div>}
                  </td>
                  <td data-label="결재 문서">
                    <Link to={`/docs/${r.docId}`}>{r.docNo || "문서 보기"}</Link>
                  </td>
                  <td>
                    {(r.status === "pending" || (r.status === "approved" && r.startDate > today())) && (
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setCancelFor(r)}>
                        취소
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <p>
        <button type="button" className="btn-secondary" onClick={() => setShowLedger((v) => !v)}>
          {showLedger ? "잔여 이력 접기" : "잔여 이력 보기"}
        </button>
      </p>
      {showLedger && ledger.data && (
        <TableWrap>
          <table className="stack-table" aria-label="잔여 이력">
            <thead>
              <tr>
                <th>일시</th>
                <th>구분</th>
                <th>종류</th>
                <th>변동</th>
                <th>사유</th>
              </tr>
            </thead>
            <tbody>
              {ledger.data.lines.map((l) => (
                <tr key={l.id}>
                  <td data-label="일시" className="nowrap">{new Date(l.createdAt).toLocaleDateString("ko-KR")}</td>
                  <td data-label="구분">{l.kindLabel}</td>
                  <td data-label="종류">{l.balanceLabel}</td>
                  <td data-label="변동" className="nowrap">{l.minutes > 0 ? "+" : ""}{fmtMinutes(l.minutes, wd)}</td>
                  <td data-label="사유">{l.reason}{l.expiresOn && <span className="hint"> · {l.expiresOn} 까지</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {cancelFor && (
        <ConfirmDialog title="휴가 취소" confirmLabel="취소하기" cancelLabel="닫기" busy={busy} onConfirm={() => void cancel(cancelFor)} onCancel={() => setCancelFor(null)}>
          <p>
            {cancelFor.startDate === cancelFor.endDate ? cancelFor.startDate : `${cancelFor.startDate} ~ ${cancelFor.endDate}`} {cancelFor.leaveName}
            {cancelFor.status === "pending" ? " 신청을 취소합니다. 결재가 회수되고 예약한 잔여가 돌아옵니다." : " 휴가를 취소합니다. 결재선에 있던 분들에게 알림이 가고 잔여가 복원됩니다."}
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}

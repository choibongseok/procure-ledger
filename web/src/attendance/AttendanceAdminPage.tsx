// (web/src/attendance). 근태 관리(B50 9.4, 13.1): 근태 마감·해제, 직접 정정, 근무 유형 배정. 권한 attendance:hr(인사 담당).
// 마감하면 그 달의 정정과 소급 휴가 입력이 막히고, 해제는 사유가 필요하며 감사에 남는다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, ConfirmDialog, EmptyState, Notice, PageHead, TableWrap, useToast } from "../layout/ui";
import { PersonPicker } from "../org/PersonPicker";
import type { PersonCard } from "../org/types";
import { errorText, useLoad } from "../purchase/useLoad";
import type { WorkType } from "./WorkTypesSection";

interface Close {
  month: string;
  closed: boolean;
  closedBy: string;
  closedAt: string;
  reopenedBy: string;
  reopenedAt: string;
  reopenReason: string;
}

const thisMonth = () => new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" }).slice(0, 7);
const prevMonth = () => {
  const [y, m] = thisMonth().split("-").map(Number);
  return m === 1 ? `${y - 1}-12` : `${y}-${String(m - 1).padStart(2, "0")}`;
};
const today = () => new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });

export function AttendanceAdminPage() {
  const toast = useToast();
  const { can } = useAuth();
  const [exportMonth, setExportMonth] = useState(prevMonth());
  const closes = useLoad(() => api.get<{ closes: Close[] }>("/api/attendance/admin/closes"), []);
  const types = useLoad(() => api.get<{ workTypes: WorkType[] }>("/api/attendance/work-types"), []);
  const [month, setMonth] = useState(prevMonth());
  const [reopenFor, setReopenFor] = useState<Close | null>(null);
  const [reopenReason, setReopenReason] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [cUser, setCUser] = useState<PersonCard | null>(null);
  const [cDate, setCDate] = useState("");
  const [cIn, setCIn] = useState("");
  const [cOut, setCOut] = useState("");
  const [cReason, setCReason] = useState("");
  const [aUser, setAUser] = useState<PersonCard | null>(null);
  const [aType, setAType] = useState("");
  const [aFrom, setAFrom] = useState(today());

  async function run(fn: () => Promise<unknown>, done: string) {
    setBusy(true);
    setError("");
    try {
      await fn();
      toast(done);
      await closes.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const closeMonth = (e: FormEvent) => {
    e.preventDefault();
    void run(() => api.post("/api/attendance/admin/closes", { month }), `${month} 근태를 마감했습니다.`);
  };
  const reopen = async () => {
    if (!reopenFor) return;
    const c = reopenFor;
    await run(() => api.post(`/api/attendance/admin/closes/${c.month}/reopen`, { reason: reopenReason }), `${c.month} 마감을 해제했습니다.`);
    setReopenFor(null);
    setReopenReason("");
  };
  const correct = (e: FormEvent) => {
    e.preventDefault();
    void run(() => api.post("/api/attendance/admin/correct", { userId: cUser?.id ?? 0, workDate: cDate, checkIn: cIn, checkOut: cOut, reason: cReason }), "기록을 정정했습니다.");
  };
  const assign = (e: FormEvent) => {
    e.preventDefault();
    void run(() => api.post("/api/attendance/admin/assignments", { userId: aUser?.id ?? 0, workTypeId: Number(aType), validFrom: aFrom }), "근무 유형을 배정했습니다.");
  };

  return (
    <section>
      <PageHead title="근태 관리" />
      {error && <Notice tone="error">{error}</Notice>}
      <h2>근태 마감</h2>
      <p className="hint">끝난 달을 마감하면 그 달의 출퇴근 정정과 소급 휴가 신청이 막힙니다(인사 담당은 소급 휴가를 입력할 수 있습니다). 월 마감(회계)과는 별개입니다.</p>
      <form onSubmit={closeMonth} aria-label="근태 마감 양식">
        <div className="field">
          <label htmlFor="cl-month">마감할 달</label>
          <input id="cl-month" type="month" value={month} max={prevMonth()} onChange={(e) => setMonth(e.target.value)} required />
        </div>
        <button type="submit" disabled={busy}>
          마감
        </button>
      </form>
      {closes.data && closes.data.closes.length === 0 && <EmptyState>마감한 달이 없습니다.</EmptyState>}
      {closes.data && closes.data.closes.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="근태 마감 현황">
            <thead>
              <tr>
                <th>달</th>
                <th>상태</th>
                <th>처리</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {closes.data.closes.map((c) => (
                <tr key={c.month}>
                  <td data-label="달">{c.month}</td>
                  <td data-label="상태">
                    <Badge tone={c.closed ? "ok" : "muted"}>{c.closed ? "마감됨" : "해제됨"}</Badge>
                  </td>
                  <td data-label="처리">
                    {c.closed ? `${c.closedBy} (${new Date(c.closedAt).toLocaleDateString("ko-KR")})` : `${c.reopenedBy} 해제: ${c.reopenReason}`}
                  </td>
                  <td>
                    {c.closed && (
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setReopenFor(c)}>
                        마감 해제
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {can("attendance:export") && (
        <>
          <h2>월 근태 내보내기</h2>
          <p className="hint">급여 연동용 월 집계(출근·지각·조퇴·근로시간·휴가 일수·결근·52시간 초과 주 수)입니다. 급여 금액과 수당은 계산하지 않고 휴가 사유는 담기지 않습니다. 마감하지 않은 달은 "미확정"으로 표시됩니다. 사번이 들어 있어 내보내기 기록과 개인정보 열람 기록이 남습니다.</p>
          <div className="field">
            <label htmlFor="ex-month">내보낼 달</label>
            <input id="ex-month" type="month" value={exportMonth} onChange={(e) => setExportMonth(e.target.value)} />
          </div>
          <ExportButtons path="/api/attendance/export" params={{ month: exportMonth }} disabled={!exportMonth} />
        </>
      )}
      <h2>출퇴근 직접 정정</h2>
      <p className="hint">본인 기록은 정정할 수 없습니다. 사유는 감사 기록에 그대로 남습니다.</p>
      <form onSubmit={correct} aria-label="직접 정정 양식">
        <PersonPicker label="직원" value={cUser} onChange={setCUser} />
        <div className="field">
          <label htmlFor="dc-date">날짜</label>
          <input id="dc-date" type="date" value={cDate} onChange={(e) => setCDate(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="dc-in">출근 시각</label>
          <input id="dc-in" type="time" value={cIn} onChange={(e) => setCIn(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="dc-out">퇴근 시각</label>
          <input id="dc-out" type="time" value={cOut} onChange={(e) => setCOut(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="dc-reason">사유</label>
          <textarea id="dc-reason" value={cReason} maxLength={200} onChange={(e) => setCReason(e.target.value)} required />
        </div>
        <button type="submit" disabled={busy || !cUser}>
          정정
        </button>
      </form>
      <h2>근무 유형 배정</h2>
      <p className="hint">효력일부터 적용되고 이력으로 남습니다. 배정이 없는 직원은 기본 근무(고정 9~18시)로 봅니다.</p>
      <form onSubmit={assign} aria-label="근무 유형 배정 양식">
        <PersonPicker label="직원" value={aUser} onChange={setAUser} />
        <div className="field">
          <label htmlFor="as-type">근무 유형</label>
          <select id="as-type" value={aType} onChange={(e) => setAType(e.target.value)} required>
            <option value="">고르세요</option>
            {(types.data?.workTypes ?? []).filter((w) => !w.default && w.active).map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="as-from">효력일</label>
          <input id="as-from" type="date" value={aFrom} onChange={(e) => setAFrom(e.target.value)} required />
        </div>
        <button type="submit" disabled={busy || !aUser}>
          배정
        </button>
      </form>
      {reopenFor && (
        <ConfirmDialog title="마감 해제" confirmLabel="해제" cancelLabel="닫기" busy={busy} onConfirm={() => void reopen()} onCancel={() => setReopenFor(null)}>
          <p>{reopenFor.month} 근태 마감을 해제합니다. 해제하면 그 달의 정정이 다시 가능해지며 사유가 감사에 남습니다.</p>
          <div className="field">
            <label htmlFor="ro-reason">해제 사유</label>
            <input id="ro-reason" value={reopenReason} maxLength={200} onChange={(e) => setReopenReason(e.target.value)} required />
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

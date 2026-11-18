// (web/src/attendance). 내 근태(B50 7절, 13.1): 출근·퇴근 단추(서버 시각), 이번 주 근로시간과 52시간 경고, 정정 요청, 내가 처리할 정정 요청.
// 시각은 서버가 정한다(화면은 시각을 보내지 않는다). 근로시간 기준은 운영 전 노무 검토가 필요하다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, ConfirmDialog, EmptyState, Notice, PageHead, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { dayLabel, fmtHours } from "./leaveFormat";

export interface AttDay {
  date: string;
  checkIn: string;
  checkOut: string;
  workMinutes: number;
  missing: boolean;
  late: boolean;
  earlyLeave: boolean;
  coreMiss: boolean;
  holidayNote: string;
  leaveNote: string;
  correctionId: number;
}
export interface AttWeek {
  weekStart: string;
  weekEnd: string;
  days: AttDay[];
  totalMinutes: number;
  level: string;
  levelLabel: string;
  weeklyTarget: number;
  belowTarget: boolean;
  workTypeName: string;
  cautionMinutes: number;
  overMinutes: number;
}
interface TodayResp {
  today: AttDay;
  week: AttWeek;
  missingYesterday: string;
  warnings: string[];
}
export interface Correction {
  id: number;
  userId: number;
  userName: string;
  workDate: string;
  oldIn: string;
  oldOut: string;
  newIn: string;
  newOut: string;
  reason: string;
  requestedName: string;
  status: string;
  statusLabel: string;
  approvedName: string;
  decideNote: string;
}

const TONE: Record<string, "info" | "ok" | "bad" | "muted"> = { requested: "info", applied: "ok", rejected: "bad" };

export function dayMarks(d: AttDay): string[] {
  const out: string[] = [];
  if (d.holidayNote) out.push(d.holidayNote);
  if (d.leaveNote) out.push(d.leaveNote);
  if (d.late) out.push("지각");
  if (d.earlyLeave) out.push("조퇴");
  if (d.coreMiss) out.push("코어 타임 미충족");
  if (d.missing) out.push("기록 누락");
  return out;
}

export function correctionText(c: Correction): string {
  const part = (label: string, a: string, b: string) => (b && b !== a ? `${label} ${a || "없음"} → ${b}` : "");
  return [part("출근", c.oldIn, c.newIn), part("퇴근", c.oldOut, c.newOut)].filter(Boolean).join(", ");
}

export function AttendancePage() {
  const toast = useToast();
  const today = useLoad(() => api.get<TodayResp>("/api/attendance/today"), []);
  const corrs = useLoad(() => api.get<{ mine: Correction[]; toDecide: Correction[] }>("/api/attendance/corrections"), []);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [wDate, setWDate] = useState("");
  const [wIn, setWIn] = useState("");
  const [wOut, setWOut] = useState("");
  const [wReason, setWReason] = useState("");
  const [decide, setDecide] = useState<{ c: Correction; approve: boolean } | null>(null);
  const [note, setNote] = useState("");

  const state = today.data;
  async function clock(kind: "check-in" | "check-out") {
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/attendance/${kind}`, {});
      toast(kind === "check-in" ? "출근을 기록했습니다." : "퇴근을 기록했습니다.");
      await today.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function requestCorrection(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.post("/api/attendance/corrections", { workDate: wDate, checkIn: wIn, checkOut: wOut, reason: wReason });
      toast("정정을 요청했습니다. 직속 조직장이 확인하면 반영됩니다.");
      setWReason("");
      setWIn("");
      setWOut("");
      await Promise.all([corrs.reload(), today.reload()]);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirmDecide() {
    if (!decide) return;
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/attendance/corrections/${decide.c.id}/decide`, { approve: decide.approve, note });
      toast(decide.approve ? "정정을 승인했습니다." : "정정을 반려했습니다.");
      setDecide(null);
      setNote("");
      await Promise.all([corrs.reload(), today.reload()]);
    } catch (err) {
      setError(errorText(err));
      setDecide(null);
    } finally {
      setBusy(false);
    }
  }

  if (today.error) return <Notice tone="error">{today.error}</Notice>;
  const t = state?.today;
  const week = state?.week;
  return (
    <section>
      <PageHead title="내 근태" />
      {error && <Notice tone="error">{error}</Notice>}
      {state?.missingYesterday && <Notice tone="warn">{state.missingYesterday}</Notice>}
      {(state?.warnings ?? []).map((w) => (
        <Notice key={w} tone="info">
          {w}
        </Notice>
      ))}
      {week && week.level !== "" && (
        <Notice tone={week.level === "over" ? "error" : "warn"} role="status">
          이번 주 근로시간이 {week.level === "over" ? `${week.overMinutes / 60}시간을 넘었습니다` : `${week.cautionMinutes / 60}시간에 이르렀습니다`}({fmtHours(week.totalMinutes)}). 출퇴근과 휴가 신청은 그대로 할 수 있습니다.
        </Notice>
      )}
      <div className="card-grid" aria-label="오늘 근태">
        <div className="card" role="group" aria-label="오늘 출퇴근">
          <h2>오늘</h2>
          <p className="card-figure">
            {t?.checkIn ? `출근 ${t.checkIn}` : "출근 전"}
            {t?.checkOut ? ` · 퇴근 ${t.checkOut}` : ""}
          </p>
          <div className="actions">
            <button type="button" disabled={busy || !!t?.checkIn} onClick={() => void clock("check-in")}>
              출근
            </button>
            <button type="button" className="btn-secondary" disabled={busy || !t?.checkIn || !!t?.checkOut} onClick={() => void clock("check-out")}>
              퇴근
            </button>
          </div>
          <p className="hint">시각은 서버가 정합니다. 근무 유형: {week?.workTypeName ?? "-"}</p>
        </div>
        <div className="card" role="group" aria-label="이번 주 근로시간">
          <h2>이번 주</h2>
          <p className="card-figure">{fmtHours(week?.totalMinutes ?? 0)}</p>
          <p className="hint">
            휴게 시간을 뺀 근로시간입니다{week?.level ? ` · ${week.levelLabel}` : ""}
            {week?.weeklyTarget ? ` · 주 소정 ${fmtHours(week.weeklyTarget)}` : ""}
          </p>
        </div>
      </div>
      <h2>이번 주 기록</h2>
      <TableWrap>
        <table className="stack-table" aria-label="이번 주 출퇴근 기록">
          <thead>
            <tr>
              <th>날짜</th>
              <th>출근</th>
              <th>퇴근</th>
              <th>근로시간</th>
              <th>비고</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(week?.days ?? []).map((d) => (
              <tr key={d.date}>
                <td data-label="날짜">{dayLabel(d.date)}</td>
                <td data-label="출근">{d.checkIn || "-"}</td>
                <td data-label="퇴근">{d.checkOut || "-"}</td>
                <td data-label="근로시간">{d.workMinutes > 0 ? fmtHours(d.workMinutes) : "-"}</td>
                <td data-label="비고">
                  {dayMarks(d).map((m) => (
                    <Badge key={m} tone={m === "지각" || m === "조퇴" || m === "기록 누락" || m === "코어 타임 미충족" ? "warn" : "muted"}>
                      {m}
                    </Badge>
                  ))}
                  {d.correctionId > 0 && <Badge tone="info">정정 대기</Badge>}
                </td>
                <td>
                  {d.date <= (state?.today.date ?? "") && (
                    <button type="button" className="btn-secondary btn-sm" onClick={() => { setWDate(d.date); setWIn(d.checkIn); setWOut(d.checkOut); }}>
                      이 날 정정
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {week?.belowTarget && <p className="hint">선택 근무: 지난 주 근로시간이 주 소정({fmtHours(week.weeklyTarget)})에 못 미칩니다. 경고는 아니며 안내입니다.</p>}
      <h2>출퇴근 정정 요청</h2>
      <form onSubmit={requestCorrection} aria-label="정정 요청 양식">
        <div className="field">
          <label htmlFor="co-date">날짜</label>
          <input id="co-date" type="date" value={wDate} onChange={(e) => setWDate(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="co-in">출근 시각</label>
          <input id="co-in" type="time" value={wIn} onChange={(e) => setWIn(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="co-out">퇴근 시각</label>
          <input id="co-out" type="time" value={wOut} onChange={(e) => setWOut(e.target.value)} />
          <span className="hint">바꿀 시각만 입력하세요. 마감된 달의 기록은 고칠 수 없습니다.</span>
        </div>
        <div className="field">
          <label htmlFor="co-reason">사유</label>
          <textarea id="co-reason" value={wReason} maxLength={200} onChange={(e) => setWReason(e.target.value)} required />
        </div>
        <button type="submit" disabled={busy}>
          정정 요청
        </button>
      </form>
      {(corrs.data?.toDecide ?? []).length > 0 && (
        <>
          <h2>처리할 정정 요청</h2>
          <TableWrap>
            <table className="stack-table" aria-label="처리할 정정 요청">
              <thead>
                <tr>
                  <th>직원</th>
                  <th>날짜</th>
                  <th>변경</th>
                  <th>사유</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(corrs.data?.toDecide ?? []).map((c) => (
                  <tr key={c.id}>
                    <td data-label="직원">{c.userName}</td>
                    <td data-label="날짜">{dayLabel(c.workDate)}</td>
                    <td data-label="변경">{correctionText(c)}</td>
                    <td data-label="사유">{c.reason}</td>
                    <td>
                      <button type="button" className="btn-sm" disabled={busy} onClick={() => setDecide({ c, approve: true })}>
                        승인
                      </button>{" "}
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setDecide({ c, approve: false })}>
                        반려
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
      <h2>내 정정 요청</h2>
      {corrs.data && corrs.data.mine.length === 0 && <EmptyState>정정을 요청한 적이 없습니다.</EmptyState>}
      {corrs.data && corrs.data.mine.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="내 정정 요청">
            <thead>
              <tr>
                <th>날짜</th>
                <th>변경</th>
                <th>사유</th>
                <th>상태</th>
                <th>처리</th>
              </tr>
            </thead>
            <tbody>
              {corrs.data.mine.map((c) => (
                <tr key={c.id}>
                  <td data-label="날짜">{dayLabel(c.workDate)}</td>
                  <td data-label="변경">{correctionText(c)}</td>
                  <td data-label="사유">{c.reason}</td>
                  <td data-label="상태">
                    <Badge tone={TONE[c.status] ?? "muted"}>{c.statusLabel}</Badge>
                  </td>
                  <td data-label="처리">
                    {c.approvedName ? c.approvedName : "-"}
                    {c.decideNote && <div className="hint">{c.decideNote}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {decide && (
        <ConfirmDialog title={decide.approve ? "정정 승인" : "정정 반려"} confirmLabel={decide.approve ? "승인" : "반려"} cancelLabel="닫기" busy={busy} onConfirm={() => void confirmDecide()} onCancel={() => setDecide(null)}>
          <p>
            {decide.c.userName}님의 {dayLabel(decide.c.workDate)} 정정({correctionText(decide.c)})을 {decide.approve ? "승인합니다. 기록에 바로 반영됩니다." : "반려합니다."}
          </p>
          <div className="field">
            <label htmlFor="co-note">처리 의견(선택)</label>
            <input id="co-note" value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

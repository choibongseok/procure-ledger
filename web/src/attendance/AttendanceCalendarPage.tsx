// (web/src/attendance). 부서 달력(B50 9.1): 한 달의 소속원별 휴가·부재 일정. 서버가 열람 범위를 정한다(본인·직속 조직장·인사 담당은 종류가, 같은 팀 동료는 "휴가"/"부재"만).
// 사유는 이 화면에 없다(휴가 신청 상세에서 열람 기록과 함께 본다).
import { useState } from "react";
import { api } from "../api/client";
import { EmptyState, Notice, PageHead, TableWrap } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";
import { WEEKDAY } from "./leaveFormat";

interface CalDay {
  date: string;
  weekend: boolean;
  holiday: string;
}
interface Entry {
  start: string;
  end: string;
  label: string;
  part: string;
}
interface Person {
  userId: number;
  name: string;
  unit: string;
  entries: Entry[];
}
interface Cal {
  month: string;
  days: CalDay[];
  people: Person[];
  scope: string;
}

const thisMonth = () => new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" }).slice(0, 7);
export function shiftMonth(m: string, delta: number): string {
  const [y, mo] = m.split("-").map(Number);
  const d = new Date(Date.UTC(y, mo - 1 + delta, 1));
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
}
const short = (d: string) => {
  const [, m, day] = d.split("-").map(Number);
  return `${m}. ${day}.`;
};
export function entryText(e: Entry): string {
  const period = e.start === e.end ? short(e.start) : `${short(e.start)}~${short(e.end)}`;
  return `${period} ${e.label}${e.part ? `(${e.part})` : ""}`;
}
/** 그 날에 걸친 일정(있으면 첫 건). */
export function entryOn(p: Person, date: string): Entry | undefined {
  return p.entries.find((e) => e.start <= date && date <= e.end);
}

export function AttendanceCalendarPage() {
  const [month, setMonth] = useState(thisMonth());
  const cal = useLoad(() => api.get<Cal>(`/api/attendance/calendar?month=${month}`), [month]);
  if (cal.error) return <Notice tone="error">{cal.error}</Notice>;
  const data = cal.data;
  return (
    <section>
      <PageHead title="부서 달력" />
      <div className="actions">
        <button type="button" className="btn-secondary btn-sm" onClick={() => setMonth(shiftMonth(month, -1))}>
          이전 달
        </button>
        <strong aria-live="polite">{month.replace("-", "년 ")}월</strong>
        <button type="button" className="btn-secondary btn-sm" onClick={() => setMonth(shiftMonth(month, 1))}>
          다음 달
        </button>
        {data?.scope && <span className="hint"> 범위: {data.scope}</span>}
      </div>
      <p className="hint">휴가와 부재 일정입니다. 같은 팀 동료의 일정은 종류 없이 "휴가" 또는 "부재"로만 보입니다.</p>
      {data && data.people.length === 0 && <EmptyState>보여 줄 소속원이 없습니다.</EmptyState>}
      {data && data.people.length > 0 && (
        <>
          <TableWrap>
            <table aria-label="월 달력">
              <thead>
                <tr>
                  <th>이름</th>
                  {data.days.map((d) => (
                    <th key={d.date} title={d.holiday || undefined} scope="col">
                      {Number(d.date.slice(8))}
                      <div className="hint">{WEEKDAY[new Date(`${d.date}T00:00:00Z`).getUTCDay()]}</div>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {data.people.map((p) => (
                  <tr key={p.userId}>
                    <th scope="row">{p.name}</th>
                    {data.days.map((d) => {
                      const e = entryOn(p, d.date);
                      return (
                        <td key={d.date} title={e ? entryText(e) : d.holiday || undefined} className={d.weekend || d.holiday ? "muted" : undefined}>
                          {e ? (e.part ? "반" : e.label.slice(0, 1)) : d.holiday ? "공" : ""}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <h2>일정 목록</h2>
          <TableWrap>
            <table className="stack-table" aria-label="휴가·부재 일정">
              <thead>
                <tr>
                  <th>이름</th>
                  <th>소속</th>
                  <th>일정</th>
                </tr>
              </thead>
              <tbody>
                {data.people
                  .filter((p) => p.entries.length > 0)
                  .map((p) => (
                    <tr key={p.userId}>
                      <td data-label="이름">{p.name}</td>
                      <td data-label="소속">{p.unit || "-"}</td>
                      <td data-label="일정">{p.entries.map(entryText).join(", ")}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </TableWrap>
          {data.days.some((d) => d.holiday) && (
            <p className="hint">공휴일: {data.days.filter((d) => d.holiday).map((d) => `${short(d.date)} ${d.holiday}`).join(", ")}</p>
          )}
        </>
      )}
    </section>
  );
}

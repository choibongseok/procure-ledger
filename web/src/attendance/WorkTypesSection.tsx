// (web/src/attendance). 근무 유형 설정(B50 7.2): 고정·시차·선택. 설정 화면의 한 절이다(attendance:policy).
// 주 52시간 같은 기준은 근로기준법 요약이며 운영 전에 노무 검토가 필요하다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, Notice, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export interface WorkType {
  id: number;
  code: string;
  name: string;
  kind: string;
  kindLabel: string;
  startTime: string;
  endTime: string;
  breakStart: string;
  breakEnd: string;
  coreStart: string;
  coreEnd: string;
  weeklyMinutes: number;
  active: boolean;
  default: boolean;
}

export function workTypeTimes(w: WorkType): string {
  if (w.kind === "flexible") return `코어 타임 ${w.coreStart}~${w.coreEnd}, 주 ${w.weeklyMinutes / 60}시간`;
  if (w.kind === "staggered") return `출근 ${w.startTime}~${w.endTime} 사이 선택`;
  return `${w.startTime}~${w.endTime}`;
}

export function WorkTypesSection() {
  const toast = useToast();
  const list = useLoad(() => api.get<{ workTypes: WorkType[] }>("/api/admin/attendance/work-types"), []);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [kind, setKind] = useState("fixed");
  const [start, setStart] = useState("09:00");
  const [end, setEnd] = useState("18:00");
  const [bs, setBs] = useState("12:00");
  const [be, setBe] = useState("13:00");
  const [cs, setCs] = useState("10:00");
  const [ce, setCe] = useState("15:00");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.post("/api/admin/attendance/work-types", {
        code,
        name,
        kind,
        startTime: kind === "flexible" ? "" : start,
        endTime: kind === "flexible" ? "" : end,
        breakStart: bs,
        breakEnd: be,
        coreStart: kind === "flexible" ? cs : "",
        coreEnd: kind === "flexible" ? ce : "",
        weeklyMinutes: 2400,
        active: true,
      });
      toast("근무 유형을 추가했습니다.");
      setCode("");
      setName("");
      await list.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function toggle(w: WorkType) {
    setBusy(true);
    setError("");
    try {
      await api.put(`/api/admin/attendance/work-types/${w.id}`, {
        name: w.name, kind: w.kind, startTime: w.startTime, endTime: w.endTime, breakStart: w.breakStart, breakEnd: w.breakEnd, coreStart: w.coreStart, coreEnd: w.coreEnd, weeklyMinutes: w.weeklyMinutes, active: !w.active,
      });
      await list.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-label="근무 유형">
      <h2>근무 유형</h2>
      <p className="hint">배정이 없는 직원은 기본 근무(고정 9~18시, 휴게 1시간)로 봅니다. 주 근로시간 48시간 이상은 주의, 52시간 초과는 경고로 알리며 출퇴근을 막지 않습니다(법정 기준은 운영 전 노무 검토가 필요합니다).</p>
      {error && <Notice tone="error">{error}</Notice>}
      <TableWrap>
        <table className="stack-table" aria-label="근무 유형 목록">
          <thead>
            <tr>
              <th>이름</th>
              <th>구분</th>
              <th>시간</th>
              <th>휴게</th>
              <th>상태</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(list.data?.workTypes ?? []).map((w) => (
              <tr key={w.id}>
                <td data-label="이름">
                  {w.name}
                  {w.default && <span className="hint"> · 기본</span>}
                </td>
                <td data-label="구분">{w.kindLabel}</td>
                <td data-label="시간">{workTypeTimes(w)}</td>
                <td data-label="휴게">{w.breakStart ? `${w.breakStart}~${w.breakEnd}` : "-"}</td>
                <td data-label="상태">
                  <Badge tone={w.active ? "ok" : "muted"}>{w.active ? "사용 중" : "사용 중지"}</Badge>
                </td>
                <td>
                  {!w.default && (
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void toggle(w)}>
                      {w.active ? "사용 중지" : "다시 사용"}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <h3>근무 유형 추가</h3>
      <form onSubmit={add} aria-label="근무 유형 추가 양식">
        <div className="field">
          <label htmlFor="wt-code">코드(영문 소문자)</label>
          <input id="wt-code" value={code} onChange={(e) => setCode(e.target.value)} pattern="[a-z][a-z0-9_]{1,29}" required />
        </div>
        <div className="field">
          <label htmlFor="wt-name">이름</label>
          <input id="wt-name" value={name} maxLength={30} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="wt-kind">구분</label>
          <select id="wt-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="fixed">고정(출근·퇴근 시각 고정)</option>
            <option value="staggered">시차(출근 가능 구간 중 선택)</option>
            <option value="flexible">선택(코어 타임만 정하고 주 총량)</option>
          </select>
        </div>
        {kind !== "flexible" && (
          <>
            <div className="field">
              <label htmlFor="wt-start">{kind === "fixed" ? "출근 시각" : "출근 가능 시작"}</label>
              <input id="wt-start" type="time" value={start} onChange={(e) => setStart(e.target.value)} required />
            </div>
            <div className="field">
              <label htmlFor="wt-end">{kind === "fixed" ? "퇴근 시각" : "출근 가능 끝"}</label>
              <input id="wt-end" type="time" value={end} onChange={(e) => setEnd(e.target.value)} required />
            </div>
          </>
        )}
        {kind === "flexible" && (
          <>
            <div className="field">
              <label htmlFor="wt-cs">코어 타임 시작</label>
              <input id="wt-cs" type="time" value={cs} onChange={(e) => setCs(e.target.value)} required />
            </div>
            <div className="field">
              <label htmlFor="wt-ce">코어 타임 끝</label>
              <input id="wt-ce" type="time" value={ce} onChange={(e) => setCe(e.target.value)} required />
            </div>
          </>
        )}
        <div className="field">
          <label htmlFor="wt-bs">휴게 시작</label>
          <input id="wt-bs" type="time" value={bs} onChange={(e) => setBs(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="wt-be">휴게 끝</label>
          <input id="wt-be" type="time" value={be} onChange={(e) => setBe(e.target.value)} />
        </div>
        <button type="submit" disabled={busy}>
          근무 유형 추가
        </button>
      </form>
    </section>
  );
}

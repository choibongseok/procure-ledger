// (web/src/attendance). 휴가 분량 표기. 서버 fmtMinutes 와 같은 규칙이다(하루 = 규칙의 하루 분량).

/** 분을 "1일 2시간 30분" 꼴로 쓴다. 0 은 "0분". */
export function fmtMinutes(m: number, workday = 480): string {
  const wd = workday > 0 ? workday : 480;
  const abs = Math.abs(m);
  const d = Math.floor(abs / wd);
  const rem = abs % wd;
  const parts: string[] = [];
  if (d > 0) parts.push(`${d}일`);
  if (Math.floor(rem / 60) > 0) parts.push(`${Math.floor(rem / 60)}시간`);
  if (rem % 60 > 0) parts.push(`${rem % 60}분`);
  const text = parts.length > 0 ? parts.join(" ") : "0분";
  return m < 0 ? `-${text}` : text;
}

export const PART_LABEL: Record<string, string> = { full: "종일", am: "오전 반차", pm: "오후 반차", hours: "시간 지정" };

export const BASIS_LABEL: Record<string, string> = { hire: "입사일 기준", fiscal: "회계연도 기준" };

/** 근로 분을 "52시간 10분" 꼴로 쓴다(주 근로시간 표기). */
export function fmtHours(m: number): string {
  const h = Math.floor(Math.abs(m) / 60);
  const mm = Math.abs(m) % 60;
  if (h === 0 && mm === 0) return "0분";
  return `${m < 0 ? "-" : ""}${h > 0 ? `${h}시간` : ""}${h > 0 && mm > 0 ? " " : ""}${mm > 0 ? `${mm}분` : ""}`;
}

export const WEEKDAY = ["일", "월", "화", "수", "목", "금", "토"];

/** 날짜(YYYY-MM-DD)를 "11. 10.(화)" 꼴로 쓴다. */
export function dayLabel(date: string): string {
  const [y, m, d] = date.split("-").map(Number);
  const w = new Date(Date.UTC(y, m - 1, d)).getUTCDay();
  return `${m}. ${d}.(${WEEKDAY[w]})`;
}

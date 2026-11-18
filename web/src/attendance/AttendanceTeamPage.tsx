// (web/src/attendance). 팀 근태 현황(B50 9.2): 오늘 출근·미출근·휴가 인원과 사람별 이번 주 근로시간(52시간 경고). 조직장은 본인 조직 소속원, 인사 담당은 전사.
// 인원 합계와 표는 같은 범위 규칙을 쓴다(범위 밖 사람은 어디에도 없다). 사유는 이 화면에 없다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, PageHead, TableWrap } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";
import { fmtHours } from "./leaveFormat";

interface Row {
  userId: number;
  name: string;
  unit: string;
  state: string;
  checkIn: string;
  checkOut: string;
  late: boolean;
  weekMinutes: number;
  weekLevel: string;
  missing: boolean;
}
interface Team {
  date: string;
  scope: string;
  total: number;
  present: number;
  notYet: number;
  onLeave: number;
  absent: number;
  pending: number;
  weekOver: number;
  weekCaution: number;
  rows: Row[];
  truncated: boolean;
  cautionMinutes: number;
  overMinutes: number;
}

const STATE_TONE: Record<string, "ok" | "warn" | "info" | "muted"> = { 출근: "ok", 미출근: "warn", 휴가: "info", 부재: "info", 휴일: "muted" };

export function AttendanceTeamPage() {
  const team = useLoad(() => api.get<Team>("/api/attendance/team"), []);
  if (team.error) return <Notice tone="error">{team.error}</Notice>;
  const t = team.data;
  return (
    <section>
      <PageHead title="팀 근태 현황" />
      {t && (
        <>
          <p className="hint">
            {t.date} 기준 · 범위: {t.scope || "내 조직"}
          </p>
          <div className="card-grid" aria-label="오늘 인원">
            <div className="card" role="group" aria-label="전체 인원">
              <h2>전체</h2>
              <p className="card-figure">{t.total}명</p>
            </div>
            <div className="card" role="group" aria-label="출근 인원">
              <h2>출근</h2>
              <p className="card-figure">{t.present}명</p>
            </div>
            <div className="card" role="group" aria-label="미출근 인원">
              <h2>미출근</h2>
              <p className="card-figure">{t.notYet}명</p>
            </div>
            <div className="card" role="group" aria-label="휴가 인원">
              <h2>휴가</h2>
              <p className="card-figure">{t.onLeave}명</p>
            </div>
            <div className="card" role="group" aria-label="부재 인원">
              <h2>부재</h2>
              <p className="card-figure">{t.absent}명</p>
            </div>
          </div>
          {(t.weekOver > 0 || t.weekCaution > 0) && (
            <Notice tone={t.weekOver > 0 ? "error" : "warn"} role="status">
              이번 주 근로시간이 {t.overMinutes / 60}시간을 넘은 사람 {t.weekOver}명, {t.cautionMinutes / 60}시간 이상인 사람 {t.weekCaution}명입니다. 알림일 뿐 출퇴근과 결재는 그대로 진행됩니다.
            </Notice>
          )}
          {t.pending > 0 && (
            <Notice tone="info">
              승인을 기다리는 출퇴근 정정 요청이 {t.pending}건 있습니다. <Link to="/attendance">내 근태에서 처리하기</Link>
            </Notice>
          )}
          {t.rows.length === 0 && <EmptyState>보여 줄 소속원이 없습니다.</EmptyState>}
          {t.rows.length > 0 && (
            <TableWrap>
              <table className="stack-table" aria-label="오늘 근태와 이번 주 근로시간">
                <thead>
                  <tr>
                    <th>이름</th>
                    <th>소속</th>
                    <th>오늘</th>
                    <th>출근</th>
                    <th>퇴근</th>
                    <th>이번 주 근로시간</th>
                    <th>비고</th>
                  </tr>
                </thead>
                <tbody>
                  {t.rows.map((r) => (
                    <tr key={r.userId}>
                      <td data-label="이름">{r.name}</td>
                      <td data-label="소속">{r.unit || "-"}</td>
                      <td data-label="오늘">
                        <Badge tone={STATE_TONE[r.state] ?? "muted"}>{r.state}</Badge>
                        {r.late && <Badge tone="warn">지각</Badge>}
                      </td>
                      <td data-label="출근">{r.checkIn || "-"}</td>
                      <td data-label="퇴근">{r.checkOut || "-"}</td>
                      <td data-label="이번 주 근로시간">
                        {fmtHours(r.weekMinutes)}
                        {r.weekLevel === "over" && <Badge tone="bad">52시간 초과</Badge>}
                        {r.weekLevel === "caution" && <Badge tone="warn">주의</Badge>}
                      </td>
                      <td data-label="비고">{r.missing ? "어제 퇴근 기록 없음" : "-"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
          {t.truncated && <p className="hint">사람이 많아 앞의 일부만 보입니다. 조직을 좁혀 보거나 월 근태 내보내기를 쓰세요.</p>}
        </>
      )}
    </section>
  );
}

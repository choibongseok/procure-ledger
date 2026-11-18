// 홈 카드(B50 13.1): 내 잔여 연차(attendance:self), 승인 대기 정정(attendance:team). 권한은 그 일을 실제로 하는 권한이고 모듈이 꺼지면 카드가 빠진다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { HomeCard } from "../layout/routeModule";
import { Notice } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";
import { fmtMinutes } from "./leaveFormat";

interface Bal {
  workdayMinutes: number;
  lines: { balance: string; label: string; minutes: number; reserved: number; expiringMinutes: number; expiringOn: string }[];
}

function LeaveBalanceCard() {
  const { data, error, loading } = useLoad(() => api.get<Bal>("/api/attendance/leave/balance"), []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  const annual = data.lines.find((l) => l.balance === "annual");
  if (!annual) return <p>연차 정보가 없습니다.</p>;
  return (
    <div>
      <p>
        사용 가능 연차 <strong>{fmtMinutes(annual.minutes, data.workdayMinutes)}</strong>
      </p>
      {annual.reserved > 0 && <p className="hint">결재 대기 {fmtMinutes(annual.reserved, data.workdayMinutes)} 포함</p>}
      {annual.expiringMinutes > 0 && (
        <p className="hint">
          {annual.expiringOn} 까지 {fmtMinutes(annual.expiringMinutes, data.workdayMinutes)} 소멸 예정
        </p>
      )}
      <Link to="/attendance/leave">내 휴가 보기</Link>
    </div>
  );
}

function CorrectionCard() {
  const { data, error, loading } = useLoad(() => api.get<{ toDecide: unknown[] }>("/api/attendance/corrections"), []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (loading || !data) return <p>불러오는 중…</p>;
  if (data.toDecide.length === 0) return <p>처리할 정정 요청이 없습니다.</p>;
  return (
    <p>
      승인 대기 정정 요청 <strong>{data.toDecide.length}건</strong> · <Link to="/attendance">처리하기</Link>
    </p>
  );
}

export const attendanceHomeCards: HomeCard[] = [
  { id: "attendance-balance", title: "내 잔여 연차", permission: "attendance:self", module: "attendance", order: 60, Component: LeaveBalanceCard },
  { id: "attendance-corrections", title: "승인 대기 정정", permission: "attendance:team", module: "attendance", order: 61, Component: CorrectionCard },
];

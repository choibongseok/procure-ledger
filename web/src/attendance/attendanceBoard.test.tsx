import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { AttendanceCalendarPage, entryText, shiftMonth } from "./AttendanceCalendarPage";
import { AttendanceTeamPage } from "./AttendanceTeamPage";
import { attendanceHomeCards } from "./homeCards";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = vi.mocked(api.get);
const user: CurrentUser = { id: 2, name: "직원", role: "staff", departmentId: 1 };
function mount(ui: React.ReactNode, perms: string[]) {
  return render(
    <AuthProvider initialUser={user} initialPermissions={perms as never}>
      <MemoryRouter>
        <ToastProvider>{ui}</ToastProvider>
      </MemoryRouter>
    </AuthProvider>,
  );
}
beforeEach(() => {
  get.mockReset();
});

describe("부서 달력(B50 6단계)", () => {
  it("월 달력과 일정 목록을 보이고, 동료 일정은 종류 없이 휴가·부재로만 보이며, 달을 옮기면 다시 부른다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/attendance/calendar")) {
        const month = path.split("month=")[1];
        return {
          month,
          scope: "구매1팀",
          days: [
            { date: `${month}-14`, weekend: true, holiday: "" },
            { date: `${month}-16`, weekend: false, holiday: "" },
            { date: `${month}-17`, weekend: false, holiday: "" },
          ],
          people: [
            { userId: 1, name: "김직원", unit: "구매1팀", entries: [{ start: `${month}-16`, end: `${month}-16`, label: "휴가", part: "" }] },
            { userId: 2, name: "박팀원", unit: "구매1팀", entries: [{ start: `${month}-17`, end: `${month}-17`, label: "부재", part: "" }] },
          ],
        };
      }
      return {};
    });
    mount(<AttendanceCalendarPage />, ["attendance:self"]);
    const list = await screen.findByRole("table", { name: "휴가·부재 일정" });
    expect(within(list).getByText(/휴가$/)).toBeInTheDocument();
    expect(within(list).getByText(/부재$/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/병가|연차|annual|sick/);
    expect(screen.getByText(/범위: 구매1팀/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "다음 달" }));
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/month=\d{4}-\d{2}$/)));
    expect(get.mock.calls.length).toBeGreaterThan(1);
  });

  it("달 계산과 일정 문구", () => {
    expect(shiftMonth("2026-12", 1)).toBe("2027-01");
    expect(shiftMonth("2026-01", -1)).toBe("2025-12");
    expect(entryText({ start: "2026-11-16", end: "2026-11-18", label: "연차", part: "" })).toBe("11. 16.~11. 18. 연차");
    expect(entryText({ start: "2026-11-16", end: "2026-11-16", label: "연차", part: "오전 반차" })).toBe("11. 16. 연차(오전 반차)");
  });
});

describe("팀 근태 현황(B50 6단계)", () => {
  it("인원 합계와 사람별 이번 주 근로시간, 52시간 경고를 한국어로 보인다", async () => {
    get.mockResolvedValue({
      date: "2026-11-16",
      scope: "구매1팀",
      total: 4,
      present: 1,
      notYet: 1,
      onLeave: 1,
      absent: 1,
      pending: 2,
      weekOver: 1,
      weekCaution: 0,
      truncated: false,
      cautionMinutes: 2880,
      overMinutes: 3120,
      rows: [
        { userId: 1, name: "김직원", unit: "구매1팀", state: "출근", checkIn: "09:20", checkOut: "", late: true, weekMinutes: 3200, weekLevel: "over", missing: false },
        { userId: 2, name: "박팀원", unit: "구매1팀", state: "미출근", checkIn: "", checkOut: "", late: false, weekMinutes: 0, weekLevel: "", missing: true },
        { userId: 3, name: "이휴가", unit: "구매1팀", state: "휴가", checkIn: "", checkOut: "", late: false, weekMinutes: 480, weekLevel: "", missing: false },
        { userId: 4, name: "최부재", unit: "구매1팀", state: "부재", checkIn: "", checkOut: "", late: false, weekMinutes: 0, weekLevel: "", missing: false },
      ],
    });
    mount(<AttendanceTeamPage />, ["attendance:self", "attendance:team"]);
    expect(await screen.findByRole("group", { name: "출근 인원" })).toHaveTextContent("1명");
    expect(screen.getByRole("group", { name: "휴가 인원" })).toHaveTextContent("1명");
    expect(screen.getByRole("group", { name: "부재 인원" })).toHaveTextContent("1명");
    expect(screen.getByText(/52시간을 넘은 사람 1명/)).toBeInTheDocument();
    expect(screen.getByText(/승인을 기다리는 출퇴근 정정 요청이 2건/)).toBeInTheDocument();
    const t = screen.getByRole("table", { name: "오늘 근태와 이번 주 근로시간" });
    expect(within(t).getByText("53시간 20분")).toBeInTheDocument();
    expect(within(t).getByText("52시간 초과")).toBeInTheDocument();
    expect(within(t).getByText("어제 퇴근 기록 없음")).toBeInTheDocument();
    expect(within(t).getByText("부재")).toBeInTheDocument(); // 휴직은 구분 없이 "부재"
    expect(document.body.textContent).not.toMatch(/휴직|질병/);
    expect(document.body.textContent).not.toMatch(/\b(over|caution|present)\b/);
  });
});

describe("홈 카드(B50)", () => {
  it("권한이 아니라 그 일을 하는 권한과 모듈에 건다", () => {
    const by = Object.fromEntries(attendanceHomeCards.map((c) => [c.id, c]));
    expect(by["attendance-balance"].permission).toBe("attendance:self");
    expect(by["attendance-corrections"].permission).toBe("attendance:team");
    expect(attendanceHomeCards.every((c) => c.module === "attendance")).toBe(true);
  });
});

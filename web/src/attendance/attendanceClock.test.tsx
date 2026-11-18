import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { AttendanceAdminPage } from "./AttendanceAdminPage";
import { AttendancePage } from "./AttendancePage";
import { fmtHours, dayLabel } from "./leaveFormat";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return {
    ...mod,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    apiDownload: vi.fn(),
    apiFetch: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  };
});
const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
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
  post.mockReset();
});

const day = (date: string, o: object = {}) => ({ date, checkIn: "", checkOut: "", workMinutes: 0, missing: false, late: false, earlyLeave: false, coreMiss: false, holidayNote: "", leaveNote: "", correctionId: 0, ...o });
const week = (level: string, total: number) => ({
  weekStart: "2026-11-09",
  weekEnd: "2026-11-15",
  days: [day("2026-11-09", { checkIn: "09:10", checkOut: "18:00", workMinutes: 470, late: true }), day("2026-11-10", { checkIn: "09:00" }), day("2026-11-11"), day("2026-11-14", { holidayNote: "휴일" })],
  totalMinutes: total,
  level,
  levelLabel: level === "over" ? "경고" : level === "caution" ? "주의" : "",
  weeklyTarget: 0,
  belowTarget: false,
  workTypeName: "기본 근무(고정 9~18시)",
  cautionMinutes: 2880,
  overMinutes: 3120,
});
function api2(today: object, w: object, corr: object = { mine: [], toDecide: [] }) {
  get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/attendance/today")) return { today, week: w, missingYesterday: "어제(11. 9.) 퇴근 기록이 없습니다. 정정 요청을 하세요.", warnings: [] };
    if (path.startsWith("/api/attendance/corrections")) return corr;
    return {};
  });
}

describe("내 근태 화면(B50 5단계)", () => {
  it("서버 시각 출근 단추, 지각·휴일 표시, 퇴근 누락 안내를 보이고 영문 코드가 없다", async () => {
    api2(day("2026-11-10", { checkIn: "09:00" }), week("", 470));
    post.mockResolvedValue({});
    mount(<AttendancePage />, ["attendance:self"]);
    expect(await screen.findByText(/어제\(11. 9.\) 퇴근 기록이 없습니다/)).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "이번 주 출퇴근 기록" });
    expect(within(table).getByText("지각")).toBeInTheDocument();
    expect(within(table).getByText("휴일")).toBeInTheDocument();
    expect(within(table).getByText("7시간 50분")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "출근" })).toBeDisabled(); // 이미 출근
    fireEvent.click(screen.getByRole("button", { name: "퇴근" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/check-out", {})); // 화면은 시각을 보내지 않는다
    expect(document.body.textContent).not.toMatch(/\b(check|late|over|caution|requested|applied)\b/);
  });

  it("주 52시간 초과는 경고로 알리되 출퇴근 단추를 막지 않는다", async () => {
    api2(day("2026-11-10"), week("over", 3200));
    mount(<AttendancePage />, ["attendance:self"]);
    expect(await screen.findByText(/이번 주 근로시간이 52시간을 넘었습니다\(53시간 20분\)/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "출근" })).toBeEnabled();
  });

  it("정정 요청은 사유와 바꿀 시각을 보내고, 처리할 요청은 승인 의견과 함께 처리한다", async () => {
    api2(
      day("2026-11-10"),
      week("", 0),
      { mine: [{ id: 5, userId: 2, userName: "직원", workDate: "2026-11-09", oldIn: "", oldOut: "", newIn: "09:00", newOut: "18:00", reason: "누락", requestedName: "직원", status: "requested", statusLabel: "승인 대기", approvedName: "", decideNote: "" }],
        toDecide: [{ id: 6, userId: 3, userName: "김팀원", workDate: "2026-11-09", oldIn: "09:00", oldOut: "", newIn: "09:00", newOut: "19:00", reason: "야근", requestedName: "김팀원", status: "requested", statusLabel: "승인 대기", approvedName: "", decideNote: "" }] },
    );
    post.mockResolvedValue({});
    mount(<AttendancePage />, ["attendance:self", "attendance:team"]);
    const mine = await screen.findByRole("table", { name: "내 정정 요청" });
    expect(within(mine).getByText("승인 대기")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("날짜"), { target: { value: "2026-11-09" } });
    fireEvent.change(screen.getByLabelText("퇴근 시각"), { target: { value: "18:00" } });
    fireEvent.change(screen.getByLabelText("사유"), { target: { value: "퇴근 기록 누락" } });
    fireEvent.click(screen.getByRole("button", { name: "정정 요청" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/corrections", { workDate: "2026-11-09", checkIn: "", checkOut: "18:00", reason: "퇴근 기록 누락" }));
    const pending = screen.getByRole("table", { name: "처리할 정정 요청" });
    expect(within(pending).getByText("퇴근 없음 → 19:00")).toBeInTheDocument();
    fireEvent.click(within(pending).getByRole("button", { name: "승인" }));
    fireEvent.change(await screen.findByLabelText("처리 의견(선택)"), { target: { value: "확인함" } });
    fireEvent.click(screen.getAllByRole("button", { name: "승인" }).pop()!);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/corrections/6/decide", { approve: true, note: "확인함" }));
  });
});

describe("근태 관리 화면(B50 5단계)", () => {
  it("끝난 달을 마감하고 해제 사유 없이는 해제를 보내지 않는다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.includes("/admin/closes")) return { closes: [{ month: "2026-10", closed: true, closedBy: "인사", closedAt: "2026-11-02T00:00:00Z", reopenedBy: "", reopenedAt: "", reopenReason: "" }] };
      if (path.includes("/work-types")) return { workTypes: [] };
      return {};
    });
    post.mockResolvedValue({});
    mount(<AttendanceAdminPage />, ["attendance:hr"]);
    const table = await screen.findByRole("table", { name: "근태 마감 현황" });
    expect(within(table).getByText("마감됨")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("마감할 달"), { target: { value: "2026-09" } });
    fireEvent.click(screen.getByRole("button", { name: "마감" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/admin/closes", { month: "2026-09" }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(within(table).getByRole("button", { name: "마감 해제" })).toBeEnabled());
    fireEvent.click(within(table).getByRole("button", { name: "마감 해제" }));
    fireEvent.change(await screen.findByLabelText("해제 사유"), { target: { value: "정정 필요" } });
    fireEvent.click(screen.getByRole("button", { name: "해제" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/admin/closes/2026-10/reopen", { reason: "정정 필요" }));
  });
});

describe("표기", () => {
  it("근로시간과 날짜를 업무 표기로 쓴다", () => {
    expect(fmtHours(3200)).toBe("53시간 20분");
    expect(fmtHours(60)).toBe("1시간");
    expect(fmtHours(0)).toBe("0분");
    expect(dayLabel("2026-11-10")).toBe("11. 10.(화)");
  });
});

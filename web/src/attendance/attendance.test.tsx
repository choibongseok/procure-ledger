import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ToastProvider } from "../layout/ui";
import { AttendanceSettingsPage } from "./AttendanceSettingsPage";
import { fmtMinutes } from "./leaveFormat";
import { MyLeavePage } from "./MyLeavePage";

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
const put = vi.mocked(api.put);
const del = vi.mocked(api.del);
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
  put.mockReset();
  del.mockReset();
});

describe("분량 표기", () => {
  it("일·시간·분으로 쓴다", () => {
    expect(fmtMinutes(480)).toBe("1일");
    expect(fmtMinutes(240)).toBe("4시간");
    expect(fmtMinutes(1050)).toBe("2일 1시간 30분");
    expect(fmtMinutes(990)).toBe("2일 30분");
    expect(fmtMinutes(0)).toBe("0분");
    expect(fmtMinutes(-480)).toBe("-1일");
    expect(fmtMinutes(600, 600)).toBe("1일");
  });
});

const lines = [
  { balance: "annual", label: "연차", minutes: 4800, reserved: 480, expiringMinutes: 960, expiringOn: "2026-12-01" },
  { balance: "compensatory", label: "대체휴가", minutes: 0, reserved: 0, expiringMinutes: 0, expiringOn: "" },
];
const types = [
  { code: "annual", name: "연차", unit: "day", balance: "annual", proofRequired: false },
  { code: "sick", name: "병가", unit: "day", balance: "", proofRequired: true },
];
const reqs = [
  { id: 1, docId: 11, docNo: "구매팀-휴가-2026-0001", leaveName: "연차", startDate: "2099-01-05", endDate: "2099-01-06", part: "full", startTime: "", endTime: "", minutes: 960, status: "approved", statusLabel: "승인됨", hasReason: false },
  { id: 2, docId: 12, docNo: "구매팀-휴가-2026-0002", leaveName: "연차", startDate: "2026-11-16", endDate: "2026-11-16", part: "am", startTime: "", endTime: "", minutes: 240, status: "pending", statusLabel: "신청 대기", hasReason: true },
  { id: 3, docId: 13, docNo: "", leaveName: "병가", startDate: "2026-10-01", endDate: "2026-10-01", part: "full", startTime: "", endTime: "", minutes: 480, status: "rejected", statusLabel: "반려됨", hasReason: false },
];

function leaveApi() {
  get.mockImplementation(async (path: string) => {
    if (path.includes("/leave/types")) return { types };
    if (path.includes("/leave/balance")) return { workdayMinutes: 480, lines };
    if (path.includes("/leave/requests")) return { requests: reqs };
    if (path.includes("/leave/ledger")) return { lines: [{ id: 1, balanceLabel: "연차", kindLabel: "발생", minutes: 7200, reason: "근속 1년 연차", expiresOn: "2027-03-15", createdAt: "2026-03-15T00:00:00Z" }] };
    return {};
  });
}

describe("내 휴가 화면(B50)", () => {
  it("잔여를 일·시간으로 보이고 결재 대기·소멸 예정을 알리며, 영문 코드가 없다", async () => {
    leaveApi();
    mount(<MyLeavePage />, ["attendance:self"]);
    const card = await screen.findByRole("group", { name: "연차 잔여" });
    expect(within(card).getByText("10일")).toBeInTheDocument();
    expect(within(card).getByText(/결재 대기 1일 포함/)).toBeInTheDocument();
    expect(within(card).getByText(/2026-12-01 까지 2일 소멸 예정/)).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "대체휴가 잔여" })).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "내 휴가 신청" });
    expect(within(table).getByText("승인됨")).toBeInTheDocument();
    expect(within(table).getByText("오전 반차")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "구매팀-휴가-2026-0001" })).toHaveAttribute("href", "/docs/11");
    expect(document.body.textContent).not.toMatch(/\b(annual|approved|pending|compensatory)\b/);
  });

  it("계산해 보기는 사용 분량과 잔여 변화를 보이고, 신청은 요청을 보낸다", async () => {
    leaveApi();
    post.mockImplementation(async (path: string) => {
      if (path.endsWith("/preview")) return { minutes: 480, days: [{ date: "2026-11-16", minutes: 480, skipped: "" }, { date: "2026-11-14", minutes: 0, skipped: "주말·공휴일" }], balance: "annual", balanceLabel: "연차", balanceBefore: 4800, balanceAfter: 4320, workdayMinutes: 480 };
      return {};
    });
    mount(<MyLeavePage />, ["attendance:self"]);
    await screen.findByRole("group", { name: "연차 잔여" });
    fireEvent.change(screen.getByLabelText("시작일"), { target: { value: "2026-11-16" } });
    fireEvent.click(screen.getByRole("button", { name: "계산해 보기" }));
    expect(await screen.findByText(/연차 잔여 10일 → 9일/)).toBeInTheDocument();
    expect(screen.getByText(/세지 않은 날: 2026-11-14\(주말·공휴일\)/)).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/attendance/leave/preview", expect.objectContaining({ leaveCode: "annual", startDate: "2026-11-16", part: "full" }));
    fireEvent.click(screen.getByRole("button", { name: "신청" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/leave/requests", expect.objectContaining({ leaveCode: "annual", startDate: "2026-11-16" })));
  });

  it("시간 지정은 시각 칸을 보이고 보낸다. 서버 거부 문구는 업무 문구로 보인다", async () => {
    leaveApi();
    post.mockRejectedValue(new ApiError(400, "입력값이 올바르지 않습니다: 잔여 연차(1일)보다 많은 5일을 신청할 수 없습니다."));
    mount(<MyLeavePage />, ["attendance:self"]);
    await screen.findByRole("group", { name: "연차 잔여" });
    fireEvent.change(screen.getByLabelText("구간"), { target: { value: "hours" } });
    expect(screen.getByLabelText("시작 시각")).toBeInTheDocument();
    expect(screen.queryByLabelText("종료일")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "신청" }));
    expect(await screen.findByText(/잔여 연차\(1일\)보다 많은 5일을 신청할 수 없습니다/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("입력값이 올바르지 않습니다:");
    expect(post).toHaveBeenCalledWith("/api/attendance/leave/requests", expect.objectContaining({ part: "hours", startTime: "09:00", endTime: "11:00" }));
  });

  it("증빙이 필요한 종류는 안내하고, 취소는 확인 뒤에 보낸다(시작 전 승인 건과 대기 건만)", async () => {
    leaveApi();
    post.mockResolvedValue({});
    mount(<MyLeavePage />, ["attendance:self"]);
    await screen.findByRole("group", { name: "연차 잔여" });
    fireEvent.change(screen.getByLabelText("휴가 종류"), { target: { value: "sick" } });
    expect(screen.getByText(/증빙 첨부가 필요합니다/)).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "내 휴가 신청" });
    const cancels = within(table).getAllByRole("button", { name: "취소" });
    expect(cancels).toHaveLength(2); // 반려된 건에는 없다
    fireEvent.click(cancels[1]);
    const dlg = await screen.findByRole("alertdialog");
    fireEvent.click(within(dlg).getByRole("button", { name: "취소하기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/attendance/leave/requests/2/cancel", { reason: "" }));
  });

  it("잔여 이력은 눌렀을 때만 불러온다", async () => {
    leaveApi();
    mount(<MyLeavePage />, ["attendance:self"]);
    await screen.findByRole("group", { name: "연차 잔여" });
    expect(get.mock.calls.some(([p]) => String(p).includes("/leave/ledger"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "잔여 이력 보기" }));
    const table = await screen.findByRole("table", { name: "잔여 이력" });
    expect(within(table).getByText("근속 1년 연차")).toBeInTheDocument();
    expect(within(table).getByText("+15일")).toBeInTheDocument();
  });
});

const policies = [
  { id: 5, effectiveFrom: "2099-01-01", basis: "fiscal", workdayMinutes: 480, hourUnitMinutes: 60, attendanceRule: true, carryMaxMinutes: 2400, carryValidMonths: 12, expireMonths: 12, firstYearMode: "monthly", createdByName: "관리자", canceled: false },
  { id: 4, effectiveFrom: "2026-01-01", basis: "hire", workdayMinutes: 480, hourUnitMinutes: 60, attendanceRule: true, carryMaxMinutes: 0, carryValidMonths: 12, expireMonths: 12, firstYearMode: "monthly", createdByName: "관리자", canceled: false },
];
const ltypes = [
  { id: 1, code: "annual", name: "연차", unit: "day", balance: "annual", balanceLabel: "연차", paid: true, proofRequired: false, maxDays: 0, system: true, active: true },
  { id: 2, code: "family_event", name: "경조휴가", unit: "day", balance: "", balanceLabel: "", paid: true, proofRequired: false, maxDays: 5, system: false, active: true },
];

describe("근태·휴가 설정 화면(B50)", () => {
  function settingsApi() {
    get.mockImplementation(async (path: string) => (path.includes("/policies") ? { policies: policies } : { types: ltypes }));
  }

  it("노무 검토 안내와 규칙 이력(적용 중·예정)·휴가 종류를 한국어로 보인다", async () => {
    settingsApi();
    mount(<AttendanceSettingsPage />, ["attendance:policy"]);
    expect(await screen.findByText(/운영 전에 노무 검토가 필요합니다/)).toBeInTheDocument();
    const hist = await screen.findByRole("table", { name: "연차 규칙 이력" });
    expect(within(hist).getByText("회계연도 기준")).toBeInTheDocument();
    expect(within(hist).getByText("예정")).toBeInTheDocument();
    expect(within(hist).getByText("적용 중")).toBeInTheDocument();
    expect(within(hist).getByText(/5일까지\(12개월 안에 사용\)/)).toBeInTheDocument();
    expect(within(hist).getByText(/이월 없음/)).toBeInTheDocument();
    const t = screen.getByRole("table", { name: "휴가 종류" });
    expect(within(t).getByText("기본 종류", { exact: false })).toBeInTheDocument();
    expect(within(t).getAllByRole("button", { name: "사용 중지" })).toHaveLength(1); // 연차는 중지할 수 없다
    expect(document.body.textContent).not.toMatch(/\b(hire|fiscal|annual|monthly|upfront)\b/);
  });

  it("규칙 저장은 시간을 분으로, 이월 일수를 분으로 바꿔 보낸다. 거부 문구는 그대로 보인다", async () => {
    settingsApi();
    post.mockRejectedValueOnce(new ApiError(400, "입력값이 올바르지 않습니다: 연차는 법정 기준(발생 후 12개월 안에 사용할 수 있어야 함)보다 짧게 소멸시킬 수 없습니다."));
    mount(<AttendanceSettingsPage />, ["attendance:policy"]);
    await screen.findByRole("table", { name: "연차 규칙 이력" });
    fireEvent.change(screen.getByLabelText("하루 분량(시간)"), { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText("이월"), { target: { value: "limited" } });
    fireEvent.change(screen.getByLabelText("이월 한도(일)"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "규칙 저장" }));
    expect(await screen.findByText(/법정 기준\(발생 후 12개월 안에 사용할 수 있어야 함\)보다 짧게/)).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/admin/attendance/policies", expect.objectContaining({ workdayMinutes: 420, carryMaxMinutes: 3 * 420, basis: "hire", expireMonths: 12 }));
  });

  it("예정 규칙은 확인 뒤 취소하고, 휴가 종류를 추가·사용 중지한다", async () => {
    settingsApi();
    post.mockResolvedValue({});
    put.mockResolvedValue({});
    del.mockResolvedValue(undefined);
    mount(<AttendanceSettingsPage />, ["attendance:policy"]);
    const hist = await screen.findByRole("table", { name: "연차 규칙 이력" });
    fireEvent.click(within(hist).getByRole("button", { name: "취소" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "취소하기" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/attendance/policies/5"));
    const form = screen.getByRole("form", { name: "휴가 종류 추가 양식" });
    fireEvent.change(within(form).getByLabelText("코드(영문 소문자)"), { target: { value: "wedding" } });
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "결혼휴가" } });
    fireEvent.click(within(form).getByRole("button", { name: "종류 추가" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/attendance/types", expect.objectContaining({ code: "wedding", name: "결혼휴가", balance: "", active: true })));
    const t = screen.getByRole("table", { name: "휴가 종류" });
    fireEvent.click(within(t).getByRole("button", { name: "사용 중지" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/attendance/types/2", expect.objectContaining({ active: false })));
  });
});

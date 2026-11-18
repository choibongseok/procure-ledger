import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { NotifyPrefsPage } from "./NotifyPrefsPage";
import { NotifyDefaults } from "../admin/NotifyDefaults";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = vi.mocked(api.get);
const put = vi.mocked(api.put);
const staff: CurrentUser = { id: 1, name: "직원", role: "staff", departmentId: 1 };

function mount(ui: React.ReactElement) {
  return render(
    <AuthProvider initialUser={staff}>
      <MemoryRouter>{ui}</MemoryRouter>
    </AuthProvider>,
  );
}

const prefs = {
  mailAvailable: true,
  workHours: "09:00~18:00",
  holdMaxHours: 72,
  digest: { mode: "default", defaultOn: false, effective: false },
  kinds: [
    { kind: "approval_pending", name: "결재 요청", defaultEnabled: true, locked: false, mode: "default", effective: true },
    { kind: "approval_done", name: "결재 완료", defaultEnabled: false, locked: false, mode: "default", effective: false },
    { kind: "receipt_arrived", name: "수신 문서 도착", defaultEnabled: true, locked: true, mode: "default", effective: true },
  ],
};

beforeEach(() => {
  get.mockReset();
  put.mockReset();
});

describe("내 알림 설정", () => {
  it("앱 안 알림은 끌 수 없다고 안내하고 종류별 기본값·내 설정·지금 상태를 보인다", async () => {
    get.mockResolvedValue(prefs);
    mount(<NotifyPrefsPage />);
    expect(await screen.findByText(/앱 안 알림은 항상 받습니다/)).toBeInTheDocument();
    // 안내 문장은 목록을 불러오기 전에도 보이므로 목록 행이 나타날 때까지 기다린다.
    const row = (await screen.findByText("결재 완료")).closest("tr")!;
    expect(within(row).getByText("앱 안에서만")).toBeInTheDocument();
    const row2 = screen.getByText("결재 요청").closest("tr")!;
    expect(within(row2).getByText("메일 받음")).toBeInTheDocument();
    expect(screen.getByText(/09:00~18:00/)).toBeInTheDocument();
    expect(screen.queryByText(/@|enc:|email|digest/)).toBeNull(); // 주소·영문 코드 노출 없음
  });

  it("필수 알림은 '꺼짐' 선택지가 없다", async () => {
    get.mockResolvedValue(prefs);
    mount(<NotifyPrefsPage />);
    const sel = (await screen.findByLabelText("수신 문서 도착 메일")) as HTMLSelectElement;
    expect([...sel.options].map((o) => o.textContent)).toEqual(["기본값 따름", "켜짐"]);
    expect(within(sel.closest("tr")!).getByText(/\(필수\)/)).toBeInTheDocument();
  });

  it("종류별 설정을 바꾸면 PUT 하고 다시 읽는다", async () => {
    get.mockResolvedValue(prefs);
    put.mockResolvedValue(undefined);
    mount(<NotifyPrefsPage />);
    fireEvent.change(await screen.findByLabelText("결재 완료 메일"), { target: { value: "on" } });
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notifications/prefs/approval_done", { mode: "on" }));
    expect(await screen.findByText("결재 완료 설정을 저장했습니다.")).toBeInTheDocument();
    expect(get.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("업무 시간 외 묶음 받기를 켠다", async () => {
    get.mockResolvedValue(prefs);
    put.mockResolvedValue(undefined);
    mount(<NotifyPrefsPage />);
    const group = await screen.findByRole("radiogroup", { name: "업무 시간 외 묶음 받기" });
    fireEvent.click(within(group).getByLabelText(/^켜짐/));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notifications/digest", { mode: "on" }));
  });

  it("메일 서버가 없으면 안내하고, 서버 오류는 업무 문구로 보인다", async () => {
    get.mockResolvedValue({ ...prefs, mailAvailable: false });
    put.mockRejectedValue(new ApiError(409, "필수 알림이라 끌 수 없습니다."));
    mount(<NotifyPrefsPage />);
    expect(await screen.findByText(/메일 서버가 설정되어 있지 않아/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("결재 요청 메일"), { target: { value: "off" } });
    expect(await screen.findByText(/필수 알림이라 끌 수 없습니다/)).toBeInTheDocument();
  });
});

const defaults = {
  mailConfigured: true,
  kinds: [
    { kind: "approval_pending", name: "결재 요청", enabled: true, locked: false },
    { kind: "approval_done", name: "결재 완료", enabled: false, locked: false },
  ],
  settings: [
    { key: "notify.work_start", label: "업무 시작 시각", value: "09:00", unit: "", type: "clock" },
    { key: "notify.hold_max_hours", label: "묶음 보류 상한", value: "72", unit: "시간", type: "int", min: 1, max: 720 },
    { key: "notify.digest_default", label: "업무 시간 외 묶음 기본값", value: "0", unit: "", type: "bool" },
  ],
};

describe("관리자 기본값", () => {
  it("기본값·필수를 바꾸면 PUT 한다(필수는 켜진 알림만)", async () => {
    get.mockResolvedValue(defaults);
    put.mockResolvedValue(undefined);
    mount(<NotifyDefaults />);
    expect(await screen.findByLabelText("결재 완료 필수")).toBeDisabled();
    fireEvent.click(screen.getByLabelText("결재 요청 필수"));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notify/defaults/approval_pending", { enabled: true, locked: true }));
    fireEvent.click(screen.getByLabelText("결재 완료 메일 기본값"));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notify/defaults/approval_done", { enabled: true, locked: false }));
  });

  it("바뀐 설정값만 보내고, 안 바꾸면 저장 단추가 꺼져 있다", async () => {
    get.mockResolvedValue(defaults);
    put.mockResolvedValue(undefined);
    mount(<NotifyDefaults />);
    const save = await screen.findByRole("button", { name: "설정 저장" });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("묶음 보류 상한 (시간)"), { target: { value: "48" } });
    fireEvent.change(screen.getByLabelText("업무 시간 외 묶음 기본값"), { target: { value: "1" } });
    expect(save).toBeEnabled();
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notify/settings", { values: { "notify.hold_max_hours": "48", "notify.digest_default": "1" } }));
  });

  it("범위 오류는 서버 문구를 보인다", async () => {
    get.mockResolvedValue(defaults);
    put.mockRejectedValue(new ApiError(400, "묶음 보류 상한: 1 이상 720 이하의 정수를 입력하세요."));
    mount(<NotifyDefaults />);
    fireEvent.change(await screen.findByLabelText("묶음 보류 상한 (시간)"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "설정 저장" }));
    expect(await screen.findByText(/1 이상 720 이하의 정수/)).toBeInTheDocument();
  });
});

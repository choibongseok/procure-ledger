import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ROLE_PERMISSIONS, type Role } from "../auth/permissions";
import { visibleMenu } from "../layout/Layout";
import { DeliveryLogPage } from "./DeliveryLogPage";
import { NotifyAdminPage } from "./NotifyAdminPage";
import { adminModule } from "./routes";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const user = (role: Role): CurrentUser => ({ id: 1, name: "사용자", role, departmentId: 1 });

function mount(ui: React.ReactElement, role: Role = "admin") {
  return render(
    <AuthProvider initialUser={user(role)}>
      <MemoryRouter>{ui}</MemoryRouter>
    </AuthProvider>,
  );
}

const status = { mailConfigured: true, baseUrlSet: true, queued: 3, sending: 0, failed: 2, queuedWarn: false };
const failed = { id: 7, kind: "approval_pending", kindName: "결재 요청", channel: "email", channelName: "메일", recipient: "구매직원", status: "failed", attempts: 1, reason: "메일 서버가 수신을 거절했습니다.", createdAt: "2026-11-12T01:00:00Z", canResend: true };
const sent = { id: 8, kind: "approval_nudge", kindName: "결재 독촉", channel: "email", channelName: "메일", recipient: "구매팀장", status: "sent", attempts: 0, createdAt: "2026-11-12T01:05:00Z", sentAt: "2026-11-12T01:05:10Z", canResend: false, groupedCount: 3 };

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("알림 채널 관리 화면", () => {
  it("메일 상태와 큐 건수를 보이고 시험 메일을 보낸다", async () => {
    get.mockResolvedValue(status);
    post.mockResolvedValue(undefined);
    mount(<NotifyAdminPage />);
    expect(await screen.findByText("대기 3건")).toBeInTheDocument();
    expect(screen.getByText("실패 2건")).toBeInTheDocument();
    expect(screen.getAllByText("설정됨").length).toBe(2);
    fireEvent.click(screen.getByRole("button", { name: "시험 메일 보내기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/notify/mail-test", {}));
    expect(await screen.findByText(/시험 메일을 보냈습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/smtp|SMTP|queued/)).toBeNull(); // 영문 코드 노출 없음
  });

  it("메일 서버가 설정되지 않았으면 안내하고 시험 메일 버튼이 꺼진다. 서버 오류는 업무 문구로 보인다", async () => {
    get.mockResolvedValue({ ...status, mailConfigured: false });
    mount(<NotifyAdminPage />);
    expect(await screen.findByText(/메일 서버가 설정되지 않아 메일은 보내지 않고 앱 안 알림만 만들어집니다/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "시험 메일 보내기" })).toBeDisabled();
  });

  it("시험 메일 실패 사유를 보인다", async () => {
    get.mockResolvedValue(status);
    post.mockRejectedValue(new ApiError(409, "현재 상태에서는 할 수 없는 동작입니다: 시험 메일을 보내지 못했습니다. 메일 서버가 수신을 거절했습니다."));
    mount(<NotifyAdminPage />);
    fireEvent.click(await screen.findByRole("button", { name: "시험 메일 보내기" }));
    expect(await screen.findByText(/시험 메일을 보내지 못했습니다/)).toBeInTheDocument();
  });
});

describe("알림 발송 기록 화면", () => {
  it("표에 한국어 상태·사유를 보이고 메일 주소·본문은 없다. 관리자는 실패 건을 다시 보낸다", async () => {
    get.mockResolvedValue({ deliveries: [failed, sent] });
    post.mockResolvedValue(undefined);
    mount(<DeliveryLogPage />);
    expect(await screen.findByText("메일 서버가 수신을 거절했습니다.")).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith("/api/notify/deliveries");
    expect(screen.getAllByText("실패").length).toBe(2); // 조건 선택지 + 상태 배지
    expect(screen.getAllByText("발송됨").length).toBe(2);
    expect(screen.getByText(/한 통 3건/)).toBeInTheDocument();
    expect(screen.queryByText(/@|failed|sent|email/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "다시 보내기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/notify/deliveries/7/resend", {}));
    expect(await screen.findByText(/결재 요청 메일을 다시 보내도록 큐에 올렸습니다/)).toBeInTheDocument();
  });

  it("감사 권한만 있으면 감사용 경로로 조회하고 다시 보내기 열이 없다. 조건을 바꾸면 다시 조회한다", async () => {
    get.mockResolvedValue({ deliveries: [{ ...failed, canResend: false }] });
    mount(<DeliveryLogPage />, "auditor");
    expect(await screen.findByText("결재 요청")).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith("/api/audit/notify-deliveries");
    expect(screen.queryByRole("button", { name: "다시 보내기" })).toBeNull();
    fireEvent.change(screen.getByLabelText("상태"), { target: { value: "failed" } });
    await waitFor(() => expect(get).toHaveBeenLastCalledWith("/api/audit/notify-deliveries?status=failed"));
  });

  it("기록이 없으면 빈 상태 문구를 보인다", async () => {
    get.mockResolvedValue({ deliveries: [] });
    mount(<DeliveryLogPage />);
    expect(await screen.findByText("조건에 맞는 발송 기록이 없습니다.")).toBeInTheDocument();
  });
});

describe("메뉴 노출", () => {
  const labels = (role: Role) => visibleMenu(adminModule.menu, (p) => p === "authenticated" || p === "public" || (ROLE_PERMISSIONS[role] as readonly string[]).includes(p)).map((m) => m.label);
  it("관리자에게는 알림 채널과 발송 기록이, 감사에게는 발송 기록만, 직원·결재자·재무에게는 알림 채널 메뉴가 없다", () => {
    expect(labels("admin")).toEqual(expect.arrayContaining(["알림 채널", "알림 발송 기록"]));
    expect(labels("auditor")).toContain("알림 발송 기록");
    expect(labels("auditor")).not.toContain("알림 채널");
    for (const r of ["staff", "manager", "finance"] as Role[]) expect(labels(r)).not.toContain("알림 채널");
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { NotifyTargets } from "./NotifyTargets";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);
const admin: CurrentUser = { id: 1, name: "관리자", role: "admin", departmentId: 1 };

function mount() {
  return render(
    <AuthProvider initialUser={admin}>
      <MemoryRouter>
        <NotifyTargets />
      </MemoryRouter>
    </AuthProvider>,
  );
}

const kinds = [
  { kind: "approval_pending", name: "결재 요청" },
  { kind: "payment_due", name: "지급 예정" },
];
const builtin = { slack: ["hooks.slack.com"], teams: ["*.logic.azure.com"], kakaowork: ["api.kakaowork.com"], termspace: ["termspace.io", "*.termspace.io"] };
const base = { kinds, sealReady: true, baseUrlSet: true, extraHosts: [], builtinHosts: builtin };
const slackTarget = {
  id: 3,
  name: "결재 채널",
  type: "slack",
  typeName: "슬랙",
  hint: "hooks.slack.com",
  kinds: ["approval_pending"],
  kindNames: ["결재 요청"],
  active: true,
  autoPaused: false,
  state: "사용 중",
  failures: 0,
  lastOkAt: "2026-11-12T01:00:00Z",
};

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
});

describe("웹훅 대상 관리", () => {
  it("대상이 없으면 빈 상태 문구를 보인다", async () => {
    get.mockResolvedValue({ ...base, targets: [] });
    mount();
    expect(await screen.findByText("등록된 웹훅 대상이 없습니다.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "웹훅 대상 추가" })).toBeInTheDocument();
  });

  it("봉인 키가 없으면 등록 단추 없이 안내한다", async () => {
    get.mockResolvedValue({ ...base, sealReady: false, targets: [] });
    mount();
    expect(await screen.findByText(/안전하게 저장할 키가 서버에 없어/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "웹훅 대상 추가" })).toBeNull();
  });

  it("목록에는 호스트만 보이고 영문 상태 코드는 없다", async () => {
    get.mockResolvedValue({ ...base, targets: [slackTarget, { ...slackTarget, id: 4, name: "재무 채널", state: "자동 중지", autoPaused: true, failures: 5, lastError: "웹훅이 없어졌거나 꺼져 있습니다. 주소를 다시 등록하세요.", lastErrorAt: "2026-11-12T02:00:00Z" }] });
    mount();
    expect(await screen.findByText("결재 채널")).toBeInTheDocument();
    expect(screen.getAllByText("hooks.slack.com").length).toBe(2);
    expect(screen.getByText("자동 중지")).toBeInTheDocument();
    expect(screen.getByText(/연속 실패 5번/)).toBeInTheDocument();
    expect(screen.queryByText(/hook_|paused|queued|autoPaused/)).toBeNull();
  });

  it("시험 보내기와 사용 중지를 부른다", async () => {
    get.mockResolvedValue({ ...base, targets: [slackTarget] });
    post.mockResolvedValue(slackTarget);
    mount();
    await screen.findByText("결재 채널");
    fireEvent.click(screen.getByRole("button", { name: "시험 보내기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/notify/targets/3/test", {}));
    expect(await screen.findByText(/시험 메시지를 보냈습니다/)).toBeInTheDocument();
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(screen.getByRole("button", { name: "사용 중지" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "사용 중지" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/notify/targets/3/pause", {}));
  });

  it("시험 보내기 실패는 서버 문구를 그대로 보인다", async () => {
    get.mockResolvedValue({ ...base, targets: [slackTarget] });
    post.mockRejectedValue(new ApiError(409, "시험 메시지를 보내지 못했습니다. 채널 서버에 연결하지 못했습니다."));
    mount();
    await screen.findByText("결재 채널");
    fireEvent.click(screen.getByRole("button", { name: "시험 보내기" }));
    expect(await screen.findByText(/채널 서버에 연결하지 못했습니다/)).toBeInTheDocument();
  });

  it("등록하면 주소를 한 번 보내고 화면에 남기지 않는다", async () => {
    get.mockResolvedValue({ ...base, targets: [] });
    post.mockResolvedValue({ ...slackTarget, id: 9 });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "웹훅 대상 추가" }));
    fireEvent.change(screen.getByLabelText("이름(40자 이하)"), { target: { value: "결재 채널" } });
    fireEvent.change(screen.getByLabelText("웹훅 주소"), { target: { value: "https://hooks.slack.com/services/T0/B0/SECRETVALUE" } });
    fireEvent.click(screen.getByLabelText("결재 요청"));
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/api/notify/targets", {
        name: "결재 채널",
        type: "slack",
        url: "https://hooks.slack.com/services/T0/B0/SECRETVALUE",
        token: "",
        conversation: "",
        kinds: ["approval_pending"],
      }),
    );
    await waitFor(() => expect(screen.queryByLabelText("웹훅 주소")).toBeNull());
    expect(screen.queryByText(/SECRETVALUE/)).toBeNull();
  });

  it("카카오워크를 고르면 앱 키와 대화방 ID 를 받는다", async () => {
    get.mockResolvedValue({ ...base, targets: [] });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "웹훅 대상 추가" }));
    expect(screen.queryByLabelText("앱 키")).toBeNull();
    fireEvent.change(screen.getByLabelText("종류"), { target: { value: "kakaowork" } });
    expect(screen.getByLabelText("앱 키")).toBeInTheDocument();
    expect(screen.getByLabelText("대화방 ID")).toBeInTheDocument();
    expect(screen.getByText(/허용 호스트: api.kakaowork.com/)).toBeInTheDocument();
  });

  it("termspace 를 고르면 허용 호스트와 주소 안내가 보이고 앱 키 칸은 없다", async () => {
    get.mockResolvedValue({ ...base, targets: [] });
    post.mockResolvedValue({});
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "웹훅 대상 추가" }));
    const kinds = screen.getByLabelText("종류") as HTMLSelectElement;
    expect(Array.from(kinds.options).map((o) => o.textContent)).toContain("termspace");
    fireEvent.change(kinds, { target: { value: "termspace" } });
    expect(screen.queryByLabelText("앱 키")).toBeNull();
    expect(screen.getByText(/허용 호스트: termspace.io, \*.termspace.io/)).toBeInTheDocument();
    expect(screen.getByLabelText("웹훅 주소")).toHaveAttribute("placeholder", expect.stringContaining("/hooks/tsh_"));
    fireEvent.change(screen.getByLabelText("이름(40자 이하)"), { target: { value: "협업방" } });
    fireEvent.change(screen.getByLabelText("웹훅 주소"), { target: { value: "https://chat.termspace.io/hooks/tsh_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" } });
    fireEvent.click(screen.getByLabelText("결재 요청"));
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/api/notify/targets", expect.objectContaining({ name: "협업방", type: "termspace", token: "", conversation: "" })),
    );
  });

  it("수정은 새 주소를 비워 두면 기존 값을 유지하도록 PUT 한다", async () => {
    get.mockResolvedValue({ ...base, targets: [slackTarget] });
    put.mockResolvedValue(slackTarget);
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "수정" }));
    expect(screen.getByLabelText("종류")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("이름(40자 이하)"), { target: { value: "결재방" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notify/targets/3", expect.objectContaining({ name: "결재방", url: "", kinds: ["approval_pending"] })));
  });

  it("추가 허용 호스트를 쉼표로 저장한다", async () => {
    get.mockResolvedValue({ ...base, targets: [] });
    put.mockResolvedValue({ hosts: ["chat.corp.example"] });
    mount();
    fireEvent.click(await screen.findByText("추가 허용 호스트", { selector: "summary" }));
    fireEvent.change(screen.getByLabelText("추가 허용 호스트"), { target: { value: "chat.corp.example, *.corp2.example" } });
    fireEvent.click(screen.getByRole("button", { name: "허용 호스트 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/notify/hosts", { hosts: ["chat.corp.example", "*.corp2.example"] }));
  });
});

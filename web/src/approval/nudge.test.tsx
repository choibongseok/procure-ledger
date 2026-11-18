import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { NudgePanel, waitingText } from "./NudgePanel";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;

const status = (over = {}) => ({ delayed: true, stepTitle: "팀장", waitingMinutes: 1640, noApprover: false, doneToday: null, records: [], ...over });

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("결재 독촉 영역", () => {
  it("대기 시간 표기", () => {
    expect(waitingText(1640)).toBe("1일 3시간");
    expect(waitingText(300)).toBe("5시간");
    expect(waitingText(20)).toBe("20분");
  });

  it("지연된 단계에서 확인 대화상자를 거쳐 독촉하고, 보낸 뒤에는 기록을 다시 불러온다", async () => {
    get.mockResolvedValueOnce(status());
    get.mockResolvedValue(status({ doneToday: "2026-11-12T05:00:00Z", records: [{ at: "2026-11-12T05:00:00Z", day: "2026-11-12", stepTitle: "팀장", recipients: 1, recipientNames: ["김팀장"] }] }));
    post.mockResolvedValue({ stepTitle: "팀장", recipients: 1, waitingMinutes: 1640 });
    render(<NudgePanel docId={7} />);
    expect(await screen.findByText(/「팀장」 단계는 대기 1일 3시간째입니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "독촉하기" }));
    fireEvent.click(await screen.findByRole("button", { name: "독촉 알림 보내기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/approvals/7/nudge"));
    expect(await screen.findByText("독촉 알림을 보냈습니다. 같은 단계는 내일 다시 독촉할 수 있습니다.")).toBeInTheDocument();
    expect(await screen.findByText(/오늘은 이미 독촉했습니다/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "독촉하기" })).toBeDisabled();
    expect(screen.getByText(/받은 사람 김팀장/)).toBeInTheDocument();
  });

  it("결재자가 없으면 단추 대신 안내를 보이고, 지연이 없고 기록도 없으면 아무것도 그리지 않는다", async () => {
    get.mockResolvedValueOnce(status({ noApprover: true }));
    const { container, unmount } = render(<NudgePanel docId={7} />);
    expect(await screen.findByText(/결재자가 없어 독촉할 수 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "독촉하기" })).toBeNull();
    unmount();
    get.mockResolvedValueOnce(status({ delayed: false }));
    const second = render(<NudgePanel docId={8} />);
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(second.container.textContent).toBe("");
    void container;
  });
});

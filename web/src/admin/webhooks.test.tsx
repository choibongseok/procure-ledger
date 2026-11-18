import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { WebhooksPage } from "./WebhooksPage";

vi.mock("../api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
    }
  },
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

let perms: string[] = [];
vi.mock("../auth/AuthProvider", () => ({
  useAuth: () => ({ can: (p: string) => perms.includes(p) }),
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

const subs = [
  { id: 1, name: "회계 연동", hint: "hooks.example.com", events: ["item.updated"], includeApi: false, active: true, pausedReason: "", state: "사용 중", failures: 0, lastOkAt: "2026-11-13T01:00:00Z", lastError: "", lastErrorAt: "", queued: 0, failed: 2 },
  { id: 2, name: "멈춘 연동", hint: "b.example.com…/hook2", events: ["journal.created", "item.updated"], includeApi: true, active: true, pausedReason: "auto", state: "자동 중지", failures: 5, lastOkAt: "", lastError: "상대 서버가 오류를 냈습니다.", lastErrorAt: "2026-11-13T02:00:00Z", queued: 3, failed: 0 },
];
const eventTypes = [
  { code: "item.updated", title: "품목 변경" },
  { code: "journal.created", title: "전표 생성" },
  { code: "journal.status_changed:approved", title: "전표 승인(으)로 상태 변경" },
];
const deliveries = [
  { id: 9, subscriptionId: 1, subscriptionName: "회계 연동", eventSeq: 77, title: "품목 변경", status: "failed", state: "실패(포기)", attempts: 9, reason: "상대 서버가 오류를 냈습니다.", createdAt: "2026-11-13T01:00:00Z" },
  { id: 8, subscriptionId: 1, subscriptionName: "회계 연동", eventSeq: 76, title: "품목 변경", status: "sent", state: "전달됨", attempts: 1, reason: "", createdAt: "2026-11-13T00:59:00Z" },
];

function mockGets() {
  get.mockImplementation(async (url: string) => {
    if (url.includes("webhook-deliveries")) return deliveries;
    if (url.endsWith("/webhook-events")) return eventTypes;
    if (url.endsWith("/webhook-hosts")) return { allowHosts: ["hooks.example.com"], extraPorts: [] };
    return subs;
  });
}

const renderPage = () => render(<MemoryRouter><WebhooksPage /></MemoryRouter>);

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = ["webhook:manage"];
});

describe("변경 웹훅 화면", () => {
  it("구독 표에 상태·미전달·마지막 실패 사유를 한국어로 보이고 주소는 요약만 보인다", async () => {
    mockGets();
    renderPage();
    const table = await screen.findByRole("table", { name: "웹훅 구독" });
    expect(within(table).getByText("자동 중지")).toBeInTheDocument();
    expect(within(table).getByText(/연속 실패 5건/)).toBeInTheDocument();
    expect(within(table).getByText("hooks.example.com")).toBeInTheDocument();
    expect(within(table).getByText(/대기 3건/)).toBeInTheDocument();
    // 중지된 구독에는 재개, 정상 구독에는 중지가 보인다. 실패 건이 있는 구독에만 일괄 재전송이 보인다.
    expect(within(table).getAllByRole("button", { name: "재개" })).toHaveLength(1);
    expect(within(table).getAllByRole("button", { name: "중지" })).toHaveLength(1);
    expect(within(table).getAllByRole("button", { name: "실패 일괄 재전송" })).toHaveLength(1);
  });

  it("구독을 등록하면 서명 비밀을 한 번만 보여 준다", async () => {
    mockGets();
    post.mockResolvedValue({ subscription: { id: 3 }, secret: "whsec_ONETIME" });
    renderPage();
    const form = await screen.findByRole("form", { name: "구독 양식" });
    const submit = within(form).getByRole("button", { name: "구독 등록" });
    expect(submit).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "새 연동" } });
    fireEvent.change(within(form).getByLabelText("받을 주소(https)"), { target: { value: "https://hooks.example.com/x" } });
    fireEvent.click(within(form).getByRole("checkbox", { name: "전표 생성" }));
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/webhooks", { name: "새 연동", url: "https://hooks.example.com/x", events: ["journal.created"], includeApi: false }));
    expect(await screen.findByTestId("webhook-secret")).toHaveTextContent("whsec_ONETIME");
    fireEvent.click(screen.getByRole("button", { name: "저장했습니다" }));
    expect(screen.queryByText("whsec_ONETIME")).toBeNull();
  });

  it("수정은 주소를 비워 두면 보내지 않고, 비밀 재발급은 확인을 거친다", async () => {
    mockGets();
    put.mockResolvedValue({});
    post.mockResolvedValue({ secret: "whsec_NEW" });
    renderPage();
    const table = await screen.findByRole("table", { name: "웹훅 구독" });
    fireEvent.click(within(table).getAllByRole("button", { name: "수정" })[0]);
    const form = screen.getByRole("form", { name: "구독 양식" });
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "회계 연동 2" } });
    fireEvent.click(within(form).getByRole("button", { name: "수정 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/integrations/webhooks/1", { name: "회계 연동 2", events: ["item.updated"], includeApi: false }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(within(table).getAllByRole("button", { name: "비밀 재발급" })[0]).toBeEnabled());
    fireEvent.click(within(table).getAllByRole("button", { name: "비밀 재발급" })[0]);
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "재발급" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/webhooks/1/rotate-secret"));
    expect(await screen.findByTestId("webhook-secret")).toHaveTextContent("whsec_NEW");
  });

  it("발송 기록은 실패 건에만 다시 보내기가 있고 일괄 재전송은 건수를 알린다", async () => {
    mockGets();
    post.mockResolvedValue({ count: 2 });
    renderPage();
    const log = await screen.findByRole("table", { name: "웹훅 발송 기록" });
    expect(within(log).getAllByRole("button", { name: "다시 보내기" })).toHaveLength(1);
    expect(within(log).getByText("전달됨")).toBeInTheDocument();
    fireEvent.click(within(await screen.findByRole("table", { name: "웹훅 구독" })).getByRole("button", { name: "실패 일괄 재전송" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/webhooks/1/resend-failed"));
    expect(await screen.findByText(/실패한 2건을 다시 보내기로 돌렸습니다/)).toBeInTheDocument();
  });

  it("감사(audit:read)만 있으면 구독 관리 없이 발송 기록만 보이고 구독 API 는 부르지 않는다", async () => {
    perms = ["audit:read"];
    mockGets();
    renderPage();
    await screen.findByRole("table", { name: "웹훅 발송 기록" });
    expect(screen.queryByRole("form", { name: "구독 양식" })).toBeNull();
    expect(screen.queryByRole("table", { name: "웹훅 구독" })).toBeNull();
    expect(screen.queryByRole("button", { name: "다시 보내기" })).toBeNull();
    expect(get.mock.calls.every((c) => String(c[0]).includes("webhook-deliveries"))).toBe(true);
  });

  it("허용 호스트 설정은 쉼표로 나눠 저장한다", async () => {
    mockGets();
    put.mockResolvedValue(undefined);
    renderPage();
    const input = await screen.findByLabelText(/허용 호스트/);
    expect(input).toHaveValue("hooks.example.com");
    fireEvent.change(input, { target: { value: "a.example, b.example" } });
    fireEvent.change(screen.getByLabelText(/추가 허용 포트/), { target: { value: "8443" } });
    fireEvent.click(screen.getByRole("button", { name: "설정 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/integrations/webhook-hosts", { allowHosts: ["a.example", "b.example"], extraPorts: ["8443"] }));
  });
});

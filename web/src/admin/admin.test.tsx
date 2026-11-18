import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { LoginPage } from "../auth/LoginPage";
import { AuditLogPage } from "./AuditLogPage";
import { TokensPage } from "./TokensPage";

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

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);

const auditor = { id: 3, name: "감사", role: "auditor" as const, departmentId: 1 };

const record = {
  seq: 7,
  at: "2026-11-10T01:02:03Z",
  actorId: 2,
  actorRole: "staff",
  channel: "mcp",
  action: "access.denied",
  targetType: "route",
  targetId: "POST /api/approvals/{id}/decision",
  result: "denied",
  diff: [{ key: "status", old: "draft", new: "submitted" }],
  detail: "필요 권한 approval:decide",
  prevHash: "a",
  hash: "b",
};

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("감사 로그 화면", () => {
  it("기록을 한국어 라벨로 보여 주고 거부 결과를 구분한다", async () => {
    get.mockResolvedValue({ records: [record], nextBeforeSeq: 0 });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    expect(await screen.findByText("접근 거부")).toBeInTheDocument();
    expect(screen.getByText("AI 도구", { selector: "td" })).toBeInTheDocument();
    expect(screen.getByText("거부", { selector: "td .badge-bad" })).toBeInTheDocument();
    expect(screen.getByText(/임시저장 → 상신/)).toBeInTheDocument();
  });

  it("검증 결과가 변조이면 어긋난 번호를 알려 준다", async () => {
    get.mockResolvedValue({ records: [record], nextBeforeSeq: 0 });
    post.mockResolvedValue({ ok: false, checked: 3, brokenSeq: 2, brokenWhy: "레코드 내용이 해시와 다릅니다(변조)", headHash: "" });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "변조 여부 검증" }));
    expect(await screen.findByRole("status")).toHaveTextContent("2번 기록에서 변조가 감지되었습니다");
    expect(post).toHaveBeenCalledWith("/api/audit-logs/verify");
  });

  it("정상이면 검증 건수를 보여 준다", async () => {
    get.mockResolvedValue({ records: [], nextBeforeSeq: 0 });
    post.mockResolvedValue({ ok: true, checked: 12, brokenSeq: 0, brokenWhy: "", headHash: "h" });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "변조 여부 검증" }));
    expect(await screen.findByRole("status")).toHaveTextContent("12건을 검증했고 변조가 없습니다");
  });

  const anchorOf = (state: string, message: string) => ({ state, message, lines: 1, lastSeq: 3 });
  const runVerify = async (resp: object) => {
    get.mockResolvedValue({ records: [], nextBeforeSeq: 0 });
    post.mockResolvedValue({ checked: 5, brokenSeq: 0, brokenWhy: "", headHash: "h", ...resp });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "변조 여부 검증" }));
    return screen.findByRole("status");
  };

  it("외부 앵커가 꺼져 있으면 통과가 아니라 외부 대조 없음을 분명히 보여 준다", async () => {
    const status = await runVerify({ ok: true, chainOk: true, anchor: anchorOf("disabled", "외부 앵커가 설정되지 않아 외부 대조를 하지 않았습니다.") });
    expect(status).toHaveTextContent("외부 대조는 하지 못했습니다");
    expect(status).not.toHaveTextContent("변조가 없습니다.");
    expect(screen.getByTestId("anchor-status")).toHaveTextContent("외부 대조 없음");
    expect(screen.getByTestId("anchor-status")).toHaveAttribute("data-state", "disabled");
  });

  it("외부 앵커와 일치하면 일치 사실을 보여 준다", async () => {
    const status = await runVerify({ ok: true, chainOk: true, anchor: anchorOf("ok", "외부 앵커 1개와 대조했고 모두 일치합니다.") });
    expect(status).toHaveTextContent("5건을 검증했고 변조가 없습니다");
    expect(screen.getByTestId("anchor-status")).toHaveTextContent("외부 앵커 일치");
  });

  it.each([
    ["tail_deleted", "마지막 기록이 삭제된 것으로 보입니다.", "마지막 기록 삭제 의심"],
    ["content_recomputed", "3번 기록의 내용이 외부 앵커와 다릅니다.", "기록 내용 재작성 의심"],
    ["file_corrupt", "외부 앵커 파일이 손상되었습니다: 2번째 줄의 서명이 올바르지 않습니다", "앵커 파일 손상"],
  ])("체인은 정상이어도 앵커 대조가 실패하면(%s) 종류를 구분해 알린다", async (state, message, title) => {
    const status = await runVerify({ ok: false, chainOk: true, anchor: anchorOf(state, message) });
    expect(status).toHaveTextContent("외부 앵커 대조에서 변조가 감지되었습니다");
    const a = screen.getByTestId("anchor-status");
    expect(a).toHaveTextContent(title);
    expect(a).toHaveTextContent(message);
    expect(a.textContent).not.toMatch(/tail_deleted|content_recomputed|file_corrupt/);
  });

  it("외부 대조 없이 체인만 맞으면(ok:true) 완전 통과와 구분해 표시한다", async () => {
    await runVerify({
      ok: true,
      chainOk: true,
      externallyVerified: false,
      level: "chain_only",
      summary: "기록 체인은 이어져 있지만 외부 대조를 하지 못했습니다(완전 통과가 아닙니다).",
      anchor: anchorOf("disabled", "외부 앵커가 설정되지 않았습니다."),
    });
    const level = screen.getByTestId("verify-level");
    expect(level).toHaveAttribute("data-level", "chain_only");
    expect(level).toHaveTextContent("외부 대조 없음");
    expect(level).not.toHaveTextContent("외부 대조까지 완전 통과");
    expect(level.textContent).not.toMatch(/chain_only/);
  });

  it("외부 대조까지 끝나면 완전 통과로 표시한다", async () => {
    await runVerify({ ok: true, chainOk: true, externallyVerified: true, level: "verified", summary: "일치합니다.", anchor: anchorOf("ok", "일치") });
    expect(screen.getByTestId("verify-level")).toHaveTextContent("외부 대조까지 완전 통과");
  });

  it("앵커 유실 이력이 있으면 복귀 뒤에도 계속 표시한다", async () => {
    const status = await runVerify({
      ok: true,
      chainOk: true,
      externallyVerified: false,
      level: "lost_history",
      anchor: { ...anchorOf("ok", "일치"), lostHistory: true },
    });
    expect(status).toHaveTextContent("앵커 유실 이력");
    expect(screen.getByTestId("verify-level")).toHaveTextContent("앵커 유실 이력 있음");
  });

  it("앵커가 유실되면 감사자에게 다시 시작 버튼이 보이고 누르면 재시작을 요청한다", async () => {
    await runVerify({ ok: false, chainOk: true, level: "failed", externallyVerified: false, anchor: anchorOf("anchor_lost", "앵커 파일이 사라졌습니다.") });
    expect(screen.getByTestId("anchor-status")).toHaveTextContent("외부 앵커 유실");
    post.mockClear();
    post.mockResolvedValue({ checked: 5, brokenSeq: 0, brokenWhy: "", headHash: "h", ok: true, chainOk: true, anchor: anchorOf("ok", "일치") });
    fireEvent.click(screen.getByRole("button", { name: "앵커 다시 시작" }));
    await screen.findByRole("status");
    expect(post).toHaveBeenCalledWith("/api/audit-logs/anchor/restart");
  });

  it("검증 권한이 없는 역할에게는 검증 버튼이 없다", async () => {
    get.mockResolvedValue({ records: [], nextBeforeSeq: 0 });
    render(
      <AuthProvider initialUser={{ ...auditor, role: "admin" }}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findByText("기록이 없습니다.");
    expect(screen.queryByRole("button", { name: "변조 여부 검증" })).toBeNull();
  });
});

describe("개인 토큰 화면", () => {
  it("발급한 원문은 한 번만 보여 주고 확인하면 사라진다", async () => {
    get.mockResolvedValue([]);
    post.mockResolvedValue({ token: "plp_secret-value" });
    render(<TokensPage />);
    fireEvent.change(screen.getByLabelText(/이름/), { target: { value: "노트북" } });
    fireEvent.click(screen.getByRole("button", { name: "발급" }));
    expect(await screen.findByTestId("fresh-token")).toHaveTextContent("plp_secret-value");
    expect(screen.getByText(/지금만 볼 수 있습니다/)).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/tokens", { name: "노트북", ttlDays: 30 });
    fireEvent.click(screen.getByRole("button", { name: "저장했습니다" }));
    await waitFor(() => expect(screen.queryByTestId("fresh-token")).toBeNull());
  });

  it("유효 토큰 상한에서 발급이 거부되면 서버의 업무 문구를 그대로 보여 준다", async () => {
    const msg = "사용 중인 개인 토큰이 상한(10개)에 도달했습니다. 쓰지 않는 토큰을 폐기한 뒤 다시 발급하세요.";
    get.mockResolvedValue([]);
    post.mockRejectedValue(new ApiError(409, msg));
    render(<TokensPage />);
    fireEvent.change(screen.getByLabelText(/이름/), { target: { value: "노트북" } });
    fireEvent.click(screen.getByRole("button", { name: "발급" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(msg);
    expect(screen.queryByTestId("fresh-token")).toBeNull();
  });

  it("화면 문구에 MCP·API 같은 내부 용어가 보이지 않는다", async () => {
    get.mockResolvedValue([]);
    const { container } = render(<TokensPage />);
    await screen.findByText("발급한 토큰이 없습니다.");
    expect(container.textContent).not.toMatch(/MCP|API|\bToken\b/);
    expect(container.textContent).toContain("외부 프로그램");
  });

  it("목록에는 원문 대신 앞부분만 있고 폐기된 토큰에는 폐기 버튼이 없다", async () => {
    get.mockResolvedValue([
      { id: 1, name: "a", prefix: "plp_abcd", createdAt: "2026-11-01T00:00:00Z", expiresAt: "2999-01-01T00:00:00Z", revokedAt: null, lastUsedAt: null },
      { id: 2, name: "b", prefix: "plp_wxyz", createdAt: "2026-11-01T00:00:00Z", expiresAt: "2999-01-01T00:00:00Z", revokedAt: "2026-11-02T00:00:00Z", lastUsedAt: null },
    ]);
    render(<TokensPage />);
    expect(await screen.findByText("plp_abcd…")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "폐기" })).toHaveLength(1);
    expect(screen.getByText("폐기됨")).toBeInTheDocument();
  });
});

describe("로그인 화면", () => {
  it("dev IdP 가 꺼져 있으면 개발용 계정을 보이지 않는다", async () => {
    get.mockResolvedValue({ login: true, devIdp: false });
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(screen.queryByText("개발용 계정")).toBeNull();
    expect(screen.getByRole("link", { name: "사내 계정으로 로그인" })).toHaveAttribute("href", "/api/auth/login");
  });

  it("dev IdP 가 켜져 있으면 계정 바로가기를 보여 주고 오류 코드를 문장으로 바꾼다", async () => {
    get.mockResolvedValue({
      login: true,
      devIdp: true,
      devAccounts: [{ email: "staff@pl.test", name: "구매직원", role: "staff", department: "구매팀" }],
    });
    render(
      <MemoryRouter initialEntries={["/login?error=forbidden"]}>
        <LoginPage />
      </MemoryRouter>,
    );
    const link = await screen.findByRole("link", { name: /구매직원/ });
    expect(link).toHaveAttribute("href", "/api/auth/login?login_hint=staff%40pl.test");
    expect(screen.getByRole("alert")).toHaveTextContent("등록되지 않았거나");
  });

  it("서버 오류 문구는 무엇이 안 됐는지와 다음 할 일을 함께 알린다", async () => {
    get.mockResolvedValue({ login: true, devIdp: false });
    render(
      <MemoryRouter initialEntries={["/login?error=server"]}>
        <LoginPage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "로그인 처리 중 서버 오류가 발생했습니다. 잠시 뒤 다시 시도하고, 계속되면 관리자에게 알려 주세요.",
    );
  });

  it("개발용 계정 목록은 서버가 준 조직도 주소속을 부서 자리에 그대로 보인다", async () => {
    get.mockResolvedValue({
      login: true,
      devIdp: true,
      devAccounts: [{ email: "staff@pl.test", name: "구매직원", role: "staff", department: "구매1팀" }],
    });
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("link", { name: "구매직원 (직원, 구매1팀)" })).toBeInTheDocument();
  });
});

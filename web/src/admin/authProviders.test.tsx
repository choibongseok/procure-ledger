import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { EmergencyLoginPage } from "../auth/EmergencyLoginPage";
import { AdminAuthPage } from "./AdminAuthPage";
import { PERMISSION_LABELS } from "./auditLabels";

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
const put = vi.mocked(api.put);

const policy = {
  password: { minLength: 10, maxLength: 128, requireClasses: 3, historyCount: 3, expireDays: 0, banCommon: true },
  emailLoginEnabled: true,
  totpRequiredPerms: ["role:manage", "auth:manage"],
  emergencyAdmins: [] as number[],
};

function provider(over: Record<string, unknown>) {
  return { enabled: false, envReady: true, configProblem: "", testedAt: null, allowedDomains: [], tenantId: "", displayName: "", linkedUsers: 0, ...over };
}

const users = [{ userId: 1, email: "admin@pl.test", name: "김관리자", role: "admin", active: true, hasPassword: true, mustChange: false, totp: false, identities: ["google"], canAuthManage: true }];

function mockApi(providers: Record<string, unknown>[], extra: Record<string, unknown> = {}) {
  get.mockImplementation(async (path: string) => {
    if (path === "/api/admin/auth/providers") return { providers, policy, mailEnabled: false, totpAvailable: true, ...extra };
    if (path === "/api/admin/auth/users") return { users, mailEnabled: false };
    if (path.endsWith("/test-result")) return { result: { ok: true, summary: { 도메인: "pl.test", 이메일: "a***@pl.test" }, createdAt: "2026-11-12T00:00:00Z" } };
    return {};
  });
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
});

describe("로그인 설정: 방식 카드", () => {
  it("환경변수가 없으면 경고하고 비밀 입력칸 없이, 시험 전에는 켜기가 막힌다", async () => {
    mockApi([provider({ provider: "google", envReady: false }), provider({ provider: "microsoft" }), provider({ provider: "saml", envProblem: "키 파일 권한이 너무 넓습니다(0600 이어야 함)", envReady: false })]);
    render(<AdminAuthPage />);
    expect(await screen.findByText(/클라이언트 ID·비밀\)가 설정되지 않았습니다/)).toBeInTheDocument();
    expect(screen.getByText(/키 파일 권한이 너무 넓습니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/비밀/)).toBeNull();
    for (const b of screen.getAllByRole("button", { name: "켜기" })) expect(b).toBeDisabled();
    expect(screen.getAllByText("시험 안 함")).toHaveLength(3);
  });

  it("구글 도메인을 저장하고 연결 시험 결과(가린 값)를 보며, 시험 성공 뒤 켠다", async () => {
    mockApi([provider({ provider: "google", allowedDomains: ["pl.test"], testedAt: "2026-11-12T00:00:00Z", redirectUri: "http://x/api/auth/callback" }), provider({ provider: "microsoft" }), provider({ provider: "saml" })]);
    put.mockResolvedValue({});
    post.mockResolvedValue({ url: "https://accounts.example/auth" });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    render(<AdminAuthPage />);
    const domains = await screen.findByLabelText(/허용 도메인/);
    fireEvent.change(domains, { target: { value: "pl.test, corp.example" } });
    fireEvent.click(screen.getAllByRole("button", { name: "설정 저장" })[0]);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/auth/providers/google", { displayName: "", allowedDomains: ["pl.test", "corp.example"] }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(screen.getAllByRole("button", { name: "연결 시험" })[0]).toBeEnabled());
    fireEvent.click(screen.getAllByRole("button", { name: "연결 시험" })[0]);
    await waitFor(() => expect(open).toHaveBeenCalledWith("https://accounts.example/auth", "_blank", "noopener"));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(screen.getAllByRole("button", { name: "시험 결과 보기" })[0]).toBeEnabled());
    fireEvent.click(screen.getAllByRole("button", { name: "시험 결과 보기" })[0]);
    expect(await screen.findByText("시험 성공")).toBeInTheDocument();
    expect(screen.getByText(/a\*\*\*@pl.test/)).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "켜기" })[0]);
    await waitFor(() => expect(put).toHaveBeenLastCalledWith("/api/admin/auth/providers/google", { enabled: true }));
    open.mockRestore();
  });

  it("켜진 방식은 설정을 잠그고 끄기만 보인다. 서버 거부 사유는 그대로 보인다", async () => {
    mockApi([provider({ provider: "google", enabled: true, allowedDomains: ["pl.test"], testedAt: "2026-11-12T00:00:00Z" }), provider({ provider: "microsoft" }), provider({ provider: "saml" })]);
    put.mockRejectedValue(new ApiError(409, JSON.stringify({ code: "invalid_state", message: "켜진 외부 로그인 방식으로 연결된 활성 관리자가 한 명도 없습니다" })));
    render(<AdminAuthPage />);
    expect(await screen.findByLabelText(/허용 도메인/)).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "끄기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("연결된 활성 관리자가 한 명도 없습니다");
  });

  it("SAML 카드에 SP 값(복사)과 메타데이터 붙여넣기, 인증서 만료 임박 경고가 있다", async () => {
    const soon = new Date(Date.now() + 10 * 86400000).toISOString();
    mockApi([provider({ provider: "google" }), provider({ provider: "microsoft" }), provider({ provider: "saml", entityId: "https://erp/api/auth/saml/metadata", acsUrl: "https://erp/api/auth/saml/acs", idpEntityId: "https://idp/meta", idpCertFingerprints: ["abcdef0123456789abcdef"], idpCertNotAfter: soon })]);
    render(<AdminAuthPage />);
    expect(await screen.findByText("https://erp/api/auth/saml/acs")).toBeInTheDocument();
    expect(screen.getByLabelText(/IdP 메타데이터/)).toBeInTheDocument();
    expect(screen.getByText(/30일 안에 만료됩니다/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "SP 메타데이터 내려받기" })).toHaveAttribute("href", "/api/auth/saml/metadata");
  });
});

describe("로그인 설정: 정책", () => {
  it("이메일 로그인 끄기 거부 사유가 보이고, 비상 로그인 사용 알림이 상단에 있다", async () => {
    mockApi([provider({ provider: "google" }), provider({ provider: "microsoft" }), provider({ provider: "saml" })], { recentEmergencyLogin: "2026-11-12T00:00:00Z" });
    put.mockRejectedValue(new ApiError(409, JSON.stringify({ code: "invalid_state", message: "이메일 로그인을 끄려면 연결 시험에 성공한 외부 로그인 방식이 하나 이상 켜져 있어야 합니다" })));
    render(<AdminAuthPage />);
    expect(await screen.findByText(/최근 비상 로그인이 사용되었습니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "이메일 로그인 끄기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("외부 로그인 방식이 하나 이상 켜져 있어야 합니다");
    expect(put).toHaveBeenCalledWith("/api/admin/auth/policy", { emailLoginEnabled: false });
  });

  it("비밀번호 정책·필수 역할·비상 관리자를 저장한다", async () => {
    mockApi([provider({ provider: "google" }), provider({ provider: "microsoft" }), provider({ provider: "saml" })]);
    put.mockResolvedValue({});
    render(<AdminAuthPage />);
    fireEvent.change(await screen.findByLabelText("최소 길이"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "정책 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/auth/policy", { password: { minLength: 12, maxLength: 128, requireClasses: 3, historyCount: 3, expireDays: 0, banCommon: true } }));
    fireEvent.click(screen.getByLabelText(PERMISSION_LABELS["ledger:write"]));
    fireEvent.click(screen.getAllByRole("button", { name: "저장" })[0]);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/auth/policy", { totpRequiredPerms: ["role:manage", "auth:manage", "ledger:write"] }));
    fireEvent.click(screen.getByLabelText("김관리자"));
    fireEvent.click(screen.getAllByRole("button", { name: "저장" })[1]);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/auth/policy", { emergencyAdmins: [1] }));
  });

  it("사용자 표에서 외부 연결을 해제한다", async () => {
    mockApi([provider({ provider: "google" }), provider({ provider: "microsoft" }), provider({ provider: "saml" })]);
    post.mockResolvedValue(undefined);
    render(<AdminAuthPage />);
    fireEvent.click(await screen.findByRole("button", { name: "김관리자 구글 연결 해제" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/users/1/auth/unlink", { provider: "google" }));
  });
});

describe("비상 로그인 화면", () => {
  it("비밀번호 뒤 2단계 코드를 묻고, 오류는 구체 문구로 보인다", async () => {
    post.mockResolvedValueOnce({ totp: true, ticket: "t1" }).mockRejectedValueOnce(new ApiError(401, JSON.stringify({ code: "bad_code", message: "인증 코드가 올바르지 않습니다" })));
    render(
      <MemoryRouter>
        <EmergencyLoginPage />
      </MemoryRouter>,
    );
    fireEvent.change(screen.getByLabelText("이메일"), { target: { value: "admin@pl.test" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    fireEvent.change(await screen.findByLabelText(/6자리 코드/), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("인증 코드가 올바르지 않습니다");
    expect(post).toHaveBeenNthCalledWith(1, "/api/auth/login/emergency", { email: "admin@pl.test", password: "pw" });
    expect(post).toHaveBeenNthCalledWith(2, "/api/auth/login/totp", { ticket: "t1", code: "000000" });
  });
});

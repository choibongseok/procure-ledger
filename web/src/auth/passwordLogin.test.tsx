import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { AdminAuthPage } from "../admin/AdminAuthPage";
import { api, ApiError } from "../api/client";
import { AuthProvider } from "./AuthProvider";
import { LoginPage } from "./LoginPage";
import { ForgotPasswordPage, ResetPasswordPage } from "./PasswordLinkPages";

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

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("이메일 로그인 화면", () => {
  it("켜진 방식만 그린다: 이메일 로그인이 꺼져 있으면 입력 칸이 없다", async () => {
    get.mockResolvedValue({ login: true, devIdp: false, emailLogin: false });
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("link", { name: "사내 계정으로 로그인" })).toBeInTheDocument();
    expect(screen.queryByLabelText("비밀번호")).toBeNull();
  });

  it("메일이 설정되지 않으면 재설정 링크 대신 관리자 안내만 보인다", async () => {
    get.mockResolvedValue({ login: false, devIdp: false, emailLogin: true, passwordResetByMail: false });
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    expect(await screen.findByLabelText("비밀번호")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "비밀번호를 잊으셨나요?" })).toBeNull();
    expect(screen.getByText(/관리자에게 임시 비밀번호를 요청/)).toBeInTheDocument();
  });

  it("실패하면 서버의 한국어 문구를 보이고 이메일은 남기며 비밀번호는 비운다", async () => {
    get.mockResolvedValue({ login: false, devIdp: false, emailLogin: true, passwordResetByMail: true });
    post.mockRejectedValue(new ApiError(401, JSON.stringify({ code: "bad_credentials", message: "이메일 또는 비밀번호가 올바르지 않습니다" })));
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("이메일"), { target: { value: "kim@pl.test" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("이메일 또는 비밀번호가 올바르지 않습니다");
    expect(screen.getByLabelText("이메일")).toHaveValue("kim@pl.test");
    expect(screen.getByLabelText("비밀번호")).toHaveValue("");
    expect(post).toHaveBeenCalledWith("/api/auth/login/password", { email: "kim@pl.test", password: "wrong" });
    expect(screen.getByRole("link", { name: "비밀번호를 잊으셨나요?" })).toHaveAttribute("href", "/login/forgot");
  });

  it("잠김 문구는 서버 문구 그대로 보인다", async () => {
    get.mockResolvedValue({ login: false, devIdp: false, emailLogin: true });
    post.mockRejectedValue(new ApiError(423, JSON.stringify({ code: "locked", message: "로그인 시도가 너무 많아 15분 동안 잠겼습니다. 14:35 이후에 다시 시도하거나 관리자에게 해제를 요청하세요" })));
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("이메일"), { target: { value: "a@b.c" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("15분 동안 잠겼습니다");
  });
});

describe("재설정 화면", () => {
  it("요청은 항상 같은 안내를 보인다", async () => {
    post.mockResolvedValue({ message: "x" });
    render(
      <MemoryRouter>
        <ForgotPasswordPage />
      </MemoryRouter>,
    );
    fireEvent.change(screen.getByLabelText("이메일"), { target: { value: "nobody@pl.test" } });
    fireEvent.click(screen.getByRole("button", { name: "재설정 링크 받기" }));
    expect(await screen.findByText(/등록된 이메일이면 재설정 링크를 보냈습니다/)).toBeInTheDocument();
  });

  it("링크의 토큰으로 새 비밀번호를 보내고, 두 칸이 다르면 보내지 않는다", async () => {
    post.mockResolvedValue({});
    render(
      <MemoryRouter initialEntries={["/login/reset?token=abc"]}>
        <ResetPasswordPage />
      </MemoryRouter>,
    );
    fireEvent.change(screen.getByLabelText("새 비밀번호"), { target: { value: "Aa1!aaaaaaaa" } });
    fireEvent.change(screen.getByLabelText("새 비밀번호 확인"), { target: { value: "다르다" } });
    fireEvent.click(screen.getByRole("button", { name: "비밀번호 바꾸기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("서로 다릅니다");
    expect(post).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("새 비밀번호 확인"), { target: { value: "Aa1!aaaaaaaa" } });
    fireEvent.click(screen.getByRole("button", { name: "비밀번호 바꾸기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/auth/password/reset", { token: "abc", password: "Aa1!aaaaaaaa" }));
  });
});

describe("제한 세션", () => {
  it("setup 이 있으면 업무 화면 대신 비밀번호 변경 화면만 그린다", async () => {
    get.mockResolvedValue({});
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider initialUser={{ id: 1, name: "홍길동", role: "staff", departmentId: 1 }} initialSetup="password_change">
          <App />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "비밀번호를 바꿔 주세요" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "주 메뉴" })).toBeNull();
    fireEvent.change(screen.getByLabelText("현재 비밀번호"), { target: { value: "Temp-1234!abc" } });
    fireEvent.change(screen.getByLabelText("새 비밀번호"), { target: { value: "short" } });
    fireEvent.change(screen.getByLabelText("새 비밀번호 확인"), { target: { value: "short" } });
    post.mockRejectedValue(new ApiError(400, JSON.stringify({ code: "policy", message: "비밀번호는 10자 이상이어야 합니다. 지금 5자입니다" })));
    fireEvent.click(screen.getByRole("button", { name: "비밀번호 바꾸기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("10자 이상이어야 합니다");
  });
});

describe("로그인 설정 화면", () => {
  const users = [
    { userId: 2, email: "staff@pl.test", name: "구매직원", role: "staff", active: true, hasPassword: false, mustChange: false, identities: [] },
    { userId: 3, email: "lead@pl.test", name: "팀장", role: "manager", active: true, hasPassword: true, mustChange: false, lockedUntil: "2999-01-01T00:00:00Z", identities: ["google"] },
  ];

  it("메일이 없으면 임시 비밀번호만 권하고, 발급한 비밀번호는 한 번만 보인다", async () => {
    get.mockResolvedValue({ users, mailEnabled: false });
    post.mockResolvedValue({ via: "temp", tempPassword: "Zz9!abcdefghij", expiresAt: "2026-11-13T00:00:00Z" });
    render(<AdminAuthPage />);
    expect(await screen.findByText(/메일 발송이 설정되지 않았습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "설정 링크 보내기" })).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "임시 비밀번호 발급" })[0]);
    expect(await screen.findByTestId("temp-password")).toHaveTextContent("Zz9!abcdefghij");
    expect(post).toHaveBeenCalledWith("/api/admin/users/2/auth/start", { via: "temp" });
    fireEvent.click(screen.getByRole("button", { name: "전달했습니다" }));
    expect(screen.queryByTestId("temp-password")).toBeNull();
    expect(screen.getByText("구글")).toBeInTheDocument();
  });

  it("잠긴 계정에만 잠금 해제 단추가 있다", async () => {
    get.mockResolvedValue({ users, mailEnabled: true });
    post.mockResolvedValue(undefined);
    render(<AdminAuthPage />);
    const btn = await screen.findByRole("button", { name: "잠금 해제" });
    expect(screen.getAllByRole("button", { name: "잠금 해제" })).toHaveLength(1);
    fireEvent.click(btn);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/users/3/auth/unlock"));
  });
});

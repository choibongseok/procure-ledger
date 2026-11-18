import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AccountPage } from "./AccountPage";
import { LoginPage } from "./LoginPage";
import { TotpPanel } from "./TotpPanel";

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

const base = { method: "password", hasPassword: true, totpAvailable: true, totpEnrolled: false, totpRequired: false };

describe("로그인의 2단계 코드 단계", () => {
  it("비밀번호가 통과하면 티켓으로 코드를 묻고, 코드를 보낸다", async () => {
    get.mockResolvedValue({ login: false, devIdp: false, emailLogin: true });
    post.mockResolvedValueOnce({ totp: true, ticket: "tk1" }).mockResolvedValueOnce({ setup: "" });
    const assign = vi.fn();
    Object.defineProperty(window, "location", { value: { ...window.location, assign }, writable: true });
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("이메일"), { target: { value: "a@pl.test" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    const codeBox = await screen.findByLabelText("인증 코드");
    expect(screen.queryByLabelText("비밀번호")).toBeNull();
    fireEvent.change(codeBox, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/api/auth/login/totp", { ticket: "tk1", code: "123456" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
  });

  it("티켓이 만료되면 처음 화면으로 돌아간다", async () => {
    get.mockResolvedValue({ login: false, devIdp: false, emailLogin: true });
    post
      .mockResolvedValueOnce({ totp: true, ticket: "tk1" })
      .mockRejectedValueOnce(new ApiError(401, JSON.stringify({ code: "bad_ticket", message: "로그인 시간이 지났거나 시도 횟수를 넘었습니다. 처음부터 다시 로그인하세요" })));
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("이메일"), { target: { value: "a@pl.test" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    fireEvent.change(await screen.findByLabelText("인증 코드"), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("처음부터 다시 로그인하세요");
    expect(await screen.findByLabelText("비밀번호")).toBeInTheDocument();
  });
});

describe("2단계 인증 패널", () => {
  it("등록 → 코드 확인 → 복구 코드는 한 번만 보인다", async () => {
    post.mockResolvedValueOnce({ secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/ProcureLedger:a?secret=JBSWY3DPEHPK3PXP" });
    post.mockResolvedValueOnce({ recoveryCodes: ["aaaaa-bbbbb", "ccccc-ddddd"] });
    const changed = vi.fn();
    render(<TotpPanel summary={base} onChanged={changed} />);
    fireEvent.click(screen.getByRole("button", { name: "2단계 인증 시작" }));
    expect(await screen.findByTestId("totp-secret")).toHaveTextContent("JBSWY3DPEHPK3PXP");
    fireEvent.change(screen.getByLabelText("인증 앱의 6자리 코드"), { target: { value: "654321" } });
    fireEvent.click(screen.getByRole("button", { name: "확인하고 켜기" }));
    expect(await screen.findByTestId("recovery-codes")).toHaveTextContent("aaaaa-bbbbb");
    expect(post).toHaveBeenLastCalledWith("/api/auth/totp/confirm", { code: "654321" });
    expect(changed).not.toHaveBeenCalled(); // 복구 코드를 보관하기 전에는 화면이 바뀌지 않는다
    fireEvent.click(screen.getByRole("button", { name: "보관했습니다" }));
    await waitFor(() => expect(changed).toHaveBeenCalled());
    expect(screen.queryByTestId("recovery-codes")).toBeNull();
  });

  it("필수 역할에는 해제 단추가 없다", () => {
    render(<TotpPanel summary={{ ...base, totpEnrolled: true, totpRequired: true }} onChanged={() => {}} />);
    expect(screen.queryByRole("button", { name: "해제" })).toBeNull();
    expect(screen.getByText(/필수라 해제할 수 없습니다/)).toBeInTheDocument();
  });

  it("서버에 키가 없으면 꺼져 있다고 알린다", () => {
    render(<TotpPanel summary={{ ...base, totpAvailable: false }} onChanged={() => {}} />);
    expect(screen.getByRole("status")).toHaveTextContent("2단계 인증이 설정되지 않았습니다");
  });
});

describe("내 계정", () => {
  it("비밀번호가 없는 계정(외부 로그인)에는 변경 양식 대신 안내를 보인다", async () => {
    get.mockResolvedValue({ auth: { ...base, method: "google", hasPassword: false } });
    render(<AccountPage />);
    expect(await screen.findByText(/로그인 시작이 되지 않은|로그인이 시작되지 않은 계정/)).toBeInTheDocument();
    expect(screen.getByText(/로그인 방식: 구글/)).toBeInTheDocument();
    expect(screen.queryByLabelText("현재 비밀번호")).toBeNull();
  });

  it("비밀번호 변경은 두 칸이 같을 때만 보낸다", async () => {
    get.mockResolvedValue({ auth: base });
    post.mockResolvedValue(undefined);
    render(<AccountPage />);
    fireEvent.change(await screen.findByLabelText("현재 비밀번호"), { target: { value: "Old-Pass-1!x" } });
    fireEvent.change(screen.getByLabelText("새 비밀번호"), { target: { value: "New-Pass-1!xx" } });
    fireEvent.change(screen.getByLabelText("새 비밀번호 확인"), { target: { value: "New-Pass-1!xx" } });
    fireEvent.click(screen.getByRole("button", { name: "비밀번호 바꾸기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/auth/password/change", { current: "Old-Pass-1!x", new: "New-Pass-1!xx" }));
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SetupWizardPage } from "./SetupWizardPage";

interface Call {
  path: string;
  code: string | null;
  body: Record<string, unknown>;
}

let calls: Call[];
let status: number; // /api/setup/status 응답 코드
let codeStatus: number; // /api/setup/check 응답 코드
let validateErrors: Record<string, string>;
let completeStatus: number;
let completeBody: Record<string, unknown>;

function respond(body: unknown, st = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: st, headers: { "Content-Type": "application/json" } }));
}

beforeEach(() => {
  calls = [];
  status = 200;
  codeStatus = 200;
  validateErrors = {};
  completeStatus = 201;
  completeBody = { done: true };
  vi.stubGlobal(
    "fetch",
    vi.fn((path: string, init?: RequestInit) => {
      if (path === "/api/setup/status") return respond({ state: "pending" }, status);
      if (path === "/api/auth/config") return respond({ providers: [] });
      const headers = new Headers(init?.headers);
      calls.push({ path, code: headers.get("X-Setup-Code"), body: JSON.parse(String(init?.body ?? "{}")) });
      if (path === "/api/setup/check") return respond(codeStatus === 200 ? { ok: true, totpKeyConfigured: false } : { error: "설치 코드가 올바르지 않습니다" }, codeStatus);
      if (path === "/api/setup/validate") return respond({ ok: Object.keys(validateErrors).length === 0, errors: validateErrors });
      if (path === "/api/setup/complete") return respond(completeStatus === 201 ? completeBody : { error: "x" }, completeStatus);
      return respond({}, 404);
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

/** 입력 칸에 값을 넣는다(user-event 없이 change 이벤트로). */
function type(el: HTMLElement, value: string) {
  fireEvent.change(el, { target: { value } });
}

function renderWizard() {
  return render(
    <MemoryRouter>
      <SetupWizardPage />
    </MemoryRouter>,
  );
}

describe("설치 마법사 화면", () => {
  it("이미 끝난 서버(상태 404)에서는 마법사 대신 안내와 로그인 링크만 보인다", async () => {
    status = 404;
    renderWizard();
    expect(await screen.findByText(/초기 설정이 이미 끝났거나/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "로그인 화면으로" })).toHaveAttribute("href", "/login");
    expect(screen.queryByLabelText("설치 코드")).toBeNull();
  });

  it("틀린 설치 코드는 한국어 문구로 막고 다음 단계로 가지 않는다", async () => {
    codeStatus = 401;
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "WRONG");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("설치 코드가 올바르지 않습니다");
    expect(screen.queryByRole("region", { name: "회사 정보" })).toBeNull();
  });

  it("잠금(429)이면 15분 안내를 보인다", async () => {
    codeStatus = 429;
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "WRONG");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("15분");
  });

  it("끝까지 진행하면 코드를 헤더로 보내 한 번에 완료하고, 응답 뒤 로그인 안내를 보인다(세션 없음)", async () => {
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "AAAA-BBBB");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    type(await screen.findByLabelText("회사명"), "에이씨엠이");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    type(await screen.findByLabelText("이름"), "오너");
    type(screen.getByLabelText("이메일"), "owner@acme.test");
    type(screen.getByLabelText("비밀번호"), "Wizard-Pass-2026!");
    type(screen.getByLabelText("비밀번호 확인"), "Wizard-Pass-2026!");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(await screen.findByText(/이메일 로그인은 항상 켜집니다/)).toBeInTheDocument();
    expect(screen.getByText(/PL_AUTH_SECRET_KEY/)).toBeInTheDocument(); // 2단계 인증 키가 없다는 경고
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(await screen.findByText(/되돌릴 수 없고/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(await screen.findByText("에이씨엠이")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "설치 완료" }));
    expect(await screen.findByText("설치가 끝났습니다")).toBeInTheDocument();
    const done = calls.find((c) => c.path === "/api/setup/complete");
    expect(done?.code).toBe("AAAA-BBBB");
    expect(done?.body.adminEmail).toBe("owner@acme.test");
    expect(done?.body.adminPassword2).toBeUndefined(); // 확인용 두 번째 비밀번호는 서버로 보내지 않는다
    expect(screen.getByRole("link", { name: "로그인" })).toHaveAttribute("href", "/login");
  });

  it("비밀번호 확인이 다르면 서버에 보내지 않고 그 칸에 문구를 보인다", async () => {
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "AAAA");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    type(await screen.findByLabelText("회사명"), "X");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    type(await screen.findByLabelText("이름"), "오너");
    type(screen.getByLabelText("이메일"), "o@x.test");
    type(screen.getByLabelText("비밀번호"), "Wizard-Pass-2026!");
    type(screen.getByLabelText("비밀번호 확인"), "different");
    const before = calls.length;
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(await screen.findByText("비밀번호 두 번이 서로 다릅니다.")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("서버 검증 오류는 해당 칸 옆에 문구로 보이고 단계가 넘어가지 않는다", async () => {
    validateErrors = { email: "이메일 형식이 올바르지 않습니다." };
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "AAAA");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    type(await screen.findByLabelText("회사명"), "X");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    type(await screen.findByLabelText("이름"), "오너");
    type(screen.getByLabelText("이메일"), "bad");
    type(screen.getByLabelText("비밀번호"), "Wizard-Pass-2026!");
    type(screen.getByLabelText("비밀번호 확인"), "Wizard-Pass-2026!");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await waitFor(() => expect(screen.getByText("이메일 형식이 올바르지 않습니다.")).toBeInTheDocument());
    expect(screen.queryByText(/이메일 로그인은 항상 켜집니다/)).toBeNull();
  });

  it("데모 데이터를 넣지 못해도 설치 완료 화면에 경고와 재시도 안내를 보인다", async () => {
    completeBody = { done: true, demoFailed: true };
    renderWizard();
    type(await screen.findByLabelText("설치 코드"), "AAAA");
    fireEvent.click(screen.getByRole("button", { name: "확인" }));
    type(await screen.findByLabelText("회사명"), "에이씨엠이");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    type(await screen.findByLabelText("이름"), "오너");
    type(screen.getByLabelText("이메일"), "owner@acme.test");
    type(screen.getByLabelText("비밀번호"), "Wizard-Pass-2026!");
    type(screen.getByLabelText("비밀번호 확인"), "Wizard-Pass-2026!");
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await screen.findByRole("heading", { name: "로그인 방식" });
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await screen.findByRole("heading", { name: "데모 데이터" });
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await screen.findByRole("heading", { name: "확인" });
    fireEvent.click(screen.getByRole("button", { name: "설치 완료" }));
    expect(await screen.findByText("설치가 끝났습니다")).toBeInTheDocument();
    expect(screen.getByText(/데모 데이터를 넣지 못했습니다/)).toBeInTheDocument();
    expect(screen.getByText(/erp-admin demo-seed/)).toBeInTheDocument();
  });
});

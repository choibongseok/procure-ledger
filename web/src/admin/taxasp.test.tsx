import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { TaxAspPage } from "./TaxAspPage";

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

const base = {
  configured: true,
  provider: "acme",
  providers: ["acme", "mock"],
  active: true,
  host: "api.acme.example",
  hasKey: true,
  hasCert: true,
  certNotAfter: "2027-03-01",
  certSubject: "…테스트 법인",
  certState: "ok",
  mockWarning: false,
  rehearsal: false,
  updatedAt: "2026-11-13T01:00:00Z",
};

const renderPage = () => render(<MemoryRouter><TaxAspPage /></MemoryRouter>);

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = ["taxasp:manage"];
});

describe("전자세금계산서 대행사 설정", () => {
  it("값은 보이지 않고 호스트·만료일만 보이며, 저장하면 입력칸이 비워진다", async () => {
    get.mockResolvedValue(base);
    put.mockResolvedValue(base);
    renderPage();
    await screen.findByText("api.acme.example");
    expect(screen.getByText(/만료일 2027-03-01/)).toBeTruthy();
    expect(screen.getByText("등록됨(보이지 않음)")).toBeTruthy();
    const key = screen.getByLabelText("API 키") as HTMLInputElement;
    expect(key.type).toBe("password");
    fireEvent.change(key, { target: { value: "KEY-SECRET" } });
    fireEvent.change(screen.getByLabelText("접속 주소(https)"), { target: { value: "https://api.acme.example/v2" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][1]).toMatchObject({ provider: "acme", apiKey: "KEY-SECRET", endpoint: "https://api.acme.example/v2" });
    await screen.findByText(/봉인되어 다시 볼 수 없습니다/);
    expect((screen.getByLabelText("API 키") as HTMLInputElement).value).toBe("");
    expect(document.body.textContent).not.toContain("KEY-SECRET");
  });

  it("모의 대행사 경고·만료 임박/만료 안내를 한국어로 보인다", async () => {
    get.mockResolvedValue({ ...base, provider: "mock", mockWarning: true, certState: "expiring", daysLeft: 9 });
    renderPage();
    await screen.findByText(/모의 대행사가 선택되어 있습니다/);
    expect(screen.getByText(/9일 뒤 만료됩니다/)).toBeTruthy();
    expect(screen.queryByText(/\bmock\b/)).toBeNull();
  });

  it("시험 연결 결과 문장과 사용 끄기를 보여 준다", async () => {
    get.mockResolvedValue(base);
    post.mockResolvedValueOnce({ ok: false, message: "대행사 인증에 실패했습니다. 대행사 설정을 확인하세요." });
    renderPage();
    await screen.findByText("api.acme.example");
    fireEvent.click(screen.getByRole("button", { name: "시험 연결" }));
    await screen.findByText(/대행사 인증에 실패했습니다/);
    post.mockResolvedValueOnce({});
    fireEvent.click(screen.getByRole("button", { name: "사용 끄기" }));
    await screen.findByText(/사용을 껐습니다/);
  });

  it("권한이 없으면 안내만 보인다", () => {
    perms = [];
    get.mockResolvedValue(base);
    renderPage();
    expect(screen.getByText(/볼 권한이 없습니다/)).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  });
});

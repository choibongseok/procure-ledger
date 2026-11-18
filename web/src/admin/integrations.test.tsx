import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { IntegrationsPage } from "./IntegrationsPage";

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

let auth: Record<string, unknown> = {};
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);
const del = vi.mocked(api.del);

const settings = { enabled: false, ratePerMin: 600, writePerMin: 30, defaultRate: 600, maxActiveTokens: 5, maxConcurrent: 32, globalCidr: false, rehearsal: false, changesKeepDays: 90, openapiPath: "/api/v1/openapi.json" };
const scopes = [
  { name: "items:read", title: "품목과 재고 수량 읽기", held: true },
  { name: "journals:read", title: "전표 읽기(금액 포함)", held: false },
  { name: "purchase_orders:read", title: "발주 읽기(전사 범위)", held: true },
];
const tokens = [
  { id: 1, name: "회계 야간 동기화", prefix: "pli_AbCd", scopes: ["items:read"], createdBy: 1, createdByName: "관리자", createdAt: "2026-11-01T00:00:00Z", expiresAt: "2026-11-20T00:00:00Z", lastUsedAt: "2026-11-13T00:00:00Z", allowCidr: "", status: "active", calls24h: 1234, denied24h: 2, warnings: ["expiring", "issuer_reduced"] },
  { id: 2, name: "옛 토큰", prefix: "pli_ZzZz", scopes: ["items:read"], createdBy: 1, createdByName: "관리자", createdAt: "2026-01-01T00:00:00Z", expiresAt: "2026-12-20T00:00:00Z", revokedAt: "2026-02-01T00:00:00Z", allowCidr: "", status: "revoked", calls24h: 0, denied24h: 0, warnings: [] },
];

function mockGets(t: unknown[] = tokens, s = settings) {
  get.mockImplementation(async (url: string) => {
    if (url.endsWith("/settings")) return s;
    if (url.endsWith("/scopes")) return scopes;
    return t;
  });
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  del.mockReset();
  auth = { entities: [], groupScope: false };
});

describe("연동 관리 화면", () => {
  it("설정·토큰 표·경고 배지를 보이고 폐기된 토큰에는 폐기 단추가 없다", async () => {
    mockGets();
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    const table = await screen.findByRole("table", { name: "API 토큰" });
    expect(await within(table).findByText("만료 임박")).toBeInTheDocument();
    expect(await within(table).findByText("발급자 권한이 줄었습니다")).toBeInTheDocument();
    expect(await within(table).findByText(/호출 1,234건/)).toBeInTheDocument();
    expect(within(table).getAllByRole("button", { name: "폐기" })).toHaveLength(1);
    expect(screen.getByText("꺼짐")).toBeInTheDocument();
    expect(screen.getByText(/유효 토큰은 5개까지/)).toBeInTheDocument();
  });

  it("권한이 없는 범위는 고를 수 없고, 범위를 고르면 발급되며 원문을 한 번만 보여 준다", async () => {
    mockGets([]);
    post.mockResolvedValue({ token: "pli_SECRETTOKEN", info: {} });
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    const form = await screen.findByRole("form", { name: "토큰 발급" });
    const journal = await within(form).findByRole("checkbox", { name: /전표 읽기/ });
    expect(journal).toBeDisabled();
    const submit = within(form).getByRole("button", { name: "토큰 발급" });
    expect(submit).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "재고 연동" } });
    fireEvent.click(within(form).getByRole("checkbox", { name: /발주 읽기/ }));
    expect(screen.getByText(/다른 부서의 발주까지 전사 범위/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("허용 출발지(선택)"), { target: { value: "10.0.0.0/8" } });
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/tokens", { name: "재고 연동", scopes: ["purchase_orders:read"], days: 180, allowCidr: "10.0.0.0/8" }));
    expect(await screen.findByTestId("api-fresh-token")).toHaveTextContent("pli_SECRETTOKEN");
    fireEvent.click(screen.getByRole("button", { name: "저장했습니다" }));
    expect(screen.queryByTestId("api-fresh-token")).toBeNull();
    expect(screen.queryByText("pli_SECRETTOKEN")).toBeNull();
  });

  it("켜기·한도 저장·폐기는 서버로 가고 폐기는 확인을 거친다", async () => {
    mockGets();
    put.mockResolvedValue(undefined);
    del.mockResolvedValue(undefined);
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "켜기" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/integrations/settings", { enabled: true }));
    fireEvent.change(screen.getByLabelText(/토큰당 분당 읽기 호출 한도/), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "한도 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/integrations/settings", { ratePerMin: 100 }));
    fireEvent.change(screen.getByLabelText(/토큰당 분당 쓰기 호출 한도/), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "쓰기 한도 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/integrations/settings", { writePerMin: 12 }));
    fireEvent.click(await screen.findByRole("button", { name: "폐기" }));
    expect(del).not.toHaveBeenCalled();
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "폐기" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/integrations/tokens/1"));
  });

  it("쓰기 범위는 쓰기 배지·안내·발급자 경고를 보이고, 발급 전에 한 번 더 확인하며, 발급 응답의 경고를 토큰과 함께 보인다", async () => {
    const writeScopes = [
      { name: "items:read", title: "품목과 재고 수량 읽기", held: true, write: false, note: "", warning: "" },
      { name: "journals:draft", title: "전표 초안 제출", held: true, write: true, note: "전표 초안만 만들 수 있고 승인은 못 합니다.", warning: "발급하는 분이 전표를 승인하는 권한도 가지고 있습니다." },
    ];
    get.mockImplementation(async (url: string) => {
      if (url.endsWith("/settings")) return settings;
      if (url.endsWith("/scopes")) return writeScopes;
      return [];
    });
    post.mockResolvedValue({ token: "pli_WRITETOKEN", info: {}, warnings: ["발급하는 분이 전표를 승인하는 권한도 가지고 있습니다."] });
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    const form = await screen.findByRole("form", { name: "토큰 발급" });
    expect(within(form).getAllByText("쓰기")).toHaveLength(1);
    expect(screen.queryByText(/전표 초안만 만들 수 있고/)).toBeNull();
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "전표 연동" } });
    // 읽기 범위만 고르면 확인 없이 바로 발급한다.
    fireEvent.click(within(form).getByRole("checkbox", { name: /품목과 재고/ }));
    fireEvent.click(within(form).getByRole("button", { name: "토큰 발급" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "저장했습니다" }));
    post.mockClear();
    // 쓰기 범위를 고르면 안내·경고가 보이고 발급 단추는 확인 대화상자를 연다.
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "전표 연동" } });
    fireEvent.click(within(form).getByRole("checkbox", { name: /전표 초안 제출/ }));
    expect(screen.getByText(/전표 초안만 만들 수 있고 승인은 못 합니다/)).toBeInTheDocument();
    expect(screen.getAllByText(/전표를 승인하는 권한도 가지고 있습니다/).length).toBeGreaterThan(0);
    // 발급 직후 목록 재조회가 끝나 busy 가 풀릴 때까지(발급 단추가 잠겨 있으면 클릭이 무시된다) 기다린다.
    await waitFor(() => expect(within(form).getByRole("button", { name: "토큰 발급" })).toBeEnabled());
    fireEvent.click(within(form).getByRole("button", { name: "토큰 발급" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(post).not.toHaveBeenCalled();
    expect(within(dialog).getByText(/허용 출발지를 좁히고/)).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "발급" })).toBeEnabled());
    fireEvent.click(within(dialog).getByRole("button", { name: "발급" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/tokens", expect.objectContaining({ scopes: ["journals:draft"] })));
    expect(await screen.findByTestId("api-fresh-token")).toHaveTextContent("pli_WRITETOKEN");
    expect(screen.getAllByText(/전표를 승인하는 권한도 가지고 있습니다/).length).toBeGreaterThan(0);
    expect(screen.getByText("주의")).toBeInTheDocument();
  });
});

describe("연동 토큰의 법인(B44)", () => {
  const entities = [
    { id: 1, code: "MAIN", name: "에이엑스 주식회사" },
    { id: 2, code: "ABC", name: "계열사" },
  ];

  it("법인이 하나면 법인 칸이 없고 발급 요청에 법인 값이 없다", async () => {
    mockGets();
    post.mockResolvedValue({ token: "pli_x" });
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    await screen.findByText("회계 야간 동기화");
    expect(screen.queryByLabelText("법인")).toBeNull();
    expect(screen.queryByRole("columnheader", { name: "법인" })).toBeNull();
  });

  it("법인이 둘 이상이면 법인을 골라 발급하고(그룹 전체는 그룹 범위가 있을 때만) 목록에 법인이 보인다", async () => {
    auth = { entities, groupScope: true };
    mockGets([{ ...tokens[0], entityId: 2, entityCode: "ABC" }, { ...tokens[1], entityId: 0, entityCode: "" }]);
    post.mockResolvedValue({ token: "pli_x" });
    render(<MemoryRouter><IntegrationsPage /></MemoryRouter>);
    await screen.findByText("회계 야간 동기화");
    expect(screen.getByRole("columnheader", { name: "법인" })).toBeInTheDocument();
    expect(screen.getAllByText("계열사").length).toBeGreaterThan(0);
    expect(screen.getByText("그룹 전체")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("이름"), { target: { value: "신규" } });
    fireEvent.click(screen.getByLabelText(/품목과 재고 수량 읽기/));
    fireEvent.change(screen.getByLabelText("법인"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "토큰 발급" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/integrations/tokens", expect.objectContaining({ entityId: 2 })));
    expect(screen.getByRole("option", { name: "그룹 전체(읽기만)" })).toBeInTheDocument();
  });
});

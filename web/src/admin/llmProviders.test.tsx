import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { LlmProvidersPage, LlmRoutesPage } from "./LlmProviderPages";

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
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: (p: string) => perms.includes(p) }) }));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

const prov = { id: 1, code: "vllm-hq", kind: "openai_compat", name: "사내 모델", defaultModel: "vllm-x", hint: "llm.example.com", mode: "", external: true, active: true, hasSecret: true, priceInMicroPerMtok: 0, priceOutMicroPerMtok: 0, timeoutMs: 30000, version: 1 };

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = ["llm:provider_manage", "llm:policy_manage"];
});

describe("LLM 공급자 화면", () => {
  it("호스트와 '키 저장됨' 만 보이고 키·주소 원문은 없다. 종류는 한국어 이름이다", async () => {
    get.mockResolvedValue({ providers: [prov], kinds: ["mock", "openai_compat"], sealerOk: true, rehearsal: false, mockModes: [] });
    const { container } = render(<LlmProvidersPage />);
    const table = await screen.findByRole("table", { name: "공급자 목록" });
    expect(await within(table).findByText("llm.example.com")).toBeTruthy();
    expect(await within(table).findByText("키 저장됨")).toBeTruthy();
    expect(await within(table).findByText(/OpenAI 호환/)).toBeTruthy();
    expect(container.textContent).not.toMatch(/openai_compat|secret|sk-/);
  });

  it("등록하면 양식 값이 API 로 가고 시험 연결 결과를 한국어로 보인다", async () => {
    get.mockResolvedValue({ providers: [prov], kinds: ["mock", "openai_compat"], sealerOk: true, rehearsal: false, mockModes: [] });
    post.mockResolvedValue({ ok: true, code: "ok" });
    render(<LlmProvidersPage />);
    await screen.findByRole("table", { name: "공급자 목록" });
    fireEvent.click(await screen.findByRole("button", { name: "사내 모델 시험 연결" }));
    await screen.findByText(/사내 모델: 연결됐습니다/);
    const form = screen.getByRole("form", { name: "공급자 양식" });
    fireEvent.change(within(form).getByLabelText("공급자 코드"), { target: { value: "new-one" } });
    fireEvent.change(within(form).getByLabelText("이름"), { target: { value: "새 공급자" } });
    fireEvent.change(within(form).getByLabelText("주소(https)"), { target: { value: "https://x.example.com/v1" } });
    fireEvent.change(within(form).getByLabelText("API 키"), { target: { value: "sk-1" } });
    fireEvent.click(within(form).getByRole("button", { name: "등록" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/llm/providers", expect.objectContaining({ code: "new-one", kind: "openai_compat", baseUrl: "https://x.example.com/v1", apiKey: "sk-1" })));
  });

  it("권한이 없으면 안내만 보인다", async () => {
    perms = [];
    get.mockResolvedValue(null);
    render(<LlmProvidersPage />);
    await waitFor(() => expect(screen.getByText(/볼 권한이 없습니다/)).toBeTruthy());
  });
});

describe("LLM 라우팅 화면", () => {
  it("규칙 표와 경로 확인 결과를 보인다", async () => {
    get.mockResolvedValue({
      routes: [{ id: 5, sortOrder: 0, purposePattern: "docverify.*", departmentId: 0, role: "", primaryProviderId: 1, primaryModel: "", fallbackProviderId: 0, fallbackModel: "", primaryCode: "vllm-hq", fallbackCode: "", active: true }],
      providers: [prov],
    });
    post.mockResolvedValue({ matched: true, primaryCode: "vllm-hq", primaryModel: "vllm-x", fallbackCode: "", defaultPath: false });
    render(<LlmRoutesPage />);
    const table = await screen.findByRole("table", { name: "라우팅 규칙" });
    expect(await within(table).findByText("docverify.*")).toBeTruthy();
    expect(await within(table).findByText("모든 부서")).toBeTruthy();
    const sim = screen.getByRole("form", { name: "경로 확인 양식" });
    fireEvent.change(within(sim).getByLabelText("용도"), { target: { value: "docverify.extract" } });
    fireEvent.click(within(sim).getByRole("button", { name: "확인" }));
    await screen.findByText(/vllm-hq \(vllm-x\) 로 갑니다/);
  });

  it("부서는 이름으로 고르고 표에도 이름이 나온다", async () => {
    get.mockResolvedValue({
      routes: [{ id: 6, sortOrder: 0, purposePattern: "*", departmentId: 7, role: "", primaryProviderId: 1, primaryModel: "", fallbackProviderId: 0, fallbackModel: "", primaryCode: "vllm-hq", fallbackCode: "", active: true }],
      providers: [prov],
      departments: [{ id: 7, name: "구매팀" }],
    });
    render(<LlmRoutesPage />);
    const table = await screen.findByRole("table", { name: "라우팅 규칙" });
    expect(await within(table).findByText("구매팀")).toBeTruthy();
    const form = screen.getByRole("form", { name: "규칙 양식" });
    const sel = within(form).getByLabelText("부서") as HTMLSelectElement;
    fireEvent.change(sel, { target: { value: "7" } });
    expect(sel.value).toBe("7");
  });
});

describe("LLM 공급자 화면: 종류별 양식", () => {
  it("Bedrock·Vertex 를 고르면 종류에 맞는 입력 칸이 나온다", async () => {
    perms = ["llm:provider_manage"];
    get.mockResolvedValue({ providers: [], kinds: ["mock", "openai_compat", "bedrock", "vertex"], sealerOk: true, rehearsal: false, mockModes: [] });
    render(<LlmProvidersPage />);
    const form = await screen.findByRole("form", { name: "공급자 양식" });
    fireEvent.change(within(form).getByLabelText("종류"), { target: { value: "bedrock" } });
    expect(within(form).getByLabelText("리전")).toBeTruthy();
    expect(within(form).getByLabelText("비밀 액세스 키")).toBeTruthy();
    expect(within(form).queryByLabelText("API 키")).toBeNull();
    fireEvent.change(within(form).getByLabelText("종류"), { target: { value: "vertex" } });
    expect(within(form).getByLabelText("서비스 계정 JSON")).toBeTruthy();
    expect(within(form).getByLabelText("로케이션")).toBeTruthy();
  });
});

describe("LLM 라우팅 화면: 저장 전 평가(B46)", () => {
  const list = { routes: [], providers: [prov], departments: [] };
  const fill = () => {
    const form = screen.getByRole("form", { name: "규칙 양식" });
    fireEvent.change(within(form).getByLabelText("용도 패턴"), { target: { value: "docverify.extract" } });
    fireEvent.change(within(form).getByLabelText("주 공급자"), { target: { value: "1" } });
    return form;
  };
  const blocked = { status: "blocked", message: "기준선(98%)보다 낮습니다(60%, 치명 실패 1건). 그래도 저장하시겠습니까?", external: false };

  it("기준선 미달이면 저장하지 않고 확인 칸을 열고, 사유와 함께 그래도 저장하면 gateOverride 로 다시 보낸다", async () => {
    get.mockResolvedValue(list);
    post.mockRejectedValueOnce(new ApiError(409, JSON.stringify({ error: blocked.message, gate: blocked }))).mockResolvedValueOnce({ id: 9, gate: blocked });
    const { container } = render(<LlmRoutesPage />);
    const form = fill();
    await screen.findByRole("form", { name: "규칙 양식" });
    fireEvent.click(within(form).getByRole("button", { name: "등록" }));
    const group = await screen.findByRole("group", { name: "저장 전 평가 확인" });
    expect(within(group).getByText(/기준선\(98%\)보다 낮습니다\(60%, 치명 실패 1건\)/)).toBeTruthy();
    expect(container.textContent).not.toMatch(/blocked|confirm_cost|\{"error"/);
    expect(post).toHaveBeenCalledTimes(1);
    fireEvent.change(within(group).getByLabelText(/무시 사유/), { target: { value: "시험 환경" } });
    fireEvent.click(within(group).getByRole("button", { name: "그래도 저장" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post).toHaveBeenLastCalledWith("/api/admin/llm/routes", expect.objectContaining({ purposePattern: "docverify.extract", gateOverride: true, gateReason: "시험 환경" }));
    await screen.findByText(/규칙을 등록했습니다\. 기준선\(98%\)보다 낮습니다/);
    expect(screen.queryByRole("group", { name: "저장 전 평가 확인" })).toBeNull();
  });

  it("외부 후보는 비용 확인을 먼저 받고 확인하면 gateConfirmCost 로 다시 보낸다. 취소하면 저장하지 않는다", async () => {
    get.mockResolvedValue(list);
    const cost = { status: "confirm_cost", message: "외부 공급자라 평가 실행에 비용이 들고 일일 한도를 씁니다. 계속하시겠습니까?", external: true };
    post.mockRejectedValueOnce(new ApiError(409, JSON.stringify({ error: cost.message, gate: cost }))).mockResolvedValueOnce({ id: 3, gate: { status: "pass", message: "저장 전 평가를 통과했습니다(100%, 기준선 100%, 20문항).", external: true } });
    render(<LlmRoutesPage />);
    const form = fill();
    fireEvent.click(within(form).getByRole("button", { name: "등록" }));
    const group = await screen.findByRole("group", { name: "저장 전 평가 확인" });
    expect(within(group).queryByLabelText(/무시 사유/)).toBeNull();
    fireEvent.click(within(group).getByRole("button", { name: "비용을 확인하고 평가 실행" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post).toHaveBeenLastCalledWith("/api/admin/llm/routes", expect.objectContaining({ gateConfirmCost: true }));
    await screen.findByText(/저장 전 평가를 통과했습니다\(100%/);
  });

  it("취소하면 확인 칸이 닫히고 다시 보내지 않으며, 다른 409 는 오류 문구로 보인다", async () => {
    get.mockResolvedValue(list);
    post.mockRejectedValueOnce(new ApiError(409, JSON.stringify({ error: blocked.message, gate: blocked }))).mockRejectedValueOnce(new ApiError(409, JSON.stringify({ error: "다른 규칙과 순서가 겹칩니다" })));
    render(<LlmRoutesPage />);
    const form = fill();
    fireEvent.click(within(form).getByRole("button", { name: "등록" }));
    const group = await screen.findByRole("group", { name: "저장 전 평가 확인" });
    fireEvent.click(within(group).getByRole("button", { name: "취소" }));
    expect(screen.queryByRole("group", { name: "저장 전 평가 확인" })).toBeNull();
    expect(post).toHaveBeenCalledTimes(1);
    fireEvent.click(within(form).getByRole("button", { name: "등록" }));
    await screen.findByText("다른 규칙과 순서가 겹칩니다");
    expect(screen.queryByRole("group", { name: "저장 전 평가 확인" })).toBeNull();
  });
});


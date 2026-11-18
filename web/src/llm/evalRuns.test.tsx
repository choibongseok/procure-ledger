import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { EvalGatePanel } from "./EvalGatePanel";
import { EvalRunPage, EvalRunPanel, type RunRow } from "./EvalRunPages";

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
  apiFetch: vi.fn(),
  apiDownload: vi.fn(),
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

let perms: string[] = [];
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: (p: string) => perms.includes(p) }) }));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

const base: RunRow = {
  id: 7, at: "t", setCode: "faq", setName: "자주 묻는 질문", setVersion: 1, promptRef: "eval.text@d2b01db5", provider: "mock-a", model: "a-1", mode: "manual", status: "done", failReason: "",
  total: 3, doneItems: 3, passed: 2, passRatePct: 66, thresholdPct: 90, criticalFails: 1, gatePassed: false, avgScore: 66, latencyMs: 30, avgLatencyMs: 10, p95LatencyMs: 12,
  inputTokens: 30, outputTokens: 15, costMicroUsd: 120, baseline: false, baselinePct: 100, warning: "기준선(100%)보다 34%p 낮습니다(허용 2%p). 치명 문항 1건이 실패했습니다", items: [
    { seq: 1, question: "가", expected: "가", pass: true, score: 100, critical: false, latencyMs: 10, detail: "" },
    { seq: 2, question: "나", expected: "나", pass: false, score: 0, critical: true, latencyMs: 10, detail: "기대한 글 1개 중 1개가 응답에 없습니다" },
  ],
};

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = ["eval:read", "eval:write", "eval:run"];
});

describe("평가 실행 패널", () => {
  it("비용 확인 없이는 실행할 수 없고 확인하면 공급자·모델과 함께 보낸다", async () => {
    get.mockImplementation(async (url: string) => (url.includes("run-options") ? { providers: [{ code: "ext1", name: "외부", defaultModel: "m1", external: true }] } : { runs: [base] }));
    post.mockResolvedValue({});
    const { container } = render(
      <MemoryRouter>
        <EvalRunPanel code="faq" published />
      </MemoryRouter>,
    );
    const table = await screen.findByRole("table", { name: "실행 이력" });
    expect(within(table).getByText("66% (2/3)")).toBeTruthy();
    expect(within(table).getByText("기준 미달")).toBeTruthy();
    expect(container.textContent).not.toMatch(/\b(manual|done|running|failed)\b/);
    const form = screen.getByRole("form", { name: "세트 실행 양식" });
    const go = within(form).getByRole("button", { name: "세트 실행" }) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    await screen.findByRole("option", { name: /외부 \(ext1\)/ });
    fireEvent.change(within(form).getByLabelText("공급자"), { target: { value: "ext1" } });
    expect(screen.getByText(/외부 공급자는 평가 실행에 비용이 들고/)).toBeTruthy();
    fireEvent.click(within(form).getByLabelText(/비용과 한도 사용을 확인했습니다/));
    fireEvent.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/eval/sets/faq/runs", { provider: "ext1", model: "", confirmCost: true }));
  });

  it("eval:run 권한이 없으면 실행 양식이 없고 이력은 보인다", async () => {
    perms = ["eval:read"];
    get.mockResolvedValue({ runs: [base] });
    render(
      <MemoryRouter>
        <EvalRunPanel code="faq" published />
      </MemoryRouter>,
    );
    await screen.findByRole("table", { name: "실행 이력" });
    expect(screen.queryByRole("form", { name: "세트 실행 양식" })).toBeNull();
  });
});

function renderRun() {
  return render(
    <MemoryRouter initialEntries={["/admin/eval/runs/7"]}>
      <Routes>
        <Route path="/admin/eval/runs/:id" element={<EvalRunPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("평가 실행 결과 화면", () => {
  it("하락·치명 경고와 실패 문항, 기준선 지정, 회귀 비교가 보인다", async () => {
    const other: RunRow = { ...base, id: 3, passRatePct: 100, passed: 3, criticalFails: 0, gatePassed: true, warning: "", items: base.items.map((i) => ({ ...i, pass: true })) };
    get.mockImplementation(async (url: string) => {
      if (url.includes("run-compare")) {
        return { a: other, b: base, sameVersion: true, note: "", regressions: 1, improvements: 0, rows: [{ seq: 1, question: "가", aPass: true, bPass: true, change: "" }, { seq: 2, question: "나", aPass: true, bPass: false, change: "regression" }] };
      }
      if (url.includes("/runs?")) return { runs: [base, other] };
      return base;
    });
    put.mockResolvedValue({ ok: true, warning: "" });
    const { container } = renderRun();
    await screen.findByText(/기준선\(100%\)보다 34%p 낮습니다/);
    const failed = screen.getByRole("table", { name: "실패 문항" });
    expect(within(failed).getByText("기대한 글 1개 중 1개가 응답에 없습니다")).toBeTruthy();
    expect(container.textContent).not.toMatch(/\b(regression|improvement|gate)\b/);
    fireEvent.click(screen.getByRole("button", { name: "기준선으로 지정" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/eval/sets/faq/baseline", { runId: 7 }));
    await screen.findByRole("option", { name: /#3 v1/ });
    fireEvent.change(screen.getByLabelText("기준 실행"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "비교" }));
    const table = await screen.findByRole("table", { name: "문항별 비교" });
    expect(within(table).getByText("회귀")).toBeTruthy();
    expect(get).toHaveBeenCalledWith("/api/admin/eval/run-compare?a=3&b=7");
  });

  it("버전이 다르면 합계만 비교한다는 안내만 보이고, 읽기 권한만 있으면 기준선 버튼이 없다", async () => {
    perms = ["eval:read"];
    const other: RunRow = { ...base, id: 3, setVersion: 2 };
    get.mockImplementation(async (url: string) => {
      if (url.includes("run-compare")) return { a: other, b: base, sameVersion: false, note: "문항이 달라 합계만 비교합니다.", regressions: 0, improvements: 0, rows: [] };
      if (url.includes("/runs?")) return { runs: [base, other] };
      return base;
    });
    renderRun();
    await screen.findByText(/기준선\(100%\)보다 34%p 낮습니다/);
    expect(screen.queryByRole("button", { name: "기준선으로 지정" })).toBeNull();
    await screen.findByRole("option", { name: /#3 v2/ });
    fireEvent.change(screen.getByLabelText("기준 실행"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "비교" }));
    await screen.findByText("문항이 달라 합계만 비교합니다.");
    expect(screen.queryByRole("table", { name: "문항별 비교" })).toBeNull();
  });

  it("실패한 실행은 부분 결과를 인정하지 않는다고 안내한다", async () => {
    get.mockImplementation(async (url: string) => (url.includes("/runs?") ? { runs: [] } : { ...base, status: "failed", failReason: "한도를 넘었습니다", warning: "", items: [] }));
    renderRun();
    await screen.findByText(/실행이 끝나지 못했습니다: 한도를 넘었습니다/);
    expect(screen.queryByRole("table", { name: "실패 문항" })).toBeNull();
  });
});

describe("저장 전 평가 칸", () => {
  const gates = { gates: [{ id: 1, purposePattern: "docverify.*", setCode: "faq", setName: "자주 묻는 질문", enabled: true, gateDropPct: 2 }, { id: 2, purposePattern: "policy.*", setCode: "other", setName: "다른 세트", enabled: true, gateDropPct: 5 }] };

  it("이 세트의 지정만 보이고, 저장하면 허용 하락과 지정을 보낸다. 기준선이 없으면 경고한다", async () => {
    get.mockResolvedValue(gates);
    put.mockResolvedValue({});
    const changed = vi.fn(async () => {});
    render(<EvalGatePanel code="faq" name="자주 묻는 질문" description="설명" gateDropPct={2} hasBaseline={false} onChanged={changed} />);
    const table = await screen.findByRole("table", { name: "저장 전 평가 지정" });
    expect(within(table).getByText("docverify.*")).toBeTruthy();
    expect(within(table).queryByText("policy.*")).toBeNull();
    expect(screen.getByText(/기준선이 없어 지금은 평가를 건너뜁니다/)).toBeTruthy();
    const form = screen.getByRole("form", { name: "저장 전 평가 양식" });
    fireEvent.change(within(form).getByLabelText("용도 패턴"), { target: { value: "*" } });
    fireEvent.change(within(form).getByLabelText("허용 하락(%p)"), { target: { value: "5" } });
    fireEvent.click(within(form).getByRole("button", { name: "지정 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/eval/gates", { purposePattern: "*", setCode: "faq", enabled: true }));
    expect(put).toHaveBeenCalledWith("/api/admin/eval/sets/faq", { name: "자주 묻는 질문", description: "설명", gateDropPct: 5 });
    await waitFor(() => expect(changed).toHaveBeenCalled());
  });

  it("읽기 권한만 있으면 양식이 없다", async () => {
    perms = ["eval:read"];
    get.mockResolvedValue(gates);
    render(<EvalGatePanel code="faq" name="n" description="" gateDropPct={2} hasBaseline onChanged={async () => {}} />);
    await screen.findByRole("table", { name: "저장 전 평가 지정" });
    expect(screen.queryByRole("form", { name: "저장 전 평가 양식" })).toBeNull();
    expect(screen.queryByText(/기준선이 없어/)).toBeNull();
  });
});

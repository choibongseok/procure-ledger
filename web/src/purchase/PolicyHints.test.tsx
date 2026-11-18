import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { PurchaseForm } from "./PurchaseForm";
import { toClauses } from "./PolicyHints";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

const hit = {
  doc_slug: "purchase-approval", doc_title: "구매 결재 규정", clause_no: "제3조", heading: "금액 구간별 결재선",
  text: "100만 원 미만은 팀장이 결재한다.", source: "구매 결재 규정 제3조(금액 구간별 결재선)", score: 9,
};

function policyCalls(): string[] {
  return get.mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith("/api/policies"));
}

function setup(policy: (url: string) => Promise<unknown>, user: CurrentUser | null = staff) {
  get.mockImplementation((u?: string) => {
    const url = String(u ?? "");
    if (url.startsWith("/api/items")) return Promise.resolve([{ id: 1, sku: "P-1", name: "복사용지" }]);
    if (url.startsWith("/api/vendors")) return Promise.resolve([]);
    if (url.startsWith("/api/policies")) return policy(url);
    return Promise.resolve(null);
  });
  return render(
    <MemoryRouter initialEntries={["/purchase-requests/new"]}>
      <AuthProvider initialUser={user}>
        <Routes>
          <Route path="/purchase-requests/new" element={<PurchaseForm />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

async function fillLine() {
  await screen.findByRole("option", { name: /복사용지/ });
  fireEvent.change(screen.getByLabelText("1번째 줄 품목"), { target: { value: "1" } });
  fireEvent.change(screen.getByLabelText("1번째 줄 단가"), { target: { value: "500000" } });
}

beforeEach(() => get.mockReset());

describe("구매요청 작성 화면의 관련 규정", () => {
  it("금액·품목이 바뀌면 관련 조항 원문과 출처를 보여 준다", async () => {
    setup(() => Promise.resolve({ hits: [hit] }));
    expect(screen.getByText("금액이나 품목을 입력하면 관련 규정이 여기에 나타납니다.")).toBeTruthy();
    await fillLine();
    expect(await screen.findByText("100만 원 미만은 팀장이 결재한다.")).toBeTruthy();
    expect(screen.getByText("출처: 구매 결재 규정 제3조(금액 구간별 결재선)")).toBeTruthy();
    const last = policyCalls().at(-1) ?? "";
    expect(last).toContain("amount=500000");
    expect(decodeURIComponent(last)).toContain("items=복사용지");
    // 내부 점수·식별자는 화면에 나오지 않는다.
    expect(screen.queryByText(/purchase-approval|score/)).toBeNull();
  });

  it("조건에 맞는 조항이 없으면 그렇게 알리고, 실패해도 작성은 막지 않는다", async () => {
    setup(() => Promise.resolve({ hits: [] }));
    await fillLine();
    expect(await screen.findByText("관련 규정이 없습니다.")).toBeTruthy();
    cleanupFailure();
  });

  it("조회가 실패하면 한국어 안내를 보이고 저장 버튼은 그대로 쓸 수 있다", async () => {
    setup(() => Promise.reject(new Error("boom")));
    await fillLine();
    expect(await screen.findByText(/관련 규정을 불러오지 못했습니다/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "임시저장" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("검색 권한이 없으면 영역을 그리지 않고 규정 API 도 부르지 않는다", async () => {
    setup(() => Promise.resolve({ hits: [hit] }), null);
    await fillLine();
    await new Promise((r) => setTimeout(r, 600));
    expect(screen.queryByLabelText("관련 규정")).toBeNull();
    expect(policyCalls()).toHaveLength(0);
  });
});

function cleanupFailure() {
  expect(screen.queryByRole("alert")).toBeNull();
}

describe("toClauses", () => {
  it("응답 키 표기와 무관하게 같은 모양으로 맞추고 점수는 버린다", () => {
    expect(toClauses({ hits: [hit] })[0]).toEqual({
      docTitle: "구매 결재 규정", clauseNo: "제3조", heading: "금액 구간별 결재선", text: hit.text, source: hit.source, reason: "",
    });
    expect(toClauses({ hits: [{ ...hit, reason: "item" }] })[0].reason).toBe("item");
    expect(toClauses({ hits: [{ DocTitle: "재고 규정", ClauseNo: "제1조", Text: "t", Source: "s" }] })[0].docTitle).toBe("재고 규정");
    expect(toClauses(null)).toEqual([]);
  });
});

describe("관련 규정 묶음", () => {
  it("reason 으로 금액 구간과 품목 관련을 나눠 보인다", async () => {
    setup(() => Promise.resolve({ hits: [{ ...hit, reason: "tier" }, { ...hit, clause_no: "제9조", text: "복사용지 조항", reason: "item" }] }));
    await fillLine();
    expect(await screen.findByRole("heading", { name: "이 금액의 결재선" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "품목 관련 참고" })).toBeTruthy();
  });
  it("품목 조항이 없으면 품목 묶음 제목을 그리지 않는다", async () => {
    setup(() => Promise.resolve({ hits: [{ ...hit, reason: "tier" }] }));
    await fillLine();
    await screen.findByText("100만 원 미만은 팀장이 결재한다.");
    expect(screen.queryByRole("heading", { name: "품목 관련 참고" })).toBeNull();
  });
  it("reason 이 없는 응답은 제목 없이 한 묶음이다", async () => {
    setup(() => Promise.resolve({ hits: [hit] }));
    await fillLine();
    await screen.findByText("100만 원 미만은 팀장이 결재한다.");
    expect(screen.queryByRole("heading", { name: "이 금액의 결재선" })).toBeNull();
  });
});

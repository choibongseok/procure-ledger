import type { Permission } from "../auth/permissions";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { fieldLabel, formatValue, noteText } from "./api";
import { DocVerifyPage } from "./DocVerifyPage";

const verdict = {
  id: 7,
  documentId: 3,
  poId: 5,
  receiptId: 0,
  outcome: "mismatch",
  items: [{ field: "total_amount", expected: "7000", actual: "7700", result: "mismatch", note: "합계가 다릅니다" }],
  extraction: { kind: "transaction_statement", supplier: "한빛상사", businessNo: "12*********", totalAmount: 7700, lines: [] },
  confirmedBy: 0,
  confirmedOutcome: "",
  createdAt: "2026-11-10T00:00:00Z",
};

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

function renderPage(user: CurrentUser, initialPermissions?: Permission[]) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user} initialPermissions={initialPermissions}>
        <DocVerifyPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("문서 검증 화면", () => {
  it("붙여 넣은 문서를 올리면 판정과 근거 항목을 한국어로 보여 준다", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.endsWith("/api/documents/verify") && init?.method === "POST") return json(verdict, 201);
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText(/붙여 넣기/), { target: { value: "거래명세서\n합계: 7700" } });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    await waitFor(() => expect(screen.getByTestId("outcome")).toHaveTextContent("불일치"));
    expect(screen.getAllByText("합계 금액").length).toBeGreaterThan(0);
    expect(screen.getAllByText("합계가 다릅니다").length).toBeGreaterThan(0);
    expect(screen.getByText(/AI 가 낸 제안/)).toBeInTheDocument();
    // 직원에게 최종 확정 양식은 보이지 않는다.
    expect(screen.queryByRole("form", { name: "최종 확정" })).toBeNull();
    const post = fetchMock.mock.calls.find((c) => c[1]?.method === "POST");
    expect(post?.[1]?.body).toBeInstanceOf(FormData);
  });

  it("재무에게는 최종 확정 양식이 보이고 확정 결과가 반영된다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.endsWith("/confirm") && init?.method === "POST") {
        return json({ ...verdict, confirmedBy: 2, confirmedOutcome: "mismatch" });
      }
      if (url.endsWith("/api/documents/verdicts")) return json({ verdicts: [verdict] });
      if (url.endsWith("/api/documents/verdicts/7")) return json(verdict);
      return json({});
    });
    renderPage({ id: 2, name: "박재무", role: "finance", departmentId: 1 });
    fireEvent.click(await screen.findByRole("button", { name: /#7/ }));
    fireEvent.click(await screen.findByRole("button", { name: "확정" }));
    await waitFor(() => expect(screen.getByText(/확정됨/)).toBeInTheDocument());
  });

  it("항목을 누르면 원문의 근거 구간이 강조되고, 불일치 항목은 발주·입고 값과 나란히 보인다", async () => {
    const source = "거래명세서\n공급자: 한빛상사\n합계: 7700원";
    const start = Array.from(source).indexOf("합", 12);
    const cited = {
      ...verdict,
      sourceText: source,
      extraction: { ...verdict.extraction, citations: { total_amount: { start, end: start + 9, quote: "합계: 7700원" } } },
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.endsWith("/api/documents/verify") && init?.method === "POST") return json(cited, 201);
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText(/붙여 넣기/), { target: { value: source } });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    await waitFor(() => expect(screen.getByTestId("outcome")).toHaveTextContent("불일치"));
    const compare = screen.getByTestId("mismatch-compare");
    expect(compare).toHaveTextContent("7,000원");
    expect(compare).toHaveTextContent("7,700원");
    expect(screen.queryByTestId("source-highlight")).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "합계 금액" })[0]);
    expect(screen.getByTestId("source-highlight")).toHaveTextContent("합계: 7700원");
  });

  it("원문이 없는 증빙(이미지 등)은 인용 대신 직접 확인 안내를 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input).endsWith("/api/documents/verify") && init?.method === "POST") return json(verdict, 201);
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText(/붙여 넣기/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    expect(await screen.findByTestId("no-citation")).toHaveTextContent("원본과 직접 확인");
    expect(screen.queryByRole("button", { name: "합계 금액" })).toBeNull();
  });

  it("발주 번호 없이 요청하면 서버에 보내지 않고 안내한다", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ verdicts: [] }));
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("발주 번호");
    expect(fetchMock.mock.calls.some((c) => c[1]?.method === "POST")).toBe(false);
  });

  it("document:read 만 있는 역할은 capabilities 를 호출하지 않는다", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      if (String(input).endsWith("/api/documents/capabilities")) return json({ error: "forbidden" }, 403);
      return json({ verdicts: [] });
    });
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 }, ["document:read"]);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/api/documents/capabilities"))).toBe(false);
  });

  it("재무에게만 이미지 선택이 보이고, 직원·설정 꺼짐이면 안내 문구로 바뀐다", async () => {
    const cap = (imageUpload: string) =>
      vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
        if (String(input).endsWith("/api/documents/capabilities")) return json({ imageUpload });
        return json({ verdicts: [] });
      });
    const input = () => document.querySelector('input[type="file"]') as HTMLInputElement;

    cap("allowed");
    const ok = renderPage({ id: 2, name: "박재무", role: "finance", departmentId: 1 });
    await waitFor(() => expect(input().accept).toContain("image/*"));
    expect(screen.queryByTestId("image-notice")).toBeNull();
    ok.unmount();
    vi.restoreAllMocks();

    cap("forbidden_role");
    const staff = renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    expect(await screen.findByTestId("image-notice")).toHaveTextContent("재무 담당자만");
    expect(input().accept).not.toContain("image");
    staff.unmount();
    vi.restoreAllMocks();

    cap("disabled");
    renderPage({ id: 2, name: "박재무", role: "finance", departmentId: 1 });
    expect(await screen.findByTestId("image-notice")).toHaveTextContent("꺼져 있어");
    expect(input().accept).not.toContain("image");
    expect(screen.getByTestId("image-notice").textContent).not.toMatch(/llm|gateway|app_settings/i);
  });

  it("이미지를 막은 상태에서 이미지 파일을 고르면 서버에 보내지 않고 안내한다", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      if (String(input).endsWith("/api/documents/capabilities")) return json({ imageUpload: "disabled" });
      return json({ verdicts: [] });
    });
    renderPage({ id: 2, name: "박재무", role: "finance", departmentId: 1 });
    await screen.findByTestId("image-notice");
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    const file = new File(["x"], "a.png", { type: "image/png" });
    fireEvent.change(document.querySelector('input[type="file"]') as HTMLInputElement, { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("꺼져 있어");
    expect(fetchMock.mock.calls.some((c) => c[1]?.method === "POST")).toBe(false);
  });

  it("파일 선택은 한국어 버튼이고 고른 파일 이름을 보여 준다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      if (String(input).endsWith("/api/documents/capabilities")) return json({ imageUpload: "disabled" });
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    const pick = await screen.findByRole("button", { name: "파일 선택" });
    expect(screen.getByTestId("file-name")).toHaveTextContent("선택한 파일이 없습니다");
    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    const clicked = vi.spyOn(input, "click");
    fireEvent.click(pick);
    expect(clicked).toHaveBeenCalled();
    fireEvent.change(input, { target: { files: [new File(["x"], "발주서.txt", { type: "text/plain" })] } });
    expect(screen.getByTestId("file-name")).toHaveTextContent("선택한 파일: 발주서.txt");
  });

  it("근거 항목의 내부 필드명을 화면용 이름으로 바꾼다", () => {
    expect(fieldLabel("line[1].unit_price")).toBe("2번째 줄 단가");
    expect(fieldLabel("business_no")).toBe("사업자번호");
    expect(fieldLabel("grounding")).toBe("원문 교차 확인");
    expect(fieldLabel("line[너트 M8]")).toBe("품목 너트 M8");
  });

  it("줄 항목은 품목 이름으로, 금액·수량은 천 단위 구분으로, 일치 항목은 일치 문구로 보인다", () => {
    const lines = [{ name: "볼트 M8" }, { name: "너트" }];
    expect(fieldLabel("line[0].quantity", lines)).toBe("볼트 M8 수량");
    expect(fieldLabel("line[1].unit_price", lines)).toBe("너트 단가");
    expect(fieldLabel("line[5].quantity", lines)).toBe("6번째 줄 수량");
    expect(formatValue("total_amount", "10000000")).toBe("10,000,000원");
    expect(formatValue("line[0].unit_price", "500")).toBe("500원");
    expect(formatValue("line[0].quantity", "12000")).toBe("12,000");
    expect(formatValue("business_no", "1234567890")).toBe("1234567890");
    expect(formatValue("total_amount", "해당 없음")).toBe("해당 없음");
    expect(noteText({ result: "match", note: "" })).toBe("발주·입고와 일치합니다.");
    expect(noteText({ result: "match", note: "직접 설명" })).toBe("직접 설명");
    expect(noteText({ result: "mismatch", note: "" })).toBe("");
  });

  it("발주 번호를 넣으면 그 발주의 입고 목록에서 고르고 선택한 입고 번호가 판정 요청에 실린다(B31)", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url.endsWith("/api/documents/verify") && init?.method === "POST") return json(verdict, 201);
      if (url.includes("/api/receipt-history?poId=5")) {
        return json({
          rows: [
            { receiptNo: "RC-000012", poNo: "PO-000005", itemName: "볼트 M8", quantity: 40, receivedAt: "2026-11-10T01:00:00Z" },
            { receiptNo: "RC-000012", poNo: "PO-000005", itemName: "너트 M8", quantity: 100, receivedAt: "2026-11-10T01:00:00Z" },
            { receiptNo: "RC-000015", poNo: "PO-000005", itemName: "볼트 M8", quantity: 60, receivedAt: "2026-11-12T01:00:00Z" },
          ],
          more: false,
        });
      }
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    // 자동 전표 안내가 항상 보인다.
    expect(screen.getByTestId("auto-journal-hint")).toHaveTextContent("입고를 고르지 않으면 전표가 만들어지지 않고 전표 대기에 올라갑니다");
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    const select = await screen.findByTestId("receipt-select");
    // 입고 줄이 입고 단위로 묶여 두 건이고 품목 요약이 보인다.
    const options = Array.from((select as HTMLSelectElement).options).map((o) => o.textContent);
    expect(options).toEqual(["입고 없이 판정", "RC-000012 · 2026-11-10 · 볼트 M8 40, 너트 M8 100", "RC-000015 · 2026-11-12 · 볼트 M8 60"]);
    fireEvent.change(select, { target: { value: "15" } });
    fireEvent.change(screen.getByLabelText(/붙여 넣기/), { target: { value: "거래명세서\n합계: 7700" } });
    fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
    await waitFor(() => expect(screen.getByTestId("outcome")).toBeInTheDocument());
    const post = fetchMock.mock.calls.find((c) => c[1]?.method === "POST");
    const form = post?.[1]?.body as FormData;
    expect(form.get("po_id")).toBe("5");
    expect(form.get("receipt_id")).toBe("15");
  });

  it("입고 목록을 읽지 못하거나 입고가 없으면 입고 번호를 직접 넣는 칸으로 남는다(B31)", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      if (String(input).includes("/api/receipt-history")) return json({ message: "권한이 없습니다" }, 403);
      return json({ verdicts: [] });
    });
    renderPage({ id: 1, name: "김직원", role: "staff", departmentId: 1 });
    fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "5" } });
    await new Promise((r) => setTimeout(r, 450));
    expect(screen.queryByTestId("receipt-select")).toBeNull();
    expect(screen.getByLabelText(/입고 번호\(선택\)/)).toBeInTheDocument();
  });
});

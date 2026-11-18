import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider } from "../auth/AuthProvider";
import { itemColumns, NO_RECEIPT_NOTICE } from "./api";
import { DocVerifyPage } from "./DocVerifyPage";

const base = {
  id: 20,
  documentId: 3,
  poId: 40,
  receiptId: 124,
  outcome: "match",
  extraction: { kind: "transaction_statement", supplier: "스마트유통", businessNo: "214-87-55021", totalAmount: 350000, lines: [{ name: "손소독제", quantity: 100, unitPrice: 3500, amount: 350000 }] },
  confirmedBy: 0,
  confirmedOutcome: "",
  createdAt: "2026-11-10T00:00:00Z",
};

const matchItems = [
  { field: "business_no", expected: "214-87-55021", actual: "214-87-55021", po: "-", receipt: "-", document: "214-87-55021", result: "match", note: "" },
  { field: "line[0].quantity", expected: "100", actual: "100", po: "100", receipt: "100", document: "100", result: "match", note: "" },
  { field: "line[0].unit_price", expected: "3500", actual: "3500", po: "3500", receipt: "-", document: "3500", result: "match", note: "" },
  { field: "total_amount", expected: "350000", actual: "350000", po: "350000", receipt: "350000", document: "350000", result: "match", note: "" },
];

function json(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

async function verifyWith(v: unknown) {
  vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    if (String(input).endsWith("/api/documents/verify") && init?.method === "POST") return json(v, 201);
    return json({ verdicts: [] });
  });
  render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 2, name: "박재무", role: "finance", departmentId: 1 }}>
        <DocVerifyPage />
      </AuthProvider>
    </MemoryRouter>,
  );
  fireEvent.change(screen.getByLabelText(/발주 번호/), { target: { value: "40" } });
  fireEvent.change(screen.getByLabelText(/붙여 넣기/), { target: { value: "x" } });
  fireEvent.click(screen.getByRole("button", { name: "판정 요청" }));
  await waitFor(() => expect(screen.getByTestId("outcome")).toBeInTheDocument());
}

function headers() {
  return within(screen.getByTestId("compare-table")).getAllByRole("columnheader").map((h) => h.textContent);
}

afterEach(() => vi.restoreAllMocks());

describe("문서 검증 3열 대조", () => {
  it("일치: 항목·발주·입고·문서·결과·설명 열과 '-' 칸을 보인다", async () => {
    await verifyWith({ ...base, items: matchItems });
    expect(headers()).toEqual(["항목", "발주", "입고", "문서", "결과", "설명"]);
    const rows = within(screen.getByTestId("compare-table")).getAllByRole("row");
    const cells = (i: number) => within(rows[i]).getAllByRole("cell").map((c) => c.textContent);
    expect(cells(1).slice(1, 4)).toEqual(["-", "-", "214-87-55021"]);
    expect(cells(2).slice(1, 4)).toEqual(["100", "100", "100"]);
    expect(cells(3).slice(1, 4)).toEqual(["3,500원", "-", "3,500원"]);
    expect(cells(4).slice(1, 4)).toEqual(["350,000원", "350,000원", "350,000원"]);
    expect(screen.queryByTestId("no-receipt")).toBeNull();
    expect(screen.queryByTestId("mismatch-compare")).toBeNull();
  });

  it("불일치: 불일치 항목 절도 발주·입고·문서 세 열이다", async () => {
    const items = [
      { field: "line[0].unit_price", expected: "22000", actual: "23000", po: "22000", receipt: "-", document: "23000", result: "mismatch", note: "단가가 다릅니다" },
      { field: "total_amount", expected: "1510000", actual: "1540000", po: "1510000", receipt: "1510000", document: "1540000", result: "mismatch", note: "합계가 다릅니다" },
    ];
    await verifyWith({ ...base, outcome: "mismatch", items });
    const t = screen.getByTestId("mismatch-compare");
    expect(within(t).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["항목", "발주", "입고", "문서"]);
    expect(within(t).getAllByRole("row")[1]).toHaveTextContent("22,000원-23,000원");
    expect(within(t).getAllByRole("row")[2]).toHaveTextContent("1,510,000원1,510,000원1,540,000원");
  });

  it("보류·입고 번호 없음: 입고 열은 전부 '-' 이고 안내 문구가 나온다", async () => {
    const items = [{ field: "extraction", expected: "x", actual: "없음", po: "-", receipt: "-", document: "없음", result: "hold", note: "글자를 읽지 못했습니다" }];
    await verifyWith({ ...base, receiptId: 0, outcome: "hold", items });
    expect(screen.getByTestId("no-receipt")).toHaveTextContent(NO_RECEIPT_NOTICE);
    const row = within(screen.getByTestId("compare-table")).getAllByRole("row")[1];
    expect(within(row).getAllByRole("cell")[2]).toHaveTextContent("-");
  });

  it("이전 형식 판정(po·receipt 없음)은 기준 값을 발주 열에 두고 입고 열은 '-' 로 둔다", () => {
    expect(itemColumns({ field: "total_amount", expected: "7000", actual: "7700", result: "mismatch", note: "" })).toEqual({ po: "7000", receipt: "-", document: "7700" });
  });
});

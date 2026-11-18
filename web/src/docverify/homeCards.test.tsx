import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PendingConfirmCard } from "./homeCards";

afterEach(() => vi.restoreAllMocks());

describe("판정 확정 대기 카드", () => {
  it("모르는 판정 값은 영문 원문 대신 한국어 문구로 보인다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ total: 1, items: [{ id: 1, poId: 5, outcome: "weird_value", supplier: "한빛상사", totalAmount: 7700, createdAt: "" }] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    render(
      <MemoryRouter>
        <PendingConfirmCard />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByText(/한빛상사/)).toBeInTheDocument());
    expect(screen.getByText(/AI 판정 확인 필요/)).toBeInTheDocument();
    expect(screen.queryByText(/weird_value/)).toBeNull();
  });
});

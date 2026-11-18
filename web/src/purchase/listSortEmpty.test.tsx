import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { PurchaseList } from "./PurchaseList";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const pr = { id: 1, no: "PR-000001", title: "모니터", requesterName: "김", requesterUnitPath: "구매팀", totalAmount: 1000, status: "in_review", createdAt: "2026-11-10T00:00:00Z", noApprover: false };
let rows: unknown[] = [pr];

beforeEach(() => {
  get.mockReset();
  rows = [pr];
  get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/purchase-requests")) return rows;
    throw new Error(path);
  });
});

function renderList() {
  return render(
    <AuthProvider initialUser={{ id: 1, name: "직원", role: "staff", departmentId: 1 }}>
      <MemoryRouter>
        <PurchaseList />
      </MemoryRouter>
    </AuthProvider>,
  );
}

describe("구매요청 목록 정렬·조건·빈 상태", () => {
  it("기본은 작성일 내림차순이고 금액 머리를 누르면 금액 내림차순, 다시 누르면 오름차순으로 다시 부른다", async () => {
    renderList();
    await screen.findByText("PR-000001");
    expect(get).toHaveBeenLastCalledWith(expect.stringContaining("sort=created_at&order=desc"));
    expect(screen.getByRole("columnheader", { name: /작성일/ })).toHaveAttribute("aria-sort", "descending");
    fireEvent.click(screen.getByRole("button", { name: /금액/ }));
    await waitFor(() => expect(get).toHaveBeenLastCalledWith(expect.stringContaining("sort=total_amount&order=desc")));
    fireEvent.click(screen.getByRole("button", { name: /금액/ }));
    await waitFor(() => expect(get).toHaveBeenLastCalledWith(expect.stringContaining("sort=total_amount&order=asc")));
    expect(get).toHaveBeenLastCalledWith(expect.not.stringContaining("offset="));
  });

  it("조건 없는 0건은 작성 안내와 주 버튼을, 작성 단추는 작업 줄에서 숨긴다", async () => {
    rows = [];
    renderList();
    await screen.findByText("아직 구매요청이 없습니다.");
    expect(screen.getAllByRole("link", { name: "새 구매요청 작성" })).toHaveLength(1);
    expect(screen.queryByText("조건에 맞는 구매요청이 없습니다.")).toBeNull();
  });

  it("조건 있는 0건은 칩과 조건 지우기를 보이고, 지우면 조건이 풀린다", async () => {
    rows = [];
    renderList();
    await screen.findByText("아직 구매요청이 없습니다.");
    fireEvent.change(screen.getByLabelText("제목 검색"), { target: { value: "없는것" } });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    await screen.findByText("조건에 맞는 구매요청이 없습니다.");
    expect(screen.getByText("조건을 바꾸거나 지워 보세요.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "조건 해제: 제목: 없는것" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "조건 지우기" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "조건 지우기" }));
    await screen.findByText("아직 구매요청이 없습니다.");
    expect(screen.queryByRole("button", { name: /조건 해제/ })).toBeNull();
  });
});

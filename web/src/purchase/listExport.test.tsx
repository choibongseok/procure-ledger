import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, apiDownload } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { PurchaseList } from "./PurchaseList";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: (() => { const get = vi.fn(); return { get, getPage: async (p: string) => { const items = ((await get(p)) ?? []) as unknown[]; return { items, total: items.length }; }, post: vi.fn(), put: vi.fn(), del: vi.fn() }; })() };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const download = apiDownload as unknown as ReturnType<typeof vi.fn>;

const pr = { id: 1, no: "PR-000001", title: "모니터", requesterName: "김", requesterUnitPath: "구매팀", totalAmount: 1000, status: "in_review", createdAt: "2026-11-10T00:00:00Z", noApprover: false };

beforeEach(() => {
  get.mockReset();
  download.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path.startsWith("/api/purchase-requests")) return [pr];
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

describe("구매요청 목록 내보내기", () => {
  it("조회를 누르기 전의 입력은 보내지 않고, 누른 뒤의 조건만 보낸다", async () => {
    renderList();
    await screen.findByText("PR-000001");
    fireEvent.change(screen.getByLabelText("제목 검색"), { target: { value: "모니터" } });
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(1));
    expect(String(download.mock.calls[0][0])).not.toContain("q=");
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    await waitFor(() => expect(get).toHaveBeenLastCalledWith(expect.stringContaining("q=")));
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(2));
    expect(String(download.mock.calls[1][0])).toContain("/api/purchase-requests/export?");
    expect(String(download.mock.calls[1][0])).toContain("q=");
  });
});

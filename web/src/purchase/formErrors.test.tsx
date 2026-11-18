import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { PurchaseForm, validateFields } from "./PurchaseForm";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

beforeEach(() => {
  get.mockReset();
  get.mockImplementation((u?: string) => {
    const url = String(u ?? "");
    if (url.startsWith("/api/items")) return Promise.resolve([{ id: 1, sku: "P-1", name: "복사용지" }]);
    if (url.startsWith("/api/vendors")) return Promise.resolve([{ id: 1, name: "대한문구" }]);
    return Promise.resolve(null);
  });
});

function mount() {
  return render(
    <MemoryRouter initialEntries={["/purchase-requests/new"]}>
      <AuthProvider initialUser={staff}>
        <Routes>
          <Route path="/purchase-requests/new" element={<PurchaseForm />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("구매요청 입력 오류 표시", () => {
  it("모든 오류를 화면 순서대로 칸 id 와 함께 돌려준다", () => {
    const line = { itemId: "", quantity: "0", unitPrice: "-1" };
    const got = validateFields("", "", [line], true);
    expect(got.map((p) => p.id)).toEqual(["pr-title", "pr-vendor", "pr-line-0-item", "pr-line-0-qty", "pr-line-0-price"]);
    expect(validateFields("a", "", [], false).map((p) => p.id)).toEqual(["pr-add-line"]);
  });

  it("막히면 칸 아래에 오류가 붙고 첫 오류 칸으로 포커스가 가며, 같은 오류로 다시 누르면 건수를 알린다", async () => {
    mount();
    await screen.findByRole("option", { name: /복사용지/ });
    expect(screen.queryByText("제목을 입력하세요.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "저장 후 상신" }));
    const title = screen.getByLabelText("제목");
    expect(title.getAttribute("aria-invalid")).toBe("true");
    expect(title.getAttribute("aria-describedby")).toBe("pr-title-err");
    expect(document.getElementById("pr-title-err")?.textContent).toContain("제목을 입력하세요.");
    expect(document.activeElement).toBe(title);
    expect(screen.getByLabelText("거래처").getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByLabelText("1번째 줄 품목").getAttribute("aria-invalid")).toBe("true");
    expect(within(screen.getByRole("status")).queryByText(/오류/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "저장 후 상신" }));
    expect(screen.getByRole("status").textContent).toBe("오류 3건이 남아 있습니다.");

    // 고친 칸은 오류가 사라지고 건수는 다시 처음부터 센다.
    fireEvent.change(title, { target: { value: "복사용지 구매" } });
    expect(title.getAttribute("aria-invalid")).toBeNull();
    expect(document.getElementById("pr-title-err")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "저장 후 상신" }));
    expect(document.activeElement).toBe(screen.getByLabelText("거래처"));
    expect(screen.getByRole("status").textContent).toBe("");
  });
});

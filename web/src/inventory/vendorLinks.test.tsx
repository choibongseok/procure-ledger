import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { VendorLinks } from "./VendorLinks";

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
const put = vi.mocked(api.put);
const two = [
  { id: 1, code: "MAIN", name: "에이엑스 주식회사" },
  { id: 2, code: "ABC", name: "계열사" },
];

beforeEach(() => {
  get.mockReset();
  put.mockReset();
  auth = { entities: two, currentEntity: { all: false, id: 1, code: "MAIN" }, can: (p: string) => p === "inventory:manage" };
  get.mockResolvedValue({
    rows: [
      { vendorId: 7, name: "계열 거래처", linkedEntityId: 0, linkedEntityName: "", suggestedEntityId: 2, suggestedEntityName: "계열사" },
      { vendorId: 8, name: "외부 거래처", linkedEntityId: 0, linkedEntityName: "", suggestedEntityId: 0, suggestedEntityName: "" },
    ],
  });
});

describe("거래처의 연결 법인", () => {
  it("고를 수 있는 법인이 하나뿐이면 아무것도 그리지 않고 서버도 부르지 않는다", () => {
    auth = { entities: [two[0]], currentEntity: { all: false, id: 1, code: "MAIN" }, can: () => true };
    const { container } = render(<VendorLinks />);
    expect(container).toBeEmptyDOMElement();
    expect(get).not.toHaveBeenCalled();
  });

  it("사업자번호가 같은 법인을 제안하고, 제안대로 연결하면 서버에 법인 번호를 보낸다", async () => {
    put.mockResolvedValue({});
    render(<VendorLinks />);
    expect(await screen.findByText(/계열사 과\(와\) 사업자번호가 같습니다/)).toBeInTheDocument();
    // 현재 법인 자신은 고를 수 없다.
    const select = screen.getByLabelText("계열 거래처 연결 법인") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.text)).toEqual(["연결 없음", "계열사"]);
    fireEvent.click(screen.getByRole("button", { name: "계열 거래처 을(를) 계열사 으로 연결" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/vendors/7/link", { entityId: 2 }));
    expect(await screen.findByText(/그룹 법인으로 연결했습니다/)).toBeInTheDocument();
  });

  it("권한이 없으면 고르는 칸이 잠기고 제안 단추가 없다", async () => {
    auth = { ...auth, can: () => false };
    render(<VendorLinks />);
    expect(await screen.findByLabelText("계열 거래처 연결 법인")).toBeDisabled();
    expect(screen.queryByRole("button", { name: /으로 연결/ })).toBeNull();
  });
});

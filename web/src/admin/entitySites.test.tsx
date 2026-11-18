import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { EntitiesPage } from "./EntitiesPage";

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
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: (p: string) => p === "entity:manage" }) }));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

const base = { representative: "", address: "", phone: "", fiscalStartMonth: 1, members: 3, sites: 1, updatedAt: "2026-11-13T00:00:00Z", version: 1, rootUnitId: 0, rootUnitName: "" };
const listing = {
  entities: [
    { ...base, id: 1, code: "MAIN", name: "에이엑스 주식회사", businessNo: "123-45-67890", isDefault: true, active: true },
    { ...base, id: 2, code: "ABC", name: "계열사", businessNo: "220-81-62517", isDefault: false, active: true },
  ],
  rootCandidates: [],
};
const site = (over: Record<string, unknown>) => ({ id: 1, entityId: 1, siteNo: "0000", name: "본사", kind: "head", kindName: "주사업장", address: "서울", vatMode: "own", vatName: "사업장별 신고", active: true, updatedAt: "", ...over });

let sites: ReturnType<typeof site>[] = [];
beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  sites = [site({}), site({ id: 2, siteNo: "0001", name: "부산 지점", kind: "branch", kindName: "지점", vatMode: "consolidated", vatName: "주사업장 총괄납부" })];
  get.mockImplementation(async (path: string) => (path.includes("/sites") ? sites : listing));
});

async function openTab() {
  render(
    <MemoryRouter>
      <EntitiesPage />
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole("tab", { name: "사업장" }));
  return await screen.findByRole("table", { name: "사업장 목록" });
}

describe("법인 관리 — 사업장 탭", () => {
  it("한국어 종류·신고 방식 이름을 보이고 내부 코드(head·consolidated)를 보이지 않는다", async () => {
    const t = within(await openTab());
    expect(await t.findByText("부산 지점")).toBeInTheDocument();
    expect(t.getByText("주사업장 총괄납부")).toBeInTheDocument();
    expect(screen.queryByText(/consolidated|head\b|factory/)).toBeNull();
    expect(get).toHaveBeenCalledWith("/api/admin/entities/1/sites");
  });

  it("법인을 바꾸면 그 법인의 사업장을 읽는다", async () => {
    await openTab();
    fireEvent.change(screen.getByLabelText("법인"), { target: { value: "2" } });
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/admin/entities/2/sites"));
  });

  it("지점을 등록한다. 주사업장이 이미 있으면 종류에서 주사업장을 고를 수 없고 0000 을 쓸 수 없다고 안내한다", async () => {
    post.mockResolvedValue({});
    await openTab();
    const kind = screen.getByLabelText("종류") as HTMLSelectElement;
    expect(within(kind).queryByRole("option", { name: "주사업장" })).toBeNull();
    fireEvent.change(screen.getByLabelText("종사업장번호"), { target: { value: "0002" } });
    fireEvent.change(screen.getByLabelText("사업장 이름"), { target: { value: "대전 공장" } });
    fireEvent.change(kind, { target: { value: "factory" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/api/admin/entities/1/sites", { siteNo: "0002", name: "대전 공장", kind: "factory", address: "", vatMode: "own", active: true }),
    );
    expect(await screen.findByText("사업장을 등록했습니다.")).toBeInTheDocument();
  });

  it("주사업장은 종류를 바꿀 수 없고 번호가 0000 으로 고정되며 사용 안 함으로 바꿀 수 없다", async () => {
    put.mockResolvedValue({});
    const t = within(await openTab());
    fireEvent.click(await t.findByRole("button", { name: "본사 수정" }));
    expect(screen.getByLabelText("종류")).toBeDisabled();
    expect(screen.getByLabelText("종사업장번호")).toBeDisabled();
    expect(screen.getByLabelText("종사업장번호")).toHaveValue("0000");
    expect(screen.getByLabelText("상태")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("부가세 신고 방식"), { target: { value: "consolidated" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/entities/1/sites/1", expect.objectContaining({ siteNo: "0000", kind: "head", vatMode: "consolidated", active: true })));
  });

  it("서버가 거부하면 업무 문구로 알린다", async () => {
    post.mockRejectedValue(new (await import("../api/client")).ApiError(400, "입력값이 올바르지 않습니다: 같은 종사업장번호가 이미 있거나 주사업장이 이미 있습니다."));
    await openTab();
    fireEvent.change(screen.getByLabelText("종사업장번호"), { target: { value: "0001" } });
    fireEvent.change(screen.getByLabelText("사업장 이름"), { target: { value: "중복" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    expect(await screen.findByText(/같은 종사업장번호가 이미 있거나/)).toBeInTheDocument();
    expect(screen.queryByText(/입력값이 올바르지 않습니다:/)).toBeNull();
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
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

let perms: string[] = [];
vi.mock("../auth/AuthProvider", () => ({
  useAuth: () => ({ can: (p: string) => perms.includes(p) }),
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

const base = { representative: "", address: "", phone: "", fiscalStartMonth: 1, members: 3, sites: 1, updatedAt: "2026-11-13T00:00:00Z", version: 1 };
const listing = {
  entities: [
    { ...base, id: 1, code: "MAIN", name: "에이엑스 주식회사", businessNo: "123-45-67890", rootUnitId: 1, rootUnitName: "PL 주식회사", isDefault: true, active: true },
    { ...base, id: 2, code: "ABC", name: "계열사", businessNo: "220-81-62517", rootUnitId: 0, rootUnitName: "", isDefault: false, active: true },
  ],
  rootCandidates: [{ unitId: 9, name: "계열사 본사", code: "AB" }],
};

const renderPage = () => render(<MemoryRouter><EntitiesPage /></MemoryRouter>);

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = ["entity:manage"];
  get.mockResolvedValue(listing);
});

describe("법인 관리 화면", () => {
  it("기본 법인은 수정·비활성 단추가 없고 내부 코드(isDefault 등)가 화면에 없다", async () => {
    renderPage();
    const table = await screen.findByRole("table", { name: "법인 목록" });
    // 표는 불러오는 동안에도 그려지므로(자리 표시 행) 행이 나타날 때까지 기다린다.
    expect(await within(table).findByText("기본 법인")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "에이엑스 주식회사 수정" })).toBeNull();
    expect(screen.getByRole("button", { name: "계열사 수정" })).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/isDefault|rootUnitId|entity_id/);
  });

  it("법인을 등록하면 코드·이름·루트 조직을 보낸다", async () => {
    post.mockResolvedValue({});
    renderPage();
    await screen.findByText("에이엑스 주식회사");
    fireEvent.change(screen.getByLabelText("법인 코드"), { target: { value: "xyz" } });
    fireEvent.change(screen.getByLabelText("법인 이름"), { target: { value: "또 다른 계열사" } });
    fireEvent.change(screen.getByLabelText("루트 조직"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    expect(post.mock.calls[0][0]).toBe("/api/admin/entities");
    expect(post.mock.calls[0][1]).toMatchObject({ code: "XYZ", name: "또 다른 계열사", rootUnitId: 9, fiscalStartMonth: 1 });
    await screen.findByText("법인을 등록했습니다.");
  });

  it("수정은 버전을 보내고 코드 칸은 잠긴다", async () => {
    put.mockResolvedValue({});
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "계열사 수정" }));
    expect((screen.getByLabelText("법인 코드") as HTMLInputElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("법인 이름"), { target: { value: "새 계열사" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][0]).toBe("/api/admin/entities/2");
    expect(put.mock.calls[0][1]).toMatchObject({ name: "새 계열사", version: 1 });
  });

  it("루트 조직을 고르면 연결하고, 비활성화는 확인 대화상자를 거친다", async () => {
    put.mockResolvedValue({});
    post.mockResolvedValue({});
    renderPage();
    fireEvent.change(await screen.findByLabelText("계열사 루트 조직"), { target: { value: "9" } });
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/entities/2/root", { rootUnitId: 9 }));
    // 앞선 요청이 끝나 단추가 풀린 뒤에 누른다(처리 중에는 잠겨 있어 클릭이 무시된다).
    await waitFor(() => expect(screen.getByRole("button", { name: "계열사 비활성화" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "계열사 비활성화" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/읽기만 되고 새로 쓸 수 없습니다/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "비활성화" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/entities/2/deactivate"));
  });

  it("서버 오류 문장을 그대로 보이고, 권한이 없으면 안내만 보인다", async () => {
    post.mockRejectedValue(new ApiError(400, "같은 코드의 법인이 이미 있습니다."));
    renderPage();
    await screen.findByText("에이엑스 주식회사");
    fireEvent.change(screen.getByLabelText("법인 코드"), { target: { value: "ABC" } });
    fireEvent.change(screen.getByLabelText("법인 이름"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    await screen.findByText(/같은 코드의 법인이 이미 있습니다/);
  });

  it("권한이 없으면 안내만 보이고 목록을 읽지 않는다", () => {
    perms = [];
    renderPage();
    expect(screen.getByText(/볼 권한이 없습니다/)).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  });

  it("등록 응답의 경고(조직에 배치되지 않은 사용자)를 안내로 보인다", async () => {
    post.mockResolvedValue({ warnings: ["조직에 배치되지 않은 사용자 2명은 법인이 둘 이상이면 소속 법인 범위의 권한을 쓸 수 없습니다."] });
    renderPage();
    await screen.findByRole("table", { name: "법인 목록" });
    fireEvent.change(screen.getByLabelText("법인 코드"), { target: { value: "XYZ" } });
    fireEvent.change(screen.getByLabelText("법인 이름"), { target: { value: "또 다른 계열사" } });
    fireEvent.click(screen.getByRole("button", { name: "등록" }));
    await screen.findByText("법인을 등록했습니다.");
    expect(await screen.findByText(/조직에 배치되지 않은 사용자 2명/)).toBeTruthy();
  });
});


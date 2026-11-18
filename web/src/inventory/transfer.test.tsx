import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { TransferPage } from "./TransferPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const user: CurrentUser = { id: 4, name: "재무", role: "finance", departmentId: 3 };

const wh = (id: number, name: string) => ({ id, code: `WH-${id}`, name, location: "", active: true, itemCount: 0 });
const slip = (o: object) => ({ id: 1, no: "MV-2026-0001", fromName: "본사 창고", toName: "물류센터", moveDate: "2026-10-20", memo: "", actorName: "재무", lines: [{ itemName: "복사용지", sku: "A", lotNo: "", qty: 4 }], ...o });

function show(targets: unknown[], recent: unknown[]) {
  get.mockReset();
  get.mockImplementation((u?: string) => {
    const url = String(u);
    if (url === "/api/warehouses") return Promise.resolve([wh(1, "본사 창고"), wh(2, "물류센터")]);
    if (url.startsWith("/api/stock-transfers?targets=1")) return Promise.resolve(targets);
    if (url.startsWith("/api/stock-transfers")) return Promise.resolve(recent);
    return Promise.resolve([]);
  });
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>
        <TransferPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("창고 이동 화면의 법인 간 이동(B51)", () => {
  beforeEach(() => {
    get.mockReset();
  });

  it("법인이 하나뿐이면 다른 법인 묶음·법인 간 표시가 없다", async () => {
    show([], [slip({})]);
    await screen.findByText("MV-2026-0001");
    expect(screen.queryByText("다른 법인")).toBeNull();
    expect(screen.queryByText("법인 간 이동")).toBeNull();
    expect(screen.queryByTestId("intercompany-notice")).toBeNull();
  });

  it("다른 법인 창고를 고르면 법인 간 이동 안내와 재무 전표 안내가 보인다", async () => {
    show([{ id: 95, code: "ABC-W", name: "계열사 창고", entityId: 2, entityName: "계열사" }], []);
    const to = await screen.findByLabelText("도착 창고 선택");
    await waitFor(() => expect(screen.getByRole("option", { name: "계열사 창고 (계열사)" })).toBeInTheDocument());
    expect(screen.queryByTestId("intercompany-notice")).toBeNull();
    fireEvent.change(to, { target: { value: "95" } });
    const notice = await screen.findByTestId("intercompany-notice");
    expect(notice).toHaveTextContent("법인 간 이동입니다.");
    expect(notice).toHaveTextContent("계열사 법인으로 넘어가며");
    expect(notice).toHaveTextContent("재무 담당이 법인 간 거래 전표를 따로 쓰세요");
  });

  it("목록에 법인 간 표시를 보이고, 범위 밖 상대 법인은 법인 이름만 보인다", async () => {
    show([], [slip({ id: 2, no: "MV-2026-0002", intercompany: true, fromEntity: "본사", toEntity: "계열사", toName: "", costAmount: 400 })]);
    await screen.findByText("MV-2026-0002");
    expect(screen.getByText("법인 간 이동")).toBeInTheDocument();
    expect(screen.getByText("계열사 법인으로 이동")).toBeInTheDocument();
    expect(screen.getByText("본사 창고 (본사 법인)")).toBeInTheDocument();
    expect(screen.getByText("400원")).toBeInTheDocument();
    expect(screen.queryByText("물류센터 (계열사 법인)")).toBeNull();
  });
});

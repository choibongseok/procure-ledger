import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { adminHomeCards } from "./homeCards";
import { RetentionPage } from "./RetentionPage";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;

const row = (id: number, over: Record<string, unknown> = {}) => ({
  id,
  no: `GEN-2020-${String(id).padStart(4, "0")}`,
  title: `기안 ${id}`,
  formCode: "GEN",
  formName: "일반 기안서",
  requesterName: "구매직원",
  completedAt: "2020-12-31",
  retentionUntil: "2025-12-31",
  daysOver: 316,
  held: false,
  ...over,
});

function renderPage(role: "admin" | "auditor") {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "사용자", role, departmentId: 1 }}>
        <RetentionPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe("보존·폐기 화면", () => {
  it("후보 목록: 경과 일수와 안내가 보이고 선택하면 폐기 승인 단추가 켜진다", async () => {
    get.mockResolvedValue({ rows: [row(1), row(2)], total: 2 });
    renderPage("admin");
    expect(await screen.findByText("GEN-2020-0001")).toBeInTheDocument();
    expect(screen.getAllByText("316일 지남")).toHaveLength(2);
    expect(screen.getByText(/법정 보존 양식\(구매 품의서·지출 결의서·재고 조정 전표\)/)).toBeInTheDocument();
    const dispose = screen.getByRole("button", { name: "폐기 승인" });
    expect(dispose).toBeDisabled();
    expect(screen.getByText("폐기할 문서를 선택하세요.")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("GEN-2020-0001 선택"));
    expect(screen.getByRole("button", { name: "선택한 1건 폐기 승인" })).toBeEnabled();
  });

  it("폐기: 확인 대화상자에서 사유가 없으면 막히고, 사유를 넣으면 선택한 번호와 사유로 요청한 뒤 목록을 다시 불러온다", async () => {
    get.mockResolvedValue({ rows: [row(1), row(2)], total: 2 });
    post.mockResolvedValue({ disposed: 2 });
    renderPage("admin");
    await screen.findByText("GEN-2020-0001");
    fireEvent.click(screen.getByLabelText("GEN-2020-0001 선택"));
    fireEvent.click(screen.getByLabelText("GEN-2020-0002 선택"));
    fireEvent.click(screen.getByRole("button", { name: "선택한 2건 폐기 승인" }));
    const dlg = await screen.findByRole("alertdialog", { name: "2건을 폐기할까요?" });
    expect(dlg).toHaveTextContent("되돌릴 수 없습니다");
    expect(dlg).toHaveTextContent("GEN-2020-0001, GEN-2020-0002");
    fireEvent.click(within(dlg).getByRole("button", { name: "폐기" }));
    expect(await within(dlg).findByText("폐기 사유를 입력하세요.")).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
    fireEvent.change(within(dlg).getByLabelText("폐기 사유"), { target: { value: "보존 연한 만료 정기 정리" } });
    get.mockClear();
    fireEvent.click(within(dlg).getByRole("button", { name: "폐기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/retention/dispose", { ids: [1, 2], reason: "보존 연한 만료 정기 정리" }));
    expect(await screen.findByText("2건을 폐기했습니다.")).toBeInTheDocument();
    await waitFor(() => expect(get).toHaveBeenCalled());
  });

  it("서버가 거부하면(후보가 아닌 문서) 문구를 대화상자에 보이고 대화상자는 닫히지 않는다", async () => {
    get.mockResolvedValue({ rows: [row(1)], total: 1 });
    post.mockImplementation(() => Promise.reject(new ApiError(400, "폐기할 수 없는 문서가 포함되어 있습니다(EXP-2026-00001). 목록을 새로 고친 뒤 다시 선택하세요.")));
    renderPage("admin");
    await screen.findByText("GEN-2020-0001");
    fireEvent.click(screen.getByLabelText("GEN-2020-0001 선택"));
    fireEvent.click(screen.getByRole("button", { name: "선택한 1건 폐기 승인" }));
    const dlg = await screen.findByRole("alertdialog");
    fireEvent.change(within(dlg).getByLabelText("폐기 사유"), { target: { value: "정리" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "폐기" }));
    expect(await within(dlg).findByText(/폐기할 수 없는 문서가 포함되어 있습니다\(EXP-2026-00001\)/)).toBeInTheDocument();
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });

  it("폐기 보류를 걸고 보류 탭에서 해제한다", async () => {
    get.mockImplementation((url: string) =>
      Promise.resolve(url.includes("holds") ? { rows: [row(3, { held: true, holdReason: "감사 진행 중" })] } : { rows: [row(1)], total: 1 }),
    );
    post.mockResolvedValue({});
    renderPage("admin");
    await screen.findByText("GEN-2020-0001");
    fireEvent.click(screen.getByRole("button", { name: "폐기 보류" }));
    const dlg = await screen.findByRole("alertdialog", { name: "폐기 보류" });
    fireEvent.click(within(dlg).getByRole("button", { name: "보류" }));
    expect(await within(dlg).findByText("폐기 보류 사유를 입력하세요.")).toBeInTheDocument();
    fireEvent.change(within(dlg).getByLabelText("보류 사유"), { target: { value: "감사 진행 중" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "보류" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/retention/hold", { id: 1, reason: "감사 진행 중" }));
    fireEvent.click(await screen.findByRole("tab", { name: "폐기 보류" }));
    expect(await screen.findByText("감사 진행 중")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "보류 해제" }));
    const clear = await screen.findByRole("alertdialog", { name: "폐기 보류 해제" });
    fireEvent.click(within(clear).getByRole("button", { name: "해제" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/retention/hold/3/clear", { reason: "" }));
  });

  it("감사 담당은 목록만 보고 선택·폐기·보류 단추가 없다", async () => {
    get.mockResolvedValue({ rows: [row(1)], total: 1 });
    renderPage("auditor");
    await screen.findByText("GEN-2020-0001");
    expect(screen.queryByLabelText("GEN-2020-0001 선택")).toBeNull();
    expect(screen.queryByRole("button", { name: /폐기 승인/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "폐기 보류" })).toBeNull();
  });

  it("빈 상태와 불러오기 실패 문구", async () => {
    get.mockResolvedValueOnce({ rows: [], total: 0 });
    const { unmount } = renderPage("admin");
    expect(await screen.findByText("폐기할 문서가 없습니다.")).toBeInTheDocument();
    unmount();
    get.mockImplementationOnce(() => Promise.reject(new Error("x")));
    renderPage("admin");
    expect(await screen.findByText("목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.")).toBeInTheDocument();
  });

  it("관리자 홈 카드: 건수와 링크, 0건이면 없음", async () => {
    const card = adminHomeCards.find((c) => c.id === "retention-expired")!;
    expect(card.permission).toBe("retention:read");
    expect(card.title).toBe("보존 기한이 지난 문서");
    get.mockResolvedValueOnce({ count: 3 });
    const { unmount } = render(
      <MemoryRouter>
        <card.Component />
      </MemoryRouter>,
    );
    expect(await screen.findByText("3건")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "보존·폐기 열기" })).toHaveAttribute("href", "/admin/retention");
    unmount();
    get.mockResolvedValueOnce({ count: 0 });
    render(
      <MemoryRouter>
        <card.Component />
      </MemoryRouter>,
    );
    expect(await screen.findByText("없음")).toBeInTheDocument();
  });
});

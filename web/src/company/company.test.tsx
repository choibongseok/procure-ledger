import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, apiFetch } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { CompanyPage } from "./CompanyPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), apiFetch: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const m = api as unknown as Record<"get" | "put", ReturnType<typeof vi.fn>>;
const upload = apiFetch as unknown as ReturnType<typeof vi.fn>;
const admin: CurrentUser = { id: 7, name: "관리자", role: "admin", departmentId: 1 };
const staff: CurrentUser = { id: 1, name: "직원", role: "staff", departmentId: 1 };
const fiscal = { startMonth: 1, name: 2026, thisFrom: "2026-01-01", thisTo: "2026-12-31", closed: false, version: 1, configured: true };
const empty = { name: "", businessNo: "", representative: "", address: "", phone: "", hasLogo: false, hasSeal: false, configured: false, version: 0 };

function mount(u: CurrentUser) {
  return render(
    <AuthProvider initialUser={u}>
      <MemoryRouter>
        <CompanyPage />
      </MemoryRouter>
    </AuthProvider>,
  );
}

let route: () => unknown = () => empty;

beforeEach(() => {
  m.get.mockReset();
  m.put.mockReset();
  upload.mockReset();
  route = () => empty;
  m.get.mockImplementation(async (p: string) => {
    if (p === "/api/company/fiscal") return fiscal;
    if (p === "/api/company/vat-rates") return [{ id: 1, effectiveFrom: "0001-01-01", percent: 10, createdBy: "", canceled: false, pending: false }];
    if (p.startsWith("/api/company/holidays")) return [];
    return route();
  });
});

describe("회사 설정 화면", () => {
  it("설정이 없으면 안내를 보이고, 관리자는 회사명을 입력해 저장하며 직인 이미지를 올린다", async () => {
    m.put.mockResolvedValue({ ...empty, name: "에이엑스", configured: true, version: 1 });
    upload.mockResolvedValue({});
    mount(admin);
    expect(await screen.findByText(/조직도의 최상위 조직 이름을 발주처로 씁니다/)).toBeInTheDocument();
    const save = screen.getByRole("button", { name: "저장" });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("회사명"), { target: { value: "에이엑스" } });
    fireEvent.change(screen.getByLabelText("사업자등록번호"), { target: { value: "123-45-67890" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(m.put).toHaveBeenCalledWith("/api/company", expect.objectContaining({ name: "에이엑스", businessNo: "123-45-67890", version: 0 })));
    expect(await screen.findByText(/출력에 반영됩니다/)).toBeInTheDocument();
    const file = new File([new Uint8Array([137, 80, 78, 71])], "seal.png", { type: "image/png" });
    fireEvent.change(screen.getByLabelText("직인 올리기"), { target: { files: [file] } });
    await waitFor(() => expect(upload).toHaveBeenCalledWith("/api/company/seal", expect.objectContaining({ method: "PUT" })));
    expect(await screen.findByText(/직인을 교체했습니다/)).toBeInTheDocument();
  });

  it("관리자가 아니면 입력과 올리기 없이 읽기만 되고, 서버 오류 문구는 업무 문구로 보인다", async () => {
    route = () => ({ ...empty, name: "에이엑스", configured: true, version: 2, hasSeal: true });
    mount(staff);
    expect(await screen.findByDisplayValue("에이엑스")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "저장" })).toBeNull();
    expect(screen.queryByLabelText("직인 올리기")).toBeNull();
    expect(screen.getByAltText("현재 직인")).toBeInTheDocument();
    expect(screen.getByText("회사 설정은 관리자만 바꿀 수 있습니다.")).toBeInTheDocument();
  });

  it("회계 구역: 시작월 변경과 세율 등록은 영향 확인 대화상자를 거치고, 시작된 세율에는 취소 버튼이 없다", async () => {
    route = () => ({ ...empty, name: "에이엑스", configured: true, version: 3 });
    m.get.mockImplementation(async (p: string) => {
      if (p === "/api/company/fiscal") return fiscal;
      if (p === "/api/company/vat-rates")
        return [
          { id: 2, effectiveFrom: "2027-01-01", percent: 12, createdBy: "관리자", canceled: false, pending: true },
          { id: 1, effectiveFrom: "0001-01-01", percent: 10, createdBy: "", canceled: false, pending: false },
        ];
      if (p.startsWith("/api/company/holidays")) return [];
      return route();
    });
    m.put.mockResolvedValue({});
    (api as unknown as { post: ReturnType<typeof vi.fn> }).post.mockResolvedValue({});
    mount(admin);
    expect(await screen.findByText(/이번 회계연도: 2026-01-01 ~ 2026-12-31/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "취소" })).toHaveLength(1); // 시작 전 세율만
    fireEvent.change(screen.getByLabelText("회계연도 시작월"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "시작월 변경" }));
    expect(await screen.findByText(/수익·비용의 이월 표시가 새 회계연도 기준으로 달라집니다/)).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "시작월 변경" }).at(-1)!);
    await waitFor(() => expect(m.put).toHaveBeenCalledWith("/api/company/fiscal-start", { month: 4, version: 1 }));
    fireEvent.change(screen.getByLabelText("효력일"), { target: { value: "2027-07-01" } });
    fireEvent.change(screen.getByLabelText("세율(%)"), { target: { value: "15" } });
    fireEvent.click(screen.getByRole("button", { name: "세율 등록" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("2027-07-01 이후 거래부터 15% 를 적용합니다");
  });

  it("달력 구역: 시간대는 읽기 전용이고, 일괄 입력은 미리보기로 오류 줄을 보인 뒤 등록한다", async () => {
    const post = (api as unknown as { post: ReturnType<typeof vi.fn> }).post;
    post.mockImplementation(async (p: string) => (p.endsWith("/preview") ? { items: [{ date: "2026-12-25", name: "성탄절" }], problems: ["2번째 줄: 날짜는 2026-12-25 와 같은 형식이어야 합니다."] } : { added: 1, skipped: 0 }));
    mount(admin);
    expect(await screen.findByText(/시간대: 한국 표준시, 읽기 전용입니다/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/여러 건 한 번에 입력/), { target: { value: "2026-12-25 성탄절\nabc" } });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText(/등록할 공휴일 1건, 오류 1줄/)).toBeInTheDocument();
    expect(screen.getByText(/2번째 줄/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "오류 줄을 빼고 등록" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/company/holidays", { items: [{ date: "2026-12-25", name: "성탄절" }] }));
  });
});

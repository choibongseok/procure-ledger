import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { DocSearchPage } from "./DocSearchPage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const row = (over = {}) => ({ id: 1, no: "GEN-2026-0001", title: "구매 정산 보고", formCode: "GEN", formName: "일반 기안서", formKind: "general", requesterName: "김직원", unitPath: "구매팀",
  status: "in_review", disposed: false, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z", matched: "제목", ...over });

beforeEach(() => {
  get.mockReset();
  get.mockImplementation(async (path: string) => {
    if (path === "/api/forms") return [{ id: 1, code: "GEN", category: "general", name: "일반 기안서", kind: "general", version: 1 }];
    return { rows: [row(), row({ id: 2, no: "GEN-2026-0002", title: "폐기된 문서", disposed: true, matched: "" })], total: 2, pageSize: 50 };
  });
});

function renderPage() {
  return render(
    <MemoryRouter>
      <DocSearchPage />
    </MemoryRouter>,
  );
}

describe("문서 검색 화면", () => {
  it("조건이 없거나 검색어가 1자면 요청하지 않고 문구를 보인다", async () => {
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "검색" }));
    expect(await screen.findByText("검색어나 조건을 하나 이상 입력하세요.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("검색어"), { target: { value: "가" } });
    fireEvent.click(screen.getByRole("button", { name: "검색" }));
    expect(await screen.findByText("검색어는 2자 이상 입력하세요.")).toBeInTheDocument();
    expect(get).not.toHaveBeenCalledWith(expect.stringContaining("/api/docs/search"));
  });

  it("결과 표에 일치한 곳과 폐기됨 배지, 건수가 보인다", async () => {
    renderPage();
    fireEvent.change(screen.getByLabelText("검색어"), { target: { value: "구매" } });
    fireEvent.click(screen.getByLabelText("결재 중"));
    fireEvent.click(screen.getByRole("button", { name: "검색" }));
    expect(await screen.findByText("제목에서 일치")).toBeInTheDocument();
    expect(screen.getByText("폐기됨", { selector: ".badge" })).toBeInTheDocument();
    expect(screen.getByText("검색 결과 2건")).toBeInTheDocument();
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/q=%EA%B5%AC%EB%A7%A4.*status=in_review/)));
  });

  it("결과가 없으면 안내 문구가 나온다", async () => {
    get.mockImplementation(async (path: string) => (path === "/api/forms" ? [] : { rows: [], total: 0, pageSize: 50 }));
    renderPage();
    fireEvent.change(screen.getByLabelText("검색어"), { target: { value: "없는내용" } });
    fireEvent.click(screen.getByRole("button", { name: "검색" }));
    expect(await screen.findByText("조건에 맞는 문서가 없습니다.")).toBeInTheDocument();
  });

  it("여러 쪽이면 공용 쪽 이동(n–m / 전체 N건)이 나오고 다음을 누르면 offset 으로 다시 조회한다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path === "/api/forms") return [];
      return { rows: [row(), row({ id: 2, no: "GEN-2026-0002" })], total: 120, pageSize: 50 };
    });
    renderPage();
    fireEvent.change(screen.getByLabelText("검색어"), { target: { value: "구매" } });
    fireEvent.click(screen.getByRole("button", { name: "검색" }));
    expect(await screen.findByText("1–50 / 전체 120건")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "쪽 이동" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringContaining("offset=50")));
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiFetch } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { roleHas, type Role } from "../auth/permissions";
import { visibleMenu } from "../layout/Layout";
import { ImportPage } from "./ImportPage";
import { orgModule } from "./routes";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return {
    ...real,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    apiFetch: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  };
});

const fetchMock = apiFetch as unknown as ReturnType<typeof vi.fn>;

function renderPage() {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={{ id: 1, name: "관리자", role: "admin", departmentId: 1 }}>
        <ImportPage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

function pick(label: string, name = "units.csv") {
  const file = new File(["조직코드\nA\n"], name, { type: "text/csv" });
  fireEvent.change(screen.getByLabelText(label), { target: { files: [file] } });
}

const base = {
  previewId: "tok",
  base: "2026-11-11",
  files: [
    { name: "units.csv", kind: "units", kindName: "조직", rows: 3, encoding: "UTF-8" },
    { name: "people.csv", kind: "people", kindName: "사람", rows: 8, encoding: "EUC-KR" },
  ],
  counts: { "조직 신설": 1, 입사: 2, "변화 없음": 3, "정보 차이(반영되지 않음)": 2 },
  items: [
    {
      effectiveDate: "2026-12-01",
      label: "조직 신설",
      card: "조직 신설",
      class: "추가",
      target: "데이터팀(DEV-3)",
      content: "상위 개발본부, 종류 팀",
      file: "조직",
      line: 4,
    },
    {
      effectiveDate: "2026-11-30",
      label: "퇴사",
      card: "퇴사",
      class: "퇴사",
      target: "구매부서장(E000016)",
      content: "마지막 근무일 2026. 11. 30.(자진)",
      file: "사람",
      line: 5,
    },
    {
      effectiveDate: "2026-12-01",
      label: "전보",
      card: "전보",
      class: "변경",
      target: "김하윤(E000125)",
      content: "구매1팀 → 구매2팀",
      file: "사람",
      line: 2,
    },
  ],
  itemsTotal: 3,
  itemsMore: 0,
  errors: [],
  moreErrors: 0,
  fileErrors: [],
  warnings: ["데이터팀: 한지우의 권한 역할이 '직원'라서 결재 권한이 없습니다."],
  impact: [
    {
      who: "구매부서장(E000016)",
      why: "퇴사 2026. 11. 30.",
      count: 2,
      docs: [{ id: 47, no: "PR-000047", title: "회의실 프로젝터 구매", requester: "구매팀장" }],
    },
  ],
  impactMore: 0,
  retroRows: [],
  noChange: false,
};

beforeEach(() => fetchMock.mockReset());

describe("조직 일괄 반영 화면", () => {
  it("메뉴는 인사 발령 바로 아래이고 관리자에게만 보인다", () => {
    const labels = (role: Role) =>
      visibleMenu(orgModule.menu, (p) => p === "authenticated" || p === "public" || roleHas(role, p)).map((m) => m.label);
    const admin = labels("admin");
    expect(admin.indexOf("조직 일괄 반영")).toBe(admin.indexOf("인사 발령") + 1);
    for (const r of ["staff", "manager", "finance", "auditor"] as Role[]) expect(labels(r)).not.toContain("조직 일괄 반영");
  });

  it("파일 없이 미리보기를 누르면 안내하고 요청하지 않는다", () => {
    renderPage();
    expect(screen.getByRole("list", { name: "진행 단계" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(screen.getByText("올릴 파일을 하나 이상 선택하세요.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("오류가 있는 미리보기: 오류 요약과 행 목록, 반영 버튼은 꺼져 있다", async () => {
    fetchMock.mockResolvedValueOnce({
      ...base,
      previewId: "",
      errors: [{ file: "사람", line: 3, col: "직위", msg: "x", text: '사람 3행 직위: 등록되지 않은 직위입니다("과장님").' }],
      fileErrors: [],
      items: [],
    });
    renderPage();
    pick("사람 파일", "people.csv");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText(/오류 1개가 있어 반영할 수 없습니다. 오류 행을 고쳐 다시 올리세요./)).toBeInTheDocument();
    expect(screen.getByText('사람 3행 직위: 등록되지 않은 직위입니다("과장님").')).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "이 내용으로 반영" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "오류 행 내려받기" })).toBeInTheDocument();
  });

  it("미리보기: 읽은 결과·건수 카드·영향·경고·필터가 보이고 반영하면 결과 단계가 된다", async () => {
    fetchMock.mockResolvedValueOnce(base);
    renderPage();
    pick("조직 파일");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText(/아직 아무것도 반영되지 않았습니다. 이 미리보기는 10분 동안 유효합니다/)).toBeInTheDocument();
    expect(screen.getByText("EUC-KR로 읽었습니다")).toBeInTheDocument();
    const cards = within(screen.getByRole("list", { name: "건수" }));
    expect(cards.getAllByRole("listitem")).toHaveLength(10);
    expect(cards.getByText("조직 신설").nextElementSibling).toHaveTextContent("1");
    expect(screen.getAllByText("구매부서장(E000016)").length).toBe(2); // 영향 표와 변경 목록
    expect(screen.getByText("회의실 프로젝터 구매")).toBeInTheDocument();
    expect(screen.getByText(/권한 역할이 '직원'라서/)).toBeInTheDocument();
    // 상태 필터
    fireEvent.click(screen.getByRole("button", { name: "퇴사" }));
    expect(screen.queryByText("데이터팀(DEV-3)")).toBeNull();
    expect(screen.getAllByText("구매부서장(E000016)").length).toBeGreaterThan(0);
    expect(screen.queryByText("김하윤(E000125)")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "변화 없음" }));
    expect(screen.getByText("변화 없음 행은 목록에 나오지 않고 건수만 셉니다.")).toBeInTheDocument();
    // 반영
    fetchMock.mockResolvedValueOnce({ batch: 12, events: 9, counts: {} });
    fireEvent.click(screen.getByRole("button", { name: "이 내용으로 반영" }));
    expect(await screen.findByText("반영했습니다. 변경 9건이 반영되었습니다(반영 번호 12).")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "조직도에서 보기" })).toHaveAttribute("href", "/org");
    const [, init] = fetchMock.mock.calls[1];
    expect((init.body as FormData).get("previewId")).toBe("tok");
    expect((init.body as FormData).get("confirm_retroactive")).toBe("false");
  });

  it("30일 넘는 소급이 있으면 확인 체크 전에는 반영 버튼이 꺼져 있다", async () => {
    fetchMock.mockResolvedValueOnce({ ...base, retroRows: ["사람 9행, 2026-09-01"] });
    renderPage();
    pick("조직 파일");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    const apply = await screen.findByRole("button", { name: "이 내용으로 반영" });
    expect(apply).toBeDisabled();
    fireEvent.click(screen.getByLabelText("확인했습니다"));
    expect(apply).toBeEnabled();
  });

  it("미리보기 이후 상태가 바뀌어 409 가 오면 문구와 다시 미리보기 버튼을 보인다", async () => {
    fetchMock.mockResolvedValueOnce(base);
    renderPage();
    pick("조직 파일");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    const apply = await screen.findByRole("button", { name: "이 내용으로 반영" });
    fetchMock.mockRejectedValueOnce(new ApiError(409, "미리보기 뒤에 조직이 바뀌었습니다. 다시 미리보기 하세요."));
    fireEvent.click(apply);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/미리보기 뒤에 조직이 바뀌었습니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "다시 미리보기" }));
    expect(screen.getByRole("button", { name: "미리보기" })).toBeInTheDocument();
  });

  it("서버 오류는 내부 문구 없이 안내하고, 반영할 변경이 없으면 반영 버튼이 꺼져 있다", async () => {
    fetchMock.mockRejectedValueOnce(new ApiError(500, "panic: nil pointer"));
    renderPage();
    pick("조직 파일");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText("처리하지 못했습니다. 잠시 후 다시 시도하세요.")).toBeInTheDocument();
    expect(screen.queryByText(/panic/)).toBeNull();
    fetchMock.mockResolvedValueOnce({ ...base, previewId: "", items: [], itemsTotal: 0, noChange: true, impact: [], warnings: [] });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText("반영할 변경이 없습니다. 이미 모두 반영된 파일입니다.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "이 내용으로 반영" })).toBeDisabled();
  });
});

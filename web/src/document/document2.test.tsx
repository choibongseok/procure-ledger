import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { PurchaseForm } from "../purchase/PurchaseForm";
import { DocForm } from "./DocForm";
import { FormVersionEditor } from "./FormAdmin";
import type { FormDetail, FormVersion } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }, apiFetch: vi.fn() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;
const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };
const admin: CurrentUser = { id: 7, name: "관리자", role: "admin", departmentId: 3 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const def = (settings = {}, line: object = { steps: [{ role: "approve", kind: "team_lead", source: "org_level", level: 1, required: true }, { role: "agree", source: "unit_head", unitId: 3, required: false }] }) => ({
  system: false,
  fields: [{ key: "target_unit", label: "협조 요청 부서", type: "unit", required: true }],
  body: "",
  line,
  recipients: { cc: [{ unit_code: "FIN" }], receive: [] },
  numbering: { pattern: "{unit}-{yyyy}-{seq:4}", assign_at: "submit", reset: "yearly" },
  retention_years: 5,
  dept_limit: [3],
  settings: { line_edit: "free", objection_action: "record", amount_field: "", dept_visible: false, urgent_allowed: true, ...settings },
});

function at(user: CurrentUser, path: string, route: string, el: import("react").JSX.Element) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider initialUser={user}>
        <Routes>
          <Route path={route} element={el} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

const step = (over = {}) => ({ order: 1, kind: "team_lead", title: "팀장", approverName: "구매팀장", unitPath: "구매1팀", unitName: "구매1팀", approverId: 2, acting: false, raisedNote: "", layerNote: "", omitted: false, missing: false, reason: "", poolSize: 0, role: "approve", stage: 1, required: true, defIndex: 0, ...over });

describe("B23 단계 1 보완 화면", () => {
  it("양식 관리: 기본 결재선·기본 참조처·사용 부서 제한을 폼으로 편집한다(JSON 입력 칸 없음)", async () => {
    const ver: FormVersion = { id: 9, formCode: "COOP", version: 2, status: "draft", definition: def() as FormVersion["definition"], createdAt: "", publishedAt: "" };
    mocked.get.mockImplementation(async (path: string) => (path === "/api/org/tree" ? { root: { id: 1, name: "PL", children: [{ id: 3, name: "재무팀", children: [] }] } } : ver));
    at(admin, "/forms/versions/9", "/forms/versions/:id", <FormVersionEditor />);
    expect(await screen.findByRole("heading", { name: "기본 결재선" })).toBeInTheDocument();
    expect(screen.getByLabelText("기본 단계 1")).toBeInTheDocument();
    expect(screen.getByLabelText("기본 단계 2")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "기본 참조처·수신처" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "사용 부서 제한" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("재무팀 제한 빼기")).toBeInTheDocument());
    expect(screen.queryByLabelText(/JSON/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "기본 단계 추가" }));
    expect(screen.getByLabelText("기본 단계 3")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "단계 3 삭제" }));
    expect(screen.queryByLabelText("기본 단계 3")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "재무팀 제한 빼기" }));
    expect(screen.getByText("제한이 없습니다(모든 부서가 씁니다).")).toBeInTheDocument();
  });

  it("작성 화면(free 양식): 필수가 아닌 기본 단계에만 빼기 버튼이 있고 누르면 편집에 기본 단계 빼기가 들어간다", async () => {
    const f: FormDetail = { id: 4, code: "COOP", category: "cooperation", name: "업무 협조전", kind: "general", version: 1, active: true, versionId: 4, definition: def() as FormDetail["definition"] };
    mocked.get.mockImplementation(async (path: string) => (path === "/api/forms/COOP" ? f : []));
    mocked.post.mockImplementation(async (_path: string, body: { edits: { role: string; targetId: number }[] }) => ({
      requesterName: "김직원", unitPath: "구매팀", complete: true, problems: [], notes: [], recipients: [],
      steps: [step(), ...(body.edits.some((e) => e.role === "remove") ? [] : [step({ order: 2, kind: "agree", title: "합의", role: "agree", stage: 2, required: false, defIndex: 2, approverName: "재무장" })])],
    }));
    at(staff, "/docs/new/COOP", "/docs/new/:code", <DocForm />);
    const table = await screen.findByRole("table");
    expect(within(table).queryByRole("button", { name: "1단계 빼기" })).toBeNull();
    fireEvent.click(await within(table).findByRole("button", { name: "2단계 빼기" }));
    expect(await screen.findByText("기본 2번째 단계를 뺍니다")).toBeInTheDocument();
    await waitFor(() => expect(mocked.post).toHaveBeenLastCalledWith("/api/approval-docs/line-preview", expect.objectContaining({ edits: [{ role: "remove", targetType: "default", targetId: 2, group: 0 }] })));
    fireEvent.click(screen.getByRole("button", { name: "되돌리기" }));
    expect(screen.queryByText("기본 2번째 단계를 뺍니다")).toBeNull();
  });

  it("작성 화면: 저장해 둔 편집에서 자격을 잃어 뺀 대상 안내를 보인다(재상신)", async () => {
    const f: FormDetail = { id: 2, code: "GEN", category: "general", name: "일반 기안서", kind: "general", version: 1, active: true, versionId: 2, definition: def({}, { steps: [{ role: "approve", kind: "team_lead", source: "org_level", level: 1, required: true }] }) as FormDetail["definition"] };
    mocked.get.mockImplementation(async (path: string) => {
      if (path === "/api/approval-docs/5")
        return { id: 5, title: "시험", formCode: "GEN", fields: {}, formFields: f.definition.fields, draftEdits: [{ role: "cc", targetType: "user", targetId: 9, group: 0, name: "영업직원" }], editNotes: ["재무님은 퇴사했거나 재직 중이 아니어서 결재선에서 뺐습니다. 필요하면 다른 사람을 더하세요."] };
      if (path === "/api/forms/GEN") return f;
      return [];
    });
    mocked.post.mockResolvedValue({ requesterName: "김직원", unitPath: "구매팀", complete: true, problems: [], notes: [], recipients: [], steps: [step()] });
    at(staff, "/docs/5/edit", "/docs/:id/edit", <DocForm />);
    expect(await screen.findByText(/재무님은 퇴사했거나 재직 중이 아니어서 결재선에서 뺐습니다/)).toBeInTheDocument();
    expect(await screen.findByText(/참조: 영업직원/)).toBeInTheDocument();
  });

  it("구매요청 작성 화면에 참조·수신 추가 칸이 있다(결재 단계 편집은 없다)", async () => {
    mocked.get.mockImplementation(async (path: string) => (path.startsWith("/api/items") || path.startsWith("/api/vendors") || path === "/api/personal-lines" ? [] : { steps: [], complete: false, problems: [], notes: [] }));
    at(staff, "/purchase-requests/new", "/purchase-requests/new", <PurchaseForm />);
    const region = await screen.findByLabelText("참조·수신");
    expect(within(region).getByRole("heading", { name: "참조·수신" })).toBeInTheDocument();
    const roles = within(region).getByLabelText("역할") as HTMLSelectElement;
    expect([...roles.options].map((o) => o.text)).toEqual(["참조", "수신"]);
  });
});

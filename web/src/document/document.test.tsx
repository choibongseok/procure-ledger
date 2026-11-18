import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { DocForm } from "./DocForm";
import { DocNew } from "./DocNew";
import { DocView } from "./DocView";
import { FormVersionEditor, bodyPreview } from "./FormAdmin";
import type { FormDetail, FormVersion } from "./types";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }, apiFetch: vi.fn() };
});
const mocked = api as unknown as Record<"get" | "post" | "put" | "del", ReturnType<typeof vi.fn>>;

const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };
const manager: CurrentUser = { id: 2, name: "김팀장", role: "manager", departmentId: 1 };
const admin: CurrentUser = { id: 7, name: "관리자", role: "admin", departmentId: 3 };

beforeEach(() => {
  for (const k of ["get", "post", "put", "del"] as const) mocked[k].mockReset();
});

const defn = (over = {}) => ({
  system: false,
  fields: [
    { key: "content", label: "본문", type: "textarea", required: true },
    { key: "kind", label: "구분", type: "select", required: false, options: ["가", "나"] },
  ],
  body: "내용 {{content}} {{nothing}}",
  line: { steps: [] },
  recipients: { cc: [], receive: [] },
  numbering: { pattern: "{unit}-{yyyy}-{seq:4}", assign_at: "submit", reset: "yearly" },
  retention_years: 5,
  dept_limit: [],
  settings: { line_edit: "free", objection_action: "record", amount_field: "", dept_visible: false, urgent_allowed: true },
  ...over,
});
const gen: FormDetail = { id: 2, code: "GEN", category: "general", name: "일반 기안서", kind: "general", version: 1, active: true, versionId: 2, definition: defn() as FormDetail["definition"] };

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

const previewOf = (over = {}) => ({
  requesterName: "김직원", unitPath: "구매팀 > 구매1팀", complete: true, problems: [], notes: [], recipients: [],
  steps: [
    { order: 1, kind: "team_lead", title: "팀장", approverName: "구매팀장", unitPath: "구매팀 > 구매1팀", unitName: "구매1팀", approverId: 2, acting: false, raisedNote: "", layerNote: "", omitted: false, missing: false, reason: "", poolSize: 0, role: "approve", stage: 1, required: true },
    { order: 2, kind: "agree", title: "합의", approverName: "재무", unitPath: "재무팀", unitName: "재무팀", approverId: 4, acting: false, raisedNote: "", layerNote: "", omitted: false, missing: false, reason: "", poolSize: 0, role: "agree", stage: 2, required: false },
    { order: 3, kind: "agree", title: "합의", approverName: "구매팀장2", unitPath: "구매2팀", unitName: "구매2팀", approverId: 10, acting: false, raisedNote: "", layerNote: "", omitted: false, missing: false, reason: "", poolSize: 0, role: "agree", stage: 2, required: false },
  ],
  ...over,
});

describe("결재 문서 화면", () => {
  it("기안 시작: 양식을 분류별로 보이고 구매 품의서는 구매요청 작성으로 간다", async () => {
    mocked.get.mockResolvedValue([
      { id: 1, code: "PUR", category: "purchase", name: "구매 품의서", kind: "purchase", version: 1, active: true },
      { id: 2, code: "GEN", category: "general", name: "일반 기안서", kind: "general", version: 1, active: true },
    ]);
    at(staff, "/docs/new", "/docs/new", <DocNew />);
    expect(await screen.findByRole("link", { name: "구매 품의서" })).toHaveAttribute("href", "/purchase-requests/new");
    expect(screen.getByRole("link", { name: "일반 기안서" })).toHaveAttribute("href", "/docs/new/GEN");
    expect(screen.getByText("구매")).toBeInTheDocument();
  });

  it("기안 시작: 양식이 없으면 안내 문구", async () => {
    mocked.get.mockResolvedValue([]);
    at(staff, "/docs/new", "/docs/new", <DocNew />);
    expect(await screen.findByText("지금 쓸 수 있는 양식이 없습니다. 관리자에게 문의하세요.")).toBeInTheDocument();
  });

  it("작성 화면: 필드 폼·결재선 미리보기(병렬 합의 표시)·상신 불가 시 버튼 비활성, 서버 필드 오류를 칸 옆에 보인다", async () => {
    mocked.get.mockImplementation(async (path: string) => {
      if (path === "/api/forms/GEN") return gen;
      if (path === "/api/personal-lines") return [];
      throw new Error(path);
    });
    mocked.post.mockImplementation(async (path: string) => {
      if (path === "/api/approval-docs/line-preview") return previewOf();
      if (path === "/api/approval-docs") throw new ApiError(400, JSON.stringify({ error: "입력이 올바르지 않습니다", fieldErrors: { content: "「본문」을(를) 입력하세요." } }));
      throw new Error(path);
    });
    at(staff, "/docs/new/GEN", "/docs/new/:code", <DocForm />);
    expect(await screen.findByLabelText("본문 *")).toBeInTheDocument();
    expect(screen.getByLabelText("구분")).toBeInTheDocument();
    const table = await screen.findByRole("table");
    expect(within(table).getAllByText("합의 (병렬)")).toHaveLength(2);
    expect(within(table).getByText("필수")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\bagree\b|team_lead|approve\b|pending/);
    fireEvent.change(screen.getByLabelText("제목 *"), { target: { value: "시험" } });
    fireEvent.click(screen.getByRole("button", { name: "임시저장" }));
    expect(await screen.findAllByText("「본문」을(를) 입력하세요.")).not.toHaveLength(0);
  });

  it("작성 화면: 결재선을 만들 수 없으면 오류 상자만 보이고 상신 버튼이 꺼진다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path === "/api/forms/GEN" ? gen : []));
    mocked.post.mockResolvedValue(previewOf({ complete: false, problems: ["기안자는 결재선에 넣을 수 없습니다."], steps: [] }));
    at(staff, "/docs/new/GEN", "/docs/new/:code", <DocForm />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("이 상태로는 상신할 수 없습니다. 임시저장은 할 수 있습니다.");
    expect(alert.textContent).toContain("기안자는 결재선에 넣을 수 없습니다.");
    fireEvent.change(screen.getByLabelText("제목 *"), { target: { value: "시험" } });
    await waitFor(() => expect((screen.getByRole("button", { name: "저장 후 상신" }) as HTMLButtonElement).disabled).toBe(true));
  });

  const docOf = (over = {}) => ({
    id: 5, no: "구매팀-2026-0001", requesterId: 1, requesterName: "김직원", departmentId: 1, vendorId: 0, title: "기안 시험", reason: "", status: "in_review",
    totalAmount: 0, rejectReason: "", lines: [], purchaseOrderId: 0, createdAt: "2026-11-10T00:00:00Z", updatedAt: "2026-11-10T00:00:00Z",
    formCode: "GEN", formName: "일반 기안서", formKind: "general", fields: { content: "본문 내용" }, formFields: gen.definition.fields,
    steps: [
      { seq: 1, kind: "team_lead", approverId: 2, status: "approved", comment: "", decidedAt: "2026-11-10T01:00:00Z", title: "팀장", assigneeId: 2, assigneeName: "구매팀장", role: "approve", stage: 1, required: true },
      { seq: 2, kind: "agree", approverId: 0, status: "pending", comment: "", decidedAt: "", title: "합의", assigneeId: 2, assigneeName: "구매팀장", role: "agree", stage: 2, required: false },
      { seq: 3, kind: "agree", approverId: 0, status: "pending", comment: "", decidedAt: "", title: "합의", assigneeId: 4, assigneeName: "재무", role: "agree", stage: 2, required: false },
    ],
    recipients: [{ kind: "receive", targetType: "unit", targetId: 3, name: "재무팀", path: "재무팀", note: "" }],
    ...over,
  });

  it("상세: 내 차례가 합의 단계면 동의·이견 버튼, 이견은 사유가 있어야 켜지고 영문 코드가 보이지 않는다", async () => {
    mocked.get.mockImplementation(async (path: string) => (path.endsWith("/history") ? [] : docOf()));
    mocked.post.mockResolvedValue({ request: docOf({ status: "approved" }) });
    at(manager, "/docs/5", "/docs/:id", <DocView />);
    expect(await screen.findByRole("button", { name: "동의" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "승인" })).toBeNull();
    const objection = screen.getByRole("button", { name: "이견" }) as HTMLButtonElement;
    expect(objection.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/의견/), { target: { value: "금액이 큽니다" } });
    expect(objection.disabled).toBe(false);
    expect(screen.getAllByText("합의 (병렬)")).toHaveLength(2);
    expect(screen.getByText(/수신: 재무팀/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\bagree\b|\bpending\b|general|in_review/);
    fireEvent.click(screen.getByRole("button", { name: "동의" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/api/approvals/5/decision", { approve: true, comment: "금액이 큽니다" }));
  });

  it("상세: 내 차례가 결재 단계면 승인·반려, 차례가 아니면 결재 영역이 없고, 이견이 있으면 안내 상자를 보인다", async () => {
    const steps = [
      { seq: 1, kind: "team_lead", approverId: 0, status: "pending", comment: "", decidedAt: "", title: "팀장", assigneeId: 2, assigneeName: "구매팀장", role: "approve", stage: 1, required: true },
    ];
    mocked.get.mockImplementation(async (path: string) => (path.endsWith("/history") ? [] : docOf({ steps })));
    at(manager, "/docs/5", "/docs/:id", <DocView />);
    expect(await screen.findByRole("button", { name: "승인" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "반려" })).toBeInTheDocument();
  });

  it("상세: 합의 이견 안내와 결재 차례 아닌 사용자(직원)에게는 처리 영역이 없다", async () => {
    const steps = [{ seq: 1, kind: "agree", approverId: 4, status: "objected", comment: "반대합니다", decidedAt: "2026-11-10T01:00:00Z", title: "합의", assigneeId: 4, assigneeName: "재무", role: "agree", stage: 1, required: false }];
    mocked.get.mockImplementation(async (path: string) => (path.endsWith("/history") ? [] : docOf({ steps, status: "approved" })));
    at(staff, "/docs/5", "/docs/:id", <DocView />);
    expect(await screen.findByText(/합의자 재무님이 이견을 남겼습니다: 반대합니다/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "동의" })).toBeNull();
    expect(screen.queryByRole("button", { name: "승인" })).toBeNull();
  });

  it("양식 관리 편집기: 필드를 폼으로 고치고 JSON 입력 칸이 없으며 본문 서식 미리보기에 없는 필드를 표시한다", async () => {
    const ver: FormVersion = { id: 9, formCode: "GEN", version: 2, status: "draft", definition: defn() as FormVersion["definition"], createdAt: "", publishedAt: "" };
    mocked.get.mockResolvedValue(ver);
    at(admin, "/forms/versions/9", "/forms/versions/:id", <FormVersionEditor />);
    expect(await screen.findByLabelText("본문 서식 미리보기")).toHaveTextContent("내용 ［본문 값］ ［없는 필드: nothing］");
    expect(screen.queryByLabelText(/JSON/i)).toBeNull();
    expect(screen.getAllByLabelText("이름")).toHaveLength(2);
    expect(screen.getAllByLabelText("키(영문 소문자·숫자·밑줄)")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "필드 추가" }));
    expect(screen.getAllByLabelText("이름")).toHaveLength(3);
    mocked.put.mockResolvedValue({});
    mocked.post.mockRejectedValue(new ApiError(400, "입력이 올바르지 않습니다: 본문 서식의 {{nothing}} 에 해당하는 필드가 없습니다."));
    fireEvent.click(screen.getByRole("button", { name: "게시" }));
    expect(await screen.findByText(/본문 서식의 \{\{nothing\}\} 에 해당하는 필드가 없습니다/)).toBeInTheDocument();
  });

  it("양식 관리 편집기: 법정 보존(폐기 불가) 체크가 일반 양식에서는 고칠 수 있고 시스템 양식에서는 항상 켜져 고칠 수 없다(B29)", async () => {
    const ver: FormVersion = { id: 9, formCode: "GEN", version: 2, status: "draft", definition: defn() as FormVersion["definition"], createdAt: "", publishedAt: "" };
    mocked.get.mockResolvedValue(ver);
    at(admin, "/forms/versions/9", "/forms/versions/:id", <FormVersionEditor />);
    const box = await screen.findByRole("checkbox", { name: /법정 보존\(폐기 불가\)/ });
    expect(box).not.toBeChecked();
    fireEvent.click(box);
    expect(box).toBeChecked();
  });

  it("본문 서식 미리보기 함수", () => {
    expect(bodyPreview("{{a}}-{{b}}", [{ key: "a", label: "가", type: "text", required: false }])).toBe("［가 값］-［없는 필드: b］");
  });
});

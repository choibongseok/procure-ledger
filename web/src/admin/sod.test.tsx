import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { MyPrivacyRequestsPage, PrivacyRequestsAdminPage } from "./PrivacyRequestsPage";
import { SodExceptionsPage, SodRulesPage, SodViolationsPage } from "./SodPages";

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

const rules = [
  { code: "pay.create_settle", kind: "txn", title: "지급 요청 작성자의 지급 처리", stepA: "지급 요청 작성", stepB: "지급 처리", mode: "block", default: "block", locked: true, violations30d: 0, blocked30d: 0, holders: 0 },
  { code: "pay.create_approve", kind: "txn", title: "지급 요청 작성자의 최종 승인", stepA: "지급 요청 작성", stepB: "최종 승인", mode: "warn", default: "warn", locked: false, violations30d: 3, blocked30d: 0, holders: 0 },
  { code: "hold.purchase_decide", kind: "hold", title: "구매 요청 + 결재 권한 보유", stepA: "구매 요청", stepB: "결재", mode: "warn", default: "warn", locked: false, violations30d: 0, blocked30d: 0, holders: 2 },
];

function renderPage(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  perms = [];
});

describe("권한 분리 규칙 화면", () => {
  it("잠긴 규칙은 모드를 바꿀 수 없고, 약화하면 확인을 거쳐 저장한다", async () => {
    perms = ["sod:read", "sod:manage"];
    get.mockResolvedValue(rules);
    put.mockResolvedValue(undefined);
    renderPage(<SodRulesPage />);
    const table = await screen.findByRole("table", { name: "권한 분리 규칙" });
    expect(within(table).getByText("잠김")).toBeInTheDocument();
    expect(within(table).getAllByRole("combobox")).toHaveLength(2);
    expect(within(table).getByText(/보유자 2명/)).toBeInTheDocument();
    // 경고 → 차단은 바로 저장한다.
    fireEvent.change(within(table).getByLabelText("지급 요청 작성자의 최종 승인 모드"), { target: { value: "block" } });
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/sod/rules/pay.create_approve", { mode: "block" }));
    // 경고 → 끔은 확인 뒤에 저장한다.
    put.mockClear();
    fireEvent.change(within(table).getByLabelText("구매 요청 + 결재 권한 보유 모드"), { target: { value: "off" } });
    expect(put).not.toHaveBeenCalled();
    fireEvent.click(await screen.findByRole("button", { name: "바꾸기" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/sod/rules/hold.purchase_decide", { mode: "off" }));
  });

  it("sod:manage 가 없으면 모드를 보기만 한다", async () => {
    perms = ["sod:read"];
    get.mockResolvedValue(rules);
    renderPage(<SodRulesPage />);
    const table = await screen.findByRole("table", { name: "권한 분리 규칙" });
    expect(within(table).queryAllByRole("combobox")).toHaveLength(0);
    expect(within(table).getAllByText("경고").length).toBeGreaterThan(0);
  });
});

describe("권한 분리 위반·예외 화면", () => {
  it("위반 목록은 결과 배지와 대상을 보이고 필터를 서버로 보낸다", async () => {
    perms = ["sod:read"];
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/admin/sod/rules")) return rules;
      return [{ id: 1, at: "2026-11-13T01:00:00Z", rule: "pay.create_approve", ruleTitle: "지급 요청 작성자의 최종 승인", result: "blocked", kind: "txn", subjectType: "payment", subjectId: 7, subjectNo: "PAY-7", actorName: "김재무", note: "" }];
    });
    renderPage(<SodViolationsPage />);
    const table = await screen.findByRole("table", { name: "권한 분리 위반" });
    expect(within(table).getByText("차단")).toBeInTheDocument();
    expect(within(table).getByText("지급 PAY-7")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("결과"), { target: { value: "blocked" } });
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringContaining("result=blocked")));
  });

  it("월별 보고서 내려받기는 조회 기록 열람 권한이 있을 때만 보이고 선택한 달로 연결된다", async () => {
    get.mockImplementation(async (url: string) => (url.startsWith("/api/admin/sod/rules") ? rules : []));
    perms = ["sod:read"];
    const first = renderPage(<SodViolationsPage />);
    await screen.findByText("위반 기록이 없습니다.");
    expect(screen.queryByRole("link", { name: "엑셀로 내려받기" })).toBeNull();
    first.unmount();
    perms = ["sod:read", "privacy:log_read"];
    renderPage(<SodViolationsPage />);
    await screen.findByText("위반 기록이 없습니다.");
    fireEvent.change(screen.getByLabelText("월별 점검 보고서"), { target: { value: "2026-10" } });
    expect(screen.getByRole("link", { name: "엑셀로 내려받기" })).toHaveAttribute("href", "/api/audit/privacy/report?month=2026-10");
  });

  it("예외 요청은 잠긴 규칙을 고를 수 없고, 승인 대기 건은 승인 권한자에게만 승인·반려가 보인다", async () => {
    perms = ["sod:read", "sod:manage"];
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/admin/sod/rules")) return rules;
      return [{ id: 5, rule: "pay.create_approve", ruleTitle: "지급 요청 작성자의 최종 승인", subjectType: "payment", subjectId: 7, subjectNo: "PAY-7", userName: "김재무", reason: "소규모 팀이라 한 사람이 처리", requestedName: "관리자", requestedAt: "2026-11-13T01:00:00Z", status: "requested" }];
    });
    post.mockResolvedValue({ id: 6 });
    renderPage(<SodExceptionsPage />);
    const select = await screen.findByLabelText("규칙");
    expect(within(select).queryByText("지급 요청 작성자의 지급 처리")).toBeNull();
    expect(within(select).getByText("지급 요청 작성자의 최종 승인")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "승인" })).toBeNull();
    fireEvent.change(select, { target: { value: "pay.create_approve" } });
    fireEvent.change(screen.getByLabelText("거래 번호(내부 번호)"), { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText("예외로 처리할 사람(사용자 번호)"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("사유(10자 이상)"), { target: { value: "소규모 팀이라 한 사람이 처리" } });
    fireEvent.click(screen.getByRole("button", { name: "예외 요청" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/api/admin/sod/exceptions", { rule: "pay.create_approve", subjectType: "payment", subjectId: 7, subjectNo: "", userId: 3, reason: "소규모 팀이라 한 사람이 처리" }),
    );
  });

  it("승인 권한자는 승인·반려를 누를 수 있다", async () => {
    perms = ["sod:read", "sod:exception_approve"];
    get.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/admin/sod/rules")) return rules;
      return [{ id: 5, rule: "pay.create_approve", ruleTitle: "지급 요청 작성자의 최종 승인", subjectType: "payment", subjectId: 7, subjectNo: "PAY-7", userName: "김재무", reason: "소규모 팀이라 한 사람이 처리", requestedName: "관리자", requestedAt: "2026-11-13T01:00:00Z", status: "requested" }];
    });
    post.mockResolvedValue(undefined);
    renderPage(<SodExceptionsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "승인" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/sod/exceptions/5/approve", {}));
    expect(screen.queryByLabelText("규칙")).toBeNull();
  });
});

describe("정보주체 열람 요청 화면", () => {
  it("직원은 기간을 골라 요청하고 승인된 요청의 결과를 부서·역할로 본다", async () => {
    get.mockImplementation(async (url: string) => {
      if (url.endsWith("/result")) return [{ at: "2026-11-13T01:00:00Z", viewerUnit: "재무팀", viewerRole: "관리자", category: "contact", screen: "조직도 사람 카드", fields: '["email"]', isExport: false, summary: false }];
      return [{ id: 4, userId: 1, status: "approved", periodFrom: "2026-10-14", periodTo: "2026-11-13", reason: "", requestedAt: "2026-11-13T00:00:00Z", expiresAt: "2026-12-13T00:00:00Z" }];
    });
    post.mockResolvedValue({ id: 5 });
    renderPage(<MyPrivacyRequestsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "결과 보기" }));
    const result = await screen.findByRole("table", { name: "내 정보 열람 결과" });
    expect(within(result).getByText("재무팀")).toBeInTheDocument();
    expect(within(result).getByText("관리자")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "요청하기" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/privacy/requests", expect.objectContaining({ reason: "" })));
  });

  it("관리자는 대기 건을 승인하고, 반려는 메모를 받는다", async () => {
    get.mockResolvedValue([{ id: 9, userId: 3, userName: "박직원", status: "requested", periodFrom: "2026-10-14", periodTo: "2026-11-13", reason: "확인", requestedAt: "2026-11-13T00:00:00Z" }]);
    post.mockResolvedValue(undefined);
    renderPage(<PrivacyRequestsAdminPage />);
    await screen.findByText("박직원");
    fireEvent.click(screen.getByRole("button", { name: "승인" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/privacy/requests/9/approve", { note: "" }));
    post.mockClear();
    fireEvent.click(await screen.findByRole("button", { name: "반려" }));
    // 사유 없이는 반려할 수 없다(서버로 보내지 않고 안내한다).
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "반려" }));
    expect(await screen.findByText(/반려 사유를 입력하세요/)).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText("반려 사유"), { target: { value: "범위 불명확" } });
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "반려" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/privacy/requests/9/reject", { note: "범위 불명확" }));
  });
});

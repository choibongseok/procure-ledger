import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ConfirmDialog, IconButton } from "../layout/ui";
import { Layout } from "../layout/Layout";
import { AuditLogPage } from "./AuditLogPage";
import { SettingsPage } from "./SettingsPage";
import { TokensPage } from "./TokensPage";

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

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const del = vi.mocked(api.del);
const put = vi.mocked(api.put);

beforeEach(() => {
  for (const f of [get, post, del, put]) f.mockReset();
});

describe("공용 확인 대화상자·아이콘 버튼", () => {
  function Host({ onConfirm }: { onConfirm: () => void }) {
    const [open, setOpen] = useState(false);
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          열기
        </button>
        {open && (
          <ConfirmDialog title="삭제" confirmLabel="삭제하기" onConfirm={onConfirm} onCancel={() => setOpen(false)}>
            <p>되돌릴 수 없습니다.</p>
          </ConfirmDialog>
        )}
      </>
    );
  }

  it("제목·결과 설명·위험 버튼·취소가 있고 취소에 포커스가 가며, Esc 로 닫으면 열었던 버튼으로 돌아간다", () => {
    const onConfirm = vi.fn();
    render(<Host onConfirm={onConfirm} />);
    const opener = screen.getByRole("button", { name: "열기" });
    opener.focus();
    fireEvent.click(opener);
    const dlg = screen.getByRole("alertdialog", { name: "삭제" });
    expect(dlg).toHaveAttribute("aria-modal", "true");
    expect(dlg).toHaveAccessibleDescription("되돌릴 수 없습니다.");
    expect(within(dlg).getByRole("button", { name: "삭제하기" })).toHaveClass("btn-danger");
    expect(within(dlg).getByRole("button", { name: "취소" })).toHaveFocus();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(opener).toHaveFocus();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("Tab 은 대화상자 안에서만 돌고, 위험 버튼을 눌러야 실행된다", () => {
    const onConfirm = vi.fn();
    render(<Host onConfirm={onConfirm} />);
    fireEvent.click(screen.getByRole("button", { name: "열기" }));
    const dlg = screen.getByRole("alertdialog");
    const danger = within(dlg).getByRole("button", { name: "삭제하기" });
    danger.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(within(dlg).getByRole("button", { name: "취소" })).toHaveFocus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(danger).toHaveFocus();
    fireEvent.click(danger);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("아이콘 전용 버튼은 aria-label 과 title 을 갖고 아이콘은 읽히지 않는다", () => {
    render(<IconButton label="행 삭제">x</IconButton>);
    const b = screen.getByRole("button", { name: "행 삭제" });
    expect(b).toHaveAttribute("title", "행 삭제");
    expect(b.querySelector("[aria-hidden='true']")).not.toBeNull();
  });
});

describe("개인 토큰 화면", () => {
  const tok = { id: 5, name: "노트북", prefix: "plp_k3f9", createdAt: "2026-11-01T00:00:00Z", expiresAt: "2999-01-01T00:00:00Z", revokedAt: null, lastUsedAt: null };

  it("폐기는 확인 대화상자를 거치고 취소하면 아무것도 하지 않는다", async () => {
    get.mockResolvedValue([tok]);
    del.mockResolvedValue(undefined);
    render(<TokensPage />);
    fireEvent.click(await screen.findByRole("button", { name: "폐기" }));
    const dlg = screen.getByRole("alertdialog", { name: "토큰 폐기" });
    expect(dlg).toHaveTextContent("노트북");
    expect(dlg).toHaveTextContent("되살릴 수 없습니다");
    fireEvent.click(within(dlg).getByRole("button", { name: "취소" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(del).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "폐기" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "폐기" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/tokens/5"));
  });

  it("폐기 실패 문구에는 다음 할 일이 있다", async () => {
    get.mockResolvedValue([tok]);
    del.mockRejectedValue(new Error("x"));
    render(<TokensPage />);
    fireEvent.click(await screen.findByRole("button", { name: "폐기" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "폐기" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("토큰을 폐기하지 못했습니다. 잠시 뒤 다시 시도하세요.");
  });

  it("발급 거부(409) 문구에서 서버 머리말을 떼고 보인다", async () => {
    get.mockResolvedValue([]);
    post.mockRejectedValue(new ApiError(409, "현재 상태에서는 할 수 없는 동작입니다: 사용 중인 개인 토큰이 상한(10개)에 도달했습니다. 쓰지 않는 토큰을 폐기한 뒤 다시 발급하세요."));
    render(<TokensPage />);
    fireEvent.change(screen.getByLabelText(/이름/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "발급" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("사용 중인 개인 토큰이 상한(10개)에 도달했습니다.");
    expect(alert.textContent).not.toMatch(/현재 상태에서는/);
  });

  it("AI 도구 연결 안내 카드에 발급 순서와 연결 명령, 문서 위치가 있다", async () => {
    get.mockResolvedValue([]);
    render(<TokensPage />);
    const card = await screen.findByRole("region", { name: "AI 도구 연결 안내" });
    expect(card).toHaveTextContent("토큰으로는 토큰을 발급하거나 폐기할 수 없고");
    expect(screen.getByTestId("mcp-command")).toHaveTextContent(`claude mcp add --transport http procure-ledger ${window.location.origin}/mcp`);
    expect(card).toHaveTextContent("docs/mcp-connect.md");
  });
});

describe("감사 로그 필터와 상세", () => {
  const auditor = { id: 3, name: "감사", role: "auditor" as const, departmentId: 1 };
  const rec = {
    seq: 15,
    at: "2026-11-10T07:02:47Z",
    actorId: 2,
    actorName: "구매팀장",
    actorRole: "manager",
    channel: "web",
    action: "approval.approve",
    targetType: "approval",
    targetId: "46",
    result: "success",
    diff: [
      { key: "status", old: "submitted", new: "in_review" },
      { key: "comment", new: "확인했습니다." },
    ],
    detail: "",
    prevHash: "a",
    hash: "b",
  };

  it("사용자 이름과 경로를 조회 매개변수로 보낸다", async () => {
    get.mockResolvedValue({ records: [rec], nextBeforeSeq: 0 });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findByText("결재 승인");
    fireEvent.change(screen.getByLabelText("경로"), { target: { value: "mcp" } });
    fireEvent.change(screen.getByLabelText("사용자"), { target: { value: " 구매 " } });
    await waitFor(() => {
      const last = String(get.mock.calls[get.mock.calls.length - 1][0]);
      expect(last).toContain("channel=mcp");
      expect(last).toContain("actorName=%EA%B5%AC%EB%A7%A4");
    });
    // 경로 선택지는 웹·AI 도구·AI 호출·서버 명령·계정 동기화
    const opts = within(screen.getByLabelText("경로")).getAllByRole("option").map((o) => o.textContent);
    expect(opts).toEqual(["전체", "웹", "AI 도구", "AI 호출", "서버 명령", "계정 동기화(SCIM)", "외부 연동"]);
  });

  it("행의 상세 버튼이 [항목, 이전, 이후] 표를 열고 닫는다", async () => {
    get.mockResolvedValue({ records: [rec], nextBeforeSeq: 0 });
    render(
      <AuthProvider initialUser={auditor}>
        <AuditLogPage />
      </AuthProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "15번 기록 상세" }));
    const detail = screen.getByTestId("audit-detail");
    expect(detail).toHaveTextContent("기록 15번 상세");
    const table = within(detail).getByRole("table", { name: "변경 내용 상세" });
    const rows = within(table).getAllByRole("row");
    expect(rows[0]).toHaveTextContent("항목이전이후");
    expect(rows[1]).toHaveTextContent("상태");
    expect(rows[1]).toHaveTextContent("상신");
    expect(rows[1]).toHaveTextContent("결재중");
    // 이전 값이 없는 항목은 이전 칸이 빈칸
    const cells = within(rows[2]).getAllByRole("cell");
    expect(cells[1]).toHaveTextContent(/^$/);
    expect(detail.textContent).not.toMatch(/in_review|approval\.approve/);
    fireEvent.click(within(detail).getByRole("button", { name: "상세 닫기" }));
    expect(screen.queryByTestId("audit-detail")).toBeNull();
  });
});

describe("관리자 설정 화면", () => {
  const policies = { policies: [{ id: 1, role: "", departmentId: 0, modelPattern: "claude-sonnet-4-5", allowed: true }] };
  const quotas = { quotas: [{ id: 1, scopeType: "role", scopeValue: "finance", dailyTokenLimit: 200000 }] };
  function mockApi() {
    get.mockImplementation(async (url: string) => {
      if (url === "/api/llm/policies") return policies;
      if (url === "/api/llm/quotas") return quotas;
      return { enabled: false };
    });
  }

  it("모델 허용 목록과 일일 한도를 t3 의 기존 API 로 읽어 보인다", async () => {
    mockApi();
    render(<SettingsPage />);
    expect(await screen.findByText("claude-sonnet-4-5")).toBeInTheDocument();
    expect(screen.getByText("200,000")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "AI 모델 허용 목록" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "일일 사용 한도" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "이미지 증빙 AI 전송" })).toBeInTheDocument();
  });

  it("규칙 추가와 한도 저장은 기존 API 로 보낸다", async () => {
    mockApi();
    post.mockResolvedValue({});
    put.mockResolvedValue({});
    render(<SettingsPage />);
    await screen.findByText("claude-sonnet-4-5");
    const add = screen.getByRole("form", { name: "모델 규칙 추가" });
    fireEvent.change(within(add).getByLabelText("역할"), { target: { value: "staff" } });
    fireEvent.change(within(add).getByLabelText("모델 이름"), { target: { value: "claude-opus*" } });
    fireEvent.click(within(add).getByRole("checkbox"));
    fireEvent.click(within(add).getByRole("button", { name: "규칙 추가" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/llm/policies", { role: "staff", modelPattern: "claude-opus*", allowed: false }));

    const q = screen.getByRole("form", { name: "한도 설정" });
    fireEvent.change(within(q).getByLabelText("대상"), { target: { value: "staff" } });
    fireEvent.change(within(q).getByLabelText("하루 한도(토큰)"), { target: { value: "50,000" } });
    fireEvent.click(within(q).getByRole("button", { name: "한도 저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/llm/quotas", { scopeType: "role", scopeValue: "staff", dailyTokenLimit: 50000 }));
  });

  it("한도 0 은 확인 대화상자를 거치고 취소하면 저장하지 않는다. 규칙 삭제도 확인한다", async () => {
    mockApi();
    put.mockResolvedValue({});
    del.mockResolvedValue(undefined);
    render(<SettingsPage />);
    await screen.findByText("claude-sonnet-4-5");
    const q = screen.getByRole("form", { name: "한도 설정" });
    fireEvent.change(within(q).getByLabelText("대상"), { target: { value: "staff" } });
    fireEvent.change(within(q).getByLabelText("하루 한도(토큰)"), { target: { value: "0" } });
    fireEvent.click(within(q).getByRole("button", { name: "한도 저장" }));
    const dlg = screen.getByRole("alertdialog", { name: "하루 한도를 0 으로 저장" });
    expect(dlg).toHaveTextContent("AI 호출이 모두 막힙니다");
    fireEvent.click(within(dlg).getByRole("button", { name: "취소" }));
    expect(put).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "삭제" }));
    fireEvent.click(within(screen.getByRole("alertdialog", { name: "모델 규칙 삭제" })).getByRole("button", { name: "삭제" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/llm/policies/1"));
  });

  it("저장 실패 문구에서 서버 머리말을 떼고 보인다", async () => {
    mockApi();
    post.mockRejectedValue(new ApiError(400, "입력값이 올바르지 않습니다: 모델 이름을 입력하세요."));
    render(<SettingsPage />);
    await screen.findByText("claude-sonnet-4-5");
    const add = screen.getByRole("form", { name: "모델 규칙 추가" });
    fireEvent.change(within(add).getByLabelText("모델 이름"), { target: { value: "m" } });
    fireEvent.click(within(add).getByRole("button", { name: "규칙 추가" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("모델 이름을 입력하세요.");
    expect(alert.textContent).not.toMatch(/입력값이 올바르지 않습니다/);
  });

  it("불러오기 실패 문구에 다음 할 일이 있다", async () => {
    get.mockRejectedValue(new Error("x"));
    render(<SettingsPage />);
    expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
    expect(screen.getAllByRole("alert")[0]).toHaveTextContent("설정을 불러오지 못했습니다.");
  });
});

describe("상단 바 소속", () => {
  it("조직도 주소속을 보이고, 없으면 부서 이름으로 대신한다", () => {
    const mk = (u: object) => (
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider initialUser={{ id: 1, name: "구매직원", role: "staff", departmentId: 1, ...u }}>
          <Routes>
            <Route element={<Layout menu={[]} />}>
              <Route path="/" element={<p>본문</p>} />
            </Route>
          </Routes>
        </AuthProvider>
      </MemoryRouter>
    );
    const { unmount } = render(mk({ unitName: "구매1팀", departmentName: "구매팀" }));
    expect(screen.getByTestId("user-unit")).toHaveTextContent("구매1팀");
    unmount();
    render(mk({ departmentName: "구매팀" }));
    expect(screen.getByTestId("user-unit")).toHaveTextContent("구매팀");
  });
});

import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { AuditLogPage } from "./AuditLogPage";
import { actorWithTokenText } from "./auditLabels";

vi.mock("../api/client", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

describe("감사 로그 AI 도구 경로의 사용자 칸(후보 7)", () => {
  it("토큰 번호가 있으면 이름 (역할) · 개인 토큰 \"이름\", 웹 경로는 이름 (역할) 만", () => {
    const base = { actorId: 8, actorName: "구매직원", actorRole: "staff" };
    expect(actorWithTokenText({ ...base, tokenName: "Claude Code 노트북", diff: [{ key: "token_id" }] })).toBe('구매직원 (직원) · 개인 토큰 "Claude Code 노트북"');
    expect(actorWithTokenText({ ...base, diff: [{ key: "token_id" }] })).toBe("구매직원 (직원) · 삭제되었거나 알 수 없는 토큰");
    expect(actorWithTokenText({ ...base, diff: [{ key: "amount" }] })).toBe("구매직원 (직원)");
  });

  it("목록과 상세에 같은 값이 보이고 웹 경로 행에는 토큰이 없다", async () => {
    const rec = { at: "2026-11-10T01:02:03Z", result: "success", detail: "", prevHash: "a", hash: "b", actorId: 8, actorName: "구매직원", actorRole: "staff", targetType: "policy", targetId: "related" };
    vi.mocked(api.get).mockResolvedValue({
      records: [
        { ...rec, seq: 2, channel: "mcp", action: "mcp.search_policy", tokenName: "Claude Code 노트북", tokenLabel: "Claude Code 노트북 (plp_ab12…)", diff: [{ key: "token_id", new: "4" }] },
        { ...rec, seq: 1, channel: "web", action: "policy.search", diff: [{ key: "count", new: "5" }] },
      ],
      nextBeforeSeq: 0,
    });
    const { container } = render(
      <AuthProvider initialUser={{ id: 3, name: "감사", role: "auditor", departmentId: 1 }}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findByText('구매직원 (직원) · 개인 토큰 "Claude Code 노트북"');
    const rows = container.querySelectorAll("tbody tr");
    expect(rows[1].textContent).not.toContain("개인 토큰");
    fireEvent.click(within(rows[0] as HTMLElement).getByRole("button", { name: /상세/ }));
    const detail = await screen.findByTestId("audit-detail");
    expect(detail.textContent).toContain('구매직원 (직원) · 개인 토큰 "Claude Code 노트북"');
  });
});

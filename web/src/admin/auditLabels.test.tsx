import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { PERMISSIONS } from "../auth/permissions";
import { AuditLogPage } from "./AuditLogPage";
import { ACTION_LABELS, PERMISSION_LABELS, actionLabel, detailText, diffKeyLabel, targetIdText } from "./auditLabels";

vi.mock("../api/client", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

const base = {
  at: "2026-11-10T01:02:03Z",
  actorId: 2,
  actorRole: "staff",
  channel: "web",
  targetType: "purchase_request",
  targetId: "7",
  result: "success",
  diff: [] as { key: string; old?: string; new?: string }[],
  detail: "",
  prevHash: "a",
  hash: "b",
};

describe("감사 로그 한국어 라벨", () => {
  it("모든 권한 이름에 한국어 라벨이 있다", () => {
    for (const p of PERMISSIONS) expect(PERMISSION_LABELS[p], p).toBeTruthy();
  });

  it("알려진 동작은 한국어로, 모르는 동작은 코드 대신 일반 문구로 보인다", () => {
    expect(actionLabel("purchase_request.create")).toBe("구매요청 작성");
    expect(actionLabel("mcp.approve_or_reject")).toBe("AI 도구: 결재 승인·반려");
    expect(actionLabel("mcp.approve_or_reject.result")).toBe("AI 도구 결과: 결재 승인·반려");
    expect(actionLabel("mcp.something_new")).toBe("AI 도구 호출");
    expect(targetIdText("llm_policy", "role:staff")).toBe("역할 직원");
    expect(actionLabel("brand_new.action")).toBe("기타 작업");
    for (const label of Object.values(ACTION_LABELS)) expect(label).not.toMatch(/[A-Za-z_]{3,}/);
  });

  it("변경 키·비고·대상 번호의 내부 코드를 숨긴다", () => {
    expect(diffKeyLabel("total_amount")).toBe("총액");
    expect(diffKeyLabel("unknown_key")).toBe("항목");
    expect(detailText("tool=approve_or_reject required=approval:decide")).toBe("필요 권한: 결재 승인·반려");
    expect(detailText("필요 권한 audit:read")).toBe("필요 권한: 감사 로그 조회");
    expect(detailText("status=ok provider=mock model=mock-1")).toBe("");
    expect(detailText("자기 결재")).toBe("자기 결재");
    expect(detailText("requested")).toBe("실행 요청");
    expect(targetIdText("route", "POST /api/approvals/{id}/decision")).toBe("");
    expect(targetIdText("session", "a1b2c3d4e5f6")).toBe("");
    expect(targetIdText("approval", "pr:12")).toBe("#12");
    expect(targetIdText("purchase_request", "7")).toBe("#7");
  });
});

describe("감사 로그 화면은 내부 코드를 노출하지 않는다", () => {
  it("전 영역의 대표 기록을 그려도 코드 문자열이 보이지 않는다", async () => {
    const records = [
      { ...base, seq: 1, action: "purchase_request.create", diff: [{ key: "total_amount", new: "10000" }] },
      { ...base, seq: 2, action: "approval.decide", targetType: "approval", diff: [{ key: "status", old: "submitted", new: "in_review" }] },
      {
        ...base,
        seq: 3,
        channel: "mcp",
        action: "mcp.approve_or_reject",
        targetType: "approval",
        targetId: "pr:7",
        result: "denied",
        detail: "tool=approve_or_reject required=approval:decide",
      },
      { ...base, seq: 4, channel: "llmgw", action: "llm.complete", targetType: "llm_call", detail: "status=ok provider=mock model=mock-1" },
      { ...base, seq: 5, action: "receipt.create", targetType: "receipt" },
      { ...base, seq: 6, action: "access.denied", targetType: "route", targetId: "POST /api/x/{id}", result: "denied", detail: "필요 권한 audit:read" },
      { ...base, seq: 7, action: "totally.unknown", targetType: "vendor", targetId: "3" },
    ];
    vi.mocked(api.get).mockResolvedValue({ records, nextBeforeSeq: 0 });
    const { container } = render(
      <AuthProvider initialUser={{ id: 3, name: "감사", role: "auditor", departmentId: 1 }}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findByText("구매요청 작성");
    const text = container.querySelector("tbody")!.textContent ?? "";
    for (const leak of [
      "purchase_request",
      "approval.decide",
      "approve_or_reject",
      "total_amount",
      "required=",
      "provider=",
      "llm.complete",
      "receipt.create",
      "approval:decide",
      "totally.unknown",
      "/api/",
      "MCP",
    ]) {
      expect(text, leak).not.toContain(leak);
    }
    expect(text).toContain("기타 작업");
    expect(text).toContain("필요 권한: 결재 승인·반려");
    expect(text).toContain("AI 도구: 결재 승인·반려");
  });
});

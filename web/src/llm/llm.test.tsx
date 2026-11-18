import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { LLMUsagePage, formatCost, parseTokenLimit, purposeLabel } from "./LLMUsagePage";

function json(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
}

function renderPage(user: CurrentUser) {
  return render(
    <MemoryRouter>
      <AuthProvider initialUser={user}>
        <LLMUsagePage />
      </AuthProvider>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

const rows = [
  { day: "2026-11-10", role: "staff", model: "mock-1", purpose: "docverify.extract", calls: 3, blocked: 1, inputTokens: 1200, outputTokens: 80, costMicroUsd: 1500, maskedCount: 4 },
];

describe("AI 사용량 화면", () => {
  it("감사 역할은 사용량 표를 보고 정책 관리는 보이지 않는다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => json({ rows }));
    renderPage({ id: 3, name: "이감사", role: "auditor", departmentId: 1 });
    await waitFor(() => expect(screen.getByText("직원")).toBeInTheDocument());
    expect(screen.getAllByText("$0.0015")).toHaveLength(2); // 모델별 표와 일별 표
    expect(screen.getByText("증빙 문서 읽기")).toBeInTheDocument();
    expect(screen.queryByText("docverify.extract")).toBeNull();
    expect(screen.queryByText("사용 가능한 모델")).toBeNull();
    expect(screen.queryByRole("link", { name: "관리자 설정" })).toBeNull();
  });

  it("관리자는 모델 규칙·일일 한도를 읽기 전용으로 보고 관리자 설정 링크를 받는다", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const url = String(input);
      if (url.includes("/policies")) return json({ policies: [{ id: 1, role: "", departmentId: 0, modelPattern: "mock*", allowed: true }] });
      if (url.includes("/quotas")) return json({ quotas: [{ id: 1, scopeType: "role", scopeValue: "staff", dailyTokenLimit: 50000 }] });
      return json({ rows: [] });
    });
    renderPage({ id: 4, name: "최관리", role: "admin", departmentId: 1 });
    expect(await screen.findByText(/mock\*/)).toBeInTheDocument();
    const quota = await screen.findByRole("table", { name: "일일 사용 한도" });
    expect(quota).toHaveTextContent("직원");
    expect(quota).toHaveTextContent("50,000");
    expect(screen.queryByRole("button", { name: "한도 저장" })).toBeNull();
    expect(screen.queryByRole("form", { name: "모델 규칙 추가" })).toBeNull();
    expect(screen.queryByRole("button", { name: "삭제" })).toBeNull();
    expect(screen.getByRole("link", { name: "관리자 설정" })).toHaveAttribute("href", "/admin/settings");
  });

  it("parseTokenLimit 은 쉼표·공백을 허용하고 숫자가 아니면 null", () => {
    expect(parseTokenLimit("1,000")).toBe(1000);
    expect(parseTokenLimit(" 50 000 ")).toBe(50000);
    expect(parseTokenLimit("0")).toBe(0);
    expect(parseTokenLimit("")).toBeNull();
    expect(parseTokenLimit("-5")).toBeNull();
    expect(parseTokenLimit("1.5")).toBeNull();
  });

  it("모르는 용도 코드는 코드 대신 일반 문구로 보인다", () => {
    expect(purposeLabel("docverify.extract")).toBe("증빙 문서 읽기");
    expect(purposeLabel("something.new")).toBe("기타 용도");
    expect(purposeLabel("")).toBe("기타 용도");
  });

  it("비용은 달러로 표시한다", () => {
    expect(formatCost(2_500_000)).toBe("$2.5000");
  });
});
